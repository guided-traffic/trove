package triage

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"unicode"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// The replay of the download blocks of the revocation journal (R-HA-054, R-VULN-013, R-VULN-026, Q-VULN-48, Q-VULN-114,
// Q-VULN-131). A change that sets a block carries the entry [vuln.BlockEntry]: a triage that blocks a finding (record in
// triage.go) and the evaluation (internal/vuln/evaluate: a first detection, a kind that turned malicious, a re-detection, a
// merge, a reopen). A restore to t brings the findings back as they were at t; the replay sets each block again.
//
//   - The finding is found by the entry's ID when the restored finding of that ID has the entry's repository, ecosystem, package
//     and version, which never change for a finding; an ID of another identity is left alone. Otherwise it is found by the table's
//     unique key (repository, ecosystem, package, version, advisory), and otherwise created, from the entry's kind and severity
//     with the other advisory members empty, when the restored repository can hold it. The repository's name and the
//     advisory of a finding found by its ID are shown only (a move renames the one, a group's canonical record can move the
//     other) (Q-VULN-115, Q-VULN-131).
//   - The shape is the one both download gates read (Q-VULN-131): the restored repository stores the version, so the finding is
//     blocked and linked to it, which the download's join reads; it does not, so the finding is resolved with version_removed while
//     blocked, which the remote miss and a commit count as blocked until the ecosystem has synced since the restore (Q-VULN-132).
//     A finding in its shape is "already in effect". In every other status the block is set again: a status without a block is the
//     finding before the change the entry records, or a lift after it, which is not journaled because it widens access, so the
//     replay cannot tell the two apart and keeps the block (Q-VULN-114).
//   - The write is the finding's own: the revision raised, a history row with no principal and the entry's ID. It writes no audit
//     event (Q-AUTHZ-232); a download reads the status at its next request on every replica (R-VULN-027).

func init() {
	journal.Register(journal.Handler{
		Domain: "vuln", Events: []string{audit.EventVulnFindingStatusChanged, audit.EventVulnFindingReopened}, Apply: replayBlock,
	})
}

// readBlockEntry decodes an entry; ok is false for one that is not complete (every member is required), names no finding, repository
// or kind, or records no block that was set.
func readBlockEntry(o journal.Object) (vuln.BlockEntry, bool) {
	var e vuln.BlockEntry
	if !o.Into(&e) {
		return e, false
	}
	if id, err := strconv.ParseInt(e.RepositoryID, 10, 64); err != nil || id < 1 {
		return e, false
	}
	kind := advisory.Kind(e.Kind)
	if kind != advisory.KindVulnerability && kind != advisory.KindMalicious {
		return e, false
	}
	for m, max := range map[string]int{e.Repository: maxRepository, e.Ecosystem: maxEcosystem, e.Package: maxPackage, e.Version: maxVersion, e.Advisory: maxAdvisory} {
		if !plainText(m, max) {
			return e, false
		}
	}
	return e, journal.IsUUID(e.Finding) && e.Block == vuln.BlockSet && advisory.Severity(e.Severity).Valid()
}

// The longest value of a text member of an entry. The columns have no limit, the replay does: an object is untrusted input and one
// that cannot be stored must be skipped, not stop the replay.
const (
	maxRepository = 256
	maxEcosystem  = 64
	maxPackage    = 512
	maxVersion    = 256
	maxAdvisory   = 256
)

// plainText reports whether s is a non-empty, valid UTF-8 text of at most max bytes without a control character (a NUL or a line
// break would fail a statement or forge a log line).
func plainText(s string, max int) bool {
	if s == "" || len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// restoredFinding is the finding of the restored database that a replayed block acts on.
type restoredFinding struct {
	ID                 string
	Status             vuln.Status
	BeforeResolution   string
	ResolvedReason     string
	VersionID, Package int64
}

// blockShape is the shape a replayed block leaves, by the version the restored repository stores (0 when it stores none).
func blockShape(versionID int64) (status vuln.Status) {
	if versionID != 0 {
		return vuln.StatusBlocked
	}
	return vuln.StatusResolved
}

// blockOutcome is what replaying a block does to a restored finding: it is in effect when it already has the shape the stored
// version (versionID, packageID; 0 for none) gives.
func blockOutcome(f restoredFinding, versionID, packageID int64) journal.Outcome {
	switch blockShape(versionID) {
	case vuln.StatusBlocked:
		if f.Status == vuln.StatusBlocked && f.VersionID == versionID && f.Package == packageID {
			return journal.AlreadyInEffect
		}
	default:
		if f.Status == vuln.StatusResolved && f.ResolvedReason == string(vuln.ResolvedVersionRemoved) &&
			f.BeforeResolution == string(vuln.StatusBlocked) && f.VersionID == 0 {
			return journal.AlreadyInEffect
		}
	}
	return journal.Applied
}

var errNotPostgres = errors.New("triage: the transaction does not come from the PostgreSQL store")

func replayBlock(ctx context.Context, tx meta.Tx, _ journal.Env, o journal.Object) (journal.Result, error) {
	e, ok := readBlockEntry(o)
	if !ok {
		return o.Malformed("the entry is not a complete record of a block that was set"), nil
	}
	repoID, _ := strconv.ParseInt(e.RepositoryID, 10, 64) // read strictly by readBlockEntry
	subject := e.Repository + " " + e.Package + "@" + e.Version
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return journal.Result{}, errNotPostgres
	}
	qs := q.New(ptx)
	target, idTaken, err := lockTarget(ctx, qs, e, repoID)
	if err != nil {
		return journal.Result{}, err
	}
	version, err := storedVersion(ctx, qs, repoID, e)
	if err != nil {
		return journal.Result{}, err
	}
	if target == nil {
		return createBlocked(ctx, qs, o, e, repoID, subject, idTaken, version)
	}
	raised, err := raiseFacts(ctx, qs, target.ID, e, o.ID)
	if err != nil {
		return journal.Result{}, err
	}
	if outcome := blockOutcome(*target, version.VersionID, version.PackageID); outcome != journal.Applied {
		if raised && outcome == journal.AlreadyInEffect {
			outcome = journal.Applied
		}
		return o.Result(outcome, subject), nil
	}
	if _, err := qs.JournalVulnFindingBlock(ctx, q.JournalVulnFindingBlockParams{ID: target.ID, VersionID: version.VersionID, PackageID: version.PackageID}); err != nil {
		return journal.Result{}, err
	}
	if err := historyRow(ctx, qs, target.ID, vuln.ChangeStatusChanged, target.Status, blockShape(version.VersionID), o.ID); err != nil {
		return journal.Result{}, err
	}
	res := o.Result(journal.Applied, subject)
	res.Detail, _ = json.Marshal(map[string]string{"from": string(target.Status)})
	return res, nil
}

// raiseFacts raises the kind and severity of a finding that a replay created to the entry's when those are stronger (Q-VULN-131),
// so that a copy that names weaker facts cannot fix what a later copy says; a finding restored from the backup is left as it is.
// The raise writes a history row of its own.
func raiseFacts(ctx context.Context, qs *q.Queries, id string, e vuln.BlockEntry, entryID string) (bool, error) {
	n, err := qs.JournalVulnFindingRaise(ctx, q.JournalVulnFindingRaiseParams{
		ID: id, Kind: e.Kind, Severity: e.Severity, SeverityRank: int16(advisory.Severity(e.Severity).Rank()), //nolint:gosec // a rank is 0 to 5
	})
	if err != nil || n == 0 {
		return false, err
	}
	details, _ := json.Marshal(map[string]any{"journal": entryID, "raised": map[string]string{"kind": e.Kind, "severity": e.Severity}})
	return true, qs.VulnInsertFindingEvent(ctx, q.VulnInsertFindingEventParams{
		FindingID: id, Change: string(vuln.ChangeAdvisoryChanged), Details: string(details),
	})
}

// createBlocked creates the finding the restored database lacks, when its repository can hold it and the package name is one the
// repository's format admits.
func createBlocked(ctx context.Context, qs *q.Queries, o journal.Object, e vuln.BlockEntry, repoID int64, subject string, idTaken bool,
	version q.JournalVulnStoredVersionRow,
) (journal.Result, error) {
	unknown, err := cannotHold(ctx, qs, repoID, e)
	if err != nil {
		return journal.Result{}, err
	}
	if unknown == nameRule {
		return o.Malformed("the package name is not one the repository's format admits"), nil
	}
	if unknown != "" {
		res := o.Result(journal.SubjectUnknown, subject)
		res.Detail, _ = json.Marshal(map[string]string{"reason": unknown})
		return res, nil
	}
	id := e.Finding
	if idTaken {
		id = store.NewFindingID()
	}
	if err := qs.JournalVulnFindingInsert(ctx, q.JournalVulnFindingInsertParams{
		ID: id, RepoID: repoID, Ecosystem: e.Ecosystem, PackageName: e.Package, Version: e.Version, AdvisoryID: e.Advisory,
		PackageID: version.PackageID, VersionID: version.VersionID, Kind: e.Kind, Severity: e.Severity,
		SeverityRank: int16(advisory.Severity(e.Severity).Rank()), //nolint:gosec // a rank is 0 to 5
	}); err != nil {
		return journal.Result{}, err
	}
	if err := historyRow(ctx, qs, id, vuln.ChangeDetected, "", blockShape(version.VersionID), o.ID); err != nil {
		return journal.Result{}, err
	}
	res := o.Result(journal.Applied, subject)
	res.Detail, _ = json.Marshal(map[string]string{"created": id})
	return res, nil
}

// lockTarget finds and locks the restored finding a replayed block acts on: the finding of the entry's ID with the entry's
// identity, advisory included (a copy with the same ID and another advisory must not absorb a genuine block), else the finding of the entry's identity. idTaken says that a finding of another identity holds the entry's ID.
func lockTarget(ctx context.Context, qs *q.Queries, e vuln.BlockEntry, repoID int64) (target *restoredFinding, idTaken bool, err error) {
	rows, err := qs.JournalVulnFindingLock(ctx, e.Finding)
	if err != nil {
		return nil, false, err
	}
	if len(rows) > 0 {
		f := rows[0]
		if f.RepoID == repoID && f.Ecosystem == e.Ecosystem && f.PackageName == e.Package && f.Version == e.Version && f.AdvisoryID == e.Advisory {
			return &restoredFinding{
				ID: f.ID, Status: vuln.Status(f.Status), BeforeResolution: f.StatusBeforeResolution, ResolvedReason: f.ResolvedReason,
				VersionID: f.VersionID, Package: f.PackageID,
			}, false, nil
		}
		idTaken = true
	}
	ident, err := qs.JournalVulnFindingLockByIdentity(ctx, q.JournalVulnFindingLockByIdentityParams{
		RepoID: repoID, Ecosystem: e.Ecosystem, PackageName: e.Package, Version: e.Version, AdvisoryID: e.Advisory,
	})
	if err != nil || len(ident) == 0 {
		return nil, idTaken, err
	}
	f := ident[0]
	return &restoredFinding{
		ID: f.ID, Status: vuln.Status(f.Status), BeforeResolution: f.StatusBeforeResolution, ResolvedReason: f.ResolvedReason,
		VersionID: f.VersionID, Package: f.PackageID,
	}, idTaken, nil
}

// storedVersion is the package version the restored repository stores under the entry's package and version; the zero row for none.
func storedVersion(ctx context.Context, qs *q.Queries, repoID int64, e vuln.BlockEntry) (q.JournalVulnStoredVersionRow, error) {
	rows, err := qs.JournalVulnStoredVersion(ctx, q.JournalVulnStoredVersionParams{RepoID: repoID, PackageName: e.Package, Version: e.Version})
	if err != nil || len(rows) == 0 {
		return q.JournalVulnStoredVersionRow{}, err
	}
	return rows[0], nil
}

// nameRule is the answer of [cannotHold] for a package name the repository's format does not admit: the entry is malformed, not
// unknown.
const nameRule = "name"

// cannotHold says why the restored database cannot hold the finding the entry names, "" when it can: no such repository, one
// in deletion, a virtual one (which stores nothing) or one whose format has another ecosystem (Q-VULN-131 step 3).
func cannotHold(ctx context.Context, qs *q.Queries, repoID int64, e vuln.BlockEntry) (string, error) {
	rows, err := qs.JournalVulnRepository(ctx, repoID)
	if err != nil {
		return "", err
	}
	switch {
	case len(rows) == 0:
		return "the restored database holds no repository of this ID", nil
	case rows[0].Lifecycle != string(repository.Active):
		return "the restored repository is not active", nil
	case rows[0].Class == string(catalog.Virtual):
		return "the restored repository is virtual and stores nothing", nil
	}
	if info, known := catalog.Default().Lookup(rows[0].Format); !known || info.Ecosystem != e.Ecosystem {
		return "the restored repository is of another ecosystem", nil
	}
	if norm, ok := catalog.Default().NormalizeName(rows[0].Format, e.Package); !ok || norm != e.Package {
		return nameRule, nil
	}
	return "", nil
}

// historyRow writes the finding's own history row of a replayed block: no principal, the entry's ID in its details.
func historyRow(ctx context.Context, qs *q.Queries, id string, change vuln.Change, from, to vuln.Status, entryID string) error {
	details, _ := json.Marshal(map[string]string{"journal": entryID})
	return qs.VulnInsertFindingEvent(ctx, q.VulnInsertFindingEventParams{
		FindingID: id, Change: string(change), FromStatus: string(from), ToStatus: string(to), Details: string(details),
	})
}
