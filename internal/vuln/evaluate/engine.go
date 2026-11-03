package evaluate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/internal/vuln/match"
	vmetrics "github.com/guided-traffic/trove/internal/vuln/metrics"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// BatchNames is the number of package names (and of queued advisories) one batch of the jobs evaluates in one transaction
// (R-VULN-011).
const BatchNames = 1000

// Options configure [New].
type Options struct {
	Runner meta.Runner
	// Repos is the replica's repository snapshot.
	Repos repository.Reader
	// Claims is the replica's claim index (repository.Routing).
	Claims ClaimIndex
	// States answers the state of an ecosystem's data from the replica's cache; nil is a replica that has not wired it, which
	// reads as "not complete" on the paths that must not query (the commit hook and the miss).
	States vuln.States
	// Recorder writes the audit events; nil writes none.
	Recorder audit.Recorder
	// Metrics are the collectors of R-VULN-024; nil counts nothing.
	Metrics *vmetrics.Metrics
	// Ecosystems are the ecosystems of the formats that have the adapter.
	Ecosystems []Ecosystem
	// Enabled is vulnerabilities.enabled, Enforcement vulnerabilities.enforcement.
	Enabled     bool
	Enforcement vuln.Enforcement
	// PublicUpstreams is vulnerabilities.matching.public_upstreams by format ID.
	PublicUpstreams map[string][]string
	// Retention is vulnerabilities.findings.resolved_retention.
	Retention time.Duration
	Log       *slog.Logger
	// BatchNames overrides [BatchNames]; tests only.
	BatchNames int
}

// Service is the evaluation: the [vuln.Evaluator] port and the jobs. A replica builds one; it holds no state between calls
// beyond its configuration.
type Service struct {
	o     Options
	rule  ClaimRule
	eco   map[string]Ecosystem
	byFmt map[string]Ecosystem
	log   *slog.Logger
}

var _ vuln.Evaluator = (*Service)(nil)

// New builds the service.
func New(o Options) (*Service, error) {
	switch {
	case o.Runner == nil:
		return nil, errors.New("evaluate: no transaction runner")
	case o.Repos == nil:
		return nil, errors.New("evaluate: no repository snapshot")
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.BatchNames <= 0 {
		o.BatchNames = BatchNames
	}
	if o.Enforcement == "" {
		o.Enforcement = vuln.EnforceBlocks
	}
	s := &Service{o: o, rule: ClaimRule{Claims: o.Claims, Repos: o.Repos}, eco: map[string]Ecosystem{}, byFmt: map[string]Ecosystem{}, log: o.Log}
	for _, e := range o.Ecosystems {
		if e.Name == "" || e.Adapter == nil {
			return nil, fmt.Errorf("evaluate: the ecosystem %q has no adapter", e.Name)
		}
		s.eco[e.Name] = e
		for _, f := range e.Formats {
			s.byFmt[f] = e
		}
	}
	return s, nil
}

// Claims returns the internal-names rule of R-VULN-010 over this service's claim index and snapshot: the one rule the
// evaluation and the client audits share.
func (s *Service) Claims() ClaimRule { return s.rule }

// stats are what one evaluation did, for the metrics.
type stats struct {
	versions    int
	unevaluable int
}

// affEntry is one affected entry of a name with what the evaluation reads of it.
type affEntry struct {
	row store.AffectedRow
}

// nameAdvisories is everything the advisory data says about one name.
type nameAdvisories struct {
	entries []affEntry
	// withdrawn is the set of canonical identifiers whose every entry for the name is withdrawn.
	withdrawn map[string]bool
	// live is the set of canonical identifiers that have a non-withdrawn entry for the name, with the kind (malicious when
	// a record of the group that is not withdrawn says so).
	live map[string]advisory.Kind
}

func groupAffected(rows []store.AffectedRow) map[string]*nameAdvisories {
	out := map[string]*nameAdvisories{}
	total := map[string]map[string]int{}
	gone := map[string]map[string]int{}
	for _, r := range rows {
		na := out[r.Name]
		if na == nil {
			na = &nameAdvisories{withdrawn: map[string]bool{}, live: map[string]advisory.Kind{}}
			out[r.Name] = na
			total[r.Name], gone[r.Name] = map[string]int{}, map[string]int{}
		}
		na.entries = append(na.entries, affEntry{row: r})
		total[r.Name][r.CanonicalID]++
		if r.Withdrawn {
			gone[r.Name][r.CanonicalID]++
		} else if na.live[r.CanonicalID] != advisory.KindMalicious {
			na.live[r.CanonicalID] = r.Kind
		}
	}
	for name, na := range out {
		for id, n := range total[name] {
			if gone[name][id] == n {
				na.withdrawn[id] = true
			}
		}
	}
	return out
}

// matchVersion evaluates one stored version against the entries of its name with the format's own order (R-VULN-009): the
// canonical advisories that affect it, and the number of ranges the order could not evaluate.
func matchVersion(a match.Adapter, na *nameAdvisories, version string, st *stats) map[string]*matched {
	out := map[string]*matched{}
	if na == nil {
		return out
	}
	for _, e := range na.entries {
		r := e.row
		if r.Withdrawn {
			continue
		}
		res := match.Affects(a, r.Entry, version)
		st.unevaluable += res.Unevaluable
		if r.RangesInvalid {
			st.unevaluable++
		}
		if !res.Affected {
			continue
		}
		m := out[r.CanonicalID]
		if m == nil {
			m = &matched{
				ID: r.CanonicalID, Kind: r.Kind, Severity: r.Severity, CVSS: r.CVSS, KnownExploited: r.KnownExploited,
				ModifiedUs: r.ModifiedUs,
			}
			out[r.CanonicalID] = m
		}
		// The kind is that of the records whose entry affects the version (Q-VULN-65): a malicious record of the group that does
		// not affect it makes nothing malicious.
		if r.Kind == advisory.KindMalicious {
			m.Kind = advisory.KindMalicious
		}
		m.FixAvailable = m.FixAvailable || res.FixAvailable
		m.Fixed = union(m.Fixed, r.Entry.Fixed)
	}
	return out
}

// repoLookup resolves a repository id to its view; false for one the scanning does not apply to.
type repoLookup func(id int64) (repoView, bool)

// run is one evaluation in one transaction.
type run struct {
	s       *Service
	tx      meta.Tx
	ev      *store.Eval
	eco     Ecosystem
	repos   repoLookup
	trigger vuln.Trigger
	// canResolve is false while the ecosystem's data is not complete: the evaluation then only adds (R-VULN-011).
	canResolve bool
	// rule is the claims rule of the run: the replica's for a job, the transaction's own for the commit hook (txClaims), whose
	// evaluation must not take a second pool connection.
	rule ClaimRule
	// single is true for the commit hook's evaluation of one version.
	single bool
	st     stats
}

// evaluateNames evaluates stored versions against the advisory data in tx: for the versions given (a commit) or every stored
// version of the names (a job), the findings are upserted and resolved by their identity (R-VULN-012). existing are the
// findings of those names, resolved ones included; a finding whose version is not among the versions is not touched.
func (r *run) evaluateNames(ctx context.Context, names []string, versions []store.StoredVersion, existing []store.Finding) error {
	rows, err := r.ev.AffectedForNames(ctx, r.eco.Name, names)
	if err != nil {
		return err
	}
	byName := groupAffected(rows)
	rekey, err := r.rekeyTargets(ctx, existing, byName)
	if err != nil {
		return err
	}
	type key struct {
		repo          int64
		name, version string
	}
	byKey := map[key][]*store.Finding{}
	for i := range existing {
		f := &existing[i]
		k := key{f.RepoID, f.Name, f.Version}
		byKey[k] = append(byKey[k], f)
	}
	sort.SliceStable(versions, func(i, j int) bool {
		a, b := versions[i], versions[j]
		if a.RepoID != b.RepoID {
			return a.RepoID < b.RepoID
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Version < b.Version
	})
	scopes := map[string]scopeResult{}
	for _, v := range versions {
		rv, ok := r.repos(v.RepoID)
		if !ok || !rv.Settings.Enabled || rv.Ecosystem != r.eco.Name {
			continue
		}
		sk := fmt.Sprintf("%d/%s", v.RepoID, v.Name)
		sc, ok := scopes[sk]
		if !ok {
			var err error
			sc.scope, sc.claimRepo, err = r.s.scopeOf(ctx, rv, v.Name, r.rule)
			if err != nil {
				return err
			}
			scopes[sk] = sc
		}
		var found map[string]*matched
		if sc.scope == ScopeMatched {
			found = matchVersion(r.eco.Adapter, byName[v.Name], v.Version, &r.st)
		}
		r.st.versions++
		var withdrawn map[string]bool
		if na := byName[v.Name]; na != nil {
			withdrawn = na.withdrawn
		}
		if err := r.apply(ctx, rv, v, found, byKey[key{v.RepoID, v.Name, v.Version}], withdrawn, rekey); err != nil {
			return err
		}
	}
	return r.recordClaimed(ctx, names, byName)
}

type scopeResult struct {
	scope     Scope
	claimRepo string
}

// rekeyTargets finds, for the advisory identifiers that findings carry and that are no canonical identifier of the advisory
// data any more, the identifier of the alias group the record belongs to now (R-VULN-006): the findings follow it, resolved ones
// too (Q-VULN-65).
func (r *run) rekeyTargets(ctx context.Context, existing []store.Finding, byName map[string]*nameAdvisories) (map[string]string, error) {
	canonical := map[string]bool{}
	for _, na := range byName {
		for id := range na.live {
			canonical[id] = true
		}
		for id := range na.withdrawn {
			canonical[id] = true
		}
	}
	seen := map[string]bool{}
	var candidates []string
	for _, f := range existing {
		if canonical[f.AdvisoryID] || seen[f.AdvisoryID] {
			continue
		}
		seen[f.AdvisoryID] = true
		candidates = append(candidates, f.AdvisoryID)
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	return r.ev.RekeyTargets(ctx, r.eco.Name, candidates)
}

// apply brings the findings of one stored version in line with what the evaluation found.
func (r *run) apply(ctx context.Context, rv repoView, v store.StoredVersion, found map[string]*matched, existing []*store.Finding,
	withdrawn map[string]bool, rekey map[string]string,
) error {
	// The findings are taken in the order of their ids: two evaluations of one version decide the same way (Q-VULN-65).
	sort.SliceStable(existing, func(i, j int) bool { return existing[i].ID < existing[j].ID })
	// A group whose canonical identifier changed: the finding follows it and keeps its status and history, a resolved one too
	// (a blocked finding that was evicted stays blocked when its advisory joined another group). When the version has a finding
	// of the new identifier already, the two merge, the stronger status first.
	gone := map[*store.Finding]bool{}
	for _, f := range existing {
		if gone[f] || found[f.AdvisoryID] != nil {
			continue
		}
		to := rekey[f.AdvisoryID]
		if to == "" || to == f.AdvisoryID || found[to] == nil {
			continue
		}
		if g := findingOfAdvisory(existing, gone, f, to); g != nil {
			if err := r.merge(ctx, rv, v, found[to], f, g); err != nil {
				return err
			}
			gone[f] = true
			continue
		}
		if err := r.ev.Rekey(ctx, f.ID, to); err != nil {
			return err
		}
		if err := r.ev.AddEvent(ctx, f.ID, vuln.ChangeAdvisoryChanged, f.Status, f.Status, "", map[string]any{"canonicalId": map[string]string{"from": f.AdvisoryID, "to": to}}); err != nil {
			return err
		}
		f.AdvisoryID = to
		f.Revision++
	}
	live := make([]*store.Finding, 0, len(existing))
	for _, f := range existing {
		if !gone[f] {
			live = append(live, f)
		}
	}
	have := map[string]*store.Finding{}
	for _, f := range live {
		have[f.AdvisoryID] = f
	}
	for _, id := range sortedIDs(found) {
		m := found[id]
		if err := r.upsert(ctx, rv, v, m, have[id]); err != nil {
			return err
		}
	}
	if !r.canResolve {
		return nil
	}
	byReason := map[vuln.ResolvedReason][]string{}
	for _, f := range live {
		if f.Status == vuln.StatusResolved || found[f.AdvisoryID] != nil {
			continue
		}
		reason := vuln.ResolvedNoLongerAffected
		if withdrawn[f.AdvisoryID] {
			reason = vuln.ResolvedWithdrawn
		}
		byReason[reason] = append(byReason[reason], f.ID)
	}
	for _, reason := range []vuln.ResolvedReason{vuln.ResolvedWithdrawn, vuln.ResolvedNoLongerAffected} {
		if err := r.resolve(ctx, byReason[reason], reason); err != nil {
			return err
		}
	}
	return nil
}

// findingOfAdvisory returns the finding of the version that carries the advisory identifier, other than self and than a merged one.
func findingOfAdvisory(list []*store.Finding, gone map[*store.Finding]bool, self *store.Finding, id string) *store.Finding {
	for _, f := range list {
		if f != self && !gone[f] && f.AdvisoryID == id {
			return f
		}
	}
	return nil
}

// resolve resolves findings and writes their history.
func (r *run) resolve(ctx context.Context, ids []string, reason vuln.ResolvedReason) error {
	if len(ids) == 0 {
		return nil
	}
	done, err := r.ev.Resolve(ctx, ids, reason)
	if err != nil {
		return err
	}
	return r.s.resolvedEvents(ctx, r.ev, done, reason)
}

func (s *Service) resolvedEvents(ctx context.Context, ev *store.Eval, done []store.Resolved, reason vuln.ResolvedReason) error {
	for _, d := range done {
		if err := ev.AddEvent(ctx, d.ID, vuln.ChangeResolved, d.Old, vuln.StatusResolved, string(reason), nil); err != nil {
			return err
		}
	}
	return nil
}

// transition changes the status of a finding as the evaluation decides it, and only while the finding still is what the
// decision was made on (Q-VULN-65): the update holds the status and the revision that were read. When it lost to a triage that
// came between, the finding is read again and decide runs on what is stored now, so a block or a decision of a person is never
// overwritten by a stale read. decide returns the status to set, false when no change applies any more. The finding is updated
// to what it is after the change.
func (r *run) transition(ctx context.Context, f *store.Finding, decide func(cur *store.Finding) (vuln.Status, bool)) (done bool, from, to vuln.Status, err error) {
	for range transitionAttempts {
		want, ok := decide(f)
		if !ok {
			return false, "", "", nil
		}
		set, err := r.ev.SetStatus(ctx, f.ID, f.Status, f.Revision, want)
		if err != nil {
			return false, "", "", err
		}
		if set {
			from = f.Status
			f.Status, f.BeforeResolution, f.ResolvedReason = want, "", ""
			f.Revision++
			return true, from, want, nil
		}
		cur, exists, err := r.ev.FindingByID(ctx, f.ID)
		if err != nil || !exists {
			return false, "", "", err
		}
		*f = cur
	}
	return false, "", "", nil
}

// transitionAttempts bounds how often a status change reads the finding again after it lost a race.
const transitionAttempts = 3

// flapWindow is how long a resolution must last to stay in the history of a finding that is re-detected: a version that is
// evicted and fetched again within it (a cache that flaps) leaves one `redetected` event and no `resolved` event (Q-VULN-66).
const flapWindow = time.Hour

// redetectedEvent writes the history of a resolved finding that is re-detected. A removal of the version (a cache eviction) that
// did not last (the last event is a `resolved` younger than [flapWindow]) is taken back, and when the event before it already
// says `redetected`, nothing is added: a flapping finding keeps one such event, not a pair per cycle. A resolution for any other
// reason (a withdrawn advisory, a claim, a disabled scan) is a fact of the advisory data or of the setting and stays.
func (r *run) redetectedEvent(ctx context.Context, f *store.Finding, was vuln.ResolvedReason, to vuln.Status) error {
	if was != vuln.ResolvedVersionRemoved {
		return r.ev.AddEvent(ctx, f.ID, vuln.ChangeRedetected, vuln.StatusResolved, to, "", nil)
	}
	recent, err := r.ev.RecentEvents(ctx, f.ID, 2)
	if err != nil {
		return err
	}
	if len(recent) > 0 && recent[0].Change == vuln.ChangeResolved && recent[0].AgeSeconds < flapWindow.Seconds() {
		if err := r.ev.DeleteEvent(ctx, f.ID, recent[0].Seq); err != nil {
			return err
		}
		if len(recent) > 1 && recent[1].Change == vuln.ChangeRedetected {
			return nil
		}
	}
	return r.ev.AddEvent(ctx, f.ID, vuln.ChangeRedetected, vuln.StatusResolved, to, "", nil)
}

// strength orders the statuses for a merge of two findings of one version: a block first, a dismissal last.
func strength(s vuln.Status) int {
	switch s {
	case vuln.StatusBlocked:
		return 4
	case vuln.StatusConfirmed:
		return 3
	case vuln.StatusNeedsTriage:
		return 2
	case vuln.StatusDismissed:
		return 1
	}
	return 0
}

// effective is the status a finding stands for in a merge: its own, a resolved one's status before it resolved.
func effective(f *store.Finding) vuln.Status {
	if f.Status != vuln.StatusResolved {
		return f.Status
	}
	if f.BeforeResolution.Valid() && f.BeforeResolution != vuln.StatusResolved {
		return f.BeforeResolution
	}
	return vuln.StatusNeedsTriage
}

// merge folds the finding f, whose advisory joined the alias group of g's advisory, into g (Q-VULN-65). Both say that the same
// advisory affects the version; the survivor g takes the stronger status (blocked, confirmed, needs_triage, dismissed), f's
// history moves to it and f is deleted, so the version never has a finding that resolves as no longer affected while it is, and
// never loses a block by the order the rows were read in. A change of the version's block is an audit event.
func (r *run) merge(ctx context.Context, rv repoView, v store.StoredVersion, m *matched, f, g *store.Finding) error {
	pol := vuln.PolicyFor(rv.Settings, rv.Ecosystem, r.s.o.Enabled, r.s.o.Enforcement)
	_, fBlocked := pol.BlockReasonOf(f.Kind, f.Status)
	_, gBlocked := pol.BlockReasonOf(g.Kind, g.Status)
	fromAdvisory, fromStatus := f.AdvisoryID, f.Status
	if err := r.ev.MergeInto(ctx, f.ID, g.ID); err != nil {
		return err
	}
	if err := r.ev.AddEvent(ctx, g.ID, vuln.ChangeAdvisoryChanged, g.Status, g.Status, "", map[string]any{
		"canonicalId": map[string]string{"from": fromAdvisory, "to": g.AdvisoryID}, "mergedFinding": f.ID, "mergedStatus": string(fromStatus),
	}); err != nil {
		return err
	}
	was := g.Status
	done, _, to, err := r.transition(ctx, g, func(cur *store.Finding) (vuln.Status, bool) {
		want := effective(cur)
		if strength(effective(f)) > strength(want) {
			want = effective(f)
		}
		if cur.Status == vuln.StatusResolved {
			return want, true // re-detected: the finding stands for the affected version again
		}
		return want, want != cur.Status
	})
	if err != nil {
		return err
	}
	after := g.Status
	if done {
		change := vuln.ChangeStatusChanged
		if was == vuln.StatusResolved {
			change = vuln.ChangeRedetected
		}
		if err := r.ev.AddEvent(ctx, g.ID, change, was, to, "merged", nil); err != nil {
			return err
		}
	}
	_, afterBlocked := pol.BlockReasonOf(m.Kind, after)
	block := ""
	switch {
	case afterBlocked && !fBlocked && !gBlocked:
		block = "set"
	case !afterBlocked && (fBlocked || gBlocked):
		block = "lifted"
	}
	if block == "" || r.s.o.Recorder == nil {
		return nil
	}
	details, err := json.Marshal(map[string]string{
		"finding": g.ID, "package": v.Name, "version": v.Version, "advisory": g.AdvisoryID, "from": string(was), "to": string(after), "block": block,
	})
	if err != nil {
		return err
	}
	return r.s.o.Recorder.RecordTx(ctx, r.tx, audit.Event{
		Type: audit.EventVulnFindingStatusChanged, Actor: audit.Actor{Kind: "system", Name: "vulnerability-scanning"}, Outcome: audit.Success,
		Subject:    audit.Subject{Kind: "finding", ID: g.ID, Name: v.Name + "@" + v.Version},
		Repository: rv.Key, Details: details,
	})
}

// upsert inserts, re-detects or refreshes the finding of one canonical advisory on one version.
func (r *run) upsert(ctx context.Context, rv repoView, v store.StoredVersion, m *matched, f *store.Finding) error {
	nf := store.NewFinding{
		RepoID: v.RepoID, Ecosystem: r.eco.Name, Name: v.Name, Version: v.Version, AdvisoryID: m.ID, PackageID: v.PackageID,
		VersionID: v.VersionID, Kind: m.Kind, Severity: m.Severity, CVSS: m.CVSS, KnownExploited: m.KnownExploited,
		FixAvailable: m.FixAvailable, FixedVersions: m.Fixed, AdvisoryModifiedU: m.ModifiedUs,
	}
	if f == nil {
		nf.ID = store.NewFindingID()
		inserted, err := r.ev.InsertFinding(ctx, nf)
		if err != nil {
			return err
		}
		if inserted {
			return r.ev.AddEvent(ctx, nf.ID, vuln.ChangeDetected, "", vuln.StatusNeedsTriage, "", nil)
		}
		// Another evaluation wrote the finding between the read and the insert: it is the finding now, and no event is written
		// for a row that this statement did not create.
		got, exists, err := r.ev.FindingByIdentity(ctx, v.RepoID, r.eco.Name, v.Name, v.Version, m.ID)
		if err != nil || !exists {
			return err
		}
		f = &got
	}
	changed := m.changes(f)
	pointsElsewhere := f.VersionID != v.VersionID || f.PackageID != v.PackageID
	switch f.Status {
	case vuln.StatusResolved:
		var status vuln.Status
		var reopened bool
		var change map[string]any
		was := f.ResolvedReason
		done, _, _, err := r.transition(ctx, f, func(cur *store.Finding) (vuln.Status, bool) {
			if cur.Status != vuln.StatusResolved {
				return "", false
			}
			status, reopened, change = redetected(cur, m)
			return status, true
		})
		if err != nil {
			return err
		}
		if err := r.ev.RefreshFinding(ctx, f.ID, nf); err != nil {
			return err
		}
		f.Revision++
		switch {
		case !done:
			return nil
		case reopened:
			return r.reopen(ctx, rv, v, f, m, vuln.StatusResolved, change)
		}
		return r.redetectedEvent(ctx, f, was, status)
	case vuln.StatusDismissed:
		if change, yes := m.material(f); yes {
			done, _, _, err := r.transition(ctx, f, func(cur *store.Finding) (vuln.Status, bool) {
				return vuln.StatusNeedsTriage, cur.Status == vuln.StatusDismissed
			})
			if err != nil {
				return err
			}
			if err := r.ev.RefreshFinding(ctx, f.ID, nf); err != nil {
				return err
			}
			f.Revision++
			if !done {
				return nil
			}
			return r.reopen(ctx, rv, v, f, m, vuln.StatusDismissed, change)
		}
	}
	// Only the record's modification time moved: it is stored without a new revision and without an event, so that an upstream
	// that touches records does not write a history entry to every finding or fail a triage's If-Match (Q-VULN-66).
	bare := len(changed) == 1 && changed[0] == "modified"
	if bare && !pointsElsewhere {
		return r.ev.TouchModified(ctx, f.ID, m.ModifiedUs)
	}
	if len(changed) > 0 || pointsElsewhere {
		if err := r.ev.RefreshFinding(ctx, f.ID, nf); err != nil {
			return err
		}
	}
	if len(changed) > 0 && !bare {
		return r.ev.AddEvent(ctx, f.ID, vuln.ChangeAdvisoryChanged, f.Status, f.Status, "", map[string]any{"changed": changed})
	}
	return nil
}

// reopen writes the history of a dismissed finding that returned to needs_triage after a material change of its advisory and
// the audit event of it (R-VULN-012): block is set when the reopened finding is malicious and blocks again.
func (r *run) reopen(ctx context.Context, rv repoView, v store.StoredVersion, f *store.Finding, m *matched, from vuln.Status, change map[string]any) error {
	if err := r.ev.AddEvent(ctx, f.ID, vuln.ChangeReopened, from, vuln.StatusNeedsTriage, "", change); err != nil {
		return err
	}
	if r.s.o.Recorder == nil {
		return nil
	}
	pol := vuln.PolicyFor(rv.Settings, rv.Ecosystem, r.s.o.Enabled, r.s.o.Enforcement)
	body := map[string]any{"finding": f.ID, "advisory": m.ID, "change": change}
	if reason, ok := pol.BlockReasonOf(m.Kind, vuln.StatusNeedsTriage); ok && reason == vuln.BlockMalicious && pol.Enforcement == vuln.EnforceBlocks {
		body["block"] = "set"
	}
	details, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return r.s.o.Recorder.RecordTx(ctx, r.tx, audit.Event{
		Type: audit.EventVulnFindingReopened, Actor: audit.Actor{Kind: "system", Name: "vulnerability-scanning"}, Outcome: audit.Success,
		Repository: rv.Key, Path: v.Name + "@" + v.Version, Details: details,
	})
}

// recordClaimed records the public advisories that name an internal name (R-VULN-010): one row per advisory and name and, when
// the row is new, the security event `vulnerability.claimed_name_match` after the transaction committed. A name that is no
// longer internal, or that an advisory no longer names, loses its rows.
func (r *run) recordClaimed(ctx context.Context, names []string, byName map[string]*nameAdvisories) error {
	var keepNames, keepIDs []string
	anyInternal := false
	for _, name := range names {
		var claimRepo string
		internal := false
		for _, f := range r.eco.Formats {
			repo, yes, err := r.rule.Claim(ctx, f, name)
			if err != nil {
				return err
			}
			if yes {
				claimRepo, internal = repo, true
				break
			}
		}
		if !internal {
			continue
		}
		anyInternal = true
		na := byName[name]
		if na == nil {
			continue
		}
		ids := make([]string, 0, len(na.live))
		for id := range na.live {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			kind := na.live[id]
			keepNames, keepIDs = append(keepNames, name), append(keepIDs, id)
			isNew, err := r.ev.InsertClaimedMatch(ctx, r.eco.Name, id, name, kind)
			if err != nil {
				return err
			}
			if isNew {
				r.claimedEvent(ctx, name, id, kind, claimRepo)
			}
		}
	}
	// The commit of one version (a publish, a fill) that names no internal name has nothing to prune and costs no statement; the
	// matches of a claim that went are removed by the next job's evaluation of the name (Q-VULN-66, H-498).
	if !anyInternal && r.single {
		return nil
	}
	return r.ev.PruneClaimedMatches(ctx, r.eco.Name, names, keepNames, keepIDs)
}

// claimedEvent queues the security event for after the commit: a transaction that rolls back and runs again writes it once.
func (r *run) claimedEvent(ctx context.Context, name, id string, kind advisory.Kind, claimRepo string) {
	rec := r.s.o.Recorder
	if rec == nil {
		return
	}
	details, err := json.Marshal(map[string]string{"package": name, "advisory": id, "kind": string(kind), "claimRepository": claimRepo})
	if err != nil {
		return
	}
	bg := context.WithoutCancel(ctx)
	r.tx.After(func() {
		rec.Record(bg, audit.Event{
			Type: audit.EventVulnClaimedNameMatch, Actor: audit.Actor{Kind: "system", Name: "vulnerability-scanning"}, Outcome: audit.Success,
			Repository: claimRepo, Path: name, Details: details,
		})
	})
}

// finish counts one evaluation in the metrics.
func (r *run) finish(started time.Time) {
	m := r.s.o.Metrics
	if m == nil {
		return
	}
	t := string(r.trigger)
	m.Evaluations.WithLabelValues(t).Add(float64(max(r.st.versions, 1)))
	m.EvaluationDuration.WithLabelValues(t).Observe(time.Since(started).Seconds())
	if r.st.unevaluable > 0 {
		m.UnevaluableRanges.WithLabelValues(r.eco.Name).Add(float64(r.st.unevaluable))
	}
}

// snapshotLookup is the repoLookup over the replica's snapshot: the repositories of a batch are read once.
func (s *Service) snapshotLookup(ctx context.Context) (repoLookup, error) {
	list, err := s.o.Repos.List(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]repoView, len(list))
	for _, repo := range list {
		if repo.Lifecycle != repository.Active {
			continue
		}
		if v, ok := viewOf(repo); ok {
			byID[v.ID] = v
		}
	}
	return func(id int64) (repoView, bool) { v, ok := byID[id]; return v, ok }, nil
}
