package token

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

const (
	tokenCacheName  = "auth_tokens" //nolint:gosec // G101: a cache name, not a credential
	maxCachedTokens = 50000
)

// cachedToken is the positive result of a lookup: the token's facts and its
// owner's status at the time of the fill (R-AUTH-023). Revoked, expired and
// owner-disabled tokens are never cached.
type cachedToken struct {
	info Info
}

// Throttle is the failed-credential throttle of R-AUTH-038 for one request's
// client address. The stage builds it; the validator consults it only for a
// credential it cannot answer from its cache.
type Throttle interface {
	// Exceeded reports whether the address is in throttled mode. Addresses in
	// auth.rate_limit.exempt_cidrs are never throttled.
	Exceeded(ctx context.Context) bool
	// Count records one rejected credential from the address.
	Count(ctx context.Context)
}

// Request carries what a validation needs besides the token: the throttle of the
// request's client address and the source for audit events.
type Request struct {
	Throttle Throttle
	Source   audit.Source
	// RetryAfter is the Retry-After of a throttled answer; the throttle's window.
	RetryAfter time.Duration
}

// ThrottledError is returned when the client address is throttled and the
// credential is neither in the replica's cache nor marked as recently validated
// (R-AUTH-038). The credential was not validated. The stage answers 429.
type ThrottledError struct{ RetryAfter time.Duration }

// Error implements error.
func (e *ThrottledError) Error() string { return "token: client address throttled" }

// ErrThrottled matches a [ThrottledError] with errors.Is.
var ErrThrottled = errors.New("token: client address throttled")

// Is reports ErrThrottled.
func (e *ThrottledError) Is(target error) bool { return target == ErrThrottled }

// rejection is a failed lookup whose row exists and was revoked or expired: the
// facts for token.rejected (R-AUTH-023 step 4).
type rejection struct {
	err   error
	id    string
	hint  string
	owner string
	due   bool
}

func (r *rejection) Error() string { return r.err.Error() }
func (r *rejection) Unwrap() error { return r.err }

// Validate implements [Validator] without a throttle.
func (s *Service) Validate(ctx context.Context, raw string) (Info, error) {
	return s.Check(ctx, raw, Request{})
}

// Check validates a presented token (R-AUTH-023): the syntax without database
// access, the replica's cache while the freshness gate is open, then the
// throttle for a credential that needs a lookup, then PostgreSQL. Every error is
// one of the Err values, a [ThrottledError], or an *errs.Error of kind
// Unavailable when PostgreSQL does not answer (never a cached acceptance).
func (s *Service) Check(ctx context.Context, raw string, req Request) (Info, error) {
	typ, err := Check(raw)
	if err != nil {
		s.count(ctx, req)
		return Info{}, ErrMalformed
	}
	hash := Hash(raw)
	gateOpen := s.o.Gate.Open()
	if gateOpen {
		if ct, ok := s.tokens.Get(hash); ok {
			if ct.info.ExpiresAt == nil || s.o.Now().Before(*ct.info.ExpiresAt) {
				return ct.info, nil
			}
			s.tokens.Remove(hash) // expired since the fill: the database path logs the rejection
		}
	}
	if req.Throttle != nil && req.Throttle.Exceeded(ctx) {
		if s.o.Recent == nil || !s.o.Recent.Seen(ctx, hash[:]) {
			return Info{}, &ThrottledError{RetryAfter: req.RetryAfter}
		}
		s.o.Metrics.ThrottleExempt("marker")
	}
	ct, err := s.fetch(ctx, hash, typ, gateOpen)
	if err != nil {
		var rej *rejection
		if errors.As(err, &rej) {
			if rej.due {
				s.logRejection(ctx, rej, req.Source)
			}
			s.count(ctx, req)
			return Info{}, rej.err
		}
		if errors.Is(err, ErrUnknown) {
			s.count(ctx, req)
		}
		return Info{}, err
	}
	if s.o.Recent != nil {
		if err := s.o.Recent.Mark(ctx, hash[:]); err != nil {
			s.log.Debug("token marker not written", "err", err)
		}
	}
	return ct.info, nil
}

func (s *Service) count(ctx context.Context, req Request) {
	if req.Throttle != nil {
		req.Throttle.Count(ctx)
	}
}

// fetch reads the token from PostgreSQL. With the gate open the result is cached
// under the fill rule; with it closed concurrent lookups of one credential share
// a query that is younger than the revocation bound and nothing is cached.
func (s *Service) fetch(ctx context.Context, hash [32]byte, typ Type, gateOpen bool) (cachedToken, error) {
	load := func(ctx context.Context) (cachedToken, error) { return s.load(ctx, hash, typ) }
	if gateOpen {
		return s.tokens.Load(ctx, hash, load)
	}
	return s.flights.Do(ctx, hash, s.o.Gate.Bound, load)
}

func (s *Service) load(ctx context.Context, hash [32]byte, typ Type) (cachedToken, error) {
	var ct cachedToken
	err := s.o.DB.Read(ctx, func(db q.DBTX) error {
		rows, err := q.New(db).IdentityTokenBySecret(ctx, hash[:])
		if err != nil {
			return err
		}
		if len(rows) == 0 || Type(rows[0].Type) != typ {
			return ErrUnknown
		}
		r := rows[0]
		owner := principal.Principal{
			ID: r.OwnerID, Kind: principal.KindFromRow(r.OwnerKind, r.OwnerBreakGlass), Name: r.OwnerName, Status: principal.Status(r.OwnerStatus),
		}
		ref := Owner{ID: owner.ID, Kind: owner.Kind, Name: owner.Name}.Ref()
		switch {
		case r.Revoked:
			return &rejection{err: ErrRevoked, id: r.ID, hint: r.Hint, owner: ref, due: r.RejectionDue}
		case r.Expired:
			return &rejection{err: ErrExpired, id: r.ID, hint: r.Hint, owner: ref, due: r.RejectionDue}
		case owner.Status != principal.StatusActive:
			return ErrOwner
		}
		scope, err := ParseStoredScope(r.Scope)
		if err != nil {
			return fmt.Errorf("token %s: stored scope: %w", r.ID, err)
		}
		ct.info = Info{ID: r.ID, Type: typ, Scope: scope, Owner: owner}
		if r.ExpiresAt.Valid {
			t := r.ExpiresAt.Time
			ct.info.ExpiresAt = &t
		}
		return nil
	})
	if err != nil {
		var rej *rejection
		switch {
		case errors.As(err, &rej), errors.Is(err, ErrUnknown), errors.Is(err, ErrOwner):
			return cachedToken{}, err
		}
		return cachedToken{}, principal.DBErr("token.validate", err)
	}
	return ct, nil
}

// logRejection writes token.rejected at most once per token and hour in the
// whole cluster: the hour is kept in the row, and the event is recorded only when
// the conditional update changed it (R-AUTH-023 step 4). The event type is
// asynchronous (R-OPS-016), so it is handed to the recorder after the update
// committed. A failure is logged and does not change the answer.
func (s *Service) logRejection(ctx context.Context, rej *rejection, src audit.Source) {
	reason := "revoked"
	if errors.Is(rej.err, ErrExpired) {
		reason = "expired"
	}
	marked := false
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		rows, err := principal.Queries(tx).IdentityMarkRejectionLogged(ctx, rej.id)
		marked = err == nil && len(rows) > 0
		return err
	})
	if err != nil {
		s.log.Warn("token.rejected not recorded", "token", rej.id, "err", err)
		return
	}
	if !marked {
		return
	}
	s.o.Recorder.Record(ctx, audit.Event{
		Type: audit.EventTokenRejected, Outcome: audit.Failure, Reason: reason, Actor: audit.Actor{Kind: "anonymous"}, Source: src,
		Subject: audit.Subject{Kind: "token", ID: rej.id, Name: rej.hint},
		Details: mustJSON(map[string]string{"reason": reason, "hint": rej.hint, "owner": rej.owner}),
	})
}

// onChange evicts what a change of the area auth ended: the tokens it names, or
// everything when a principal changed or the payload is not understood.
func (s *Service) onChange(c *cache.Cache[[32]byte, cachedToken], _ string, _ int, payload json.RawMessage) {
	ch, ok := principal.Decode(payload)
	if !ok {
		c.Flush(cache.ReasonChange)
		return
	}
	switch ch.Kind {
	case principal.ChangeToken:
		for _, ref := range ch.Tokens {
			var h [32]byte
			if b, err := hex.DecodeString(ref.Hash); err == nil && len(b) == len(h) {
				copy(h[:], b)
				c.Invalidate(h)
				continue
			}
			c.Flush(cache.ReasonChange)
			return
		}
	case principal.ChangeMembership, principal.ChangeGroup, principal.ChangeSettings, principal.ChangeSession:
		// A token carries no group list and no setting (R-AUTHZ-002).
	default: // principal, all, and kinds this build does not know
		c.Flush(cache.ReasonChange)
	}
}
