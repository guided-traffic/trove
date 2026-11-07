package ci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The replay of the CI issuer and mapping entries of the revocation journal (R-CI-020, R-HA-054, Q-CI-11). A restore
// to t undoes the disables, deletions and narrowings of issuers and mappings made after t; replay re-applies them:
//
//   - ci.issuer.disabled and ci.issuer.deleted disable the issuer as R-CI-002 does (its unexpired tokens revoked);
//     a deleted issuer is disabled, never deleted;
//   - ci.mapping.disabled and ci.mapping.deleted disable the mapping as R-CI-019 does (tokens revoked, principal
//     disabled), never delete it, and suspend every later enabled mapping of its tier, so no job falls through to a
//     mapping that grants more (Q-CI-11);
//   - ci.issuer.updated and ci.mapping.updated go through the update rule of R-HA-054 with the comparators of
//     R-CI-020 ([issuerNarrower], [mappingNarrower]); a suspension disables the object as above.
//
// A journal object is untrusted input (R-HA-054 "Forged entries cannot escalate"): the handlers find the subject by
// its ID and act only when the restored object is the entry's (the mapping's issuer and tier). A record the
// comparator cannot read in full proves no narrowing (R-CI-020 "a field the comparator does not know"). Every write
// goes through the same helpers as the API's writes, with the replay's notifier (journal.Env), so it notifies the
// change areas (ci_mappings, authz, and auth for the revoked tokens); like every replayed change it writes no audit
// event of its own, the replay's lines and system.restore.journal_replayed are the record (Q-AUTHZ-232).
//
// ci.token.revoked is replayed with token.revoked by the identity domain (internal/auth/token), the row of R-HA-054
// that both events share; this file registers the issuer and mapping events only.

func init() {
	journal.Register(journal.Handler{
		Domain: "ci", Events: []string{audit.EventCIIssuerUpdated, audit.EventCIIssuerDisabled, audit.EventCIIssuerDeleted}, Apply: replayIssuer,
	})
	journal.Register(journal.Handler{
		Domain: "ci", Events: []string{audit.EventCIMappingUpdated, audit.EventCIMappingDisabled, audit.EventCIMappingDeleted}, Apply: replayMapping,
	})
}

// replayCaller is the caller of the replay's writes: the system, never a principal, so no revoked_by names one.
func replayCaller() Actor { return authz.JournalReplaySubject() }

// ciReplayWriter makes the replay's writes with the helpers of the services, over the replay's notifier and a recorder
// that records nothing (Q-AUTHZ-232). Only the helpers that run in the caller's transaction are called on its services.
type ciReplayWriter struct {
	issuers  *Service
	mappings *MappingService
}

func newCIReplayWriter(env journal.Env) ciReplayWriter {
	revoker := &TokenRevoker{o: RevokerOptions{Changes: env, Recorder: audit.Nop{}}}
	return ciReplayWriter{
		issuers:  &Service{o: Options{Changes: env, Recorder: audit.Nop{}, Revoker: revoker}},
		mappings: &MappingService{o: MappingsOptions{Changes: env, Recorder: audit.Nop{}, Revoker: revoker}},
	}
}

// ciDetail is the detail of a result.
func ciDetail(kv ...any) json.RawMessage {
	m := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[fmt.Sprint(kv[i])] = kv[i+1]
	}
	return mustJSON(m)
}

// ciSuspendedDetail prints the journaled after-state of a suspended object, so an administrator can review and
// re-apply it (R-HA-054); a state that is no JSON value is printed as text.
func ciSuspendedDetail(after json.RawMessage) json.RawMessage {
	if c, ok := ciCanonicalJSON(after); ok {
		return ciDetail("enabled", false, "after", json.RawMessage(c))
	}
	return ciDetail("enabled", false, "after", string(after))
}

// --- issuers ---

// issuerJournalEntry is the entry issuerEvent writes: the issuer's ID and name and its record before (and, for
// .updated, after) the change.
type issuerJournalEntry struct {
	IssuerID string          `json:"issuerId"`
	Name     string          `json:"name"`
	Before   json.RawMessage `json:"before"`
	After    json.RawMessage `json:"after"`
}

func replayIssuer(ctx context.Context, tx meta.Tx, env journal.Env, o journal.Object) (journal.Result, error) {
	var e issuerJournalEntry
	if !o.Into(&e) || !journal.IsUUID(e.IssuerID) {
		return o.Malformed("issuerId is not an ID"), nil
	}
	res := o.Result("", e.Name)
	qs, err := queriesOf(tx)
	if err != nil {
		return res, err
	}
	cur, ok, err := lockIssuerByID(ctx, qs, e.IssuerID)
	if err != nil || !ok {
		res.Outcome = journal.SubjectUnknown
		return res, err
	}
	res.Subject = cur.Name
	w := newCIReplayWriter(env)
	if o.Event != audit.EventCIIssuerUpdated {
		if !cur.Enabled {
			res.Outcome = journal.AlreadyInEffect
			return res, nil
		}
		res.Outcome = journal.Applied
		return res, w.disableIssuer(ctx, tx, qs, cur)
	}
	cmp := issuerNarrower{}
	current := issuerJournalState{Valid: true, Record: *recordOf(cur)}
	before, after := readIssuerState(e.Before), readIssuerState(e.After)
	if cmp.Narrows(current, after) && cmp.Narrows(after, current) {
		res.Outcome = journal.AlreadyInEffect
		return res, nil
	}
	res.Outcome, res.Detail, err = journal.ApplyUpdate(cmp, current, before, after, journal.UpdateActions{
		Apply: func() error { return w.updateIssuer(ctx, tx, qs, cur, after.Record) },
		Suspend: func() (json.RawMessage, error) {
			if cur.Enabled {
				if err := w.disableIssuer(ctx, tx, qs, cur); err != nil {
					return nil, err
				}
			}
			return ciSuspendedDetail(e.After), nil
		},
	})
	return res, err
}

// lockIssuerByID takes the issuer row FOR UPDATE, as the API's writes of the issuer and its mappings do, and reads it
// under the lock; ok is false when the restored database holds no issuer of the ID.
func lockIssuerByID(ctx context.Context, qs *q.Queries, id string) (Issuer, bool, error) {
	is, ok, err := loadByID(ctx, qs, id)
	if err != nil || !ok {
		return Issuer{}, false, err
	}
	lock, err := lockIssuer(ctx, qs, is.Name, errs.AnyRevision)
	if err != nil {
		return Issuer{}, false, err
	}
	if lock.ID != id { // the name is immutable; another row under it is not the entry's issuer
		return Issuer{}, false, nil
	}
	return loadByID(ctx, qs, id)
}

// disableIssuer is the disable of R-CI-002: the unexpired tokens revoked, the version moved, the change area
// ci_mappings, ci.issuer.disabled.
func (w ciReplayWriter) disableIssuer(ctx context.Context, tx meta.Tx, qs *q.Queries, cur Issuer) error {
	next := cur
	next.Enabled = false
	_, err := w.issuers.writeUpdate(ctx, tx, qs, replayCaller(), cur, next)
	return err
}

// updateIssuer sets the access-bearing fields of the issuer to the journaled after-state; name, issuer, kind,
// description and source stay (R-CI-020).
func (w ciReplayWriter) updateIssuer(ctx context.Context, tx meta.Tx, qs *q.Queries, cur Issuer, after record) error {
	next := cur
	next.Enabled = after.Enabled
	next.DiscoveryURL, next.JWKSURI, next.CABundleRef = after.DiscoveryURL, after.JWKSURI, after.CABundleRef
	next.AllowedAudiences, next.AllowedAlgorithms, next.AllowedTyp = after.AllowedAudiences, after.AllowedAlgorithms, after.AllowedTyp
	next.ExclusiveAudience, next.AllowMissingTyp, next.ClockSkewSeconds = after.ExclusiveAudience, after.AllowMissingTyp, after.ClockSkewSeconds
	next.RequiredClaims, next.PublicIssuer, next.PinClaims = after.RequiredClaims, after.PublicIssuer, after.PinClaims
	next.SingleUseSubjectTokens, next.MaxTTLSeconds = after.SingleUseSubjectTokens, after.MaxTTLSeconds
	next.UseOutboundProxy, next.JobTokenFallback = after.UseOutboundProxy, after.JobTokenFallback
	_, err := w.issuers.writeUpdate(ctx, tx, qs, replayCaller(), cur, next)
	return err
}

// issuerJournalState is an issuer record as the comparator reads it. Valid is false for a record that did not read
// under the rules of [readIssuerState]; the comparator never proves a narrowing for it.
type issuerJournalState struct {
	Valid  bool
	Record record
}

// readIssuerState reads a journaled issuer record strictly: exactly the members recordOf writes, each of its type.
func readIssuerState(raw json.RawMessage) issuerJournalState {
	var r record
	if !readStrict(raw, &r) {
		return issuerJournalState{}
	}
	return issuerJournalState{Valid: true, Record: r}
}

// issuerNarrower is the comparator narrows(current, after) of R-CI-020 for an issuer: every field of the table is
// equal or differs in the direction the table allows; the identifying fields (id, name, issuer, kind) and the source
// are equal; description and the record's version are ignored.
type issuerNarrower struct{}

var _ journal.Narrower[issuerJournalState] = issuerNarrower{}

// Narrows answers true only when after accepts no subject token that current does not.
func (issuerNarrower) Narrows(current, after issuerJournalState) bool {
	if !current.Valid || !after.Valid {
		return false
	}
	c, a := current.Record, after.Record
	switch {
	case c.ID != a.ID, c.Name != a.Name, c.Issuer != a.Issuer, c.Kind != a.Kind,
		c.Source != a.Source, c.RemovedFromConfiguration != a.RemovedFromConfiguration:
		return false
	// None: must be equal.
	case c.Enabled != a.Enabled, !equalPtr(c.DiscoveryURL, a.DiscoveryURL), !equalPtr(c.JWKSURI, a.JWKSURI),
		!equalPtr(c.CABundleRef, a.CABundleRef), c.UseOutboundProxy != a.UseOutboundProxy, c.PublicIssuer != a.PublicIssuer,
		!sameSet(c.PinClaims, a.PinClaims):
		return false
	// after is a non-empty subset.
	case !narrowedSet(c.AllowedAudiences, a.AllowedAudiences), !narrowedSet(c.AllowedAlgorithms, a.AllowedAlgorithms),
		!narrowedSet(c.AllowedTyp, a.AllowedTyp):
		return false
	// after is a superset.
	case !subset(c.RequiredClaims, a.RequiredClaims):
		return false
	// after is true; after is false.
	case c.ExclusiveAudience && !a.ExclusiveAudience, c.SingleUseSubjectTokens && !a.SingleUseSubjectTokens,
		!c.AllowMissingTyp && a.AllowMissingTyp, !c.JobTokenFallback.Enabled && a.JobTokenFallback.Enabled:
		return false
	// after is not larger.
	case a.ClockSkewSeconds > c.ClockSkewSeconds, !ttlNotLarger(c.MaxTTLSeconds, a.MaxTTLSeconds):
		return false
	}
	return true
}

// --- mappings ---

// mappingJournalEntry is the entry mappingEvent writes: the mapping's ID, CI issuer, name, tier and priority, and
// its record before (and, for .updated, after) the change.
type mappingJournalEntry struct {
	MappingID string          `json:"mappingId"`
	IssuerID  string          `json:"issuerId"`
	Issuer    string          `json:"issuer"`
	Name      string          `json:"name"`
	Tier      string          `json:"tier"`
	Priority  int             `json:"priority"`
	Before    json.RawMessage `json:"before"`
	After     json.RawMessage `json:"after"`
}

func replayMapping(ctx context.Context, tx meta.Tx, env journal.Env, o journal.Object) (journal.Result, error) {
	var e mappingJournalEntry
	if !o.Into(&e) || !journal.IsUUID(e.MappingID) || !journal.IsUUID(e.IssuerID) || e.Tier == "" {
		return o.Malformed("mappingId or issuerId is not an ID"), nil
	}
	res := o.Result("", e.Issuer+"/"+e.Name)
	qs, err := queriesOf(tx)
	if err != nil {
		return res, err
	}
	m, ok, err := lockMappingByID(ctx, qs, e.MappingID)
	if err != nil {
		return res, err
	}
	if !ok || m.IssuerID != e.IssuerID || m.Tier != e.Tier {
		// Missing, or another issuer's or tier's mapping: not the entry's subject.
		res.Outcome = journal.SubjectUnknown
		return res, nil
	}
	res.Subject = m.PrincipalName()
	w := newCIReplayWriter(env)
	if o.Event != audit.EventCIMappingUpdated {
		if !m.Enabled {
			// Its jobs already fall through; nothing to suspend (Q-CI-11).
			res.Outcome = journal.AlreadyInEffect
			return res, nil
		}
		res.Outcome = journal.Applied
		res.Also, err = w.disableMapping(ctx, tx, qs, o, m)
		return res, err
	}
	return w.updateMapping(ctx, tx, qs, o, m, e, res)
}

// lockMappingByID takes the row of the mapping's issuer FOR UPDATE, as the API's mapping writes do, and reads the
// mapping under it; ok is false when the restored database holds no mapping of the ID.
func lockMappingByID(ctx context.Context, qs *q.Queries, id string) (Mapping, bool, error) {
	rows, err := qs.CiMappingByID(ctx, id)
	if err != nil || len(rows) == 0 {
		return Mapping{}, false, err
	}
	if _, ok, err := lockIssuerByID(ctx, qs, rows[0].IssuerID); err != nil || !ok {
		return Mapping{}, false, err
	}
	m, err := mappingByID(ctx, qs, id)
	return m, err == nil, err
}

// updateMapping is the update rule of R-HA-054 with the mapping comparator of R-CI-020. An after-state the mapping
// already has is in effect (Q-EP-77 applies to every update entry), so a resumed replay reports it as such.
func (w ciReplayWriter) updateMapping(ctx context.Context, tx meta.Tx, qs *q.Queries, o journal.Object, m Mapping, e mappingJournalEntry, res journal.Result) (journal.Result, error) {
	cmp := mappingNarrower{}
	current := mappingJournalState{Valid: true, Record: *recordOfMapping(&m)}
	before, after := readMappingState(e.Before), readMappingState(e.After)
	if cmp.Narrows(current, after) && cmp.Narrows(after, current) {
		res.Outcome = journal.AlreadyInEffect
		return res, nil
	}
	var rg resolvedGrants
	if after.Valid {
		var fields []errs.FieldError
		var err error
		// The policies of a pure removal are the mapping's own; a name the restored database does not resolve makes
		// the after-state one the replay cannot write, which proves nothing.
		if rg, fields, err = w.mappings.resolveGrants(ctx, qs, m.Project, m.PrincipalID, after.Record.Grants); err != nil {
			return res, err
		}
		if len(fields) > 0 {
			after = mappingJournalState{}
		}
	}
	var err error
	res.Outcome, res.Detail, err = journal.ApplyUpdate(cmp, current, before, after, journal.UpdateActions{
		Apply: func() error { return w.writeMapping(ctx, tx, qs, m, after.Record, rg) },
		Suspend: func() (json.RawMessage, error) {
			if m.Enabled {
				also, err := w.disableMapping(ctx, tx, qs, o, m)
				if err != nil {
					return nil, err
				}
				res.Also = also
			}
			return ciSuspendedDetail(e.After), nil
		},
	})
	return res, err
}

// writeMapping sets the grants, the lifetime bound and the options of the mapping to the journaled after-state, as a
// PUT does: the policies' principal lists in step (a pure removal only removes entries), the issuer's
// mappings_version, the change areas, ci.mapping.updated. Priority, enabled, subject kinds, condition, name and
// description stay.
func (w ciReplayWriter) writeMapping(ctx context.Context, tx meta.Tx, qs *q.Queries, m Mapping, after mappingRecord, rg resolvedGrants) error {
	rows, err := qs.CiMappingUpdate(ctx, q.CiMappingUpdateParams{
		ID: m.ID, Priority: int32(m.Priority), Enabled: m.Enabled, SubjectKinds: m.SubjectKinds, //nolint:gosec // a stored priority, 1 to 1000000
		Match: conditionJSON(m.Match), Grants: storedGrants(after.Grants), MaxTtlSecs: derefInt(after.MaxTTLSeconds),
		Options: optionsJSON(after.Options), Description: m.Description,
	})
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return errMappingVanished
	}
	if _, _, err := w.mappings.applyBindings(ctx, tx, qs, m.PrincipalID, rg); err != nil {
		return err
	}
	return w.recordMapping(ctx, tx, qs, m)
}

var errMappingVanished = errors.New("ci: the mapping vanished under its issuer's lock")

// recordMapping moves the issuer's mappings_version and notifies ci_mappings, as every write of a mapping does
// (R-CI-009), so a replica's copy of the issuer's mapping set is read again.
func (w ciReplayWriter) recordMapping(ctx context.Context, tx meta.Tx, qs *q.Queries, before Mapping) error {
	return w.mappings.bump(ctx, tx, qs, before.IssuerID)
}

// disableMapping disables an enabled mapping as R-CI-019 does and then suspends every later enabled mapping of its
// tier the same way (Q-CI-11): within a tier the first match decides (R-CI-009), so a disabled mapping lets its jobs
// fall through to a later one, which may grant more. Tiers combine by union, so other tiers are not touched. It
// returns one suspended result per later mapping.
func (w ciReplayWriter) disableMapping(ctx context.Context, tx meta.Tx, qs *q.Queries, o journal.Object, m Mapping) ([]journal.Result, error) {
	if err := w.disableOne(ctx, tx, qs, m); err != nil {
		return nil, err
	}
	rows, err := qs.CiMappingsOfIssuerID(ctx, m.IssuerID)
	if err != nil {
		return nil, err
	}
	all, err := readMappings(ctx, qs, listRows(rows))
	if err != nil {
		return nil, err
	}
	later := fallThrough(all, m)
	out := make([]journal.Result, 0, len(later))
	for _, l := range later {
		if err := w.disableOne(ctx, tx, qs, l); err != nil {
			return nil, err
		}
		out = append(out, journal.Result{
			ID: o.ID, Event: o.Event, Outcome: journal.Suspended, Subject: l.PrincipalName(),
			Detail: ciDetail("reason", "first_match_fall_through", "below", m.PrincipalName(), "enabled", false),
		})
	}
	return out, nil
}

// fallThrough returns the mappings a disable of m lets its jobs fall through to: the enabled mappings of m's issuer
// and tier with a higher priority number, in evaluation order.
func fallThrough(all []Mapping, m Mapping) []Mapping {
	var out []Mapping
	for _, l := range all {
		if l.ID != m.ID && l.IssuerID == m.IssuerID && l.Tier == m.Tier && l.Enabled && l.Priority > m.Priority {
			out = append(out, l)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Priority < out[j].Priority })
	return out
}

// disableOne is the disable of one mapping as a PUT with enabled false makes it (R-CI-019, R-CI-017): its unexpired
// tokens revoked, its principal disabled, the change area authz for the grant index, the row, the issuer's
// mappings_version and ci_mappings, ci.mapping.disabled.
func (w ciReplayWriter) disableOne(ctx context.Context, tx meta.Tx, qs *q.Queries, m Mapping) error {
	if err := w.mappings.revokeTokens(ctx, tx, qs, replayCaller(), m); err != nil {
		return err
	}
	if err := w.mappings.setStatus(ctx, tx, qs, m.PrincipalID, false); err != nil {
		return err
	}
	if err := w.mappings.notifyMappingState(tx, m.ID); err != nil {
		return err
	}
	rows, err := qs.CiMappingUpdate(ctx, q.CiMappingUpdateParams{
		ID: m.ID, Priority: int32(m.Priority), Enabled: false, SubjectKinds: m.SubjectKinds, //nolint:gosec // a stored priority, 1 to 1000000
		Match: conditionJSON(m.Match), Grants: storedGrants(m.Grants), MaxTtlSecs: derefInt(m.MaxTTLSeconds),
		Options: optionsJSON(m.Options), Description: m.Description,
	})
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return errMappingVanished
	}
	return w.recordMapping(ctx, tx, qs, m)
}

// mappingJournalState is a mapping record as the comparator reads it. Valid is false for a record that did not read
// under the rules of [readMappingState]; the comparator never proves a narrowing for it.
type mappingJournalState struct {
	Valid  bool
	Record mappingRecord
}

// readMappingState reads a journaled mapping record strictly: exactly the members recordOfMapping writes, each of its
// type; the condition with the parser of R-CI-010, which refuses an unknown member.
func readMappingState(raw json.RawMessage) mappingJournalState {
	var r mappingRecord
	if !readStrict(raw, &r) {
		return mappingJournalState{}
	}
	return mappingJournalState{Valid: true, Record: r}
}

// mappingNarrower is the comparator narrows(current, after) of R-CI-020 for a mapping: grants a pure removal, the
// lifetime bound not larger, the options in the restricting direction; enabled, priority, subject kinds and the
// condition equal, because each decides which mapping is a tier's first match; the identifying fields (id, name, CI
// issuer, tier) equal; description and the record's version ignored.
type mappingNarrower struct{}

var _ journal.Narrower[mappingJournalState] = mappingNarrower{}

// Narrows answers true only when after grants no job anything current does not.
func (mappingNarrower) Narrows(current, after mappingJournalState) bool {
	if !current.Valid || !after.Valid {
		return false
	}
	c, a := current.Record, after.Record
	switch {
	case c.ID != a.ID, c.Name != a.Name, c.Issuer != a.Issuer, c.Tier != a.Tier, c.Project != a.Project:
		return false
	case c.Enabled != a.Enabled, c.Priority != a.Priority, !sameSet(c.SubjectKinds, a.SubjectKinds), !sameNode(c.Match, a.Match):
		return false
	case !ttlNotLarger(c.MaxTTLSeconds, a.MaxTTLSeconds), !optionsNarrow(c.Options, a.Options):
		return false
	}
	return grantsNarrow(c.Grants, a.Grants)
}

// optionsNarrow: options.cross_project_source may become deny, options.allow_unprotected_refs may become false.
func optionsNarrow(c, a claims.Options) bool {
	cps := func(o claims.Options) string {
		if o.CrossProjectSource == "" {
			return claims.CrossProjectDeny
		}
		return o.CrossProjectSource
	}
	if cps(a) != cps(c) && cps(a) != claims.CrossProjectDeny {
		return false
	}
	return c.AllowUnprotectedRefs || !a.AllowUnprotectedRefs
}

// grantsNarrow is the pure removal of R-CI-020: policies and groups are subsets; every inline grant of after narrows
// an inline grant of current; the result is not empty.
func grantsNarrow(c, a claims.Spec) bool {
	if a.Empty() || !subset(a.Policies, c.Policies) || !subset(a.Groups, c.Groups) {
		return false
	}
	for _, g := range a.Inline {
		if !slices.ContainsFunc(c.Inline, func(h claims.InlineGrant) bool { return inlineNarrows(h, g) }) {
			return false
		}
	}
	return true
}

// inlineNarrows: the repositories, the actions and the include list of the packages or paths may be non-empty
// subsets; the excludes, the ownership and every template stay unchanged, and the grant keeps its dimension.
func inlineNarrows(c, a claims.InlineGrant) bool {
	acts := func(as []claims.Action) []string {
		out := make([]string, len(as))
		for i, x := range as {
			out[i] = string(x)
		}
		return out
	}
	if c.Ownership != a.Ownership || !narrowedSet(c.Repositories, a.Repositories) || !narrowedSet(acts(c.Actions), acts(a.Actions)) {
		return false
	}
	return patternsNarrow(c.Packages, a.Packages) && patternsNarrow(c.Paths, a.Paths)
}

func patternsNarrow(c, a *claims.PatternSet) bool {
	if c == nil || a == nil {
		return c == nil && a == nil
	}
	return sameSet(c.Exclude, a.Exclude) && narrowedSet(c.Include, a.Include)
}

// --- the comparison helpers ---

// narrowedSet reports whether after is current, or a non-empty subset of it. An emptied list never narrows: an empty
// list is unrestricted for patterns, and refused for audiences, algorithms, repositories and actions.
func narrowedSet(current, after []string) bool {
	return sameSet(current, after) || (len(after) > 0 && subset(after, current))
}

// subset reports whether every element of a is in b.
func subset(a, b []string) bool {
	for _, x := range a {
		if !slices.Contains(b, x) {
			return false
		}
	}
	return true
}

func sameSet(a, b []string) bool { return subset(a, b) && subset(b, a) }

func equalPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// ttlNotLarger: a nil bound is unbounded.
func ttlNotLarger(current, after *int64) bool {
	switch {
	case current == nil:
		return true
	case after == nil:
		return false
	}
	return *after <= *current
}

// sameNode compares two conditions as values.
func sameNode(a, b claims.Node) bool {
	ca, oka := ciCanonicalJSON(mustJSON(a))
	cb, okb := ciCanonicalJSON(mustJSON(b))
	return oka && okb && bytes.Equal(ca, cb)
}

// readStrict decodes a journaled record into v: a JSON object without a member v does not know, and with every
// member v writes, each of its type. The record must survive a round trip through v unchanged as a value.
func readStrict(raw json.RawMessage, v any) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil || dec.More() {
		return false
	}
	a, oka := ciCanonicalJSON(raw)
	b, okb := ciCanonicalJSON(mustJSON(v))
	return oka && okb && bytes.Equal(a, b)
}

func ciCanonicalJSON(b []byte) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || dec.More() {
		return nil, false
	}
	out, err := json.Marshal(v)
	return out, err == nil
}
