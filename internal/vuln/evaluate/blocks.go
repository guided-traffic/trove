package evaluate

import (
	"context"
	"encoding/json"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// The download blocks the evaluation sets are journaled (R-VULN-012, R-HA-054, Q-VULN-130): a restore to a point before the change
// would bring the version back as downloadable, and the replay of the entry (R-VULN-026) sets the block again.

// The causes of a block the evaluation sets, the `cause` of vulnerability.finding.status_changed.
const (
	// causeDetected is a finding of kind malicious detected for the first time.
	causeDetected = "detected"
	// causeKindChanged is an open finding whose advisory turned malicious.
	causeKindChanged = "kind_changed"
	// causeRedetected is a finding that resolved and is detected again, or a blocked one whose version returned while the data
	// could not judge it (Q-VULN-132).
	causeRedetected = "redetected"
	// causeMerged is a merge of two findings whose stronger status blocks.
	causeMerged = "merged"
)

// setsBlock reports whether a change of one finding sets a download block under the policy: the finding blocks its version after
// the change and did not before, and the installation enforces (the condition of the reopen, in the mode report nothing is
// blocked and nothing is journaled). existed is false for a finding that is created by the change.
func setsBlock(pol vuln.Policy, existed bool, beforeKind advisory.Kind, beforeStatus vuln.Status, kind advisory.Kind, status vuln.Status) bool {
	if pol.Enforcement != vuln.EnforceBlocks {
		return false
	}
	_, after := pol.BlockReasonOf(kind, status)
	if !after {
		return false
	}
	if !existed {
		return true
	}
	_, before := pol.BlockReasonOf(beforeKind, beforeStatus)
	return !before
}

// blockFacts are the members of a finding that its block event names.
type blockFacts struct {
	ID, AdvisoryID string
	Kind           advisory.Kind
	Severity       advisory.Severity
}

// blockEvent builds the audit event of a change of the evaluation that set a block, with the journal entry: from is empty for a
// first detection and equals to for a change of the kind.
func blockEvent(rv repoView, v store.StoredVersion, f blockFacts, from, to vuln.Status, cause string) (audit.Event, error) {
	details, err := json.Marshal(map[string]string{
		"finding": f.ID, "package": v.Name, "version": v.Version, "advisory": f.AdvisoryID, "from": string(from), "to": string(to),
		"block": vuln.BlockSet, "cause": cause,
	})
	if err != nil {
		return audit.Event{}, err
	}
	entry, err := vuln.NewBlockEntry(f.ID, rv.ID, rv.Key, rv.Ecosystem, v.Name, v.Version, f.AdvisoryID, string(f.Kind), string(f.Severity)).JSON()
	if err != nil {
		return audit.Event{}, err
	}
	return audit.Event{
		Type: audit.EventVulnFindingStatusChanged, Actor: audit.Actor{Kind: "system", Name: "vulnerability-scanning"}, Outcome: audit.Success,
		Subject:    audit.Subject{Kind: "finding", ID: f.ID, Name: v.Name + "@" + v.Version},
		Repository: rv.Key, Details: details, Journal: entry,
	}, nil
}

// recordBlockSet records the change of the evaluation that set a block, in the evaluation's transaction. It writes nothing when
// the change sets none under the repository's policy (an ordinary detection, a lift, the mode report).
func (r *run) recordBlockSet(ctx context.Context, rv repoView, v store.StoredVersion, f blockFacts, was *store.Finding, from, to vuln.Status, cause string) error {
	if r.s.o.Recorder == nil {
		return nil
	}
	pol := vuln.PolicyFor(rv.Settings, rv.Ecosystem, r.s.o.Enabled, r.s.o.Enforcement)
	existed, beforeKind, beforeStatus := was != nil, advisory.Kind(""), vuln.Status("")
	if existed {
		beforeKind, beforeStatus = was.Kind, was.Status
	}
	if !setsBlock(pol, existed, beforeKind, beforeStatus, f.Kind, to) {
		return nil
	}
	ev, err := blockEvent(rv, v, f, from, to, cause)
	if err != nil {
		return err
	}
	return r.recordOnce(ctx, f.ID, ev)
}

// recordOnce records the event of a change that set a block unless this run recorded a block of the finding already: a merge and
// the upsert after it can both set the block of one finding, and one entry says it.
func (r *run) recordOnce(ctx context.Context, findingID string, ev audit.Event) error {
	if r.journaled[findingID] {
		return nil
	}
	if r.journaled == nil {
		r.journaled = map[string]bool{}
	}
	r.journaled[findingID] = true
	return r.s.o.Recorder.RecordTx(ctx, r.tx, ev)
}

// redetectHeldAll brings every finding of the version that resolved with version_removed while blocked and that no advisory
// matches back as blocked when held says the data cannot judge it. It returns them, so that the same evaluation does not resolve
// what it returned: complete data older than the restore would read the missing advisory as "no longer affected" (Q-VULN-132).
func (r *run) redetectHeldAll(ctx context.Context, rv repoView, v store.StoredVersion, live []*store.Finding, found map[string]*matched, held bool) (map[*store.Finding]bool, error) {
	returned := map[*store.Finding]bool{}
	if !held {
		return returned, nil
	}
	for _, f := range live {
		if found[f.AdvisoryID] != nil || !isEvictedBlock(f) {
			continue
		}
		if err := r.redetectHeld(ctx, rv, v, f); err != nil {
			return nil, err
		}
		returned[f] = true
	}
	return returned, nil
}

// redetectHeld brings a finding that resolved with version_removed while blocked back as blocked, linked to the version that was
// committed, without a matching advisory (Q-VULN-132): the data is incomplete or older than the restore, so it cannot say that
// the advisory stopped applying. The finding keeps its advisory facts; the first evaluation with data that can judge it decides.
func (r *run) redetectHeld(ctx context.Context, rv repoView, v store.StoredVersion, f *store.Finding) error {
	was := *f
	done, _, to, err := r.transition(ctx, f, func(cur *store.Finding) (vuln.Status, bool) {
		if !isEvictedBlock(cur) {
			return "", false
		}
		return vuln.StatusBlocked, true
	})
	if err != nil || !done {
		return err
	}
	if err := r.ev.RefreshFinding(ctx, f.ID, store.NewFinding{
		Kind: f.Kind, Severity: f.Severity, CVSS: f.CVSS, KnownExploited: f.KnownExploited, FixAvailable: f.FixAvailable,
		FixedVersions: f.FixedVersions, AdvisoryModifiedU: f.AdvisoryModifiedU, PackageID: v.PackageID, VersionID: v.VersionID,
	}); err != nil {
		return err
	}
	f.Revision++
	if err := r.redetectedEvent(ctx, f, was.ResolvedReason, to); err != nil {
		return err
	}
	return r.recordBlockSet(ctx, rv, v, blockFacts{ID: f.ID, AdvisoryID: f.AdvisoryID, Kind: f.Kind, Severity: f.Severity}, &was, vuln.StatusResolved, to, causeRedetected)
}
