package ci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/ci/reason"
	"github.com/guided-traffic/trove/internal/ci/subject"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
)

// RateLimiter is one of the exchange's cluster limiters (R-CI-021): *cluster.Limiter.
type RateLimiter interface {
	Take(ctx context.Context, bucket string) cluster.Verdict
}

// TokenIssuer issues the token row in the exchange's transaction: *token.Service (work package W5).
type TokenIssuer interface {
	IssueCITx(ctx context.Context, tx meta.Tx, r token.CITRequest) (token.CITIssued, error)
}

// ExchangeOptions configure [NewExchange].
type ExchangeOptions struct {
	// DB runs the exchange's one transaction.
	DB meta.Runner
	// Issuers resolves the issuer, its key source and its FOR SHARE re-read; Mappings the mapping set.
	Issuers  Issuers
	Mappings Mappings
	// Tokens writes the token row (IssueCITx); Grants answers the scope questions from the grant index.
	Tokens TokenIssuer
	Grants GrantIndexes
	// Recorder records the events; nil discards them. Metrics counts; nil counts nothing.
	Recorder audit.Recorder
	Metrics  *Metrics
	// Config is the ci section; BaseURL is server.base_url, which audience and resource must name.
	Config  config.CI
	BaseURL string
	// AddressLimiter and SubjectLimiter are ci_exchange_address and ci_exchange_subject.
	AddressLimiter, SubjectLimiter RateLimiter
	Log                            *slog.Logger
	// Now overrides the clock, for tests.
	Now func() time.Time
}

// ExchangeService implements [Exchanger]. It is a component of the replica: Flush, which the shutdown runs before the
// audit buffer's own, writes the denial counts that are left (R-CI-021).
type ExchangeService struct {
	o       ExchangeOptions
	exempt  []netip.Prefix
	denials *denials
	// queries are the transaction's statements: pgQueries, a fake in the unit tests.
	queries func(meta.Tx) (exchangeQueries, error)
}

var _ Exchanger = (*ExchangeService)(nil)

// NewExchange creates the exchange.
func NewExchange(o ExchangeOptions) (*ExchangeService, error) {
	if o.DB == nil || o.Issuers == nil || o.Mappings == nil || o.Tokens == nil || o.Grants == nil {
		return nil, errors.New("ci: the exchange needs a database, the issuers, the mappings, the token issuer and the grant index")
	}
	if o.Recorder == nil {
		o.Recorder = audit.Nop{}
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	s := &ExchangeService{o: o, denials: newDenials(o.Recorder, o.Now), queries: pgQueries}
	for _, c := range o.Config.Exchange.RateLimit.ExemptCIDRs {
		p, err := parsePrefix(c)
		if err != nil {
			return nil, fmt.Errorf("ci.exchange.rate_limit.exempt_cidrs: %w", err)
		}
		s.exempt = append(s.exempt, p)
	}
	return s, nil
}

// parsePrefix reads a CIDR or a single address.
func parsePrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		return p.Masked(), err
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()), nil
}

// Name implements the composition root's Component.
func (s *ExchangeService) Name() string { return "ci-exchange" }

// Start implements the Component; the exchange has nothing to start.
func (s *ExchangeService) Start(context.Context) error { return nil }

// Flush writes the denial counts that are left, in the flush phase of the shutdown (R-ARCH-024 step 6): the components
// flush in reverse start order, so the audit buffer, started before the exchange, writes them after this.
func (s *ExchangeService) Flush(context.Context) error {
	s.denials.stop()
	return nil
}

// Stop ends the aggregation; a denial after the flush is written at once.
func (s *ExchangeService) Stop(context.Context) error {
	s.denials.stop()
	return nil
}

func (s *ExchangeService) now() time.Time { return s.o.Now() }

// CheckAddress is the limiter ci_exchange_address (R-CI-021).
func (s *ExchangeService) CheckAddress(ctx context.Context, src audit.Source) error {
	if s.o.AddressLimiter == nil || s.isExempt(src.ClientAddr) {
		return nil
	}
	v := s.o.AddressLimiter.Take(ctx, src.ClientAddr)
	if v.Allowed {
		return nil
	}
	err := &ExchangeError{Reason: reason.RateLimited, Description: "too many exchange requests from this address; retry after the period in Retry-After", RetryAfter: retryAfter(v)}
	s.Refused(ctx, src, err)
	return err
}

func (s *ExchangeService) isExempt(addr string) bool {
	a, err := netip.ParseAddr(addr)
	if err != nil {
		return false
	}
	a = a.Unmap()
	for _, p := range s.exempt {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func retryAfter(v cluster.Verdict) time.Duration {
	if v.RetryAfter <= 0 {
		return time.Second
	}
	return v.RetryAfter
}

// Refused records a refusal of the handler (R-CI-008, R-CI-020).
func (s *ExchangeService) Refused(ctx context.Context, src audit.Source, err *ExchangeError) {
	st := &trace{src: src}
	s.finish(ctx, st, err, s.now())
}

// trace is what one exchange learnt on its way, for the metric, the denial event and the log: what may be recorded
// before the signature is verified (iss, kid) and after (the claim snapshot).
type trace struct {
	src      audit.Source
	iss, kid string
	issuer   string
	verified bool
	snapshot map[string]any
	stale    bool
	// replayed, repeated are the R-CI-018 events to write after the transaction, with the jti's count.
	replayed, repeated bool
	jti                string
	count              int32
}

// Exchange runs R-CI-007's processing order.
func (s *ExchangeService) Exchange(ctx context.Context, req ExchangeRequest) (ExchangeResult, error) {
	start := s.now()
	st := &trace{src: req.Source}
	res, err := s.exchange(ctx, req, st)
	if err != nil {
		var ee *ExchangeError
		if !errors.As(err, &ee) {
			ee = s.internal(err)
		}
		s.finish(ctx, st, ee, start)
		return ExchangeResult{}, ee
	}
	s.finish(ctx, st, nil, start)
	return res, nil
}

func (s *ExchangeService) exchange(ctx context.Context, req ExchangeRequest, st *trace) (ExchangeResult, error) {
	// 1. The request.
	p, ee := s.checkRequest(req)
	if ee != nil {
		return ExchangeResult{}, ee
	}
	// 2. The subject token (R-CI-004): the structural steps, the issuer by the unverified iss, the rest.
	u, err := subject.Parse(req.SubjectToken, s.o.Config.Exchange.MaxSubjectTokenBytes)
	if err != nil {
		return ExchangeResult{}, s.failure(err)
	}
	st.iss, st.kid = bounded(u.Issuer), u.KID
	is, found, err := s.o.Issuers.Resolve(ctx, u.Issuer)
	if err != nil {
		return ExchangeResult{}, s.unavailable(err)
	}
	if err := subject.CheckIssuer(found, is.Enabled); err != nil {
		if found {
			st.issuer = is.Name
		}
		return ExchangeResult{}, s.failure(err)
	}
	st.issuer = is.Name
	if req.CIIssuer != "" && req.CIIssuer != is.Name {
		return ExchangeResult{}, refuse(reason.IssuerMismatch, "ci_issuer does not name the issuer of the token's iss")
	}
	keys := &heldKeys{src: s.o.Issuers.KeySource(is)}
	v, err := u.Verify(ctx, is.Settings(), keys, s.now())
	if err != nil {
		return ExchangeResult{}, s.failure(err)
	}
	st.verified, st.snapshot = true, v.Claims.Snapshot()
	if ee := checkJTI(is, v); ee != nil {
		return ExchangeResult{}, ee
	}
	// The per-subject limit, after validation and before the transaction (R-CI-021).
	if ee := s.checkSubject(ctx, is, v.Claims); ee != nil {
		return ExchangeResult{}, ee
	}
	// The copy of the mapping set, read before the transaction: a miss reads PostgreSQL on a connection of its
	// own, which must not happen while the transaction holds one.
	set, err := s.o.Mappings.Set(ctx, is.ID)
	if err != nil {
		return ExchangeResult{}, s.unavailable(err)
	}
	// Steps 4 to 7 on the copy, before the transaction (Q-CI-49): the transaction holds a connection and the issuer
	// row FOR SHARE only to confirm the versions the copy was read at.
	sel := Select(set, SubjectIDToken, v.Claims)
	return s.issue(ctx, req, p, exchangeInput{unverified: u, keys: keys, issuer: is, verified: v, set: set, sel: sel}, st)
}

// heldKeys is the key source of one exchange: it asks the issuer's key source once and keeps the answer, so the
// second validation inside the transaction (R-CI-007, an issuer whose version changed) runs with the key set the
// replica holds and makes no read or fetch.
type heldKeys struct {
	src  subject.KeySource
	done bool
	keys []subject.Key
	err  error
}

// Keys implements subject.KeySource.
func (h *heldKeys) Keys(ctx context.Context, kid, alg string) ([]subject.Key, error) {
	if !h.done {
		h.keys, h.err = h.src.Keys(ctx, kid, alg)
		h.done = true
	}
	return h.keys, h.err
}

// maxJTI is the longest jti the replay set stores (ci_subject_token_uses.jti, R-CI-018).
const maxJTI = 256

// checkJTI is R-CI-018's rule on the jti: single use needs one, and the replay set stores at most maxJTI characters.
func checkJTI(is Issuer, v subject.Verified) *ExchangeError {
	if v.JTI == "" && is.SingleUseSubjectTokens {
		return refuse(reason.RequiredClaimMissing, "the issuer allows each subject token once, and the token has no jti")
	}
	if utf8.RuneCountInString(v.JTI) > maxJTI {
		return refuse(reason.SubjectTokenMalformed, "the jti claim is longer than 256 characters")
	}
	return nil
}

// checkSubject is the limiter ci_exchange_subject (R-CI-021): per CI issuer and subject, the first of the issuer's
// pin claims the token carries, else sub.
func (s *ExchangeService) checkSubject(ctx context.Context, is Issuer, c claims.Claims) *ExchangeError {
	if s.o.SubjectLimiter == nil {
		return nil
	}
	subj := ""
	for _, name := range is.PinClaims {
		if v, ok := c.First(name); ok {
			subj = name + "=" + v
			break
		}
	}
	if subj == "" {
		if v, ok := c.First("sub"); ok {
			subj = "sub=" + v
		}
	}
	v := s.o.SubjectLimiter.Take(ctx, is.ID+"\x00"+subj)
	if v.Allowed {
		return nil
	}
	return &ExchangeError{Reason: reason.RateLimited, Description: "too many exchanges for this project; retry after the period in Retry-After", RetryAfter: retryAfter(v)}
}

// failure maps an error of the subject validation.
func (s *ExchangeService) failure(err error) *ExchangeError {
	var f *subject.Failure
	switch {
	case errors.As(err, &f):
		return refuse(f.Reason, f.Detail)
	case errors.Is(err, subject.ErrKeysUnavailable):
		return &ExchangeError{Reason: reason.SigningKeysUnavailable, Description: "the signing keys of the issuer are unavailable; retry later", RetryAfter: 30 * time.Second, Err: err}
	}
	return s.unavailable(err)
}

// unavailable maps an error of a read or the transaction: a database that is not available is
// database_unavailable, anything else internal.
func (s *ExchangeService) unavailable(err error) *ExchangeError {
	var ee *ExchangeError
	if errors.As(err, &ee) {
		return ee
	}
	if e, ok := errs.As(err); ok && e.Kind == errs.Unavailable {
		ra := e.RetryAfter
		if ra <= 0 {
			ra = 5 * time.Second
		}
		return &ExchangeError{Reason: reason.DatabaseUnavailable, Description: "the database is not available; retry later", RetryAfter: ra, Err: err}
	}
	return s.internal(err)
}

func (s *ExchangeService) internal(err error) *ExchangeError {
	return &ExchangeError{Reason: reason.Internal, Description: "the exchange failed; the request ID names it in the log", Err: err}
}

// maxLoggedIss bounds the unverified iss in a denial event: a forged token chooses it freely (R-CI-020).
const maxLoggedIss = 256

// bounded cuts a string to maxLoggedIss bytes on a rune boundary.
func bounded(s string) string {
	if len(s) <= maxLoggedIss {
		return s
	}
	s = s[:maxLoggedIss]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// finish counts the exchange, writes the denial or the replay events and logs a failure (R-CI-020, R-CI-021). The
// log line and the event hold the reason and what the trace may hold, never a token.
func (s *ExchangeService) finish(ctx context.Context, st *trace, ee *ExchangeError, start time.Time) {
	if m := s.o.Metrics; m != nil {
		m.ExchangeDuration.WithLabelValues(st.issuer).Observe(s.now().Sub(start).Seconds())
		if st.stale {
			m.MappingSetStale.WithLabelValues(st.issuer).Inc()
		}
	}
	if st.replayed || st.repeated {
		s.recordReplay(ctx, st)
	}
	if ee == nil {
		s.count(st.issuer, OutcomeSuccess, "")
		return
	}
	status, _ := ee.Reason.OAuth()
	outcome := OutcomeDenied
	if status >= 500 {
		outcome = OutcomeError
	}
	s.count(st.issuer, outcome, string(ee.Reason))
	log := s.o.Log.With("reason", string(ee.Reason), "issuer", st.issuer, "request_id", st.src.TraceID)
	if status >= 500 {
		log.Warn("ci exchange failed", "error", ee.Err)
		return
	}
	log.Debug("ci exchange refused", "description", ee.Description)
	s.denials.record(ctx, st.issuer, ee.Reason, s.deniedEvent(st, ee))
}

func (s *ExchangeService) count(issuer, outcome, r string) {
	if s.o.Metrics != nil {
		s.o.Metrics.Exchanges.WithLabelValues(issuer, outcome, r).Inc()
	}
}

// deniedEvent is ci.exchange.denied of one refusal: before the signature is verified only the unverified iss and
// kid, after it the claim snapshot (R-CI-020).
func (s *ExchangeService) deniedEvent(st *trace, ee *ExchangeError) audit.Event {
	d := map[string]any{"reason": string(ee.Reason)}
	if st.issuer != "" {
		d["issuer"] = st.issuer
	}
	if st.verified {
		d["claims"] = st.snapshot
	} else {
		if st.iss != "" {
			d["iss"] = st.iss
		}
		if st.kid != "" {
			d["kid"] = st.kid
		}
	}
	b, _ := json.Marshal(d)
	return audit.Event{
		Type: audit.EventCIExchangeDenied, Outcome: audit.Failure, Reason: string(ee.Reason),
		Actor: audit.Actor{Kind: string(principal.KindAnonymous)}, Source: st.src, Details: b,
	}
}

// recordReplay writes ci.exchange.replayed or ci.exchange.repeated (R-CI-018): the jti and its count. Both go
// through the buffer: the replayed exchange's transaction rolled back.
func (s *ExchangeService) recordReplay(ctx context.Context, st *trace) {
	typ := audit.EventCIExchangeRepeated
	outcome := audit.Success
	if st.replayed {
		typ, outcome = audit.EventCIExchangeReplayed, audit.Failure
	}
	b, _ := json.Marshal(map[string]any{"issuer": st.issuer, "jti": st.jti, "exchangeCount": st.count})
	ev := audit.Event{Type: typ, Outcome: outcome, Actor: audit.Actor{Kind: string(principal.KindAnonymous)}, Source: st.src, Details: b}
	if st.replayed {
		ev.Reason = string(reason.SubjectTokenReplayed)
	}
	s.o.Recorder.Record(context.WithoutCancel(ctx), ev)
}
