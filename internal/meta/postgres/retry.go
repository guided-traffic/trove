package postgres

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
)

// maxSerializationRetries is how often meta.InTx repeats a transaction that
// failed with SQLSTATE 40001 or 40P01 (R-ARCH-019).
const maxSerializationRetries = 3

type retryKind uint8

const (
	retryNo retryKind = iota
	// retrySerialization is a serialization failure or a deadlock: the server
	// rolled the transaction back and the same work can run again at once.
	retrySerialization
	// retryFailover is an error that a failover or a restart of the server or
	// pooler produces: the connection is gone, refused or read-only. The work
	// can run again once a new connection finds the new primary.
	retryFailover
)

// classify says whether err is worth another attempt.
func classify(err error) retryKind {
	if err == nil {
		return retryNo
	}
	var unknown *outcomeUnknownError
	if errors.As(err, &unknown) {
		return retryNo
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch {
		case pe.Code == "40001" || pe.Code == "40P01":
			return retrySerialization
		case pe.Code == "08P01": // protocol_violation: a refused startup parameter, never transient
			return retryNo
		case strings.HasPrefix(pe.Code, "08"), // connection exception
			pe.Code == "57P01", // admin_shutdown
			pe.Code == "57P02", // crash_shutdown
			pe.Code == "57P03", // cannot_connect_now (starting up, in recovery)
			pe.Code == "25006", // read_only_sql_transaction: a demoted primary
			pe.Code == "53300": // too_many_connections: transient during a failover
			return retryFailover
		}
		return retryNo
	}
	if isTLSVerification(err) {
		// A certificate the configured CA does not verify is a configuration
		// error, not an outage: it does not pass by waiting (CR2-6).
		return retryNo
	}
	if pgconn.SafeToRetry(err) {
		return retryFailover
	}
	var ce *pgconn.ConnectError
	if errors.As(err, &ce) {
		return retryFailover
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, net.ErrClosed) {
		return retryFailover
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return retryFailover
	}
	var cc *connClosedError
	if errors.As(err, &cc) {
		return retryFailover
	}
	return retryNo
}

// isTLSVerification reports a failed verification of the server's certificate or
// of the chain it presented: unknown authority, wrong host name, expired, not
// yet valid. pgconn wraps it in a ConnectError, which classify would take for an
// outage.
func isTLSVerification(err error) bool {
	var (
		cv *tls.CertificateVerificationError
		ua x509.UnknownAuthorityError
		hn x509.HostnameError
		ci x509.CertificateInvalidError
	)
	return errors.As(err, &cv) || errors.As(err, &ua) || errors.As(err, &hn) || errors.As(err, &ci)
}

// connClosedError marks an error after which the connection was found closed:
// the statement died with its connection, so the whole transaction is lost and
// can run again.
type connClosedError struct{ err error }

func (e *connClosedError) Error() string { return e.err.Error() }
func (e *connClosedError) Unwrap() error { return e.err }

// outcomeUnknownError is a COMMIT that lost its connection: the server may or
// may not have committed. It is never retried, because the work may not be
// idempotent; the caller sees the database as unavailable.
type outcomeUnknownError struct{ err error }

func (e *outcomeUnknownError) Error() string {
	return "transaction outcome unknown, the connection ended during COMMIT: " + e.err.Error()
}
func (e *outcomeUnknownError) Unwrap() error { return e.err }

// retrier repeats an operation per R-ARCH-019 and R-HA-015.
type retrier struct {
	cfg   config.DatabaseRetry
	sleep func(ctx context.Context, d time.Duration) error
	now   func() time.Time
}

func newRetrier(cfg config.DatabaseRetry) retrier {
	return retrier{cfg: cfg, sleep: sleepCtx, now: time.Now}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// jitter spreads d by ±20 % so replicas that failed together do not retry together.
func jitter(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (0.8 + 0.4*rand.Float64())) //nolint:gosec // not security relevant
}

// do runs op until it succeeds, fails with an error that is not retryable, or
// the bounds are used up: three repeats for serialization failures, and
// retry.max_elapsed (cut short by the context deadline) for failover errors.
// The error of an exhausted failover retry is the database being unavailable.
func (r retrier) do(ctx context.Context, op func() error) error {
	start := r.now()
	backoff := r.cfg.InitialBackoff
	serial := 0
	for {
		err := op()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return err
		}
		switch classify(err) {
		case retrySerialization:
			serial++
			if serial > maxSerializationRetries {
				return err
			}
			if r.sleep(ctx, jitter(time.Duration(serial)*10*time.Millisecond)) != nil {
				return err
			}
		case retryFailover:
			wait := jitter(backoff)
			if r.now().Sub(start)+wait > r.cfg.MaxElapsed {
				return Unavailable(err)
			}
			if r.sleep(ctx, wait) != nil {
				return Unavailable(err)
			}
			backoff = min(backoff*2, r.cfg.MaxBackoff)
		default:
			var unknown *outcomeUnknownError
			if errors.As(err, &unknown) {
				return Unavailable(err)
			}
			return err
		}
	}
}

// Unavailable wraps a database failure as the 503 of R-ARCH-023.
func Unavailable(err error) error {
	var e *errs.Error
	if errors.As(err, &e) {
		return err
	}
	if IsStatementTimeout(err) {
		return errs.E(errs.Unavailable, errs.CodeDatabaseTimeout, "the database did not answer in time", errs.WithCause(err))
	}
	return errs.E(errs.Unavailable, errs.CodeDatabaseUnavailable, "the database is not available", errs.WithCause(err))
}

// IsUnavailable reports whether err is a connectivity failure that survived
// the retries, or a statement that the server cancelled for its timeout.
func IsUnavailable(err error) bool {
	return errs.KindOf(err) == errs.Unavailable || classify(err) == retryFailover || IsStatementTimeout(err)
}

// IsLockTimeout reports a statement cancelled by lock_timeout (55P03), also
// when the error travels as the cause of an *errs.Error. The idempotent create
// of R-API-014 sets a short lock_timeout and reads this as "the first request
// has not finished".
func IsLockTimeout(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "55P03"
}

// IsStatementTimeout reports a statement cancelled by statement_timeout or lock_timeout.
func IsStatementTimeout(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && (pe.Code == "57014" || pe.Code == "55P03")
}
