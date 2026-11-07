package ci

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/ci/reason"
	"github.com/guided-traffic/trove/internal/ci/subject"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The exchange's one transaction (R-CI-007 steps 3 to 10, R-CI-018, R-CI-020, Q-CI-15, Q-CI-49): the issuer row FOR
// SHARE and the versions compared with what the replica's copies hold, which confirms the selection made on the copy
// before the transaction; on a mismatch the second validation against the re-read issuer (R-CI-004 steps 5 to 9,
// with the key set already held) and the evaluation of the mapping set read from PostgreSQL FOR SHARE; the restore
// cut-off; the jti; the selection's failure, the bundle, the scope, the lifetimes (Q-CI-47), the token row, the
// issuance record and ci.exchange.succeeded. Everything between the statements is
// computed in memory: no Valkey, issuer or other service is called (R-ARCH-019). Any failure rolls the
// transaction back, so a refused or failed exchange leaves no trace of its jti.

// exchangeQueries are the statements of ci_exchange.sql the transaction runs; *q.Queries implements them, and the
// unit tests of the second mismatch replace them (ExchangeService.queries).
type exchangeQueries interface {
	CiExchangeLockMappings(ctx context.Context, ids []string) ([]q.CiExchangeLockMappingsRow, error)
	CiExchangeClock(ctx context.Context) (q.CiExchangeClockRow, error)
	CiExchangeRestoredAt(ctx context.Context) (int64, error)
	CiExchangeUseJTI(ctx context.Context, arg q.CiExchangeUseJTIParams) (int32, error)
	CiExchangeInsertIssuance(ctx context.Context, arg q.CiExchangeInsertIssuanceParams) error
}

// pgQueries is the statements of the PostgreSQL store on the exchange's transaction.
func pgQueries(tx meta.Tx) (exchangeQueries, error) { return queriesOf(tx) }

// exchangeInput is what steps 1 and 2 established outside the transaction, and the selection on the replica's copy
// of the mapping set (Q-CI-49): the transaction only confirms the versions it was made at.
type exchangeInput struct {
	unverified subject.Unverified
	keys       *heldKeys
	issuer     Issuer
	verified   subject.Verified
	set        MappingSet
	sel        Selection
}

// evaluation is steps 4 to 7 on one mapping set.
type evaluation struct {
	set MappingSet
	sel Selection
}

// issued is what the transaction produced.
type issued struct {
	res ExchangeResult
}

// issue runs the transaction. The transaction runner may run fn again after a failover, so fn starts each attempt
// from the input and leaves its findings in the trace only through the attempt that ends.
func (s *ExchangeService) issue(ctx context.Context, req ExchangeRequest, p params, in exchangeInput, st *trace) (ExchangeResult, error) {
	var out issued
	var at trace
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		at = *st
		var err error
		out, err = s.inTx(ctx, tx, req, p, in, &at)
		return err
	})
	*st = at
	if st.stale {
		s.o.Issuers.Reload(ctx, in.issuer)
	}
	if err != nil {
		return ExchangeResult{}, s.unavailable(err)
	}
	return out.res, nil
}

func (s *ExchangeService) inTx(ctx context.Context, tx meta.Tx, req ExchangeRequest, p params, in exchangeInput, st *trace) (issued, error) {
	qs, err := s.queries(tx)
	if err != nil {
		return issued{}, err
	}
	row, err := s.o.Issuers.LockShareTx(ctx, tx, in.issuer.ID)
	if e, ok := errs.As(err); ok && e.Kind == errs.NotFound {
		return issued{}, refuse(reason.UnknownIssuer, "the CI issuer of the token's iss no longer exists")
	}
	if err != nil {
		return issued{}, err
	}
	v := in.verified
	ev, current, err := s.evaluateCached(ctx, qs, row, in)
	if err != nil {
		return issued{}, err
	}
	if !current {
		st.stale = true
		if row.Version != in.issuer.Version {
			// The issuer's settings changed under the copy: R-CI-004 steps 5 to 9 again against the re-read row,
			// with the keys this exchange already holds (R-CI-007).
			if err := subject.CheckIssuer(true, row.Enabled); err != nil {
				return issued{}, s.failure(err)
			}
			if v, err = in.unverified.Verify(ctx, row.Settings(), in.keys, s.now()); err != nil {
				return issued{}, s.failure(err)
			}
			if ee := checkJTI(row, v); ee != nil {
				return issued{}, ee
			}
		}
		if ev, err = s.evaluateFresh(ctx, tx, qs, row, v.Claims); err != nil {
			return issued{}, err
		}
	}
	if ee := s.replay(ctx, qs, row, v, st); ee != nil {
		return issued{}, ee
	}
	if ev.sel.Failure != "" {
		return issued{}, refuse(ev.sel.Failure, ev.sel.Detail)
	}
	return s.mint(ctx, tx, qs, req, p, row, v, ev.sel)
}

// evaluateCached confirms the selection made on the replica's copy before the transaction (Q-CI-49) against the
// locked issuer row and the contributing mapping rows (FOR SHARE): current is false when any of them changed
// (Q-CI-15). Every mapping write takes the issuer row FOR UPDATE and moves its mappings_version, and this
// transaction holds the row FOR SHARE, so equal versions mean that the copy is the committed set and that it stays
// so until the commit: the selection is the one an evaluation inside the transaction would make, and the
// transaction evaluates nothing on this path.
func (s *ExchangeService) evaluateCached(ctx context.Context, qs exchangeQueries, row Issuer, in exchangeInput) (evaluation, bool, error) {
	if row.Version != in.issuer.Version || row.MappingsVersion != in.set.MappingsVersion {
		return evaluation{}, false, nil
	}
	same, err := s.matchedUnchanged(ctx, qs, in.sel)
	if err != nil || !same {
		return evaluation{}, false, err
	}
	return evaluation{set: in.set, sel: in.sel}, true, nil
}

// evaluateFresh reads the mapping set FOR SHARE in the transaction, which refreshes the replica's copy, and
// evaluates it. A set that disagrees with the locked issuer row, or matched rows that changed again, are the second
// mismatch: configuration_changed (R-CI-007). Under the lock protocol of R-CI-009 neither can happen (every
// mapping write locks the issuer row this transaction holds FOR SHARE); a writer outside the protocol can cause it.
// This is the one path that evaluates inside the transaction (Q-CI-49): only after the copy was found behind, at
// most once per exchange, over a set bounded by ci.mappings.max_per_issuer and the condition limits, with the
// compiled conditions and without a trace.
func (s *ExchangeService) evaluateFresh(ctx context.Context, tx meta.Tx, qs exchangeQueries, row Issuer, c claims.Claims) (evaluation, error) {
	set, err := s.o.Mappings.SetTx(ctx, tx, row.ID)
	if err != nil {
		return evaluation{}, err
	}
	if set.MappingsVersion != row.MappingsVersion || set.IssuerVersion != row.Version {
		return evaluation{}, configurationChanged()
	}
	sel := Select(set, SubjectIDToken, c)
	same, err := s.matchedUnchanged(ctx, qs, sel)
	if err != nil {
		return evaluation{}, err
	}
	if !same {
		return evaluation{}, configurationChanged()
	}
	return evaluation{set: set, sel: sel}, nil
}

func configurationChanged() *ExchangeError {
	return &ExchangeError{Reason: reason.ConfigurationChanged, Description: "the configuration of the issuer changed during the exchange; retry", RetryAfter: time.Second}
}

// matchedUnchanged re-reads the contributing mappings FOR SHARE and reports whether each is still enabled and at
// the version the evaluation used (R-CI-007).
func (s *ExchangeService) matchedUnchanged(ctx context.Context, qs exchangeQueries, sel Selection) (bool, error) {
	if len(sel.Contributions) == 0 {
		return true, nil
	}
	ids := make([]string, 0, len(sel.Contributions))
	want := map[string]int64{}
	for _, c := range sel.Contributions {
		ids = append(ids, c.Mapping.ID)
		want[c.Mapping.ID] = c.Mapping.Version
	}
	rows, err := qs.CiExchangeLockMappings(ctx, ids)
	if err != nil {
		return false, err
	}
	if len(rows) != len(ids) {
		return false, nil
	}
	for _, r := range rows {
		if !r.Enabled || r.Version != want[r.ID] {
			return false, nil
		}
	}
	return true, nil
}

// replay is the restore cut-off and the jti upsert (R-CI-018), with the settings of the re-read issuer row. Under
// single use a second exchange of a jti is subject_token_replayed; without it the count is kept and, past
// ci.replay.warn_after_exchanges, the exchange records ci.exchange.repeated.
func (s *ExchangeService) replay(ctx context.Context, qs exchangeQueries, row Issuer, v subject.Verified, st *trace) *ExchangeError {
	if row.SingleUseSubjectTokens {
		us, err := qs.CiExchangeRestoredAt(ctx)
		if err != nil {
			return s.unavailable(err)
		}
		skew := time.Duration(row.ClockSkewSeconds) * time.Second
		if us > 0 && !v.IssuedAt.After(time.UnixMicro(us).Add(skew)) {
			return refuse(reason.SubjectTokenPredatesRestore, "the subject token was issued before the last restore of trove finished; request a new one")
		}
	}
	if v.JTI == "" {
		return nil
	}
	n, err := qs.CiExchangeUseJTI(ctx, q.CiExchangeUseJTIParams{IssuerID: row.ID, Jti: v.JTI, ExpiresUnix: ceilUnix(v.ExpiresAt)})
	if err != nil {
		return s.unavailable(err)
	}
	st.jti, st.count = v.JTI, n
	switch {
	case row.SingleUseSubjectTokens && n > 1:
		st.replayed = true
		return refuse(reason.SubjectTokenReplayed, "the issuer allows each subject token once, and this one was exchanged before")
	case !row.SingleUseSubjectTokens && int(n) > s.o.Config.Replay.WarnAfterExchanges:
		st.repeated = true
	}
	return nil
}

// ceilUnix is an instant in whole seconds, rounded up: the replay set never stores a subject expiry earlier than the
// token's own (R-CI-018, Q-CI-51), whatever fraction of a second its exp carries.
func ceilUnix(t time.Time) int64 {
	s := t.Unix()
	if time.Unix(s, 0).Before(t) {
		s++
	}
	return s
}

// mint is steps 8 to 10: the lifetime of the token and of each contribution, the bundle, the scope, the token row,
// the issuance record and the event.
//
// Every contribution is the tier's own (R-CI-009, Q-CI-47): the token belongs to its owner's mapping (tokenOwner:
// the system tier's when it contributes), whose bound with the issuer's and the global bounds is the token's
// lifetime (R-CI-016); a project tier's maxTtlSeconds ends that contribution only, frozen as its expiry on the
// database clock that the token row's expiry counts from; and the bundle carries the authorization epoch read
// here, against which the evaluator later decides whether each contributing mapping is still active.
func (s *ExchangeService) mint(ctx context.Context, tx meta.Tx, qs exchangeQueries, req ExchangeRequest, p params, row Issuer, v subject.Verified, sel Selection) (issued, error) {
	if len(sel.Contributions) == 0 {
		return issued{}, errors.New("ci: a selection without a failure has no contribution")
	}
	clock, err := qs.CiExchangeClock(ctx)
	if err != nil {
		return issued{}, err
	}
	projectID, _ := v.Claims.First(claims.ClaimProjectID)
	projectPath, _ := v.Claims.First("project_path")
	f := claims.Frozen{
		Version: claims.FrozenVersion, Issuer: row.Name, IssuerID: row.ID, ProjectID: projectID, ProjectPath: projectPath,
		CrossProject: sel.CrossProject, AuthzEpoch: clock.AuthzEpoch,
	}
	names := make([]string, 0, len(sel.Contributions))
	for _, c := range sel.Contributions {
		f.Add(c.Frozen())
		names = append(names, c.Mapping.ShortName())
	}
	// Step 8, the scope, before step 9, the lifetime (R-CI-007's order).
	sc, ee := narrow(f, p.items, s.o.Grants.CIScope(&f))
	if ee != nil {
		return issued{}, ee
	}
	var issuerMax *time.Duration
	if row.MaxTTLSeconds != nil {
		d := time.Duration(*row.MaxTTLSeconds) * time.Second
		issuerMax = &d
	}
	cfg := s.o.Config.Token
	life, failure, detail := ComputeLifetime(TTLInput{
		Requested: p.ttl, Default: cfg.DefaultTTL, Max: cfg.MaxTTL, Min: cfg.MinTTL, IssuerMax: issuerMax,
		SubjectExpiresAt: v.ExpiresAt, Now: s.now(),
	}, sel.Contributions)
	if failure != "" {
		return issued{}, refuse(failure, detail)
	}
	dbNow := time.UnixMicro(clock.NowUs).UTC()
	sc.bundle.Contributions = slices.Clone(sc.bundle.Contributions)
	for i := range sc.bundle.Contributions {
		if life.Windows[i] < life.TTL {
			end := dbNow.Add(life.Windows[i])
			sc.bundle.Contributions[i].ExpiresAt = &end
		}
	}
	ttl := life.TTL
	snapshot := v.Claims.Snapshot()
	owner := sel.Contributions[life.Owner].Mapping
	jobID, _ := v.Claims.First("job_id")
	tok, err := s.o.Tokens.IssueCITx(ctx, tx, token.CITRequest{
		OwnerID: owner.PrincipalID, Description: description(row.Name, jobID), Frozen: sc.bundle, Scope: sc.stored, TTL: ttl,
		Claims: snapshot, Source: req.Source,
	})
	if err != nil {
		return issued{}, err
	}
	if err := s.record(ctx, tx, qs, req, row, v, sc, owner, names, snapshot, tok, ttl); err != nil {
		return issued{}, err
	}
	return issued{res: ExchangeResult{
		AccessToken: tok.Secret, IssuedTokenType: TokenTypeAccessToken, TokenType: "Bearer", ExpiresIn: int64(ttl / time.Second),
		Scope: sc.granted, CIIssuer: row.Name, Mappings: names,
	}}, nil
}

// description is the token's description, "CI exchange <issuer> job <job_id>", at most 1024 characters.
func description(issuer, jobID string) string {
	d := "CI exchange " + issuer
	if jobID != "" {
		d += " job " + jobID
	}
	if utf8.RuneCountInString(d) > 1024 {
		d = string([]rune(d)[:1024])
	}
	return d
}

// record writes the issuance record (R-CI-020) beside the token row and ci.exchange.succeeded, both in the
// transaction. The record's expiry is the token row's.
func (s *ExchangeService) record(ctx context.Context, tx meta.Tx, qs exchangeQueries, req ExchangeRequest, row Issuer, v subject.Verified, sc scoped,
	owner Mapping, names []string, snapshot map[string]any, tok token.CITIssued, ttl time.Duration,
) error {
	claimsJSON, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	grants, err := json.Marshal(sc.bundle)
	if err != nil {
		return err
	}
	first := func(name string) string { val, _ := v.Claims.First(name); return val }
	refProtected := first("ref_protected")
	if refProtected != "true" && refProtected != "false" {
		refProtected = ""
	}
	if err := qs.CiExchangeInsertIssuance(ctx, q.CiExchangeInsertIssuanceParams{
		TokenID: tok.ID, IssuerID: row.ID, MappingIds: sc.bundle.MappingIDs, MappingNames: names, SubjectKind: SubjectIDToken,
		Jti: v.JTI, ProjectID: first(claims.ClaimProjectID), ProjectPath: first("project_path"), NamespaceID: first("namespace_id"),
		PipelineID: first("pipeline_id"), JobID: first("job_id"), Ref: first("ref"), RefType: first("ref_type"), RefProtected: refProtected,
		PipelineSource: first("pipeline_source"), Environment: first("environment"), Sha: first("sha"), UserID: first("user_id"),
		UserLogin: first("user_login"), Claims: string(claimsJSON), Grants: string(grants), RequestedScope: req.Scope,
		GrantedScope: sc.granted, CrossProject: sc.bundle.CrossProject, ExpiresUs: tok.ExpiresAt.UnixMicro(),
		ClientIp: req.Source.ClientAddr, UserAgent: boundedAgent(req.Source.UserAgent),
	}); err != nil {
		return err
	}
	d := map[string]any{
		"tokenId": tok.ID, "issuer": row.Name, "mappings": sc.bundle.Mappings, "claims": snapshot, "grantedScope": sc.granted,
		"ttlSeconds": int64(ttl / time.Second), "crossProject": sc.bundle.CrossProject,
	}
	if req.Scope != "" {
		d["requestedScope"] = req.Scope
	}
	if v.JTI != "" {
		d["jti"] = v.JTI
	}
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return s.o.Recorder.RecordTx(ctx, tx, auditSucceeded(req, owner, tok, b))
}

// boundedAgent cuts a user agent to 1024 bytes on a rune boundary; the column is text, the header is the caller's.
func boundedAgent(ua string) string {
	if len(ua) <= 1024 {
		return ua
	}
	ua = ua[:1024]
	for !utf8.ValidString(ua) {
		ua = ua[:len(ua)-1]
	}
	return strings.TrimSpace(ua)
}

// auditSucceeded is ci.exchange.succeeded: the actor is the mapping that owns the token, the subject the token.
func auditSucceeded(req ExchangeRequest, owner Mapping, tok token.CITIssued, details []byte) audit.Event {
	return audit.Event{
		Type: audit.EventCIExchangeSucceeded, Outcome: audit.Success,
		Actor:   audit.Actor{Kind: string(principal.KindCI), ID: owner.PrincipalID, Name: owner.PrincipalName()},
		Source:  req.Source,
		Subject: audit.Subject{Kind: "token", ID: tok.ID, Name: tok.Hint}, Details: details,
	}
}
