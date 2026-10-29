package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// End reasons of a session row (ui_session.end_reason).
const (
	EndLogout           = "logout"
	EndIdle             = "idle"
	EndAbsolute         = "absolute"
	EndEvicted          = "evicted"
	EndRevoked          = "revoked"
	EndPrincipalDisable = "principal_disabled"
	EndReauthRotated    = "reauth_rotated"
	EndPasswordChanged  = "password_changed"
)

// touchEvery is the touch interval of R-AUTH-010: a request 60 s or more after
// the newest last_seen_at the replica has read sends the conditional touch.
const touchEvery = 60 * time.Second

// Defaults of the identity keys this package reads (R-AUTH-023), used while the
// composition root passes none.
const (
	DefaultRevocationBound = 2 * time.Second
	DefaultCacheTTL        = 15 * time.Second
	defaultMaxEntries      = 100000
)

// Options configure a [Store].
type Options struct {
	// DB is the replica's PostgreSQL store; its request pool runs every statement.
	DB *postgres.DB
	// Changes records the end of a session in the area auth; nil records none (a
	// command that ends nothing). *cluster.Changes implements it.
	Changes Notifier
	// Fresh is the freshness gate of Q-AUTH-10; nil bypasses the cache on every
	// lookup, the safe reading of "no change log".
	Fresh Freshness
	// Caches holds the session cache; nil caches nothing.
	Caches *cache.Registry
	// Audit records auth.session.ended and auth.csrf_failed; nil discards.
	Audit audit.Recorder
	Log   *slog.Logger
	// Metrics registers the session families; AuthMetrics counts the cache
	// bypasses in the family the identity area owns (trove_auth_cache_bypassed_total).
	Metrics     prometheus.Registerer
	AuthMetrics *principal.Metrics

	Sessions   config.AuthSessions
	BreakGlass config.AuthBreakGlass
	// RevocationBound is auth.revocation_bound and CacheTTL auth.tokens.cache_ttl
	// of the identity area; zero takes the defaults above.
	RevocationBound time.Duration
	CacheTTL        time.Duration
	// Origin returns the origin of the base URL that serves the request
	// (R-AUTH-012), without a trailing slash; an empty result refuses every
	// unsafe cookie-authenticated request.
	Origin func(*http.Request) string
	// MaxCacheEntries bounds the session cache.
	MaxCacheEntries int
	// Source fills the source of the events this store writes; nil records the
	// user agent only.
	Source SourceFunc
}

// Store is the UI session component of one replica (R-AUTH-010 to R-AUTH-013):
// it creates sessions, implements [Validator] and ends sessions. It keeps no
// session state that a later request depends on: the cache holds positive
// copies of rows invalidated through the area auth, and the newest
// last_seen_at per cached session only saves touch statements (R-AUTH-042).
type Store struct {
	o     Options
	log   *slog.Logger
	m     *metrics
	cache *cache.Cache[string, *entry]

	fmu     sync.Mutex
	flights map[string]*flight

	cmu      sync.Mutex
	csrfLast map[string]time.Time // auth.csrf_failed de-duplication per session, per replica
}

var _ Validator = (*Store)(nil)

// New builds the store and registers its cache.
func New(o Options) (*Store, error) {
	if o.DB == nil {
		return nil, errors.New("session: the store needs the database")
	}
	if o.RevocationBound <= 0 {
		o.RevocationBound = DefaultRevocationBound
	}
	if o.CacheTTL <= 0 {
		o.CacheTTL = DefaultCacheTTL
	}
	if o.MaxCacheEntries <= 0 {
		o.MaxCacheEntries = defaultMaxEntries
	}
	if o.Audit == nil {
		o.Audit = audit.Nop{}
	}
	log := o.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	m, err := newMetrics(o.Metrics)
	if err != nil {
		return nil, err
	}
	s := &Store{o: o, log: log.With("component", "session"), m: m, flights: map[string]*flight{}, csrfLast: map[string]time.Time{}}
	if o.Caches != nil {
		s.cache = registerCache(o.Caches, o.CacheTTL, o.MaxCacheEntries)
	}
	return s, nil
}

// entry is a validated session row on this replica. Its deadlines are on the
// replica's monotonic clock, derived from the database's ages at the read, so
// every replica ends a session at the moment the row says (Q-AUTH-13).
type entry struct {
	id, principalID, principalName, principalStatus, principalKind string
	breakGlass                                                     bool
	authMethod, providerID                                         string
	csrf                                                           []byte
	authenticatedAt                                                time.Time // as stored
	authLocal                                                      time.Time
	absDeadline                                                    time.Time
	idle                                                           time.Duration

	mu       sync.Mutex
	lastSeen time.Time // the newest last_seen_at read for the session, a lower bound of the row's
}

func (e *entry) seen() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastSeen
}

// advance keeps the newer of the kept value and t: the kept value never exceeds
// the row's, so a replica never skips a touch the row needs (R-AUTH-010).
func (e *entry) advance(t time.Time) {
	e.mu.Lock()
	if t.After(e.lastSeen) {
		e.lastSeen = t
	}
	e.mu.Unlock()
}

func (e *entry) info() Info {
	kind := principal.KindUser
	if e.breakGlass {
		kind = principal.KindBreakGlass
	}
	return Info{
		ID:         e.id,
		Principal:  principal.Principal{ID: e.principalID, Kind: kind, Name: e.principalName, Status: principal.Status(e.principalStatus)},
		AuthMethod: e.authMethod, ProviderID: e.providerID, AuthenticatedAt: e.authenticatedAt,
		csrf: e.csrf, authLocal: e.authLocal,
	}
}

// errGone marks a row that does not authenticate: absent, ended, or of a
// principal that is not an active user. The cache never stores it.
var errGone = errors.New("session: not live")

func entryFrom(r q.SessionBySecretRow, at time.Time) (*entry, error) {
	if r.Ended || r.PrincipalStatus != string(principal.StatusActive) || r.PrincipalKind != string(principal.KindUser) {
		return nil, errGone
	}
	return &entry{
		id: r.ID, principalID: r.PrincipalID, principalName: r.PrincipalName, principalStatus: r.PrincipalStatus,
		principalKind: r.PrincipalKind, breakGlass: r.IsBreakGlass, authMethod: r.AuthMethod, providerID: r.ProviderID,
		csrf: r.CsrfSha256, authenticatedAt: r.AuthenticatedAt.Time,
		authLocal:   at.Add(-seconds(r.AuthenticatedAgoS)),
		absDeadline: at.Add(seconds(r.AbsoluteLeftS)),
		idle:        seconds(r.IdleS),
		lastSeen:    at.Add(-seconds(r.SeenAgoS)),
	}, nil
}

func seconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

// Validate resolves a session cookie (R-AUTH-010, R-AUTH-023). It returns
// ErrInvalid for every cookie that does not name a live session of an active
// user, and a 503 error when PostgreSQL does not answer, never a stale
// acceptance.
func (s *Store) Validate(ctx context.Context, cookie string) (Info, error) {
	if !WellFormed(cookie) {
		return Info{}, ErrInvalid
	}
	key := string(Hash(cookie))
	e, err := s.lookup(ctx, key)
	if errors.Is(err, errGone) {
		return Info{}, ErrInvalid
	}
	if err != nil {
		return Info{}, err
	}
	if e, err = s.checkDeadlines(ctx, key, e); err != nil {
		if errors.Is(err, errGone) {
			return Info{}, ErrInvalid
		}
		return Info{}, err
	}
	if e.breakGlass && !s.o.BreakGlass.Enabled {
		// R-AUTH-014: a replica that runs with the account disabled ends every
		// break-glass session presented to it, whichever replica opened it.
		if err := s.EndBreakGlass(ctx, EndRevoked, audit.Actor{Kind: "system"}); err != nil {
			s.log.Warn("ending the break-glass sessions of a disabled account failed", "error", err)
		}
		return Info{}, ErrInvalid
	}
	s.touch(ctx, e)
	return e.info(), nil
}

// lookup answers from the cache while the change log is fresh (Q-AUTH-10) and
// from PostgreSQL otherwise, sharing one query per credential.
func (s *Store) lookup(ctx context.Context, key string) (*entry, error) {
	if s.cache != nil && s.o.Fresh != nil && s.o.Fresh.FreshWithin(s.o.RevocationBound) {
		return s.cache.Load(ctx, key, func(ctx context.Context) (*entry, error) { return s.fill(ctx, key) })
	}
	if s.cache != nil {
		s.o.AuthMetrics.Bypassed("changes_stale")
	}
	return s.shared(ctx, key)
}

// fill reads the row. A row that does not authenticate is errGone, so the
// cache keeps positive results only.
func (s *Store) fill(ctx context.Context, key string) (*entry, error) {
	var rows []q.SessionBySecretRow
	err := s.o.DB.Read(ctx, func(db q.DBTX) error {
		var err error
		rows, err = q.New(db).SessionBySecret(ctx, []byte(key))
		return err
	})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errGone
	}
	return entryFrom(rows[0], time.Now())
}

// flight is one lookup that concurrent requests for the same credential share
// while the gate bypasses the cache (R-AUTH-023).
type flight struct {
	started time.Time
	done    chan struct{}
	e       *entry
	err     error
}

// shared joins a running query for key that started less than the revocation
// bound ago, or starts one. The query then started after every change older
// than the bound, so sharing keeps the bound; its result is never cached.
func (s *Store) shared(ctx context.Context, key string) (*entry, error) {
	return s.sharedFill(ctx, key, s.fill)
}

// flightFillTimeout bounds the query of a shared lookup. It runs without the
// cancellation of the request that started it, because the requests that joined
// it must not fail with that request's cancellation.
const flightFillTimeout = 10 * time.Second

func (s *Store) sharedFill(ctx context.Context, key string, fill func(context.Context, string) (*entry, error)) (*entry, error) {
	s.fmu.Lock()
	if f, ok := s.flights[key]; ok && time.Since(f.started) < s.o.RevocationBound {
		s.fmu.Unlock()
		select {
		case <-f.done:
			if f.err != nil {
				return nil, f.err
			}
			return f.e.copyForRequest(), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f := &flight{started: time.Now(), done: make(chan struct{})}
	s.flights[key] = f
	s.fmu.Unlock()

	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), flightFillTimeout)
	f.e, f.err = fill(fctx, key)
	cancel()
	close(f.done)
	s.fmu.Lock()
	if s.flights[key] == f {
		delete(s.flights, key)
	}
	s.fmu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return f.e.copyForRequest(), nil
}

// copyForRequest gives each request of a shared flight its own kept value.
func (e *entry) copyForRequest() *entry {
	c := &entry{
		id: e.id, principalID: e.principalID, principalName: e.principalName, principalStatus: e.principalStatus,
		principalKind: e.principalKind, breakGlass: e.breakGlass, authMethod: e.authMethod, providerID: e.providerID,
		csrf: e.csrf, authenticatedAt: e.authenticatedAt, authLocal: e.authLocal, absDeadline: e.absDeadline, idle: e.idle,
	}
	c.lastSeen = e.seen()
	return c
}

// checkDeadlines applies both deadlines (R-AUTH-010, R-AUTH-011). A deadline
// that has passed on this replica's view is decided from the row: only when the
// row's deadline has passed too does the session end; otherwise the fresher row
// replaces the entry.
func (s *Store) checkDeadlines(ctx context.Context, key string, e *entry) (*entry, error) {
	now := time.Now()
	if now.Before(e.absDeadline) && now.Before(e.seen().Add(e.idle)) {
		return e, nil
	}
	if s.cache != nil {
		s.cache.Invalidate(key)
	}
	fresh, err := s.fill(ctx, key)
	if errors.Is(err, errGone) {
		return nil, ErrInvalid
	}
	if err != nil {
		return nil, err
	}
	now = time.Now()
	switch {
	case !now.Before(fresh.absDeadline):
		s.endIf(ctx, fresh.id, true)
		return nil, ErrInvalid
	case !now.Before(fresh.seen().Add(fresh.idle)):
		if s.endIf(ctx, fresh.id, false) {
			return nil, ErrInvalid
		}
		// Another replica touched the row between the read and the end.
		return s.fill(ctx, key)
	}
	return fresh, nil
}

// endIf ends the session for its absolute or idle deadline with the conditional
// statement of R-AUTH-010 and reports whether the row ended now. Neither end is
// audited (R-AUTH-041), and neither needs a change message: every replica
// decides both deadlines from the row.
func (s *Store) endIf(ctx context.Context, id string, absolute bool) bool {
	var ended []string
	err := s.o.DB.Read(ctx, func(db q.DBTX) error {
		var err error
		if absolute {
			ended, err = q.New(db).EndSessionIfAbsolute(ctx, id)
		} else {
			ended, err = q.New(db).EndSessionIfIdle(ctx, id)
		}
		return err
	})
	if err != nil {
		s.log.Warn("ending an expired session failed; the next request decides again", "session", id, "error", err)
		return absolute // an absolute deadline holds whatever the write did
	}
	return len(ended) > 0 || absolute
}

// touch sends the conditional touch when the request arrives touchEvery or more
// after the newest last_seen_at the replica has read. A failed write is counted
// and never fails the request; the kept value stays, so the next request tries
// again (R-AUTH-010).
func (s *Store) touch(ctx context.Context, e *entry) {
	if time.Since(e.seen()) < touchEvery {
		return
	}
	var ages []float64
	err := s.o.DB.Read(ctx, func(db q.DBTX) error {
		var err error
		ages, err = q.New(db).TouchSession(ctx, e.id)
		return err
	})
	if err != nil {
		s.m.touchFailures.Inc()
		s.log.Debug("session touch failed", "session", e.id, "error", err)
		return
	}
	if len(ages) > 0 {
		e.advance(time.Now().Add(-seconds(ages[0])))
	}
}

// NewSession describes a session to create.
type NewSession struct {
	PrincipalID string
	// BreakGlass selects the auth method break_glass and the shorter timeouts of
	// auth.break_glass (R-AUTH-014).
	BreakGlass bool
	ProviderID string
	// IDPSID is the sid claim; IDToken the raw ID token kept for id_token_hint
	// (R-AUTH-013). Neither is ever returned by an API.
	IDPSID  string
	IDToken string
	// ClientAddr and UserAgent are shown in the session list.
	ClientAddr string
	UserAgent  string
	// ReplaceCookie is the session cookie presented with the sign-in; that
	// session ends (fixation, R-AUTH-010).
	ReplaceCookie string
	// ReplaceID is the session a re-authentication replaces (R-AUTH-011); it ends
	// with reason reauth_rotated if it belongs to the same principal.
	ReplaceID string
}

// Created is a new session: the two cookie values, given to the browser once.
type Created struct {
	ID     string
	Secret string
	CSRF   string
	// Evicted are the sessions the limit ended (R-AUTH-010).
	Evicted []string
}

// Create creates a session in one transaction that first locks the principal's
// row, so concurrent logins of one user on different replicas cannot exceed
// max_per_user (R-AUTH-010). The ended predecessors are recorded in the area
// auth in the same transaction.
func (s *Store) Create(ctx context.Context, n NewSession) (Created, error) {
	c := Created{ID: NewID(), Secret: NewSecret(), CSRF: NewSecret()}
	idle, absolute := s.o.Sessions.IdleTimeout, s.o.Sessions.AbsoluteTimeout
	method := MethodOIDC
	if n.BreakGlass {
		idle, absolute, method = s.o.BreakGlass.IdleTimeout, s.o.BreakGlass.AbsoluteTimeout, MethodBreakGlass
	}
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		c.Evicted = nil
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		locked, err := qs.LockPrincipal(ctx, n.PrincipalID)
		if err != nil {
			return err
		}
		if len(locked) == 0 {
			return fmt.Errorf("session: principal %s does not exist", n.PrincipalID)
		}
		// Checked under the lock: a disable that ran meanwhile has ended the
		// sessions it saw, and one written now would be valid again on the
		// re-enable (R-AUTH-016).
		if locked[0].Status != "active" {
			return ErrPrincipalNotActive
		}
		var ended []endedSession
		// The session a re-authentication replaces is usually the presented
		// cookie's: it ends first, with its own reason, so the cookie's end below
		// finds nothing live.
		if n.ReplaceID != "" {
			rows, err := s.endOwnSession(ctx, qs, n.ReplaceID, n.PrincipalID)
			if err != nil {
				return err
			}
			ended = append(ended, rows...)
		}
		if WellFormed(n.ReplaceCookie) {
			rows, err := qs.EndSessionBySecret(ctx, q.EndSessionBySecretParams{SecretSha256: Hash(n.ReplaceCookie), Reason: EndRevoked})
			if err != nil {
				return err
			}
			for _, r := range rows {
				ended = append(ended, endedSession{r.ID, r.SecretSha256Hex})
			}
		}
		evicted, err := s.evict(ctx, tx, qs, n.PrincipalID)
		if err != nil {
			return err
		}
		ended = append(ended, evicted...)
		for _, e := range evicted {
			c.Evicted = append(c.Evicted, e.id)
		}
		if err := qs.InsertSession(ctx, q.InsertSessionParams{
			ID: c.ID, SecretSha256: Hash(c.Secret), CsrfSha256: Hash(c.CSRF), PrincipalID: n.PrincipalID,
			AuthMethod: method, ProviderID: n.ProviderID, IdpSid: n.IDPSID, IDToken: n.IDToken,
			IdleS: idle.Seconds(), AbsoluteS: absolute.Seconds(), CreatedAddr: n.ClientAddr, UserAgent: truncate(n.UserAgent, 512),
		}); err != nil {
			return err
		}
		return notifyEnded(s.o.Changes, tx, ended)
	})
	if err != nil {
		return Created{}, err
	}
	return c, nil
}

// endOwnSession ends the session a re-authentication replaces, only if it is a
// live session of the same principal.
func (s *Store) endOwnSession(ctx context.Context, qs *q.Queries, id, principalID string) ([]endedSession, error) {
	own, err := qs.LiveSessionsOfPrincipal(ctx, principalID)
	if err != nil {
		return nil, err
	}
	for _, o := range own {
		if o == id {
			rows, err := qs.EndSessionsByID(ctx, q.EndSessionsByIDParams{Ids: []string{id}, Reason: EndReauthRotated})
			if err != nil {
				return nil, err
			}
			var out []endedSession
			for _, r := range rows {
				out = append(out, endedSession{r.ID, r.SecretSha256Hex})
			}
			return out, nil
		}
	}
	return nil, nil
}

// evict ends the oldest live sessions so that one more fits under
// max_per_user, and records auth.session.ended with reason evicted.
func (s *Store) evict(ctx context.Context, tx meta.Tx, qs *q.Queries, principalID string) ([]endedSession, error) {
	live, err := qs.LiveSessionsOfPrincipal(ctx, principalID)
	if err != nil {
		return nil, err
	}
	over := len(live) - s.o.Sessions.MaxPerUser + 1
	if over <= 0 {
		return nil, nil
	}
	rows, err := qs.EndSessionsByID(ctx, q.EndSessionsByIDParams{Ids: live[:over], Reason: EndEvicted})
	if err != nil {
		return nil, err
	}
	var out []endedSession
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, endedSession{r.ID, r.SecretSha256Hex})
		ids = append(ids, r.ID)
	}
	if len(ids) > 0 {
		if err := s.o.Audit.RecordTx(ctx, tx, endedEvent(audit.Actor{Kind: "system"}, EndEvicted, principalID, ids)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// queries returns the sqlc queries on the transaction behind tx.
func queries(tx meta.Tx) (*q.Queries, error) {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return nil, errors.New("session: the transaction does not come from the PostgreSQL store")
	}
	return q.New(ptx), nil
}
