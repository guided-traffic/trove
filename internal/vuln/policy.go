package vuln

import (
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
)

// Enforcement is the installation-wide `vulnerabilities.enforcement` (R-VULN-015).
type Enforcement string

// The enforcement modes.
const (
	// EnforceBlocks refuses downloads.
	EnforceBlocks Enforcement = "enforce"
	// EnforceReport refuses nothing: would-be blocks are counted and recorded with enforced false.
	EnforceReport Enforcement = "report"
)

// Policy is what the download path needs of the scanning for one repository: the repository member joined with the
// installation's enforcement mode, resolved for the repository's format. It is a value: repository.Repository carries it
// (Repository.VulnerabilityPolicy), read from the replica's snapshot, so a download costs no query for it (R-VULN-015).
type Policy struct {
	// Ecosystem is the OSV ecosystem of the repository's format; empty for a format without the adapter, which makes
	// the policy disabled.
	Ecosystem string
	// Enabled is true when the repository is scanned: its member says enabled, its format has the adapter and the
	// installation has vulnerabilities.enabled. A disabled policy blocks nothing and is the zero value's.
	Enabled bool
	// Enforcement is the installation's mode.
	Enforcement Enforcement
	// BlockMalicious and OnMissingData are the repository's members.
	BlockMalicious bool
	OnMissingData  OnMissingData
}

// PolicyFor resolves the repository's member for its format's ecosystem and the installation's switches.
// installationEnabled is vulnerabilities.enabled, enforcement vulnerabilities.enforcement.
func PolicyFor(s Settings, ecosystem string, installationEnabled bool, enforcement Enforcement) Policy {
	return Policy{
		Ecosystem:      ecosystem,
		Enabled:        installationEnabled && s.Enabled && ecosystem != "",
		Enforcement:    enforcement,
		BlockMalicious: s.BlockMalicious,
		OnMissingData:  s.OnMissingData,
	}
}

// QueryEnforce is the `enforce` parameter of the lateral join of R-VULN-015: the join reads the blocking finding of the
// version while the repository is scanned. In the mode report it still reads it, so that the would-be block can be
// counted; [Verdict] applies the mode.
func (p Policy) QueryEnforce() bool { return p.Enabled }

// QueryBlockMalicious is the `block_malicious` parameter of the same join.
func (p Policy) QueryBlockMalicious() bool { return p.Enabled && p.BlockMalicious }

// BlockReasonOf applies the rule of R-VULN-015 to one finding: it blocks when its status is blocked (reason triage), or
// when it is open and of kind malicious while blockMalicious is on (reason malicious). A malicious finding that is
// blocked reports malicious while blockMalicious is on, as the join's ORDER BY prefers it. A dismissed or resolved
// finding never blocks. The API computes blocked and blockReason with this function, so the report and the download agree.
func (p Policy) BlockReasonOf(kind advisory.Kind, status Status) (BlockReason, bool) {
	if !p.Enabled || !status.Open() {
		return "", false
	}
	if kind == advisory.KindMalicious && p.BlockMalicious {
		return BlockMalicious, true
	}
	if status == StatusBlocked {
		return BlockTriage, true
	}
	return "", false
}

// BlockRow is the row of the lateral join of R-VULN-015, the finding that blocks the version in the repository. The zero
// value (no FindingID) is no row.
type BlockRow struct {
	FindingID  string
	AdvisoryID string
	Kind       advisory.Kind
	Status     Status
}

// Block is the verdict on one download.
type Block struct {
	// Reason is empty when nothing blocks.
	Reason BlockReason
	// FindingID and AdvisoryID name the blocking finding and its canonical advisory; empty for missing_data.
	FindingID  string
	AdvisoryID string
	// Enforced is false in the mode report: the condition holds and nothing is refused.
	Enforced bool
}

// Active reports whether a blocking condition holds, enforced or not. The metrics and the audit event count it.
func (b Block) Active() bool { return b.Reason != "" }

// Refused reports whether the download is refused.
func (b Block) Refused() bool { return b.Reason != "" && b.Enforced }

// Code is the problem code of the refusal (R-VULN-016): vulnerability_data_missing for missing_data, else
// vulnerability_blocked.
func (b Block) Code() string {
	if b.Reason == BlockMissingData {
		return errs.CodeVulnerabilityDataMissing
	}
	return errs.CodeVulnerabilityBlocked
}

// Verdict decides one download from the policy of the repository, the blocking row the item lookup returned (the zero
// BlockRow when none) and the state of the version's ecosystem (R-VULN-015):
//
//  1. a policy that is not Enabled blocks nothing;
//  2. a blocking row decides, with the reason [Policy.BlockReasonOf] gives;
//  3. else, with onMissingData block and an ecosystem whose data is not complete, the reason is missing_data (stale data
//     does not count as missing);
//  4. in the mode report the verdict is returned with Enforced false.
//
// data is the state of Policy.Ecosystem as States answers it; the caller does not look it up for a version without a
// scanned format.
func Verdict(p Policy, row BlockRow, data advisory.DataState) Block {
	if !p.Enabled {
		return Block{}
	}
	enforced := p.Enforcement != EnforceReport
	if row.FindingID != "" {
		if reason, ok := p.BlockReasonOf(row.Kind, row.Status); ok {
			return Block{Reason: reason, FindingID: row.FindingID, AdvisoryID: row.AdvisoryID, Enforced: enforced}
		}
	}
	if p.OnMissingData == MissingBlock && !data.Complete {
		return Block{Reason: BlockMissingData, Enforced: enforced}
	}
	return Block{}
}
