package vuln

import (
	"context"

	"github.com/guided-traffic/trove/internal/qname"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
)

// DataState is the state of one ecosystem's advisory data; the type lives in the leaf so that the format contract can
// hand it to a format without importing this package.
type DataState = advisory.DataState

// States answers the state of an ecosystem's advisory data from the replica's cache vuln_source_state (change area
// vuln_data, SafetyTTL 60 s, R-VULN-027): no query per call, so the download path, the audits and the Host may ask on
// every request. An ecosystem it does not know is the zero DataState (not complete). Work package A implements it
// (sync/states.go).
type States interface {
	State(ecosystem string) DataState
}

// CommittedVersion is what the commit hook tells the evaluation of a stored version (R-VULN-011): the repository, the
// item the commit wrote and the trigger. The item's items.version_id, which the format's OnCommit has set before this
// runs, names the version; an item without one (a file that belongs to no version) is no evaluation.
type CommittedVersion struct {
	RepoID int64
	// Repo is the repository's name; the evaluation reads the repository's snapshot with it, which costs no query.
	Repo   qname.Repo
	Format string
	ItemID int64
	// Trigger is TriggerPublish for an upload and TriggerFill for the fill of a remote cache.
	Trigger Trigger
}

// Miss is a remote miss that is about to be fetched (R-VULN-015 "Remote miss"): the version a path maps to
// (formats.PathInfo) in a repository, before anything is fetched.
type Miss struct {
	// Repo is the repository the miss is in; the evaluation reads its snapshot (class, upstream, claims, member).
	Repo   qname.Repo
	Format string
	// Name and Version are normalized by the format.
	Name, Version string
}

// Evaluator is the evaluation of stored versions that the commit path and the remote engine call. Work package B
// implements it (evaluate/). A nil Evaluator is a replica that has not wired the scanning: the hooks skip it.
type Evaluator interface {
	// OnVersionCommitted evaluates the version of a committed item in the commit's transaction, after the format's
	// OnCommit linked the item to its version (R-VULN-011): one indexed query, findings inserted in the same
	// transaction. An error rolls the commit back.
	OnVersionCommitted(ctx context.Context, tx meta.Tx, ev CommittedVersion) error
	// OnVersionsRemoved resolves the open findings of the removed versions with reason version_removed, in the removing
	// transaction (R-VULN-011). The ids are package_versions.id values.
	OnVersionsRemoved(ctx context.Context, tx meta.Tx, versionIDs []int64) error
	// EvaluateMiss returns the block the miss would carry once fetched, without storing anything: a finding of the
	// version in the repository that resolved with version_removed while blocked counts as blocked (R-VULN-015). The
	// zero Block is no block.
	EvaluateMiss(ctx context.Context, m Miss) (Block, error)
}

// Advisories reads advisory data for the client audits (R-VULN-017, R-VULN-018). Work package D implements it over
// vuln/store; the format contract's VulnerabilityData hands it to the formats.
type Advisories interface {
	// ForNames returns the non-withdrawn advisories of an ecosystem that name any of the names, keyed by name, in one
	// query. The names are normalized by the format's adapter; advisories carry their affected entries.
	ForNames(ctx context.Context, ecosystem string, names []string) (map[string][]advisory.Advisory, error)
	// All returns every non-withdrawn advisory of an ecosystem (the NuGet page).
	All(ctx context.Context, ecosystem string) ([]advisory.Advisory, error)
	// InternalNames reports which of the names are internal names of the format (R-VULN-010): covered by a namespace
	// claim of a local repository of the format that does not set matchPublicAdvisories always. Client audits withhold
	// them. Claims are read from the replica's claim index, so the call costs no query.
	InternalNames(ctx context.Context, format string, names []string) (map[string]bool, error)
}
