package store

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
)

// The statements of work package E (queries/vuln_api.sql): the report over the findings, the advisory view, the sources and
// the status change of a triager. Every read takes the caller's [Scope], the repositories and rows the decision point let
// the caller read (R-VULN-019), and applies it in the statement that selects, so that a count, a page and an export agree.

// DB is what the package needs of the database: reads with the failover retry, and transactions.
type DB interface {
	meta.Runner
	Read(ctx context.Context, fn func(q.DBTX) error) error
}

// API reads and writes through the statements of the report and the triage.
type API struct{ db DB }

// NewAPI returns the package's access to the database.
func NewAPI(db DB) *API { return &API{db: db} }

// Scope is what a caller may read, as the statements take it: the IDs of the repositories whose every finding the caller may
// read, the "<repository id>\x1f<package>\x1f<version>" entries of the versions it may read in the others, and the
// "<repository id>\x1f<package>" entries of the packages it may read in the others at every version (the decision does not look
// at the version, Q-VULN-81). Repositories travel as IDs so that the statements compare the indexed column vuln_finding.repo_id
// and not an expression over the names they return (R-VULN-028).
type Scope struct {
	Whole          []int64
	Rows, Packages []string
}

// RowEntry is the entry of Scope.Rows for a version of a repository.
func RowEntry(repoID int64, pkg, version string) string {
	return strconv.FormatInt(repoID, 10) + "\x1f" + pkg + "\x1f" + version
}

// PackageEntry is the entry of Scope.Packages for a package of a repository.
func PackageEntry(repoID int64, pkg string) string {
	return strconv.FormatInt(repoID, 10) + "\x1f" + pkg
}

// Filter narrows the findings a statement selects. The zero value selects every finding in scope that is not resolved.
type Filter struct {
	// Repos are the IDs of the physical repositories a repository filter stands for; HasRepos tells "no filter" from "names
	// nothing".
	Repos    []int64
	HasRepos bool
	Formats  []string
	Packages []string
	// PackagePrefix is matched case-insensitively against the normalized name.
	PackagePrefix string
	Versions      []string
	// Ecosystem is exact; Advisory is a canonical identifier or an alias.
	Ecosystem, Advisory string
	Severities          []string
	// Statuses empty means every status but resolved.
	Statuses []string
	Kind     string
	// KnownExploited, FixAvailable and Blocked are tri-state: nil does not filter.
	KnownExploited, FixAvailable, Blocked *bool
	// BlockReason is malicious or triage.
	BlockReason string
	// EnabledRepos and MaliciousRepos carry the policy of the repositories, by ID (vuln.Policy.QueryEnforce,
	// QueryBlockMalicious).
	EnabledRepos, MaliciousRepos []int64
	DetectedFrom, DetectedTo     *time.Time
	IDs                          []string
	// Q is a text that occurs in the package name, the advisory identifier or the summary.
	Q string
}

// Order is the sort of a list: severity, detectedAt, package, advisory or status.
type Order struct {
	Sort string
	Desc bool
}

// After is the position after which a page starts: the sort key of the last row of the previous page.
type After struct {
	Num int64
	Txt string
	ID  string
}

// ProjectOf is the project key of a repository's qualified name "<project>/<key>", the form the findings statements
// return as the repository (R-VULN-020); "" for a string that is none.
func ProjectOf(repository string) string {
	project, _, ok := strings.Cut(repository, "/")
	if !ok {
		return ""
	}
	return project
}

// Project is the key of the project of the finding's repository, the `project` of the findings API and the CSV column.
func (r Row) Project() string { return ProjectOf(r.Repository) }

// Project is the key of the project of the finding's repository.
func (l Locked) Project() string { return ProjectOf(l.Repository) }

// Row is one finding as the list reads it, with the lookups of its advisory.
type Row struct {
	// RepoID is the database ID of the finding's repository, the key of the topology; Repository is its qualified name for
	// the answer.
	RepoID                                                        int64
	ID, Repository, Format, Ecosystem, Package, Version, Advisory string
	Aliases                                                       []string
	Kind                                                          advisory.Kind
	Summary                                                       string
	Severity                                                      advisory.Severity
	// CVSSScore is negative when the advisory carries none.
	CVSSScore               float64
	CVSSVersion, CVSSVector string
	KnownExploited          bool
	KnownExploitedAddedAt   string
	FixAvailable            bool
	FixedVersions           []string
	Status                  vuln.Status
	DismissalReason         string
	ResolvedReason          string
	Comment                 string
	Revision                int64
	DetectedAt              time.Time
	ResolvedAt              *time.Time
	StatusChangedAt         time.Time
	StatusChangedBy         string
	StatusChangedByName     string
	KeyNum                  int64
	KeyTxt                  string
	// Components is the JSON of the packages inside an image the advisory affects (R-VULN-012); nil for a package finding.
	Components json.RawMessage
}

// rows32 is a row limit as the statements take it.
func rows32(n int) int32 { return int32(min(max(n, 0), math.MaxInt32)) }

func nid(s []int64) []int64 {
	if s == nil {
		return []int64{}
	}
	return s
}

func nn(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func stamp(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func tri(b *bool) (has, v bool) {
	if b == nil {
		return false, false
	}
	return true, *b
}

// Candidate is a (repository ID, package, version) triple a scope decides on.
type Candidate struct {
	RepoID           int64
	Package, Version string
}

// Candidates returns the triples of the findings of the repositories that match the filter, at most max; the caller asks for
// one more than it accepts.
func (a *API) Candidates(ctx context.Context, repoIDs []int64, f Filter, max int) ([]Candidate, error) {
	var rows []q.VulnScopeCandidatesRow
	err := a.db.Read(ctx, func(db q.DBTX) (err error) {
		rows, err = q.New(db).VulnScopeCandidates(ctx, q.VulnScopeCandidatesParams{
			Keys: nid(repoIDs), Formats: nn(f.Formats), Packages: nn(f.Packages), PackagePrefix: f.PackagePrefix,
			Versions: nn(f.Versions), Ids: nn(f.IDs), Statuses: nn(f.Statuses), MaxRows: rows32(max),
		})
		return err
	})
	out := make([]Candidate, 0, len(rows))
	for _, r := range rows {
		out = append(out, Candidate{RepoID: r.RepoID, Package: r.Name, Version: r.Version})
	}
	return out, err
}

// Pair is a (repository ID, package) pair a scope decides on once for every version.
type Pair struct {
	RepoID  int64
	Package string
}

// CandidatePackages returns the distinct pairs of the findings of the repositories that match the filter, read from at most max
// findings, and the number of findings read; the caller asks for one more than it accepts and compares the findings, not the pairs,
// with its limit. The statement does not group (the planner sorts a large number of packages to do it); the repeats are removed
// here.
func (a *API) CandidatePackages(ctx context.Context, repoIDs []int64, f Filter, max int) (pairs []Pair, findings int, err error) {
	var rows []q.VulnScopePackagesRow
	err = a.db.Read(ctx, func(db q.DBTX) (err error) {
		rows, err = q.New(db).VulnScopePackages(ctx, q.VulnScopePackagesParams{
			Keys: nid(repoIDs), Formats: nn(f.Formats), Packages: nn(f.Packages), PackagePrefix: f.PackagePrefix,
			Versions: nn(f.Versions), Ids: nn(f.IDs), Statuses: nn(f.Statuses), MaxRows: rows32(max),
		})
		return err
	})
	seen := make(map[Pair]struct{}, len(rows)/4)
	for _, r := range rows {
		p := Pair{RepoID: r.RepoID, Package: r.Name}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		pairs = append(pairs, p)
	}
	return pairs, len(rows), err
}

// PackageCandidates returns the triples of one package's stored versions in the repositories; version "" means every one.
func (a *API) PackageCandidates(ctx context.Context, repoIDs []int64, format, name, version string, max int) ([]Candidate, error) {
	var rows []q.VulnPackageCandidatesRow
	err := a.db.Read(ctx, func(db q.DBTX) (err error) {
		rows, err = q.New(db).VulnPackageCandidates(ctx, q.VulnPackageCandidatesParams{
			Keys: nid(repoIDs), Format: format, Name: name, Version: version, MaxRows: rows32(max),
		})
		return err
	})
	out := make([]Candidate, 0, len(rows))
	for _, r := range rows {
		out = append(out, Candidate{RepoID: r.RepoID, Package: r.Name, Version: r.Version})
	}
	return out, err
}

func (f Filter) listParams(sc Scope) q.VulnFindingsListParams {
	hasKE, ke := tri(f.KnownExploited)
	hasFix, fix := tri(f.FixAvailable)
	hasBl, bl := tri(f.Blocked)
	return q.VulnFindingsListParams{
		Whole: nid(sc.Whole), Rows: nn(sc.Rows), PackageRows: nn(sc.Packages), HasRepos: f.HasRepos, Repos: nid(f.Repos), Formats: nn(f.Formats),
		Packages: nn(f.Packages), PackagePrefix: f.PackagePrefix, Versions: nn(f.Versions), Ecosystem: f.Ecosystem,
		Advisory: f.Advisory, Severities: nn(f.Severities), Statuses: nn(f.Statuses), Kind: f.Kind,
		HasKnownExploited: hasKE, KnownExploited: ke, HasFixAvailable: hasFix, FixAvailable: fix, HasBlocked: hasBl, Blocked: bl,
		EnabledRepos: nid(f.EnabledRepos), MaliciousRepos: nid(f.MaliciousRepos), BlockReason: f.BlockReason,
		HasFrom: f.DetectedFrom != nil, DetectedFrom: stamp(f.DetectedFrom), HasTo: f.DetectedTo != nil, DetectedTo: stamp(f.DetectedTo),
		Ids: nn(f.IDs), Q: f.Q,
	}
}

// List returns at most max findings after the position in the order, so that the caller learns whether a next page exists.
func (a *API) List(ctx context.Context, sc Scope, f Filter, o Order, after *After, max int) ([]Row, error) {
	p := f.listParams(sc)
	p.Sort, p.Descending, p.MaxRows = o.Sort, o.Desc, rows32(max)
	if after != nil {
		p.HasAfter, p.AfterNum, p.AfterTxt, p.AfterID = true, after.Num, after.Txt, after.ID
	}
	var rows []q.VulnFindingsListRow
	err := a.db.Read(ctx, func(db q.DBTX) (err error) {
		rows, err = q.New(db).VulnFindingsList(ctx, p)
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		out = append(out, rowOf(r))
	}
	return out, nil
}

// ListTx is List inside a transaction, for the read that follows a change.
func (a *API) ListTx(ctx context.Context, tx meta.Tx, sc Scope, f Filter, o Order, max int) ([]Row, error) {
	qs, err := queries(tx)
	if err != nil {
		return nil, err
	}
	p := f.listParams(sc)
	p.Sort, p.Descending, p.MaxRows = o.Sort, o.Desc, rows32(max)
	rows, err := qs.VulnFindingsList(ctx, p)
	if err != nil {
		return nil, err
	}
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		out = append(out, rowOf(r))
	}
	return out, nil
}

func rowOf(r q.VulnFindingsListRow) Row {
	out := Row{
		ID: r.ID, RepoID: r.RepoID, Repository: r.RepoKey, Format: r.Format, Ecosystem: r.Ecosystem, Package: r.PackageName, Version: r.Version,
		Advisory: r.AdvisoryID, Aliases: r.Aliases, Kind: advisory.Kind(r.Kind), Summary: r.Summary,
		Severity: advisory.Severity(r.Severity), CVSSScore: r.CvssScore, CVSSVersion: r.CvssVersion, CVSSVector: r.CvssVector,
		KnownExploited: r.KnownExploited, KnownExploitedAddedAt: r.KevAdded, FixAvailable: r.FixAvailable,
		FixedVersions: r.FixedVersions, Status: vuln.Status(r.Status), DismissalReason: r.DismissalReason,
		ResolvedReason: r.ResolvedReason, Comment: r.Comment, Revision: r.Revision, DetectedAt: r.DetectedAt.Time.UTC(),
		StatusChangedAt: r.StatusChangedAt.Time.UTC(), StatusChangedBy: r.StatusChangedBy, StatusChangedByName: r.StatusChangedByName,
		KeyNum: r.KNum, KeyTxt: r.KTxt,
	}
	if r.ResolvedAt.Valid {
		t := r.ResolvedAt.Time.UTC()
		out.ResolvedAt = &t
	}
	return out
}

// Count returns the number of findings the filter selects in scope, at most cap.
func (a *API) Count(ctx context.Context, sc Scope, f Filter, limit int) (int64, error) {
	lp := f.listParams(sc)
	p := q.VulnFindingsCountParams{
		Whole: lp.Whole, Rows: lp.Rows, PackageRows: lp.PackageRows, HasRepos: lp.HasRepos, Repos: lp.Repos, Formats: lp.Formats, Packages: lp.Packages,
		PackagePrefix: lp.PackagePrefix, Versions: lp.Versions, Ecosystem: lp.Ecosystem, Advisory: lp.Advisory,
		Severities: lp.Severities, Statuses: lp.Statuses, Kind: lp.Kind, HasKnownExploited: lp.HasKnownExploited,
		KnownExploited: lp.KnownExploited, HasFixAvailable: lp.HasFixAvailable, FixAvailable: lp.FixAvailable,
		HasBlocked: lp.HasBlocked, Blocked: lp.Blocked, EnabledRepos: lp.EnabledRepos, MaliciousRepos: lp.MaliciousRepos,
		BlockReason: lp.BlockReason, HasFrom: lp.HasFrom, DetectedFrom: lp.DetectedFrom, HasTo: lp.HasTo, DetectedTo: lp.DetectedTo,
		Ids: lp.Ids, Q: lp.Q, Cap: rows32(limit),
	}
	var n int64
	err := a.db.Read(ctx, func(db q.DBTX) (err error) {
		n, err = q.New(db).VulnFindingsCount(ctx, p)
		return err
	})
	return n, err
}

// Event is one row of a finding's history.
type Event struct {
	At                                                    time.Time
	ActorID, ActorName, Change, From, To, Reason, Comment string
	Details                                               json.RawMessage
}

// Events returns the history of a finding, oldest first.
func (a *API) Events(ctx context.Context, id string) ([]Event, error) {
	var rows []q.VulnFindingEventsRow
	err := a.db.Read(ctx, func(db q.DBTX) (err error) {
		rows, err = q.New(db).VulnFindingEvents(ctx, id)
		return err
	})
	out := make([]Event, 0, len(rows))
	for _, r := range rows {
		e := Event{
			At: r.At.Time.UTC(), ActorID: r.ActorID, ActorName: r.ActorName, Change: r.Change, From: r.FromStatus,
			To: r.ToStatus, Reason: r.Reason, Comment: r.Comment,
		}
		if r.Details != "" {
			e.Details = json.RawMessage(r.Details)
		}
		out = append(out, e)
	}
	return out, err
}

// Advisory is an advisory group as the API shows it: the record of the canonical identifier, with the aliases of the group.
type Advisory struct {
	advisory.Advisory
	// CVSSScore is negative when the advisory carries none.
	CVSSScore             float64
	KnownExploitedAddedAt string
}

// Advisory resolves an identifier or alias of an ecosystem; found is false for an unknown one.
func (a *API) Advisory(ctx context.Context, ecosystem, id string) (out Advisory, found bool, err error) {
	var r q.VulnAdvisoryResolveRow
	err = a.db.Read(ctx, func(db q.DBTX) (err error) {
		r, err = q.New(db).VulnAdvisoryResolve(ctx, q.VulnAdvisoryResolveParams{Ecosystem: ecosystem, ID: id})
		return err
	})
	if postgres.IsNoRows(err) {
		return Advisory{}, false, nil
	}
	if err != nil {
		return Advisory{}, false, err
	}
	adv := advisory.Advisory{
		Ecosystem: r.Ecosystem, ID: r.ID, CanonicalID: r.ID, Kind: advisory.Kind(r.Kind), Aliases: r.Aliases, Summary: r.Summary,
		Details: r.Details, Severity: advisory.Severity(r.Severity), KnownExploited: r.KnownExploited, CWEIDs: r.CweIds,
		CVSS:     advisory.CVSS{Version: r.CvssVersion, Vector: r.CvssVector, Score: max(r.CvssScore, 0)},
		Modified: r.Modified.Time.UTC(),
	}
	if r.Published.Valid {
		adv.Published = r.Published.Time.UTC()
	}
	if r.Withdrawn.Valid {
		adv.Withdrawn = r.Withdrawn.Time.UTC()
	}
	if len(r.Refs) > 0 {
		if err := json.Unmarshal(r.Refs, &adv.References); err != nil {
			return Advisory{}, false, errs.Wrap(err, "vuln store: the references of an advisory")
		}
	}
	var raw []struct {
		Name     string           `json:"name"`
		Ranges   []advisory.Range `json:"ranges"`
		Versions []string         `json:"versions"`
		Fixed    []string         `json:"fixed"`
	}
	if r.Affected != "" {
		if err := json.Unmarshal([]byte(r.Affected), &raw); err != nil {
			return Advisory{}, false, errs.Wrap(err, "vuln store: the affected entries of an advisory")
		}
	}
	for _, e := range raw {
		adv.Affected = append(adv.Affected, advisory.Affected{Name: e.Name, Ranges: e.Ranges, Versions: e.Versions, Fixed: e.Fixed})
	}
	return Advisory{Advisory: adv, CVSSScore: r.CvssScore, KnownExploitedAddedAt: r.KevAdded}, true, nil
}

// VersionCount is the number of open findings of one severity of one version.
type VersionCount struct {
	Version  string
	Severity advisory.Severity
	N        int64
}

// PackageCounts returns the open findings of one package in scope, per version and severity.
func (a *API) PackageCounts(ctx context.Context, sc Scope, format, name string, f Filter) ([]VersionCount, error) {
	var rows []q.VulnPackageCountsRow
	err := a.db.Read(ctx, func(db q.DBTX) (err error) {
		rows, err = q.New(db).VulnPackageCounts(ctx, q.VulnPackageCountsParams{
			Whole: nid(sc.Whole), Rows: nn(sc.Rows), PackageRows: nn(sc.Packages), Format: format, Name: name,
			HasRepos: f.HasRepos, Repos: nid(f.Repos),
		})
		return err
	})
	out := make([]VersionCount, 0, len(rows))
	for _, r := range rows {
		out = append(out, VersionCount{Version: r.Version, Severity: advisory.Severity(r.Severity), N: r.N})
	}
	return out, err
}

// PackageCount is the number of open findings of one severity of one package.
type PackageCount struct {
	Format, Package string
	Severity        advisory.Severity
	N               int64
}

// PackageListCounts returns the open findings of the packages named in f (with f's repositories and formats) in scope, per format,
// package and severity: the one statement behind the vulnerability counts of a page of the packages list (R-UI-052).
func (a *API) PackageListCounts(ctx context.Context, sc Scope, f Filter) ([]PackageCount, error) {
	var rows []q.VulnPackageListCountsRow
	err := a.db.Read(ctx, func(db q.DBTX) (err error) {
		rows, err = q.New(db).VulnPackageListCounts(ctx, q.VulnPackageListCountsParams{
			Whole: nid(sc.Whole), Rows: nn(sc.Rows), PackageRows: nn(sc.Packages), Packages: nn(f.Packages),
			Formats: nn(f.Formats), HasRepos: f.HasRepos, Repos: nid(f.Repos),
		})
		return err
	})
	out := make([]PackageCount, 0, len(rows))
	for _, r := range rows {
		out = append(out, PackageCount{Format: r.Format, Package: r.PackageName, Severity: advisory.Severity(r.Severity), N: r.N})
	}
	return out, err
}

// SummaryRow counts the open findings of one severity and kind.
type SummaryRow struct {
	Severity                       advisory.Severity
	Kind                           advisory.Kind
	Total, NeedsTriage, FixAvailab int64
}

// SummaryCounts returns the open findings in scope per severity and kind, narrowed by repositories and formats.
func (a *API) SummaryCounts(ctx context.Context, sc Scope, f Filter) ([]SummaryRow, error) {
	var rows []q.VulnSummaryCountsRow
	err := a.db.Read(ctx, func(db q.DBTX) (err error) {
		rows, err = q.New(db).VulnSummaryCounts(ctx, q.VulnSummaryCountsParams{
			Whole: nid(sc.Whole), Rows: nn(sc.Rows), PackageRows: nn(sc.Packages), HasRepos: f.HasRepos, Repos: nid(f.Repos), Formats: nn(f.Formats),
		})
		return err
	})
	out := make([]SummaryRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, SummaryRow{
			Severity: advisory.Severity(r.Severity), Kind: advisory.Kind(r.Kind), Total: r.Total,
			NeedsTriage: r.NeedsTriage, FixAvailab: r.FixAvailable,
		})
	}
	return out, err
}

// BlockedVersions counts the versions in scope that an open finding blocks under the repositories' policy.
func (a *API) BlockedVersions(ctx context.Context, sc Scope, f Filter) (int64, error) {
	var n int64
	err := a.db.Read(ctx, func(db q.DBTX) (err error) {
		n, err = q.New(db).VulnSummaryBlocked(ctx, q.VulnSummaryBlockedParams{
			EnabledRepos: nid(f.EnabledRepos), MaliciousRepos: nid(f.MaliciousRepos), Whole: nid(sc.Whole), Rows: nn(sc.Rows), PackageRows: nn(sc.Packages),
			HasRepos: f.HasRepos, Repos: nid(f.Repos), Formats: nn(f.Formats),
		})
		return err
	})
	return n, err
}

// DayCount is the number of findings of one severity on one day (YYYY-MM-DD).
type DayCount struct {
	Day      string
	Severity advisory.Severity
	N        int64
}

// Trend returns the detections and the resolutions in scope up to the last day, per day and severity; the days before the
// first day are summed into the day before it.
func (a *API) Trend(ctx context.Context, sc Scope, f Filter, from, to string) (detected, resolved []DayCount, err error) {
	err = a.db.Read(ctx, func(db q.DBTX) error {
		qs := q.New(db)
		d, err := qs.VulnTrendDetected(ctx, q.VulnTrendDetectedParams{
			FromDay: from, ToDay: to, Whole: nid(sc.Whole), Rows: nn(sc.Rows), PackageRows: nn(sc.Packages),
			HasRepos: f.HasRepos, Repos: nid(f.Repos), Formats: nn(f.Formats), Kind: f.Kind,
		})
		if err != nil {
			return err
		}
		for _, r := range d {
			detected = append(detected, DayCount{Day: r.Day, Severity: advisory.Severity(r.Severity), N: r.N})
		}
		rs, err := qs.VulnTrendResolved(ctx, q.VulnTrendResolvedParams{
			FromDay: from, ToDay: to, Whole: nid(sc.Whole), Rows: nn(sc.Rows), PackageRows: nn(sc.Packages),
			HasRepos: f.HasRepos, Repos: nid(f.Repos), Formats: nn(f.Formats), Kind: f.Kind,
		})
		if err != nil {
			return err
		}
		for _, r := range rs {
			resolved = append(resolved, DayCount{Day: r.Day, Severity: advisory.Severity(r.Severity), N: r.N})
		}
		return nil
	})
	return detected, resolved, err
}

// SourceRow is a row of vuln_source_state as the sources operation shows it (SourceState is the gauges' and facts' view).
type SourceRow struct {
	Source, Ecosystem, State                 string
	SyncedAt, FullSyncedAt, AttemptedAt      *time.Time
	Failures, Records, Rejected, Unevaluable int
	// FullRejected is the rejected count of the last full sync; Rejected is that of the last run of any kind.
	FullRejected int
	// LastError is the problem object of the last failed run, nil when there is none.
	LastError json.RawMessage
}

func optT(valid bool, t time.Time) *time.Time {
	if !valid {
		return nil
	}
	u := t.UTC()
	return &u
}

// Sources returns every source row.
func (a *API) Sources(ctx context.Context) ([]SourceRow, error) { return ReadSources(ctx, a.db) }

// ReadSources is the one read of vuln_source_state: the sources operation, the system information and the gauges all go through
// it (A6), so that they cannot disagree on a row.
func ReadSources(ctx context.Context, db Database) ([]SourceRow, error) {
	var rows []q.VulnSourceStatesRow
	err := db.Read(ctx, func(c q.DBTX) (err error) {
		rows, err = q.New(c).VulnSourceStates(ctx)
		return err
	})
	out := make([]SourceRow, 0, len(rows))
	for _, r := range rows {
		s := SourceRow{
			Source: r.Source, Ecosystem: r.Ecosystem, State: r.State,
			SyncedAt: optT(r.SyncedAt.Valid, r.SyncedAt.Time), FullSyncedAt: optT(r.FullSyncedAt.Valid, r.FullSyncedAt.Time),
			AttemptedAt: optT(r.AttemptedAt.Valid, r.AttemptedAt.Time), Failures: int(r.Failures), Records: int(r.Records),
			Rejected: int(r.Rejected), Unevaluable: int(r.Unevaluable), FullRejected: int(r.FullRejected),
		}
		if r.LastError != "" {
			s.LastError = json.RawMessage(r.LastError)
		}
		out = append(out, s)
	}
	return out, err
}

// ClaimedMatch is a public advisory that names an internal name.
type ClaimedMatch struct {
	Ecosystem, Advisory, Name string
	Kind                      advisory.Kind
	FirstSeenAt               time.Time
}

// ClaimedAfter is the position of a page of claimed-name matches.
type ClaimedAfter struct{ Ecosystem, Advisory, Name string }

// ClaimedMatches returns at most max matches after the position, in the order of (ecosystem, advisory, name).
func (a *API) ClaimedMatches(ctx context.Context, after *ClaimedAfter, max int) ([]ClaimedMatch, error) {
	p := q.VulnClaimedMatchesParams{MaxRows: rows32(max)}
	if after != nil {
		p.HasAfter, p.AfterEcosystem, p.AfterAdvisory, p.AfterName = true, after.Ecosystem, after.Advisory, after.Name
	}
	var rows []q.VulnClaimedMatch
	err := a.db.Read(ctx, func(db q.DBTX) (err error) {
		rows, err = q.New(db).VulnClaimedMatches(ctx, p)
		return err
	})
	out := make([]ClaimedMatch, 0, len(rows))
	for _, r := range rows {
		out = append(out, ClaimedMatch{
			Ecosystem: r.Ecosystem, Advisory: r.AdvisoryID, Name: r.Name, Kind: advisory.Kind(r.Kind),
			FirstSeenAt: r.FirstSeenAt.Time.UTC(),
		})
	}
	return out, err
}

// Locked is a finding the caller may read, locked for a status change.
type Locked struct {
	RepoID                                                        int64
	ID, Repository, Format, Ecosystem, Package, Version, Advisory string
	Kind                                                          advisory.Kind
	Severity                                                      string
	Status                                                        vuln.Status
	DismissalReason, Comment                                      string
	Revision                                                      int64
}

func queries(tx meta.Tx) (*q.Queries, error) {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return nil, errors.New("vuln store: the transaction is not one of internal/meta/postgres")
	}
	return q.New(ptx), nil
}

// uuidShape is the canonical text of a finding id, the only form the old text comparison matched.
var uuidShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// uuidTexts keeps the ids that are canonical UUID text: the lock compares the array with the primary key (uuid[]), and an id
// that is none can match no finding but would fail the cast of the whole statement.
func uuidTexts(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if uuidShape.MatchString(id) {
			out = append(out, id)
		}
	}
	return out
}

// Lock locks the findings of the ids that are in scope, in id order; an id that is not in scope, or does not exist, is absent
// from the result.
func (a *API) Lock(ctx context.Context, tx meta.Tx, sc Scope, ids []string) ([]Locked, error) {
	qs, err := queries(tx)
	if err != nil {
		return nil, err
	}
	rows, err := qs.VulnFindingsLock(ctx, q.VulnFindingsLockParams{Ids: uuidTexts(ids), Whole: nid(sc.Whole), Rows: nn(sc.Rows), PackageRows: nn(sc.Packages)})
	if err != nil {
		return nil, err
	}
	out := make([]Locked, 0, len(rows))
	for _, r := range rows {
		out = append(out, Locked{
			ID: r.ID, RepoID: r.RepoID, Repository: r.RepoKey, Format: r.Format, Ecosystem: r.Ecosystem, Package: r.PackageName,
			Version: r.Version, Advisory: r.AdvisoryID, Kind: advisory.Kind(r.Kind), Severity: r.Severity, Status: vuln.Status(r.Status),
			DismissalReason: r.DismissalReason, Comment: r.Comment, Revision: r.Revision,
		})
	}
	return out, nil
}

// Change is a status change of one locked finding.
type Change struct {
	ID, From, To, DismissalReason string
	// Comment is stored when HasComment, else the finding keeps its comment.
	Comment    string
	HasComment bool
	// Actor is the principal's ID, "" for none. Expected is the revision the caller read, negative for any.
	Actor    string
	Expected int64
	// Details is the JSON object of the event's details ("" for none): the bulk operation id.
	Details string
}

// ErrStale says the finding's revision is no longer the expected one.
var ErrStale = errors.New("vuln store: the finding changed")

// SetStatus writes the change and its history row, and returns the new revision. A stale revision is [ErrStale].
func (a *API) SetStatus(ctx context.Context, tx meta.Tx, c Change) (int64, error) {
	qs, err := queries(tx)
	if err != nil {
		return 0, err
	}
	rev, err := qs.VulnFindingSetStatus(ctx, q.VulnFindingSetStatusParams{
		Status: c.To, DismissalReason: c.DismissalReason, HasComment: c.HasComment, Comment: c.Comment, Actor: c.Actor, ID: c.ID, Expected: c.Expected,
	})
	if postgres.IsNoRows(err) {
		return 0, ErrStale
	}
	if err != nil {
		return 0, err
	}
	comment := ""
	if c.HasComment {
		comment = c.Comment
	}
	err = qs.VulnFindingEventInsert(ctx, q.VulnFindingEventInsertParams{
		FindingID: c.ID, FromStatus: c.From, ToStatus: c.To, Reason: c.DismissalReason, Comment: comment, Actor: c.Actor, Details: c.Details,
	})
	return rev, err
}
