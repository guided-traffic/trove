package vuln

import (
	"fmt"

	"github.com/guided-traffic/trove/internal/errs"
)

// Status is the status of a finding (R-VULN-012, Q-VULN-7, Q-VULN-22).
type Status string

// The statuses. needs_triage, confirmed and blocked are the open ones. resolved is set by trove only.
const (
	StatusNeedsTriage Status = "needs_triage"
	StatusConfirmed   Status = "confirmed"
	StatusBlocked     Status = "blocked"
	StatusDismissed   Status = "dismissed"
	StatusResolved    Status = "resolved"
)

// Statuses lists the five statuses, the order of the CHECK of vuln_finding.status.
func Statuses() []Status {
	return []Status{StatusNeedsTriage, StatusConfirmed, StatusBlocked, StatusDismissed, StatusResolved}
}

// Valid reports whether s is one of the five statuses.
func (s Status) Valid() bool {
	for _, x := range Statuses() {
		if s == x {
			return true
		}
	}
	return false
}

// Open reports whether the finding counts as open: needs_triage, confirmed or blocked (the conventions of the page,
// and the predicate of the partial index vuln_finding_open_version).
func (s Status) Open() bool {
	return s == StatusNeedsTriage || s == StatusConfirmed || s == StatusBlocked
}

// Settable reports whether a person may set the status: every one but resolved (R-VULN-012).
func (s Status) Settable() bool { return s.Valid() && s != StatusResolved }

// OpenStatuses lists the open statuses as the strings the SQL predicate and the filters use.
func OpenStatuses() []string {
	return []string{string(StatusNeedsTriage), string(StatusConfirmed), string(StatusBlocked)}
}

// CheckTransition applies the transition rules of R-VULN-012 to a change a person asks for: a resolved finding is not
// changed (409 finding_resolved), resolved is not set (422 status_not_settable), blocked is reached from confirmed only
// (409 finding_not_confirmed), and blocked is left to confirmed or dismissed only. A change to the status the finding
// has is allowed (the caller reports it as unchanged). The caller adds what the status needs (a dismissal reason).
func CheckTransition(from, to Status) error {
	switch {
	case !to.Valid():
		return errs.E(errs.Invalid, errs.CodeValidation, fmt.Sprintf("%q is no finding status", string(to)))
	case from == StatusResolved:
		return errs.E(errs.Conflict, errs.CodeFindingResolved, "The finding is resolved; trove resolves and re-detects findings, a person does not change one.")
	case to == StatusResolved:
		return errs.E(errs.Unprocessable, errs.CodeStatusNotSettable, "The status resolved is set by trove only.")
	case from == to:
		return nil
	case to == StatusBlocked && from != StatusConfirmed:
		return errs.E(errs.Conflict, errs.CodeFindingNotConfirmed, "A block is set on a confirmed finding; confirm the finding first.")
	case from == StatusBlocked && to != StatusConfirmed && to != StatusDismissed:
		return errs.E(errs.Conflict, errs.CodeConflict, "A blocked finding returns to confirmed or is dismissed.")
	}
	return nil
}

// DismissalReason is the reason a triager gives for a dismissal (R-VULN-012).
type DismissalReason string

// The dismissal reasons.
const (
	DismissNotExploitable DismissalReason = "not_exploitable"
	DismissFalsePositive  DismissalReason = "false_positive"
	DismissRiskAccepted   DismissalReason = "risk_accepted"
	DismissMitigated      DismissalReason = "mitigated"
	DismissTestOnly       DismissalReason = "test_only"
)

// DismissalReasons lists the reasons, the order of the CHECK of vuln_finding.dismissal_reason.
func DismissalReasons() []DismissalReason {
	return []DismissalReason{DismissNotExploitable, DismissFalsePositive, DismissRiskAccepted, DismissMitigated, DismissTestOnly}
}

// Valid reports whether r is one of the five reasons.
func (r DismissalReason) Valid() bool {
	for _, x := range DismissalReasons() {
		if r == x {
			return true
		}
	}
	return false
}

// ResolvedReason is the reason trove resolves a finding (R-VULN-012).
type ResolvedReason string

// The resolved reasons.
const (
	ResolvedNoLongerAffected ResolvedReason = "no_longer_affected"
	ResolvedWithdrawn        ResolvedReason = "advisory_withdrawn"
	ResolvedVersionRemoved   ResolvedReason = "version_removed"
	ResolvedScanningDisabled ResolvedReason = "scanning_disabled"
)

// ResolvedReasons lists the reasons, the order of the CHECK of vuln_finding.resolved_reason.
func ResolvedReasons() []ResolvedReason {
	return []ResolvedReason{ResolvedNoLongerAffected, ResolvedWithdrawn, ResolvedVersionRemoved, ResolvedScanningDisabled}
}

// Valid reports whether r is one of the four reasons.
func (r ResolvedReason) Valid() bool {
	for _, x := range ResolvedReasons() {
		if r == x {
			return true
		}
	}
	return false
}

// Change is the kind of a row of vuln_finding_event (R-VULN-012).
type Change string

// The changes, the order of the CHECK of vuln_finding_event.change.
const (
	ChangeDetected        Change = "detected"
	ChangeStatusChanged   Change = "status_changed"
	ChangeResolved        Change = "resolved"
	ChangeRedetected      Change = "redetected"
	ChangeReopened        Change = "reopened"
	ChangeAdvisoryChanged Change = "advisory_changed"
)

// Changes lists the changes.
func Changes() []Change {
	return []Change{ChangeDetected, ChangeStatusChanged, ChangeResolved, ChangeRedetected, ChangeReopened, ChangeAdvisoryChanged}
}

// BlockReason is why a download is refused (R-VULN-015, R-VULN-024 `reason`).
type BlockReason string

// The reasons of a block.
const (
	// BlockMalicious: an open finding of kind malicious blocks while the repository's blockMalicious is on.
	BlockMalicious BlockReason = "malicious"
	// BlockTriage: a triager set the block (status blocked).
	BlockTriage BlockReason = "triage"
	// BlockMissingData: the advisory data of the ecosystem is not complete and onMissingData is block.
	BlockMissingData BlockReason = "missing_data"
)

// Evaluation is the derived evaluation state of a stored version (R-VULN-001). It is never stored.
type Evaluation string

// The evaluation states.
const (
	// EvaluationEvaluated: scanning is on, the version is in scope and the ecosystem's data is complete.
	EvaluationEvaluated Evaluation = "evaluated"
	// EvaluationPendingData: as above, but the data was never imported completely.
	EvaluationPendingData Evaluation = "pending_data"
	// EvaluationInternal: public advisories do not apply to the version (R-VULN-010).
	EvaluationInternal Evaluation = "internal"
	// EvaluationNotCovered: scanning is off, the upstream is not recognised, or the format has no adapter.
	EvaluationNotCovered Evaluation = "not_covered"
	// EvaluationPendingScan: an image of this version has not been scanned yet (R-VULN-034); no finding says nothing about it.
	EvaluationPendingScan Evaluation = "pending_scan"
	// EvaluationScanFailed: the scan of an image of this version did not finish, and the code says why (R-VULN-034).
	EvaluationScanFailed Evaluation = "scan_failed"
)

// Evaluations lists the states.
func Evaluations() []Evaluation {
	return []Evaluation{EvaluationEvaluated, EvaluationPendingData, EvaluationInternal, EvaluationNotCovered, EvaluationPendingScan, EvaluationScanFailed}
}

// Trigger is what caused an evaluation (R-VULN-011, the `trigger` label of the evaluation metrics).
type Trigger string

// The triggers.
const (
	TriggerPublish  Trigger = "publish"
	TriggerFill     Trigger = "fill"
	TriggerMiss     Trigger = "miss"
	TriggerAdvisory Trigger = "advisory"
	TriggerFull     Trigger = "full"
	TriggerRescan   Trigger = "rescan"
)

// Triggers lists the six triggers.
func Triggers() []Trigger {
	return []Trigger{TriggerPublish, TriggerFill, TriggerMiss, TriggerAdvisory, TriggerFull, TriggerRescan}
}
