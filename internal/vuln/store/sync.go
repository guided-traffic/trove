package store

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
)

// The statements of work package A, the advisory sync (queries/vuln_sync.sql), over the sqlc queries. Every function takes the
// connection or transaction it runs on, so that a batch and the state it belongs to commit together.

// Source names of vuln_source_state.
const (
	SourceOSV = "osv"
	SourceKEV = "kev"
)

// State values of vuln_source_state.state.
const (
	StateEmpty    = "empty"
	StateComplete = "complete"
)

// Sync is the store of the advisory sync. It holds no state.
type Sync struct{}

// DBOf returns what the statements run on in a transaction of the store.
func DBOf(tx meta.Tx) (q.DBTX, error) {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return nil, errors.New("vuln store: the transaction does not come from the PostgreSQL store")
	}
	return ptx, nil
}

// SyncState is one row of vuln_source_state as the sync reads and writes it (R-VULN-004); SourceState is the view of the gauges and facts.
type SyncState struct {
	Source, Ecosystem string
	State             string
	DataEpoch         int64
	ETag              string
	ArchiveSHA256     []byte
	// The times are the zero time when unset.
	Watermark, FullSyncedAt, SyncedAt, AttemptedAt, EvalRequestedAt time.Time
	Failures                                                        int
	LastError                                                       json.RawMessage
	Records, Rejected, Unevaluable, MatcherVersion                  int
	// AdapterVersion is the version of the format adapter's normalization the stored records were imported with (match.AdapterVersion).
	AdapterVersion int
}

// Complete reports whether a full import of the source has finished.
func (s SyncState) Complete() bool { return s.State == StateComplete }

func microTime(us int64) time.Time {
	if us <= 0 {
		return time.Time{}
	}
	return time.UnixMicro(us).UTC()
}

func micros(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMicro()
}

func jsonOrNil(b []byte) json.RawMessage {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	return b
}

// Ensure creates the empty row of a source and ecosystem when there is none.
func (Sync) Ensure(ctx context.Context, db q.DBTX, source, ecosystem string) error {
	return q.New(db).VulnStateEnsure(ctx, q.VulnStateEnsureParams{Source: source, Ecosystem: ecosystem})
}

// Get reads the row; ok is false when there is none.
func (Sync) Get(ctx context.Context, db q.DBTX, source, ecosystem string) (SyncState, bool, error) {
	r, err := q.New(db).VulnStateGet(ctx, q.VulnStateGetParams{Source: source, Ecosystem: ecosystem})
	if err != nil {
		if postgres.IsNoRows(err) {
			return SyncState{}, false, nil
		}
		return SyncState{}, false, err
	}
	return stateOf(r.Source, r.Ecosystem, r.State, r.DataEpoch, r.Etag, r.ArchiveSha256, r.WatermarkUs, r.FullSyncedUs, r.SyncedUs,
		r.AttemptedUs, r.Failures, r.LastError, r.Records, r.Rejected, r.Unevaluable, r.MatcherVersion, r.EvalRequestedUs, r.AdapterVersion), true, nil
}

// Lock reads the row and locks it until the transaction ends; the row must exist ([Sync.Ensure]).
func (Sync) Lock(ctx context.Context, db q.DBTX, source, ecosystem string) (SyncState, error) {
	r, err := q.New(db).VulnStateLock(ctx, q.VulnStateLockParams{Source: source, Ecosystem: ecosystem})
	if err != nil {
		return SyncState{}, err
	}
	return stateOf(r.Source, r.Ecosystem, r.State, r.DataEpoch, r.Etag, r.ArchiveSha256, r.WatermarkUs, r.FullSyncedUs, r.SyncedUs,
		r.AttemptedUs, r.Failures, r.LastError, r.Records, r.Rejected, r.Unevaluable, r.MatcherVersion, r.EvalRequestedUs, r.AdapterVersion), nil
}

func stateOf(source, eco, state string, epoch int64, etag string, sha []byte, wm, full, synced, attempted int64, failures int32,
	lastErr []byte, records, rejected, unevaluable, matcher int32, evalReq int64, adapter int32,
) SyncState {
	return SyncState{
		Source: source, Ecosystem: eco, State: state, DataEpoch: epoch, ETag: etag, ArchiveSHA256: sha,
		Watermark: microTime(wm), FullSyncedAt: microTime(full), SyncedAt: microTime(synced), AttemptedAt: microTime(attempted),
		EvalRequestedAt: microTime(evalReq), Failures: int(failures), LastError: jsonOrNil(lastErr),
		Records: int(records), Rejected: int(rejected), Unevaluable: int(unevaluable), MatcherVersion: int(matcher), AdapterVersion: int(adapter),
	}
}

// StateSummary is what the replica's copy of the state needs of a row.
type StateSummary struct {
	Ecosystem string
	Complete  bool
	DataEpoch int64
	SyncedAt  time.Time
}

// List returns the rows of a source.
func (Sync) List(ctx context.Context, db q.DBTX, source string) ([]StateSummary, error) {
	rows, err := q.New(db).VulnStateList(ctx, source)
	if err != nil {
		return nil, err
	}
	out := make([]StateSummary, 0, len(rows))
	for _, r := range rows {
		out = append(out, StateSummary{Ecosystem: r.Ecosystem, Complete: r.State == StateComplete, DataEpoch: r.DataEpoch, SyncedAt: microTime(r.SyncedUs)})
	}
	return out, nil
}

// Save writes the state a run ends with: failures and last_error are cleared, attempted_at is now. The caller read the row with
// [Sync.Lock] and passes back what stays.
func (Sync) Save(ctx context.Context, db q.DBTX, s SyncState) error {
	return q.New(db).VulnStateSave(ctx, q.VulnStateSaveParams{
		State: s.State, DataEpoch: s.DataEpoch, Etag: s.ETag, ArchiveSha256: s.ArchiveSHA256,
		WatermarkUs: micros(s.Watermark), FullSyncedUs: micros(s.FullSyncedAt), SyncedUs: micros(s.SyncedAt),
		Records: int32(s.Records), Rejected: int32(s.Rejected), Unevaluable: int32(s.Unevaluable), //nolint:gosec // counts of one archive
		MatcherVersion: int32(s.MatcherVersion), EvalRequestedUs: micros(s.EvalRequestedAt), //nolint:gosec // a small constant
		AdapterVersion: int32(s.AdapterVersion), //nolint:gosec // a small constant
		Source:         s.Source, Ecosystem: s.Ecosystem,
	})
}

// Failure records a failed run: one more consecutive failure and the problem object.
func (Sync) Failure(ctx context.Context, db q.DBTX, source, ecosystem string, problem json.RawMessage) error {
	return q.New(db).VulnStateFailure(ctx, q.VulnStateFailureParams{LastError: problem, Source: source, Ecosystem: ecosystem})
}

// Cancelled records a run whose job an operator cancelled: the problem object, with the consecutive failures as they are
// (Q-VULN-80).
func (Sync) Cancelled(ctx context.Context, db q.DBTX, source, ecosystem string, problem json.RawMessage) error {
	return q.New(db).VulnStateCancelled(ctx, q.VulnStateCancelledParams{LastError: problem, Source: source, Ecosystem: ecosystem})
}

// Attempt records that a run started and returns the attempted_at it wrote. The row must exist ([Sync.Ensure]).
func (Sync) Attempt(ctx context.Context, db q.DBTX, source, ecosystem string) (time.Time, error) {
	us, err := q.New(db).VulnStateAttempt(ctx, q.VulnStateAttemptParams{Source: source, Ecosystem: ecosystem})
	return microTime(us), err
}

// BumpEpoch raises the data epoch of an ecosystem's advisory data and returns the new value.
func (Sync) BumpEpoch(ctx context.Context, db q.DBTX, ecosystem string) (int64, error) {
	return q.New(db).VulnStateBumpEpoch(ctx, ecosystem)
}

// Stored is what a batch compares a record with.
type Stored struct {
	Modified    time.Time
	SHA256      []byte
	CanonicalID string
}

// Stored returns the stored modified time and digest of the identifiers that exist.
func (Sync) Stored(ctx context.Context, db q.DBTX, ecosystem string, ids []string) (map[string]Stored, error) {
	rows, err := q.New(db).VulnAdvisoryStored(ctx, q.VulnAdvisoryStoredParams{Ecosystem: ecosystem, Ids: ids})
	if err != nil {
		return nil, err
	}
	out := make(map[string]Stored, len(rows))
	for _, r := range rows {
		out[r.ID] = Stored{Modified: microTime(r.ModifiedUs), SHA256: r.RecordSha256, CanonicalID: r.CanonicalID}
	}
	return out, nil
}

// IDs returns every stored advisory identifier of the ecosystem.
func (Sync) IDs(ctx context.Context, db q.DBTX, ecosystem string) ([]string, error) {
	return q.New(db).VulnAdvisoryIDs(ctx, ecosystem)
}

// Count returns the number of stored advisories of the ecosystem.
func (Sync) Count(ctx context.Context, db q.DBTX, ecosystem string) (int, error) {
	n, err := q.New(db).VulnAdvisoryCount(ctx, ecosystem)
	return int(n), err
}

// Written is one record of a batch with the digest of the bytes it was decoded from.
type Written struct {
	Advisory advisory.Advisory
	SHA256   []byte
}

type advisoryDoc struct {
	ID           string               `json:"id"`
	Modified     time.Time            `json:"modified"`
	Published    *time.Time           `json:"published"`
	Withdrawn    *time.Time           `json:"withdrawn"`
	Kind         string               `json:"kind"`
	Aliases      []string             `json:"aliases"`
	Summary      string               `json:"summary"`
	Details      string               `json:"details"`
	Severity     string               `json:"severity"`
	SeveritySrc  string               `json:"severity_source"`
	CVSSVersion  *string              `json:"cvss_version"`
	CVSSVector   *string              `json:"cvss_vector"`
	CVSSScore    *float64             `json:"cvss_score"`
	Refs         []advisory.Reference `json:"refs"`
	CWEIDs       []string             `json:"cwe_ids"`
	RecordSHA256 string               `json:"record_sha256"`
}

type affectedDoc struct {
	AdvisoryID   string           `json:"advisory_id"`
	Ord          int              `json:"ord"`
	Name         string           `json:"name"`
	Ranges       []advisory.Range `json:"ranges"`
	Versions     []string         `json:"versions"`
	EveryVersion bool             `json:"every_version"`
	Fixed        []string         `json:"fixed"`
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Upsert writes the records of a batch and replaces their affected entries. A rewritten record keeps its canonical identifier
// and known-exploited flag until [Sync.SetCanonical] and [Sync.ApplyKEV] decide them.
func (Sync) Upsert(ctx context.Context, db q.DBTX, ecosystem string, batch []Written) error {
	if len(batch) == 0 {
		return nil
	}
	docs := make([]advisoryDoc, 0, len(batch))
	var entries []affectedDoc
	ids := make([]string, 0, len(batch))
	for _, w := range batch {
		a := w.Advisory
		d := advisoryDoc{
			ID: a.ID, Modified: a.Modified, Kind: string(a.Kind), Aliases: nonNil(a.Aliases), Summary: a.Summary, Details: a.Details,
			Severity: string(a.Severity), SeveritySrc: string(a.SeveritySource), Refs: a.References, CWEIDs: nonNil(a.CWEIDs),
			RecordSHA256: hex.EncodeToString(w.SHA256),
		}
		if d.Refs == nil {
			d.Refs = []advisory.Reference{}
		}
		if !a.Published.IsZero() {
			d.Published = &a.Published
		}
		if !a.Withdrawn.IsZero() {
			d.Withdrawn = &a.Withdrawn
		}
		if a.CVSS.Present() {
			v, vec, s := a.CVSS.Version, a.CVSS.Vector, a.CVSS.Score
			d.CVSSVersion, d.CVSSVector, d.CVSSScore = &v, &vec, &s
		}
		docs = append(docs, d)
		ids = append(ids, a.ID)
		for i, e := range a.Affected {
			entries = append(entries, affectedDoc{
				AdvisoryID: a.ID, Ord: i, Name: e.Name, Ranges: e.Ranges, Versions: nonNil(e.Versions),
				EveryVersion: e.EveryVersion, Fixed: nonNil(e.Fixed),
			})
		}
	}
	body, err := json.Marshal(docs)
	if err != nil {
		return err
	}
	qs := q.New(db)
	if err := qs.VulnAdvisoryUpsert(ctx, q.VulnAdvisoryUpsertParams{Ecosystem: ecosystem, Docs: body}); err != nil {
		return err
	}
	if err := qs.VulnAffectedDelete(ctx, q.VulnAffectedDeleteParams{Ecosystem: ecosystem, Ids: ids}); err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	for i := range entries {
		if entries[i].Ranges == nil {
			entries[i].Ranges = []advisory.Range{}
		}
	}
	body, err = json.Marshal(entries)
	if err != nil {
		return err
	}
	return qs.VulnAffectedInsert(ctx, q.VulnAffectedInsertParams{Ecosystem: ecosystem, Entries: body})
}

// AddPending queues advisories for the evaluation (R-VULN-011).
func (Sync) AddPending(ctx context.Context, db q.DBTX, ecosystem string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return q.New(db).VulnPendingAdd(ctx, q.VulnPendingAddParams{Ecosystem: ecosystem, Ids: ids})
}

// Delete deletes advisories and their affected entries and returns how many.
func (Sync) Delete(ctx context.Context, db q.DBTX, ecosystem string, ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	n, err := q.New(db).VulnAdvisoryDelete(ctx, q.VulnAdvisoryDeleteParams{Ecosystem: ecosystem, Ids: ids})
	return int(n), err
}

// GroupRow is a stored record as the alias grouping reads it.
type GroupRow struct {
	ID, CanonicalID string
	Aliases         []string
	Kind            advisory.Kind
	// Withdrawn is true for a record the source has withdrawn; a withdrawn record is never the canonical record of a group that
	// has another (R-VULN-006, Q-VULN-61).
	Withdrawn bool
	// Severity, SeveritySource and CVSS are the severity decided for the record by its group (R-VULN-007 rule 3), what the
	// readers read; Own is what the record itself decides (rules 1, 2, 4, 5), the input of the group decision (Q-VULN-62).
	Severity       advisory.Severity
	SeveritySource advisory.SeveritySource
	CVSS           advisory.CVSS
	Own            GroupOwn
}

// GroupOwn is the severity a record decides for itself.
type GroupOwn struct {
	Severity       advisory.Severity
	SeveritySource advisory.SeveritySource
	CVSS           advisory.CVSS
}

// GroupRows returns the stored records that are, or alias, any of the names.
func (Sync) GroupRows(ctx context.Context, db q.DBTX, ecosystem string, names []string) ([]GroupRow, error) {
	rows, err := q.New(db).VulnAdvisoryGroupRows(ctx, q.VulnAdvisoryGroupRowsParams{Ecosystem: ecosystem, Names: names})
	if err != nil {
		return nil, err
	}
	out := make([]GroupRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, GroupRow{
			ID: r.ID, CanonicalID: r.CanonicalID, Aliases: r.Aliases, Kind: advisory.Kind(r.Kind), Withdrawn: r.IsWithdrawn,
			Severity: advisory.Severity(r.Severity), SeveritySource: advisory.SeveritySource(r.SeveritySource),
			CVSS: advisory.CVSS{Version: r.CvssVersion, Vector: r.CvssVector, Score: r.CvssScore},
			Own: GroupOwn{
				Severity: advisory.Severity(r.OwnSeverity), SeveritySource: advisory.SeveritySource(r.OwnSeveritySource),
				CVSS: advisory.CVSS{Version: r.OwnCvssVersion, Vector: r.OwnCvssVector, Score: r.OwnCvssScore},
			},
		})
	}
	return out, nil
}

// SetCanonical gives the records the canonical identifier of their group.
func (Sync) SetCanonical(ctx context.Context, db q.DBTX, ecosystem string, ids []string, canonical string) error {
	return q.New(db).VulnAdvisorySetCanonical(ctx, q.VulnAdvisorySetCanonicalParams{Ecosystem: ecosystem, Ids: ids, Canonical: canonical})
}

// SetSeverity writes the severity of a canonical record decided with its aliases.
func (Sync) SetSeverity(ctx context.Context, db q.DBTX, ecosystem, id string, level advisory.Severity, src advisory.SeveritySource, c advisory.CVSS) error {
	return q.New(db).VulnAdvisorySetSeverity(ctx, q.VulnAdvisorySetSeverityParams{
		Severity: string(level), SeveritySource: string(src), CvssVersion: c.Version, CvssVector: c.Vector, CvssScore: c.Score,
		Ecosystem: ecosystem, ID: id,
	})
}

// KEV is one row of vuln_kev.
type KEV struct {
	CVE        string `json:"cve_id"`
	DateAdded  string `json:"date_added"`
	DueDate    string `json:"due_date"`
	Ransomware string `json:"ransomware"`
}

// ReplaceKEV replaces the catalog.
func (Sync) ReplaceKEV(ctx context.Context, db q.DBTX, entries []KEV) error {
	qs := q.New(db)
	if err := qs.VulnKEVDelete(ctx); err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	body, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	return qs.VulnKEVInsert(ctx, body)
}

// KEVIDs returns every listed identifier.
func (Sync) KEVIDs(ctx context.Context, db q.DBTX) ([]string, error) {
	return q.New(db).VulnKEVIDs(ctx)
}

// KEVCandidates returns the canonical records of the ecosystem whose flag the listed identifiers may change.
func (Sync) KEVCandidates(ctx context.Context, db q.DBTX, ecosystem string, cves []string) ([]string, error) {
	return q.New(db).VulnKEVCandidates(ctx, q.VulnKEVCandidatesParams{Ecosystem: ecosystem, Cves: cves})
}

// ApplyKEV sets the known-exploited flag of the canonical records among ids from the catalog and returns the records that
// changed.
func (Sync) ApplyKEV(ctx context.Context, db q.DBTX, ecosystem string, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return q.New(db).VulnKEVApply(ctx, q.VulnKEVApplyParams{Ecosystem: ecosystem, Ids: ids})
}

// TouchedAdd records names of a run in the transaction that wrote them (Q-VULN-62).
func (Sync) TouchedAdd(ctx context.Context, db q.DBTX, ecosystem string, names []string) error {
	if len(names) == 0 {
		return nil
	}
	return q.New(db).VulnTouchedAdd(ctx, q.VulnTouchedAddParams{Ecosystem: ecosystem, Names: names})
}

// TouchedAddRecords records the identifiers and aliases of stored records, before they are deleted.
func (Sync) TouchedAddRecords(ctx context.Context, db q.DBTX, ecosystem string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return q.New(db).VulnTouchedAddRecords(ctx, q.VulnTouchedAddRecordsParams{Ecosystem: ecosystem, Ids: ids})
}

// TouchedPage returns up to max touched names of the ecosystem that sort after the given one.
func (Sync) TouchedPage(ctx context.Context, db q.DBTX, ecosystem, after string, max int) ([]string, error) {
	return q.New(db).VulnTouchedPage(ctx, q.VulnTouchedPageParams{Ecosystem: ecosystem, After: after, MaxRows: int32(max)}) //nolint:gosec // a page size
}

// TouchedExists reports whether a run of the ecosystem left names behind.
func (Sync) TouchedExists(ctx context.Context, db q.DBTX, ecosystem string) (bool, error) {
	return q.New(db).VulnTouchedExists(ctx, ecosystem)
}

// TouchedClear ends the set of the ecosystem; it runs in the transaction that ends the run.
func (Sync) TouchedClear(ctx context.Context, db q.DBTX, ecosystem string) error {
	return q.New(db).VulnTouchedClear(ctx, ecosystem)
}

// Orphans returns the records of the ecosystem whose canonical record is gone.
func (Sync) Orphans(ctx context.Context, db q.DBTX, ecosystem string) ([]string, error) {
	return q.New(db).VulnAdvisoryOrphans(ctx, ecosystem)
}

// Analyze refreshes the planner statistics of the tables a large import fills (Q-VULN-96).
func (Sync) Analyze(ctx context.Context, db q.DBTX) error {
	return q.New(db).VulnSyncAnalyze(ctx)
}
