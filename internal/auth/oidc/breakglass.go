package oidc

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// LockoutKey is the auth_lockout row of the break-glass account (R-AUTH-038).
const LockoutKey = "break-glass"

// Unlock clears a lockout of the break-glass account at once on every replica
// (R-AUTH-014, R-AUTH-015: every login attempt reads the row) and records
// auth.breakglass.unlocked with severity high in the same transaction (Q-AUTH-37).
// It reports whether a lock was in force. `trove admin break-glass unlock` and
// `POST /api/v1/users/{breakGlassName}/unlock` both call it.
func Unlock(ctx context.Context, db meta.Runner, rec audit.Recorder, actor audit.Actor, src audit.Source) (bool, error) {
	var was bool
	err := db.InTx(ctx, func(tx meta.Tx) error {
		ptx, ok := postgres.Unwrap(tx)
		if !ok {
			return errors.New("the transaction does not come from the PostgreSQL store")
		}
		rows, err := q.New(ptx).Unlock(ctx, LockoutKey)
		if err != nil {
			return err
		}
		was = len(rows) == 1 && rows[0]
		details, _ := json.Marshal(map[string]any{"severity": "high", "was_locked": was})
		return rec.RecordTx(ctx, tx, audit.Event{
			Type: audit.EventAuthBreakGlassUnlocked, Actor: actor, Source: src, Outcome: audit.Success, Details: details,
		})
	})
	return was, err
}

// slotWait is how long a local login waits for a verification slot before it
// answers 503 server_busy (R-AUTH-014).
const slotWait = time.Second

// BreakGlass is the one local account (R-AUTH-014) on a replica that serves the
// client listener. The password lives in a mounted file, never in PostgreSQL;
// the lockout and the rotation hold live in the auth_lockout row that every
// attempt reads, so both are in force on every replica at once (R-AUTH-038,
// Q-AUTH-14).
type BreakGlass struct {
	s      *Service
	cfg    config.AuthBreakGlass
	log    *slog.Logger
	slots  chan struct{}
	userID string

	mu    sync.RWMutex
	pw    *password // nil: no usable password, the local login answers 403
	mtime time.Time
	size  int64
}

// newBreakGlass loads the password file, makes sure the break-glass user row
// exists, and with the account disabled ends every break-glass session that is
// left (R-AUTH-014). A configured file that cannot be read, a plaintext shorter
// than 20 characters and a hash below the Argon2id floor stop the replica; an
// enabled account without a configured file refuses every local login
// (Q-AUTH-33).
func newBreakGlass(ctx context.Context, s *Service) (*BreakGlass, error) {
	cfg := s.o.Auth.BreakGlass
	b := &BreakGlass{s: s, cfg: cfg, log: s.log.With("account", "break-glass"), slots: make(chan struct{}, max(cfg.MaxConcurrentVerifications, 1))}
	if !cfg.Enabled {
		if err := s.o.Sessions.EndBreakGlass(ctx, session.EndRevoked, audit.Actor{Kind: "system"}); err != nil {
			return nil, fmt.Errorf("ending the break-glass sessions of the disabled account: %w", err)
		}
		return b, nil
	}
	if cfg.PasswordFile == "" {
		b.log.Warn("auth.break_glass.enabled is true but auth.break_glass.password_file is empty: every local login is refused with 403 local_login_disabled")
	} else if err := b.load(); err != nil {
		return nil, fmt.Errorf("auth.break_glass.password_file: %w", err)
	}
	id, err := ensureUser(ctx, s.o.DB, cfg.Username)
	if err != nil {
		return nil, err
	}
	b.userID = id
	return b, nil
}

// load reads the password file and replaces the verifier.
func (b *BreakGlass) load() error {
	fi, err := os.Stat(b.cfg.PasswordFile)
	if err != nil {
		return err
	}
	content, err := os.ReadFile(b.cfg.PasswordFile)
	if err != nil {
		return err
	}
	pw, err := parsePassword(content)
	if err != nil {
		return err
	}
	if pw.hashed {
		b.log.Warn("the break-glass password file holds a hash; its strength cannot be checked (R-AUTH-014)")
	}
	b.mu.Lock()
	b.pw, b.mtime, b.size = pw, fi.ModTime(), fi.Size()
	b.mu.Unlock()
	return nil
}

// usable reports whether the account can sign in on this replica.
func (b *BreakGlass) usable() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.cfg.Enabled && b.pw != nil
}

// UserID is the principal ID of the break-glass user, "" while disabled.
func (b *BreakGlass) UserID() string { return b.userID }

// ensureUser creates the break-glass user row, a user with origin system and
// is_break_glass, or renames it to the configured username. Concurrent replicas
// serialize on the unique indexes (R-AUTH-014).
func ensureUser(ctx context.Context, db *postgres.DB, name string) (string, error) {
	var id string
	err := db.InTx(ctx, func(tx meta.Tx) error {
		ptx, ok := postgres.Unwrap(tx)
		if !ok {
			return errors.New("break-glass: the transaction does not come from the PostgreSQL store")
		}
		qs := q.New(ptx)
		rows, err := qs.BreakGlassUser(ctx)
		if err != nil {
			return err
		}
		if len(rows) == 1 {
			id = rows[0].ID
			n, err := qs.RenameBreakGlassUser(ctx, q.RenameBreakGlassUserParams{ID: id, Name: name})
			if err != nil {
				return fmt.Errorf("renaming the break-glass user to %q (is the name taken by another user?): %w", name, err)
			}
			_ = n
			return nil
		}
		created, err := qs.InsertBreakGlassPrincipal(ctx, q.InsertBreakGlassPrincipalParams{ID: session.NewID(), Name: name})
		if err != nil {
			return err
		}
		if len(created) == 0 {
			// The name is taken: by a break-glass row a concurrent replica just
			// committed, or by an ordinary user.
			if rows, err = qs.BreakGlassUser(ctx); err != nil {
				return err
			}
			if len(rows) == 1 {
				id = rows[0].ID
				return nil
			}
			return fmt.Errorf("auth.break_glass.username %q is the name of an existing user; choose another", name)
		}
		id = created[0]
		return qs.InsertBreakGlassUser(ctx, id)
	})
	return id, err
}

// watch looks at the password file every check_interval and handles a rotation
// (R-AUTH-014). Each iteration has its own deadline and holds no connection
// between iterations (R-AUTH-042).
func (b *BreakGlass) watch(ctx context.Context) {
	if !b.cfg.Enabled || b.cfg.PasswordFile == "" {
		return
	}
	t := time.NewTicker(b.cfg.CheckInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			ictx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if err := b.CheckRotation(ictx); err != nil {
				b.log.Warn("break-glass rotation check failed; retrying at the next interval", "error", err)
			}
			cancel()
		}
	}
}

// CheckRotation handles a changed password file (Q-AUTH-14). When the content
// changed, it opens the rotation hold in auth_lockout unless one opened less
// than rotation_hold ago, and the transaction that opens it ends every
// break-glass session with reason password_changed and records the ends in the
// area auth. Only after that commit does this replica switch to the new file,
// so the hold always precedes the new password.
func (b *BreakGlass) CheckRotation(ctx context.Context) error {
	fi, err := os.Stat(b.cfg.PasswordFile)
	if err != nil {
		return err
	}
	b.mu.RLock()
	same := fi.ModTime().Equal(b.mtime) && fi.Size() == b.size
	cur := b.pw
	b.mu.RUnlock()
	if same {
		return nil
	}
	content, err := os.ReadFile(b.cfg.PasswordFile)
	if err != nil {
		return err
	}
	next, perr := parsePassword(content)
	if perr == nil && cur != nil && next.fingerprint == cur.fingerprint {
		b.mu.Lock()
		b.mtime, b.size = fi.ModTime(), fi.Size()
		b.mu.Unlock()
		return nil
	}
	if err := b.openHold(ctx); err != nil {
		return err
	}
	b.mu.Lock()
	b.mtime, b.size = fi.ModTime(), fi.Size()
	if perr != nil {
		// A rotated file that does not hold a usable password refuses every login
		// until it is fixed; the old password never comes back.
		b.pw = nil
	} else {
		b.pw = next
	}
	b.mu.Unlock()
	if perr != nil {
		b.log.Error("the rotated break-glass password file is not usable; local login is refused", "error", perr)
	} else {
		b.log.Info("break-glass password rotated")
	}
	return nil
}

func (b *BreakGlass) openHold(ctx context.Context) error {
	return b.s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		ptx, ok := postgres.Unwrap(tx)
		if !ok {
			return errors.New("break-glass: the transaction does not come from the PostgreSQL store")
		}
		opened, err := q.New(ptx).OpenRotationHold(ctx, q.OpenRotationHoldParams{AccountKey: LockoutKey, HoldS: b.cfg.RotationHold.Seconds()})
		if err != nil || len(opened) == 0 {
			return err
		}
		_, err = b.s.o.Sessions.EndBreakGlassTx(ctx, tx, session.EndPasswordChanged, audit.Actor{Kind: "system"})
		return err
	})
}

// LocalLoginInput is one sign-in attempt of the break-glass account, already parsed
// by the edge (POST /auth/local/login).
type LocalLoginInput struct {
	Username, Password string
	// SessionCookie is the session cookie the request presented; a session it
	// names is replaced.
	SessionCookie string
	ClientAddr    string
	UserAgent     string
	// Source is the audit source of the request.
	Source audit.Source
}

// AdmitLocalLogin is what a request of POST /auth/local/login passes before its
// body is read (R-AUTH-014): the flow-start limit of the client address
// (R-AUTH-038) and the account being usable. 429 rate_limited and 403
// local_login_disabled are its refusals.
func (s *Service) AdmitLocalLogin(ctx context.Context, clientAddr string) error {
	if err := s.allow(ctx, clientAddr); err != nil {
		return err
	}
	if s.bg == nil || !s.bg.usable() {
		return errs.E(errs.Forbidden, errs.CodeLocalLoginDisabled, "The local login is disabled.")
	}
	return nil
}

// LocalLogin verifies the attempt and creates the session (R-AUTH-014). The
// errors are 401 invalid_credentials, 403 local_login_disabled, 429 for the
// lockout, and 503 during a rotation hold or while the verification slots are
// taken; the caller has passed [Service.AdmitLocalLogin].
func (s *Service) LocalLogin(ctx context.Context, in LocalLoginInput) (session.Created, error) {
	if s.bg == nil {
		return session.Created{}, errs.E(errs.Forbidden, errs.CodeLocalLoginDisabled, "The local login is disabled.")
	}
	return s.bg.login(ctx, in)
}

// login runs one attempt: the lockout row first (every attempt reads it), then
// one Argon2id verification, which runs for unknown usernames too, so the
// answer time does not reveal the account name.
func (b *BreakGlass) login(ctx context.Context, in LocalLoginInput) (session.Created, error) {
	if err := b.checkLockout(ctx); err != nil {
		return session.Created{}, err
	}
	b.mu.RLock()
	pw := b.pw
	b.mu.RUnlock()
	if pw == nil {
		return session.Created{}, errs.E(errs.Forbidden, errs.CodeLocalLoginDisabled, "The local login is disabled.")
	}
	// The attempt is counted before it is verified, so attempts in parallel (on
	// this replica or on others) cannot all be verified before the failure that
	// locks the account has committed (R-AUTH-038).
	if err := b.reserveAttempt(ctx); err != nil {
		return session.Created{}, err
	}
	ok, err := b.verify(ctx, pw, in)
	if err != nil {
		b.releaseAttempt(ctx)
		return session.Created{}, err
	}
	if !ok {
		if err := b.recordFailure(ctx, in.Source); err != nil {
			return session.Created{}, err
		}
		matched := subtle.ConstantTimeCompare([]byte(in.Username), []byte(b.cfg.Username)) == 1
		b.audit(ctx, in.Source, audit.Failure, "invalid_credentials", "", matched)
		b.s.m.Attempt("breakglass", "failure", "invalid_credentials")
		return session.Created{}, errs.E(errs.Unauthenticated, errs.CodeInvalidCredentials, "The username or the password is wrong.")
	}
	// A failure that locked the account while this password was verified wins: the
	// right guess does not log in through a lock that has been committed.
	if err := b.checkLockout(ctx); err != nil {
		return session.Created{}, err
	}
	if err := b.s.o.DB.Read(ctx, func(db q.DBTX) error { return q.New(db).ResetLockoutFailures(ctx, LockoutKey) }); err != nil {
		return session.Created{}, postgres.Unavailable(err)
	}
	created, err := b.s.o.Sessions.Create(ctx, session.NewSession{
		PrincipalID: b.userID, BreakGlass: true, ClientAddr: in.ClientAddr, UserAgent: in.UserAgent,
		ReplaceCookie: in.SessionCookie,
	})
	if errors.Is(err, session.ErrPrincipalNotActive) {
		return session.Created{}, errs.E(errs.Forbidden, errs.CodeLocalLoginDisabled, "The local login is disabled.")
	}
	if err != nil {
		return session.Created{}, postgres.Unavailable(err)
	}
	b.audit(ctx, in.Source, audit.Success, "", created.ID, true)
	b.s.m.Attempt("breakglass", "success", "")
	details, _ := json.Marshal(map[string]string{"method": "breakglass", "session_id": created.ID})
	b.s.o.Audit.Record(ctx, audit.Event{
		Type: audit.EventAuthLoginSucceeded, Actor: audit.Actor{Kind: "break_glass", ID: b.userID, Name: b.cfg.Username},
		Outcome: audit.Success, Source: in.Source, Details: details,
	})
	return created, nil
}

// checkLockout reads the auth_lockout row: a lock answers 429 even for the
// right password (R-AUTH-038), a rotation hold 503 break_glass_rotating
// (R-AUTH-014). No replica caches the row.
func (b *BreakGlass) checkLockout(ctx context.Context) error {
	var rows []q.GetLockoutRow
	err := b.s.o.DB.Read(ctx, func(db q.DBTX) error {
		var err error
		rows, err = q.New(db).GetLockout(ctx, q.GetLockoutParams{AccountKey: LockoutKey, HoldS: b.cfg.RotationHold.Seconds()})
		return err
	})
	if err != nil {
		return postgres.Unavailable(err)
	}
	if len(rows) == 0 {
		return nil
	}
	if l := rows[0].LockedForS; l > 0 {
		b.s.m.RateLimited("break_glass_lockout")
		return errs.E(errs.RateLimited, errs.CodeBreakGlassLocked, "The break-glass account is locked after too many failed logins.",
			errs.WithRetryAfter(seconds(l)))
	}
	if h := rows[0].HoldForS; h > 0 {
		return errs.E(errs.Unavailable, errs.CodeBreakGlassRotating, "The break-glass password was rotated a moment ago; retry later.",
			errs.WithRetryAfter(seconds(h)))
	}
	return nil
}

// verify runs the Argon2id verification in one of the replica's slots. An
// attempt that gets no slot within 1 s answers 503 server_busy (R-AUTH-014):
// each verification holds at least 64 MiB.
func (b *BreakGlass) verify(ctx context.Context, pw *password, in LocalLoginInput) (bool, error) {
	t := time.NewTimer(slotWait)
	defer t.Stop()
	select {
	case b.slots <- struct{}{}:
	case <-t.C:
		b.s.m.RateLimited("break_glass_verifications")
		return false, errs.E(errs.Unavailable, errs.CodeServerBusy, "Too many local logins are being verified; retry.", errs.WithRetryAfter(time.Second))
	case <-ctx.Done():
		return false, ctx.Err()
	}
	defer func() { <-b.slots }()
	passOK := pw.matches(in.Password)
	nameOK := subtle.ConstantTimeCompare([]byte(in.Username), []byte(b.cfg.Username)) == 1
	return passOK && nameOK, nil
}

// reserveAttempt counts one attempt in one statement on any replica. Only as many
// attempts as the lockout threshold are admitted to the verification between
// two resets: the next one answers 429 although the lock is not yet written,
// because the attempts in flight decide whether it is (R-AUTH-038).
func (b *BreakGlass) reserveAttempt(ctx context.Context) error {
	l := b.cfg.Lockout
	var rows []q.ReserveLockoutAttemptRow
	err := b.s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		ptx, ok := postgres.Unwrap(tx)
		if !ok {
			return errors.New("break-glass: the transaction does not come from the PostgreSQL store")
		}
		var err error
		rows, err = q.New(ptx).ReserveLockoutAttempt(ctx, q.ReserveLockoutAttemptParams{AccountKey: LockoutKey, WindowS: l.Window.Seconds()})
		return err
	})
	if err != nil {
		return postgres.Unavailable(err)
	}
	if len(rows) == 1 && (rows[0].Locked || int(rows[0].Failures) > l.Threshold) {
		b.s.m.RateLimited("break_glass_lockout")
		return errs.E(errs.RateLimited, errs.CodeBreakGlassLocked, "The break-glass account is locked after too many failed logins.",
			errs.WithRetryAfter(time.Second))
	}
	return nil
}

// releaseAttempt takes back an attempt that was never verified. It runs on a
// context of its own: the request may be the reason the verification did not run.
func (b *BreakGlass) releaseAttempt(ctx context.Context) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = b.s.o.DB.Read(rctx, func(db q.DBTX) error { return q.New(db).ReleaseLockoutAttempt(rctx, LockoutKey) })
}

// recordFailure locks the account when the failed attempt brought the count to the
// threshold and, when it does, writes auth.breakglass.locked in the same
// transaction (R-AUTH-038). The count itself was taken by reserveAttempt.
func (b *BreakGlass) recordFailure(ctx context.Context, src audit.Source) error {
	l := b.cfg.Lockout
	err := b.s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		ptx, ok := postgres.Unwrap(tx)
		if !ok {
			return errors.New("break-glass: the transaction does not come from the PostgreSQL store")
		}
		rows, err := q.New(ptx).LockOnLockoutFailure(ctx, q.LockOnLockoutFailureParams{
			AccountKey: LockoutKey, Threshold: int32(l.Threshold), DurationS: l.Duration.Seconds(), //nolint:gosec // validated range
		})
		if err != nil || len(rows) == 0 {
			return err
		}
		details, _ := json.Marshal(map[string]any{"severity": "high", "failures": rows[0].Failures, "locked_until": rows[0].LockedUntil.Time.UTC().Format(time.RFC3339)})
		return b.s.o.Audit.RecordTx(ctx, tx, audit.Event{
			Type: audit.EventAuthBreakGlassLocked, Actor: audit.Actor{Kind: "anonymous"}, Outcome: audit.Success,
			Source: src, Subject: audit.Subject{Kind: "user", ID: b.userID, Name: b.cfg.Username}, Details: details,
		})
	})
	if err != nil {
		return postgres.Unavailable(err)
	}
	return nil
}

// audit writes auth.breakglass.login with severity high, for success and
// failure alike (R-AUTH-014). It records whether the presented username was the
// account's, never the presented value: a password typed into the username
// field would otherwise land in the audit trail. The password never is recorded.
func (b *BreakGlass) audit(ctx context.Context, src audit.Source, outcome audit.Outcome, reason, sessionID string, usernameMatched bool) {
	d := map[string]any{"severity": "high", "usernameMatched": usernameMatched}
	if sessionID != "" {
		d["session_id"] = sessionID
	}
	details, _ := json.Marshal(d)
	actor := audit.Actor{Kind: "anonymous"}
	if outcome == audit.Success {
		actor = audit.Actor{Kind: "break_glass", ID: b.userID, Name: b.cfg.Username}
	}
	b.s.o.Audit.Record(ctx, audit.Event{
		Type: audit.EventAuthBreakGlassLogin, Actor: actor, Outcome: outcome, Reason: reason, Source: src,
		Subject: audit.Subject{Kind: "user", ID: b.userID, Name: b.cfg.Username}, Details: details,
	})
}

func seconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }
