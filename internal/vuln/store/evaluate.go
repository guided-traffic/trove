package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
)

// The data access of the evaluation (work package B, queries/vuln_evaluate.sql): the affected entries of names, the stored
// versions, the findings and their events, the claimed-name matches, the request of a full evaluation and the queue of changed
// advisories. Every method runs on the transaction it was made for.

// Eval is the evaluation's view of one transaction.
type Eval struct {
	qs *q.Queries
	tx q.Tx
}

// EvalOf returns the evaluation's access to a transaction of the PostgreSQL store.
func EvalOf(tx meta.Tx) (*Eval, error) {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return nil, errors.New("vuln store: the transaction does not come from the PostgreSQL store")
	}
	return &Eval{qs: q.New(ptx), tx: ptx}, nil
}

// Pipeline runs the reads of several steps in one round trip (Q-VULN-82): each step calls the methods of the Eval it is given
// and keeps their results. The rules of [q.Pipeline] hold: a step is a pure function of its arguments, issues the same
// statements whatever the answers, and none of its statements depends on the result of another step of the same pipeline.
func (e *Eval) Pipeline(ctx context.Context, steps ...func(*Eval) error) error {
	qsteps := make([]func(*q.Queries) error, len(steps))
	for i, step := range steps {
		qsteps[i] = func(qs *q.Queries) error { return step(&Eval{qs: qs, tx: e.tx}) }
	}
	return q.Pipeline(ctx, e.tx, qsteps...)
}

// NewFindingID returns the id of a new finding, a UUIDv7 (RFC 9562).
func NewFindingID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil { // crypto/rand does not fail on supported platforms
		panic(err)
	}
	ms := uint64(time.Now().UnixMilli()) //nolint:gosec // after 1970
	b[0], b[1], b[2], b[3], b[4], b[5] = byte(ms>>40), byte(ms>>32), byte(ms>>24), byte(ms>>16), byte(ms>>8), byte(ms)
	b[6] = b[6]&0x0f | 0x70
	b[8] = b[8]&0x3f | 0x80
	var s [36]byte
	hex.Encode(s[0:8], b[0:4])
	s[8] = '-'
	hex.Encode(s[9:13], b[4:6])
	s[13] = '-'
	hex.Encode(s[14:18], b[6:8])
	s[18] = '-'
	hex.Encode(s[19:23], b[8:10])
	s[23] = '-'
	hex.Encode(s[24:], b[10:])
	return string(s[:])
}

// NoScore is the CVSS score of an advisory without one, in the rows of this file.
const NoScore = -1.0

// AffectedRow is one affected entry of one record of an ecosystem, with what the evaluation needs of the advisory.
type AffectedRow struct {
	// Name is the entry's package name; RecordID the record as published, CanonicalID the identifier findings carry.
	Name, RecordID, CanonicalID string
	// Modified is the record's `modified` (the canonical record's when it is stored), in microseconds since the epoch.
	ModifiedUs int64
	Withdrawn  bool
	Kind       advisory.Kind
	Severity   advisory.Severity
	// CVSS is the score, NoScore when there is none.
	CVSS           float64
	KnownExploited bool
	// Entry is the entry as the matcher takes it.
	Entry advisory.Affected
	// RangesInvalid is true when the stored ranges do not decode; the evaluation counts the entry as one unevaluable range.
	RangesInvalid bool
}

// AffectedForNames returns the affected entries that name any of the names, withdrawn records included.
func (e *Eval) AffectedForNames(ctx context.Context, ecosystem string, names []string) ([]AffectedRow, error) {
	rows, err := e.qs.VulnAffectedForNames(ctx, q.VulnAffectedForNamesParams{Ecosystem: ecosystem, Names: names})
	if err != nil {
		return nil, fmt.Errorf("affected entries: %w", err)
	}
	return affectedRows(rows), nil
}

// AffectedForItem returns the affected entries that name the package of an item's version, withdrawn records included: the
// commit hook asks before it knows the name (Q-VULN-82). No entry for an item that belongs to no version.
func (e *Eval) AffectedForItem(ctx context.Context, ecosystem string, itemID int64) ([]AffectedRow, error) {
	rows, err := e.qs.VulnAffectedForItem(ctx, q.VulnAffectedForItemParams{Ecosystem: ecosystem, ItemID: itemID})
	if err != nil {
		return nil, fmt.Errorf("affected entries: %w", err)
	}
	named := make([]q.VulnAffectedForNamesRow, len(rows))
	for i, r := range rows {
		named[i] = q.VulnAffectedForNamesRow(r)
	}
	return affectedRows(named), nil
}

func affectedRows(rows []q.VulnAffectedForNamesRow) []AffectedRow {
	out := make([]AffectedRow, len(rows))
	for i, r := range rows {
		a := AffectedRow{
			Name: r.Name, RecordID: r.RecordID, CanonicalID: r.CanonicalID, ModifiedUs: r.ModifiedUs, Withdrawn: r.Withdrawn,
			Kind: advisory.Kind(r.Kind), Severity: advisory.Severity(r.Severity), CVSS: r.CvssScore, KnownExploited: r.KnownExploited,
			Entry: advisory.Affected{Name: r.Name, Versions: r.Versions, EveryVersion: r.EveryVersion, Fixed: r.Fixed},
		}
		if len(r.Ranges) > 0 {
			if err := json.Unmarshal(r.Ranges, &a.Entry.Ranges); err != nil {
				a.Entry.Ranges, a.RangesInvalid = nil, true
			}
		}
		out[i] = a
	}
	return out
}

// StoredVersion is one stored version: a row of package_versions with its package and repository.
type StoredVersion struct {
	RepoID, PackageID, VersionID int64
	Name, Version                string
	// LeadItemID is the version's lead item, 0 for none; set by ItemVersion only.
	LeadItemID int64
}

// StoredVersions returns the stored versions of the names in the repositories of the formats, in one repository when repoID is
// not zero.
func (e *Eval) StoredVersions(ctx context.Context, formats, names []string, repoID int64) ([]StoredVersion, error) {
	rows, err := e.qs.VulnStoredVersions(ctx, q.VulnStoredVersionsParams{Names: names, Formats: formats, RepoID: repoID})
	if err != nil {
		return nil, fmt.Errorf("stored versions: %w", err)
	}
	out := make([]StoredVersion, len(rows))
	for i, r := range rows {
		out[i] = StoredVersion{RepoID: r.RepoID, PackageID: r.PackageID, VersionID: r.VersionID, Name: r.Name, Version: r.Version}
	}
	return out, nil
}

// PackageNamesPage returns up to limit distinct package names after the cursor, in byte order.
func (e *Eval) PackageNamesPage(ctx context.Context, formats []string, repoID int64, after string, limit int) ([]string, error) {
	return e.qs.VulnPackageNamesPage(ctx, q.VulnPackageNamesPageParams{After: after, Formats: formats, RepoID: repoID, MaxRows: int32(limit)}) //nolint:gosec // a page size
}

// CountStoredVersions counts the stored versions of the formats, in one repository when repoID is not zero.
func (e *Eval) CountStoredVersions(ctx context.Context, formats []string, repoID int64) (int64, error) {
	return e.qs.VulnCountStoredVersions(ctx, q.VulnCountStoredVersionsParams{Formats: formats, RepoID: repoID})
}

// ItemVersion returns the version an item belongs to; false for an item of no live version.
func (e *Eval) ItemVersion(ctx context.Context, itemID int64) (StoredVersion, bool, error) {
	rows, err := e.qs.VulnItemVersion(ctx, itemID)
	if err != nil || len(rows) == 0 {
		return StoredVersion{}, false, err
	}
	r := rows[0]
	return StoredVersion{PackageID: r.PackageID, VersionID: r.VersionID, Name: r.Name, Version: r.Version, LeadItemID: r.LeadItemID}, true, nil
}

// Finding is a row of vuln_finding as the evaluation reads it.
type Finding struct {
	ID                string
	RepoID            int64
	Name, Version     string
	AdvisoryID        string
	PackageID         int64
	VersionID         int64
	Kind              advisory.Kind
	Severity          advisory.Severity
	CVSS              float64
	KnownExploited    bool
	FixAvailable      bool
	FixedVersions     []string
	Status            vuln.Status
	BeforeResolution  vuln.Status
	ResolvedReason    vuln.ResolvedReason
	AdvisoryModifiedU int64
	// Revision is the finding's revision as read: a status change of the evaluation holds only while it is still current.
	Revision int64
}

// FindingsForNames returns every finding of the names of an ecosystem, in every repository, resolved ones too.
func (e *Eval) FindingsForNames(ctx context.Context, ecosystem string, names []string) ([]Finding, error) {
	rows, err := e.qs.VulnFindingsForNames(ctx, q.VulnFindingsForNamesParams{Ecosystem: ecosystem, Names: names})
	if err != nil {
		return nil, fmt.Errorf("findings of names: %w", err)
	}
	out := make([]Finding, len(rows))
	for i, r := range rows {
		out[i] = findingOf(q.VulnFindingsOfRepoNamesRow(r))
	}
	return out, nil
}

// FindingsOfRepoNames returns the findings of the names in one repository, resolved ones too.
func (e *Eval) FindingsOfRepoNames(ctx context.Context, repoID int64, ecosystem string, names []string) ([]Finding, error) {
	rows, err := e.qs.VulnFindingsOfRepoNames(ctx, q.VulnFindingsOfRepoNamesParams{RepoID: repoID, Ecosystem: ecosystem, Names: names})
	if err != nil {
		return nil, fmt.Errorf("findings of a repository: %w", err)
	}
	out := make([]Finding, len(rows))
	for i, r := range rows {
		out[i] = findingOf(r)
	}
	return out, nil
}

func findingOf(r q.VulnFindingsOfRepoNamesRow) Finding {
	return Finding{
		ID: r.ID, RepoID: r.RepoID, Name: r.PackageName, Version: r.Version, AdvisoryID: r.AdvisoryID, PackageID: r.PackageID,
		VersionID: r.VersionID, Kind: advisory.Kind(r.Kind), Severity: advisory.Severity(r.Severity), CVSS: r.CvssScore,
		KnownExploited: r.KnownExploited, FixAvailable: r.FixAvailable, FixedVersions: r.FixedVersions, Status: vuln.Status(r.Status),
		BeforeResolution: vuln.Status(r.StatusBeforeResolution), ResolvedReason: vuln.ResolvedReason(r.ResolvedReason),
		AdvisoryModifiedU: r.AdvisoryModifiedUs, Revision: r.Revision,
	}
}

// NewFinding is a finding to insert; its status is needs_triage.
type NewFinding struct {
	ID                string
	RepoID            int64
	Ecosystem         string
	Name, Version     string
	AdvisoryID        string
	PackageID         int64
	VersionID         int64
	Kind              advisory.Kind
	Severity          advisory.Severity
	CVSS              float64
	KnownExploited    bool
	FixAvailable      bool
	FixedVersions     []string
	AdvisoryModifiedU int64
}

// InsertFinding inserts a finding; it reports whether this call wrote the row. A finding of the same identity that exists is
// left as it is and false is returned: the caller reads it with [Eval.FindingByIdentity].
func (e *Eval) InsertFinding(ctx context.Context, f NewFinding) (bool, error) {
	ids, err := e.qs.VulnInsertFinding(ctx, q.VulnInsertFindingParams{
		ID: f.ID, RepoID: f.RepoID, Ecosystem: f.Ecosystem, PackageName: f.Name, Version: f.Version, AdvisoryID: f.AdvisoryID,
		PackageID: f.PackageID, VersionID: f.VersionID, Kind: string(f.Kind), Severity: string(f.Severity),
		SeverityRank: int16(f.Severity.Rank()), //nolint:gosec // a rank is 0 to 5
		CvssScore:    f.CVSS, KnownExploited: f.KnownExploited, FixAvailable: f.FixAvailable,
		FixedVersions: nonNil(f.FixedVersions), AdvisoryModifiedUs: f.AdvisoryModifiedU,
	})
	return len(ids) > 0, err
}

// FindingsOfRepoItem returns the findings of the package of an item's version in one repository, resolved ones too: the commit hook
// asks before it knows the name (Q-VULN-82).
func (e *Eval) FindingsOfRepoItem(ctx context.Context, repoID int64, ecosystem string, itemID int64) ([]Finding, error) {
	rows, err := e.qs.VulnFindingsOfRepoItem(ctx, q.VulnFindingsOfRepoItemParams{RepoID: repoID, Ecosystem: ecosystem, ItemID: itemID})
	if err != nil {
		return nil, fmt.Errorf("findings of a repository: %w", err)
	}
	out := make([]Finding, len(rows))
	for i, r := range rows {
		out[i] = findingOf(q.VulnFindingsOfRepoNamesRow(r))
	}
	return out, nil
}

// FindingByIdentity reads the finding of one identity; false when there is none.
func (e *Eval) FindingByIdentity(ctx context.Context, repoID int64, ecosystem, name, version, advisoryID string) (Finding, bool, error) {
	rows, err := e.qs.VulnFindingsByIdentity(ctx, q.VulnFindingsByIdentityParams{
		RepoID: repoID, Ecosystem: ecosystem, PackageName: name, Version: version, AdvisoryID: advisoryID,
	})
	if err != nil || len(rows) == 0 {
		return Finding{}, false, err
	}
	return findingOf(q.VulnFindingsOfRepoNamesRow(rows[0])), true, nil
}

// FindingByID reads one finding; false when it is gone.
func (e *Eval) FindingByID(ctx context.Context, id string) (Finding, bool, error) {
	rows, err := e.qs.VulnFindingByID(ctx, id)
	if err != nil || len(rows) == 0 {
		return Finding{}, false, err
	}
	return findingOf(q.VulnFindingsOfRepoNamesRow(rows[0])), true, nil
}

// RefreshFinding stores the advisory's facts as the evaluation last saw them and the version the finding points to.
func (e *Eval) RefreshFinding(ctx context.Context, id string, f NewFinding) error {
	return e.qs.VulnRefreshFinding(ctx, q.VulnRefreshFindingParams{
		ID: id, Kind: string(f.Kind), Severity: string(f.Severity), SeverityRank: int16(f.Severity.Rank()), //nolint:gosec // a rank is 0 to 5
		CvssScore:      f.CVSS,
		KnownExploited: f.KnownExploited, FixAvailable: f.FixAvailable, FixedVersions: nonNil(f.FixedVersions),
		AdvisoryModifiedUs: f.AdvisoryModifiedU, PackageID: f.PackageID, VersionID: f.VersionID,
	})
}

// SetStatus is trove's own status change: a re-detection or a reopen. It clears the resolution. It holds only while the finding
// still has the status and the revision the caller read, and reports whether it did: a finding that a triage changed meanwhile is
// left as it is.
func (e *Eval) SetStatus(ctx context.Context, id string, expect vuln.Status, revision int64, status vuln.Status) (bool, error) {
	n, err := e.qs.VulnSetFindingStatus(ctx, q.VulnSetFindingStatusParams{
		ID: id, Status: string(status), ExpectStatus: string(expect), ExpectRevision: revision,
	})
	return n > 0, err
}

// TouchModified stores the modification time of the advisory's record without a revision or a history entry.
func (e *Eval) TouchModified(ctx context.Context, id string, modifiedUs int64) error {
	return e.qs.VulnTouchFindingModified(ctx, q.VulnTouchFindingModifiedParams{ID: id, AdvisoryModifiedUs: modifiedUs})
}

// MergeInto moves the history of a finding to another one of the same version and deletes it.
func (e *Eval) MergeInto(ctx context.Context, from, to string) error {
	if err := e.qs.VulnMoveFindingEvents(ctx, q.VulnMoveFindingEventsParams{FromID: from, ToID: to}); err != nil {
		return err
	}
	return e.qs.VulnDeleteFinding(ctx, from)
}

// RecentEvent is one of the last events of a finding with its age.
type RecentEvent struct {
	Seq        int64
	Change     vuln.Change
	AgeSeconds float64
}

// RecentEvents returns the last events of a finding, newest first.
func (e *Eval) RecentEvents(ctx context.Context, findingID string, limit int) ([]RecentEvent, error) {
	rows, err := e.qs.VulnRecentFindingEvents(ctx, q.VulnRecentFindingEventsParams{FindingID: findingID, MaxRows: int32(limit)}) //nolint:gosec // a small count
	if err != nil {
		return nil, err
	}
	out := make([]RecentEvent, len(rows))
	for i, r := range rows {
		out[i] = RecentEvent{Seq: r.Seq, Change: vuln.Change(r.Change), AgeSeconds: r.AgeSeconds}
	}
	return out, nil
}

// DeleteEvent deletes one event of a finding's history.
func (e *Eval) DeleteEvent(ctx context.Context, findingID string, seq int64) error {
	return e.qs.VulnDeleteFindingEvent(ctx, q.VulnDeleteFindingEventParams{FindingID: findingID, Seq: seq})
}

// Rekey moves a finding to the new canonical identifier of its advisory group.
func (e *Eval) Rekey(ctx context.Context, id, advisoryID string) error {
	return e.qs.VulnRekeyFinding(ctx, q.VulnRekeyFindingParams{ID: id, AdvisoryID: advisoryID})
}

// Resolved is a finding trove resolved, with the status it had.
type Resolved struct {
	ID  string
	Old vuln.Status
}

// Resolve resolves the findings that are not resolved yet with a reason; the findings it changed are returned.
func (e *Eval) Resolve(ctx context.Context, ids []string, reason vuln.ResolvedReason) ([]Resolved, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := e.qs.VulnResolveFindings(ctx, q.VulnResolveFindingsParams{Ids: ids, Reason: string(reason)})
	if err != nil {
		return nil, fmt.Errorf("resolve findings: %w", err)
	}
	out := make([]Resolved, len(rows))
	for i, r := range rows {
		out[i] = Resolved{ID: r.ID, Old: vuln.Status(r.OldStatus)}
	}
	return out, nil
}

// ResolveRemovedVersions resolves the findings of the versions that have no stored item left, with version_removed.
func (e *Eval) ResolveRemovedVersions(ctx context.Context, versionIDs []int64) ([]Resolved, error) {
	if len(versionIDs) == 0 {
		return nil, nil
	}
	rows, err := e.qs.VulnResolveRemovedVersions(ctx, versionIDs)
	if err != nil {
		return nil, fmt.Errorf("resolve removed versions: %w", err)
	}
	out := make([]Resolved, len(rows))
	for i, r := range rows {
		out[i] = Resolved{ID: r.ID, Old: vuln.Status(r.OldStatus)}
	}
	return out, nil
}

// ResolveRepo resolves every finding of a repository that is not resolved, with scanning_disabled.
func (e *Eval) ResolveRepo(ctx context.Context, repoID int64) ([]Resolved, error) {
	rows, err := e.qs.VulnResolveRepoFindings(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("resolve a repository's findings: %w", err)
	}
	out := make([]Resolved, len(rows))
	for i, r := range rows {
		out[i] = Resolved{ID: r.ID, Old: vuln.Status(r.OldStatus)}
	}
	return out, nil
}

// AddEvent appends a row to the history of a finding; details is any JSON-encodable value or nil.
func (e *Eval) AddEvent(ctx context.Context, findingID string, change vuln.Change, from, to vuln.Status, reason string, details any) error {
	var d string
	if details != nil {
		b, err := json.Marshal(details)
		if err != nil {
			return fmt.Errorf("finding event details: %w", err)
		}
		d = string(b)
	}
	return e.qs.VulnInsertFindingEvent(ctx, q.VulnInsertFindingEventParams{
		FindingID: findingID, Change: string(change), FromStatus: string(from), ToStatus: string(to), Reason: reason, Details: d,
	})
}

// DeleteResolved deletes up to limit findings that resolved longer ago than the retention, with their events; it returns how
// many.
func (e *Eval) DeleteResolved(ctx context.Context, retention time.Duration, limit int) (int64, error) {
	return e.qs.VulnDeleteResolvedFindings(ctx, q.VulnDeleteResolvedFindingsParams{RetentionSeconds: retention.Seconds(), MaxRows: int32(limit)}) //nolint:gosec // a batch size
}

// InsertClaimedMatch records that an advisory names a claimed name; it reports whether the match is new.
func (e *Eval) InsertClaimedMatch(ctx context.Context, ecosystem, advisoryID, name string, kind advisory.Kind) (bool, error) {
	n, err := e.qs.VulnInsertClaimedMatch(ctx, q.VulnInsertClaimedMatchParams{Ecosystem: ecosystem, AdvisoryID: advisoryID, Name: name, Kind: string(kind)})
	return n > 0, err
}

// PruneClaimedMatches removes the matches of the names that are not among the pairs (keepNames[i], keepIDs[i]).
func (e *Eval) PruneClaimedMatches(ctx context.Context, ecosystem string, names, keepNames, keepIDs []string) error {
	keep := make([]string, len(keepNames))
	for i := range keepNames {
		keep[i] = keepNames[i] + "\n" + keepIDs[i]
	}
	return e.qs.VulnPruneClaimedMatches(ctx, q.VulnPruneClaimedMatchesParams{Ecosystem: ecosystem, Names: nonNil(names), Keep: keep})
}

// FullRequest is a requested full evaluation of an ecosystem (Q-VULN-18).
type FullRequest struct {
	Ecosystem   string
	RequestedUs int64
}

// FullEvaluations returns the ecosystems whose full evaluation is requested.
func (e *Eval) FullEvaluations(ctx context.Context) ([]FullRequest, error) {
	rows, err := e.qs.VulnSourceFullEvaluations(ctx)
	if err != nil {
		return nil, fmt.Errorf("full evaluation requests: %w", err)
	}
	out := make([]FullRequest, len(rows))
	for i, r := range rows {
		out[i] = FullRequest{Ecosystem: r.Ecosystem, RequestedUs: r.RequestedUs}
	}
	return out, nil
}

// ClearFullEvaluation clears the request when it still holds the value read at the run's start; it reports whether it did.
func (e *Eval) ClearFullEvaluation(ctx context.Context, r FullRequest) (bool, error) {
	n, err := e.qs.VulnClearFullEvaluation(ctx, q.VulnClearFullEvaluationParams{Ecosystem: r.Ecosystem, RequestedUs: r.RequestedUs})
	return n > 0, err
}

// DataComplete reports whether the ecosystem's advisory data has been imported completely, read from PostgreSQL.
func (e *Eval) DataComplete(ctx context.Context, ecosystem string) (bool, error) {
	return e.qs.VulnDataComplete(ctx, ecosystem)
}

// PendingCount counts the queued advisories of the ecosystems.
func (e *Eval) PendingCount(ctx context.Context, ecosystems []string) (int64, error) {
	return e.qs.VulnPendingCount(ctx, nonNil(ecosystems))
}

// Pending is a queued advisory with the time it was queued.
type Pending struct {
	Ecosystem, AdvisoryID string
	EnqueuedUs            int64
}

// PendingBatch returns the oldest queued advisories of the ecosystems; a row of another ecosystem stays queued for a build
// that can evaluate it.
func (e *Eval) PendingBatch(ctx context.Context, ecosystems []string, limit int) ([]Pending, error) {
	rows, err := e.qs.VulnPendingBatch(ctx, q.VulnPendingBatchParams{Ecosystems: nonNil(ecosystems), MaxRows: int32(limit)}) //nolint:gosec // a batch size
	if err != nil {
		return nil, fmt.Errorf("pending evaluation: %w", err)
	}
	out := make([]Pending, len(rows))
	for i, r := range rows {
		out[i] = Pending{Ecosystem: r.Ecosystem, AdvisoryID: r.AdvisoryID, EnqueuedUs: r.EnqueuedUs}
	}
	return out, nil
}

// DeletePending removes the queued advisories of a batch that still hold the time they were read with.
func (e *Eval) DeletePending(ctx context.Context, batch []Pending) error {
	var eco, ids []string
	var at []int64
	for _, p := range batch {
		eco, ids, at = append(eco, p.Ecosystem), append(ids, p.AdvisoryID), append(at, p.EnqueuedUs)
	}
	return e.qs.VulnDeletePending(ctx, q.VulnDeletePendingParams{Ecosystems: nonNil(eco), Ids: nonNil(ids), EnqueuedUs: nonNilInt(at)})
}

// GroupNames returns the names the alias groups of the advisories name now.
func (e *Eval) GroupNames(ctx context.Context, ecosystem string, ids []string) ([]string, error) {
	return e.qs.VulnGroupNames(ctx, q.VulnGroupNamesParams{Ecosystem: ecosystem, Ids: nonNil(ids)})
}

// CanonicalOf returns the canonical identifier of each stored record among the ids.
func (e *Eval) CanonicalOf(ctx context.Context, ecosystem string, ids []string) (map[string]string, error) {
	rows, err := e.qs.VulnCanonicalOfRecords(ctx, q.VulnCanonicalOfRecordsParams{Ecosystem: ecosystem, Ids: nonNil(ids)})
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.ID] = r.CanonicalID
	}
	return out, nil
}

// FindingNamesForAdvisories returns the names that findings of the advisories exist for.
func (e *Eval) FindingNamesForAdvisories(ctx context.Context, ecosystem string, ids []string) ([]string, error) {
	return e.qs.VulnFindingNamesForAdvisories(ctx, q.VulnFindingNamesForAdvisoriesParams{Ecosystem: ecosystem, Ids: nonNil(ids)})
}

// RekeyTargets returns, for each identifier that is the id or an alias of a stored record, the canonical identifier of that
// record's group.
func (e *Eval) RekeyTargets(ctx context.Context, ecosystem string, ids []string) (map[string]string, error) {
	rows, err := e.qs.VulnRekeyTargets(ctx, q.VulnRekeyTargetsParams{Ecosystem: ecosystem, Ids: nonNil(ids)})
	if err != nil {
		return nil, err
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	out := map[string]string{}
	for _, r := range rows {
		if want[r.ID] {
			out[r.ID] = r.CanonicalID
		}
		for _, a := range r.Aliases {
			if want[a] {
				out[a] = r.CanonicalID
			}
		}
	}
	return out, nil
}

// ClaimHolder is one claim of a local repository with the holder's configuration document, as the commit's transaction reads it.
type ClaimHolder struct {
	RepoID  int64
	Key     string
	Config  string
	Pattern string
	Scope   string
	Source  string
}

// ClaimHolders returns the claims of the local repositories of the format that can cover the normalized name, from PostgreSQL in
// the transaction. The query narrows by the name; the caller matches the patterns.
func (e *Eval) ClaimHolders(ctx context.Context, format, name string) ([]ClaimHolder, error) {
	rows, err := e.qs.VulnClaimHolders(ctx, q.VulnClaimHoldersParams{Format: format, Name: name})
	if err != nil {
		return nil, fmt.Errorf("claims of %s: %w", format, err)
	}
	out := make([]ClaimHolder, len(rows))
	for i, r := range rows {
		out[i] = ClaimHolder{RepoID: r.RepoID, Key: r.Key, Config: r.Config, Pattern: r.Pattern, Scope: r.Scope, Source: r.Source}
	}
	return out, nil
}

// ClaimHoldersForItem is [Eval.ClaimHolders] for the name of an item's version: the commit hook asks before it knows the name
// (Q-VULN-82).
func (e *Eval) ClaimHoldersForItem(ctx context.Context, format string, itemID int64) ([]ClaimHolder, error) {
	rows, err := e.qs.VulnClaimHoldersForItem(ctx, q.VulnClaimHoldersForItemParams{Format: format, ItemID: itemID})
	if err != nil {
		return nil, fmt.Errorf("claims of %s: %w", format, err)
	}
	out := make([]ClaimHolder, len(rows))
	for i, r := range rows {
		out[i] = ClaimHolder{RepoID: r.RepoID, Key: r.Key, Config: r.Config, Pattern: r.Pattern, Scope: r.Scope, Source: r.Source}
	}
	return out, nil
}

// RepoInputs is a repository row as a rescan reads it.
type RepoInputs struct {
	ID                                    int64
	Key, Class, Format, Lifecycle, Config string
}

// RepositoryInputs reads a repository row; false for a repository that does not exist.
func (e *Eval) RepositoryInputs(ctx context.Context, id int64) (RepoInputs, bool, error) {
	rows, err := e.qs.VulnRepositoryInputs(ctx, id)
	if err != nil || len(rows) == 0 {
		return RepoInputs{}, false, err
	}
	r := rows[0]
	return RepoInputs{ID: r.ID, Key: r.Key, Class: r.Class, Format: r.Format, Lifecycle: r.Lifecycle, Config: r.Config}, true, nil
}

func nonNilInt(s []int64) []int64 {
	if s == nil {
		return []int64{}
	}
	return s
}

// UnversionedItem is a cached file of a remote repository that no version belongs to.
type UnversionedItem struct {
	ItemID, RepoID int64
	Path           string
}

// UnversionedItems returns up to limit cached files of the remote repositories of the formats whose path matches the SQL pattern
// and that belong to no version, with an id after the cursor.
func (e *Eval) UnversionedItems(ctx context.Context, formats []string, pathLike string, after int64, limit int) ([]UnversionedItem, error) {
	rows, err := e.qs.VulnUnversionedItems(ctx, q.VulnUnversionedItemsParams{After: after, Formats: formats, PathLike: pathLike, MaxRows: int32(limit)}) //nolint:gosec // a batch size
	if err != nil {
		return nil, fmt.Errorf("unversioned items: %w", err)
	}
	out := make([]UnversionedItem, len(rows))
	for i, r := range rows {
		out[i] = UnversionedItem{ItemID: r.ItemID, RepoID: r.RepoID, Path: r.Path}
	}
	return out, nil
}
