package authz

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The replay of the exposure entries of the revocation journal (endpoints.md R-EP-020, ha-and-helm.md R-HA-054). A
// restore to t undoes the exposure narrowings made after t; replay re-applies them:
//
//   - endpoint.exposure.deleted deletes the rule;
//   - endpoint.exposure.updated goes through the update rule of R-HA-054 (journal.ApplyUpdate) with the comparator
//     of R-EP-020 ([exposureNarrower]); a suspension disables the rule.
//
// A journal object is untrusted input: whoever holds the runtime S3 credential can write one (R-HA-054 "Forged entries
// cannot escalate"). The handler therefore takes the entry as a claim about the restored database: it finds the rule
// by its endpoint and name, which never change, and acts only when the restored rule has the entry's ID and repository
// ID (Q-EP-77). A journaled state that does not read as a rule's state, or an after-state that is no rule the API would
// accept, proves no narrowing, so the update rule suspends the rule (Q-EP-78). Every write notifies the change area
// authz through the replay's notifier (journal.Env); like every replayed change it writes no audit event of its own,
// the replay's lines and system.restore.journal_replayed are the record (Q-AUTHZ-232).

// JournalReplaySubject is the caller of the writes a replay of the revocation journal makes through a service that
// takes a subject (the CI issuers and mappings, whose disables revoke tokens): the system, never a principal, so no
// revoked_by names one. Only this package builds a Subject (R-EP-013).
func JournalReplaySubject() Subject {
	return Subject{Kind: principal.Kind("system"), Name: "revocation-journal"}
}

func init() {
	r := exposureReplay{reg: catalog.Default()}
	journal.Register(journal.Handler{
		Domain: "exposure", Events: []string{EventExposureUpdated, EventExposureDeleted}, Apply: r.apply,
	})
}

// exposureReplay applies the exposure entries. reg is the format catalog, for the name rule of a rule's format (the
// comparator folds package patterns as the matcher does) and for the validation of an after-state.
type exposureReplay struct{ reg *catalog.Registry }

// exposureJournalEntry is the entry exposureEvent writes (R-EP-020): the rule's ID, endpoint, name and repository ID,
// and for an update the states before and after the change.
type exposureJournalEntry struct {
	RuleID       string          `json:"ruleId"`
	Endpoint     string          `json:"endpoint"`
	Name         string          `json:"name"`
	RepositoryID int64           `json:"repositoryId"`
	Before       json.RawMessage `json:"before"`
	After        json.RawMessage `json:"after"`
}

// exposureJournalState is a rule's access-bearing state as the comparator of R-EP-020 reads it: the rule's endpoint and
// repository ID and its dimensions. Valid is false for a journaled state that did not read under the rules of
// [readExposureState] or is no rule the API would accept; the comparator never proves a narrowing for it.
type exposureJournalState struct {
	Valid        bool
	Endpoint     string
	RepositoryID int64
	State        ExposureRuleInput
}

// exposureNarrower is the comparator narrows(current, after) of R-EP-020. Names is the name rule of the rule's format:
// package patterns are folded with it, as the matcher folds them (R-AUTHZ-005); paths, version globs and tag globs
// compare as written. Patterns are compared as strings, never by glob containment, and a range only by equality
// (Q-EP-79), so the comparator answers false whenever it cannot tell.
type exposureNarrower struct{ Names catalog.Names }

var _ journal.Narrower[exposureJournalState] = exposureNarrower{}

// Narrows answers true only when after exposes nothing on its endpoint that current does not.
func (n exposureNarrower) Narrows(current, after exposureJournalState) bool {
	if !current.Valid || !after.Valid {
		return false
	}
	// 1. Endpoint and repository ID are equal.
	if current.Endpoint != after.Endpoint || current.RepositoryID != after.RepositoryID {
		return false
	}
	c, a := normInput(current.State), normInput(after.State)
	// 2. after.enabled implies current.enabled, after.prereleases implies current.prereleases.
	if (a.Enabled && !c.Enabled) || (a.Prereleases && !c.Prereleases) {
		return false
	}
	// 3. Per dimension: the exclude list of after holds every exclude pattern of current, and a non-empty include
	// list of current is followed by a non-empty include list of after that is a subset of it. An emptied include
	// list is unrestricted and never narrows.
	pkg := func(p string) string { return fold(n.Names, p) }
	asIs := func(p string) string { return p }
	if !exposureListsNarrow(c.Packages, a.Packages, pkg) || !exposureListsNarrow(c.Paths, a.Paths, asIs) || !exposureListsNarrow(c.Tags, a.Tags, asIs) ||
		!exposureListsNarrow(PatternLists{Include: c.Versions.Include, Exclude: c.Versions.Exclude}, PatternLists{Include: a.Versions.Include, Exclude: a.Versions.Exclude}, asIs) {
		return false
	}
	// 4. The ranges are equal, or current has none.
	return exposureRange(c.Versions) == "" || exposureRange(a.Versions) == exposureRange(c.Versions)
}

// exposureListsNarrow is rule 3 of R-EP-020 for one dimension.
func exposureListsNarrow(current, after PatternLists, norm func(string) string) bool {
	if !exposureHoldsAll(after.Exclude, current.Exclude, norm) {
		return false
	}
	if len(current.Include) == 0 {
		return true
	}
	return len(after.Include) > 0 && exposureHoldsAll(current.Include, after.Include, norm)
}

// exposureHoldsAll reports whether set holds every pattern of want, both normalized by norm.
func exposureHoldsAll(set, want []string, norm func(string) string) bool {
	in := make(map[string]bool, len(set))
	for _, p := range set {
		in[norm(p)] = true
	}
	for _, p := range want {
		if !in[norm(p)] {
			return false
		}
	}
	return true
}

func exposureRange(v VersionLists) string {
	if v.Range == nil {
		return ""
	}
	return *v.Range
}

// readExposureState reads a journaled state strictly: a JSON object with exactly the members of the state that the
// producer writes (stateOfRule), each of its type. A member the comparator does not know, a missing member or another
// type makes the state unreadable (Q-EP-78).
func readExposureState(raw json.RawMessage) (ExposureRuleInput, bool) {
	var st exposureState
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ExposureRuleInput{}, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&st); err != nil || dec.More() {
		return ExposureRuleInput{}, false
	}
	if !exposureSameJSON(raw, mustJSON(st)) {
		return ExposureRuleInput{}, false
	}
	return ExposureRuleInput{
		Enabled: st.Enabled, Packages: st.Packages, Paths: st.Paths, Versions: st.Versions, Prereleases: st.Prereleases, Tags: st.Tags,
	}, true
}

// exposureSameJSON reports whether two JSON documents are equal as values: the same members, the same arrays, the same
// scalars, whatever the spacing and member order.
func exposureSameJSON(a, b []byte) bool {
	ca, oka := exposureCanonicalJSON(a)
	cb, okb := exposureCanonicalJSON(b)
	return oka && okb && bytes.Equal(ca, cb)
}

func exposureCanonicalJSON(b []byte) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || dec.More() {
		return nil, false
	}
	out, err := json.Marshal(v)
	return out, err == nil
}

// journalState is a journaled state of the rule: read strictly and, with the rule's name, repository and format,
// checked as the API checks a rule (R-EP-012). Anything else is not valid.
func (r exposureReplay) journalState(raw json.RawMessage, rule ExposureRule) exposureJournalState {
	in, ok := readExposureState(raw)
	if !ok {
		return exposureJournalState{}
	}
	in.Name, in.Description, in.Repository = rule.Name, "", rule.Repository
	in = normInput(in)
	svc := r.pure()
	if len(validateSyntax(in, "")) > 0 || len(svc.validateForFormat(in, rule.Format, "")) > 0 {
		return exposureJournalState{}
	}
	return exposureJournalState{Valid: true, Endpoint: rule.Endpoint, RepositoryID: rule.RepositoryID, State: in}
}

// pure is a rule service for its pure helpers: reading a row as a rule and validating a rule against its format.
// It holds no database, recorder or change notifier, so it writes nothing.
func (r exposureReplay) pure() *ExposureService {
	return &ExposureService{o: ExposureServiceOptions{Ceiling: NewExposureCeiling(nil, r.reg)}}
}

// detail is the detail of a result: a reason, or the journaled after-state of a suspended rule.
func exposureDetail(kv ...any) json.RawMessage {
	m := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[fmt.Sprint(kv[i])] = kv[i+1]
	}
	return mustJSON(m)
}

func (r exposureReplay) apply(ctx context.Context, tx meta.Tx, env journal.Env, o journal.Object) (journal.Result, error) {
	var e exposureJournalEntry
	if !o.Into(&e) || !journal.IsUUID(e.RuleID) || e.Endpoint == "" || e.Name == "" || e.RepositoryID <= 0 {
		return o.Malformed("the entry names no exposure rule"), nil
	}
	res := o.Result("", e.Endpoint+"/"+e.Name)
	qs, err := queriesOf(tx)
	if err != nil {
		return res, err
	}
	// The lock of the API's writes: the before-state is read under it.
	if err := qs.EndpointsLockRule(ctx, q.EndpointsLockRuleParams{Endpoint: e.Endpoint, Name: e.Name}); err != nil {
		return res, err
	}
	rows, err := qs.EndpointsGetRule(ctx, q.EndpointsGetRuleParams{Endpoint: e.Endpoint, Name: e.Name})
	if err != nil {
		return res, err
	}
	if len(rows) == 0 {
		// No rule of the name: a deletion is in effect, an update has no subject (Q-EP-77).
		res.Outcome = journal.SubjectUnknown
		if o.Event == EventExposureDeleted {
			res.Outcome = journal.AlreadyInEffect
		}
		return res, nil
	}
	if rows[0].ID != e.RuleID || rows[0].RepoID != e.RepositoryID {
		// Another rule under the name, or the rule of another repository: not the entry's subject (Q-EP-77).
		res.Outcome, res.Detail = journal.SubjectUnknown, exposureDetail("reason", "the restored rule of this name has another ID or repository")
		return res, nil
	}
	rule, err := r.pure().ruleOf(getRow(rows[0]))
	if err != nil {
		return res, err
	}
	w := exposureWriter{qs: qs, notes: env, pure: r.pure()}
	if o.Event == EventExposureDeleted {
		res.Outcome = journal.Applied
		return res, w.delete(ctx, tx, rule)
	}
	res.Outcome, res.Detail, err = r.update(ctx, tx, w, rule, e)
	return res, err
}

// update is the update rule of R-HA-054 with the comparator of R-EP-020. An after-state the rule already has is in
// effect (Q-EP-77), so a resumed replay reports it as such.
func (r exposureReplay) update(ctx context.Context, tx meta.Tx, w exposureWriter, rule ExposureRule, e exposureJournalEntry) (journal.Outcome, json.RawMessage, error) {
	cmp := exposureNarrower{Names: namesIn(r.reg, rule.Format)}
	current := exposureJournalState{Valid: true, Endpoint: rule.Endpoint, RepositoryID: rule.RepositoryID, State: rule.ExposureRuleInput}
	before, after := r.journalState(e.Before, rule), r.journalState(e.After, rule)
	if cmp.Narrows(current, after) && cmp.Narrows(after, current) {
		return journal.AlreadyInEffect, nil, nil
	}
	return journal.ApplyUpdate(cmp, current, before, after, journal.UpdateActions{
		Apply: func() error { return w.update(ctx, tx, rule, after.State) },
		Suspend: func() (json.RawMessage, error) {
			if rule.Enabled {
				off := rule.ExposureRuleInput
				off.Enabled = false
				if err := w.update(ctx, tx, rule, off); err != nil {
					return nil, err
				}
			}
			return exposureSuspendedDetail(e.After), nil
		},
	})
}

// exposureSuspendedDetail prints the journaled after-state of a suspended rule, so an administrator can review and re-apply
// it (R-EP-020); a state that is no JSON value is printed as text.
func exposureSuspendedDetail(after json.RawMessage) json.RawMessage {
	if c, ok := exposureCanonicalJSON(after); ok {
		return exposureDetail("enabled", false, "after", json.RawMessage(c))
	}
	return exposureDetail("enabled", false, "after", string(after))
}

// exposureWriter makes the replay's writes of a rule the way the rule service makes them: the row under the lock the
// handler holds and the change area authz, so a replica that starts afterwards builds its grant index with it.
type exposureWriter struct {
	qs    *q.Queries
	notes journal.Notifier
	pure  *ExposureService
}

var errExposureRuleVanished = errors.New("authz: the exposure rule vanished under its lock")

// update sets the access-bearing dimensions of the rule to st; the name, the repository and the description stay.
func (w exposureWriter) update(ctx context.Context, tx meta.Tx, rule ExposureRule, st ExposureRuleInput) error {
	st = normInput(st)
	rows, err := w.qs.EndpointsUpdateRule(ctx, q.EndpointsUpdateRuleParams{
		Description: rule.Description, Enabled: st.Enabled, Packages: mustText(st.Packages), Paths: mustText(st.Paths),
		Versions: mustText(st.Versions), Prereleases: st.Prereleases, DistTags: mustText(st.Tags),
		Endpoint: rule.Endpoint, Name: rule.Name, Expected: rule.Revision,
	})
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return errExposureRuleVanished
	}
	return w.notes.Notify(tx, AreaAuthz, payloadVersion, changePayload{Type: "exposure_rule", ID: rule.ID})
}

// delete removes the rule.
func (w exposureWriter) delete(ctx context.Context, tx meta.Tx, rule ExposureRule) error {
	rows, err := w.qs.EndpointsDeleteRule(ctx, q.EndpointsDeleteRuleParams{Endpoint: rule.Endpoint, Name: rule.Name, Expected: rule.Revision})
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return errExposureRuleVanished
	}
	return w.notes.Notify(tx, AreaAuthz, payloadVersion, changePayload{Type: "exposure_rule", ID: rule.ID})
}
