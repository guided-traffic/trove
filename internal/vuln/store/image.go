package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
)

// SourceTrivy is the source of the vulnerability database of the image scanning in vuln_source_state, with the ecosystem
// advisory.EcosystemOCI: the row carries the state, the staleness and the failures, so that the sources API, the gauges and the alerts
// cover the database without a second mechanism (R-VULN-035).
const SourceTrivy = "trivy"

// The states of vuln_image_scan.state.
const (
	ImageScanPending = "pending"
	ImageScanScanned = "scanned"
	ImageScanFailed  = "failed"
)

var errImageTx = errors.New("vuln store: the transaction does not come from the PostgreSQL store")

// Images is the access to the tables of the image scanning.
type Images struct {
	run meta.Runner
}

// NewImages returns the access over the transaction runner.
func NewImages(run meta.Runner) *Images { return &Images{run: run} }

func (s *Images) tx(ctx context.Context, fn func(*q.Queries) error) error {
	return s.run.InTx(ctx, func(tx meta.Tx) error {
		ptx, ok := postgres.Unwrap(tx)
		if !ok {
			return errImageTx
		}
		return fn(q.New(ptx))
	})
}

// ImageDue is one manifest to work on.
type ImageDue struct {
	RepoID   int64
	Name     string
	Digest   [32]byte
	Attempts int
}

// Discover adds the scan rows of manifests that have none, at most limit, and reports how many it added.
func (s *Images) Discover(ctx context.Context, delay time.Duration, limit int) (n int64, err error) {
	err = s.tx(ctx, func(qs *q.Queries) (err error) {
		n, err = qs.VulnImageDiscover(ctx, q.VulnImageDiscoverParams{DelaySeconds: delay.Seconds(), MaxRows: rows32(limit)})
		return err
	})
	return n, err
}

// Due returns the next manifests to work on.
func (s *Images) Due(ctx context.Context, dbDigest string, maxAttempts, limit int) ([]ImageDue, error) {
	var out []ImageDue
	err := s.tx(ctx, func(qs *q.Queries) error {
		rows, err := qs.VulnImageDue(ctx, q.VulnImageDueParams{MaxAttempts: rows32(maxAttempts), DbDigest: dbDigest, MaxRows: rows32(limit)})
		if err != nil {
			return err
		}
		out = make([]ImageDue, 0, len(rows))
		for _, r := range rows {
			d := ImageDue{RepoID: r.RepoID, Name: r.Name, Attempts: int(r.Attempts)}
			copy(d.Digest[:], r.Digest)
			out = append(out, d)
		}
		return nil
	})
	return out, err
}

// DueCount is the progress total of a run.
func (s *Images) DueCount(ctx context.Context, dbDigest string) (n int64, err error) {
	err = s.tx(ctx, func(qs *q.Queries) (err error) {
		n, err = qs.VulnImageDueCount(ctx, dbDigest)
		return err
	})
	return n, err
}

// Failed records a scan that did not finish.
func (s *Images) Failed(ctx context.Context, repoID int64, name string, digest [32]byte, code, detail string, backoff time.Duration) error {
	return s.tx(ctx, func(qs *q.Queries) error {
		return qs.VulnImageFailed(ctx, q.VulnImageFailedParams{
			RepoID: repoID, Name: name, Digest: digest[:], ErrorCode: code, ErrorDetail: detail, BackoffSeconds: backoff.Seconds(),
		})
	})
}

// Deferred records a scan that did not start for lack of room in the spool of the worker: it stays pending with the reason, is due again
// after the wait, and no attempt is spent.
func (s *Images) Deferred(ctx context.Context, repoID int64, name string, digest [32]byte, code, detail string, wait time.Duration) error {
	return s.tx(ctx, func(qs *q.Queries) error {
		return qs.VulnImageDeferred(ctx, q.VulnImageDeferredParams{
			RepoID: repoID, Name: name, Digest: digest[:], ErrorCode: code, ErrorDetail: detail, WaitSeconds: wait.Seconds(),
		})
	})
}

// Rearm makes every manifest of a repository due again.
func (s *Images) Rearm(ctx context.Context, repoID int64) (n int64, err error) {
	err = s.tx(ctx, func(qs *q.Queries) (err error) {
		n, err = qs.VulnImageRearm(ctx, repoID)
		return err
	})
	return n, err
}

// Orphans returns open and dismissed findings of images whose manifest is gone.
func (s *Images) Orphans(ctx context.Context, limit int) ([]string, error) {
	var out []string
	err := s.tx(ctx, func(qs *q.Queries) (err error) {
		out, err = qs.VulnImageOrphanFindings(ctx, rows32(limit))
		return err
	})
	return out, err
}

// SBOM returns the gzip of the analysis of a manifest by an analyser version; found is false when there is none.
func (s *Images) SBOM(ctx context.Context, digest [32]byte, analyser string) (sbom []byte, found bool, err error) {
	err = s.tx(ctx, func(qs *q.Queries) error {
		rows, err := qs.VulnImageSBOMGet(ctx, q.VulnImageSBOMGetParams{Digest: digest[:], Analyser: analyser})
		if err != nil || len(rows) == 0 {
			return err
		}
		sbom, found = rows[0], true
		return nil
	})
	return sbom, found, err
}

// PutSBOM stores the gzip of an analysis.
func (s *Images) PutSBOM(ctx context.Context, digest [32]byte, analyser string, sbom []byte) error {
	return s.tx(ctx, func(qs *q.Queries) error {
		return qs.VulnImageSBOMPut(ctx, q.VulnImageSBOMPutParams{Digest: digest[:], Analyser: analyser, Sbom: sbom})
	})
}

// PruneSBOMs removes the analyses nothing refers to any more.
func (s *Images) PruneSBOMs(ctx context.Context, analyser string) (n int64, err error) {
	err = s.tx(ctx, func(qs *q.Queries) (err error) {
		n, err = qs.VulnImageSBOMPrune(ctx, analyser)
		return err
	})
	return n, err
}

// ImageDB is the vulnerability database in use.
type ImageDB struct {
	Digest     string
	Blob       [32]byte
	Size       int64
	UpdatedAt  time.Time
	NextUpdate time.Time
}

// DB reads the database in use; found is false before the first import.
func (s *Images) DB(ctx context.Context) (db ImageDB, found bool, err error) {
	err = s.tx(ctx, func(qs *q.Queries) error {
		rows, err := qs.VulnImageDBGet(ctx)
		if err != nil || len(rows) == 0 {
			return err
		}
		r := rows[0]
		db = ImageDB{Digest: r.Digest, Size: r.Size, UpdatedAt: microTime(r.UpdatedUs), NextUpdate: microTime(r.NextUpdateUs)}
		copy(db.Blob[:], r.BlobSha256)
		found = true
		return nil
	})
	return db, found, err
}

// PutDB records a new database in the transaction that links its blob.
func PutDB(ctx context.Context, tx meta.Tx, db ImageDB) error {
	qs, err := queries(tx)
	if err != nil {
		return err
	}
	return qs.VulnImageDBPut(ctx, q.VulnImageDBPutParams{
		Digest: db.Digest, BlobSha256: db.Blob[:], Size: db.Size, UpdatedUs: micros(db.UpdatedAt), NextUpdateUs: micros(db.NextUpdate),
	})
}

// ImageVerdict is what a pull of a manifest needs to know.
type ImageVerdict struct {
	Row vuln.BlockRow
	// ScanState is "" when the manifest has no scan row yet, else a state of vuln_image_scan; Detected says that a detection has finished
	// at some time, also when a later one failed.
	ScanState string
	Detected  bool
}

// Verdict reads the finding that blocks a manifest and how far the manifest has been scanned, in one statement.
func (s *Images) Verdict(ctx context.Context, repoID int64, name string, digest [32]byte, version string, blockMalicious bool) (ImageVerdict, error) {
	var out ImageVerdict
	err := s.tx(ctx, func(qs *q.Queries) error {
		r, err := qs.VulnImageVerdict(ctx, q.VulnImageVerdictParams{RepoID: repoID, Name: name, Digest: digest[:], Version: version, BlockMalicious: blockMalicious})
		if err != nil {
			return err
		}
		if r.FindingID != "" {
			out.Row = vuln.BlockRow{FindingID: r.FindingID, AdvisoryID: r.AdvisoryID, Kind: advisory.Kind(r.Kind), Status: vuln.Status(r.Status)}
		}
		out.ScanState, out.Detected = r.ScanState, r.Detected
		return nil
	})
	return out, err
}

// ImageManifest is a manifest a tag stands for, with how far it is scanned.
type ImageManifest struct {
	RepoID           int64
	Digest, Platform string
	State, ErrorCode string
	ScannedAt        *time.Time
	OSFamily, OSName string
	EOSL             bool
	Packages         int
}

// Manifests lists what a tag stands for in the repositories.
func (s *Images) Manifests(ctx context.Context, repos []int64, name, tag string) ([]ImageManifest, error) {
	var out []ImageManifest
	err := s.tx(ctx, func(qs *q.Queries) error {
		rows, err := qs.VulnImageManifests(ctx, q.VulnImageManifestsParams{Repos: nonNilInt(repos), Name: name, Tag: tag})
		if err != nil {
			return err
		}
		out = make([]ImageManifest, 0, len(rows))
		for _, r := range rows {
			m := ImageManifest{RepoID: r.RepoID, Digest: r.Digest, Platform: r.Platform, State: r.State, ErrorCode: r.ErrorCode, OSFamily: r.OsFamily, OSName: r.OsName, EOSL: r.OsEosl, Packages: int(r.Packages)}
			if r.ScannedUs > 0 {
				at := microTime(r.ScannedUs)
				m.ScannedAt = &at
			}
			out = append(out, m)
		}
		return nil
	})
	return out, err
}

// ImageTagCounts counts the open findings of an image name per tag and severity, over what the scope may read.
func (a *API) ImageTagCounts(ctx context.Context, sc Scope, name string, f Filter) ([]VersionCount, error) {
	var rows []q.VulnImageTagCountsRow
	err := a.db.Read(ctx, func(db q.DBTX) (err error) {
		rows, err = q.New(db).VulnImageTagCounts(ctx, q.VulnImageTagCountsParams{Whole: nid(sc.Whole), Rows: nn(sc.Rows), PackageRows: nn(sc.Packages), Name: name, HasRepos: f.HasRepos, Repos: nid(f.Repos)})
		return err
	})
	out := make([]VersionCount, 0, len(rows))
	for _, r := range rows {
		out = append(out, VersionCount{Version: r.Version, Severity: advisory.Severity(r.Severity), N: r.N})
	}
	return out, err
}

// ImageAdvisory is an advisory of a detection as it is stored.
type ImageAdvisory struct {
	ID, Summary, Details    string
	Modified, Published     time.Time
	Severity                advisory.Severity
	SeveritySource          string
	CVSSVersion, CVSSVector string
	CVSSScore               float64
	References              []string
	CWEs                    []string
	RecordSHA256            []byte
}

// ImageApplied is the record of a finished detection.
type ImageApplied struct {
	Name             string
	RepoID           int64
	Digest           [32]byte
	DBDigest         string
	OSFamily, OSName string
	EOSL             bool
	Packages         int
}

// ImageTx is the part of a detection's transaction that belongs to the image tables.
type ImageTx struct {
	qs *q.Queries
}

// ImageTxOf returns it for a transaction of the store.
func ImageTxOf(tx meta.Tx) (*ImageTx, error) {
	qs, err := queries(tx)
	if err != nil {
		return nil, err
	}
	return &ImageTx{qs: qs}, nil
}

// UpsertAdvisory writes the advisory of a finding; an unchanged record is left as it is.
func (t *ImageTx) UpsertAdvisory(ctx context.Context, a ImageAdvisory) error {
	refs, err := json.Marshal(referenceObjects(a.References))
	if err != nil {
		return err
	}
	return t.qs.VulnImageUpsertAdvisory(ctx, q.VulnImageUpsertAdvisoryParams{
		ID: a.ID, ModifiedUs: micros(a.Modified), PublishedUs: micros(a.Published), Summary: a.Summary, Details: a.Details,
		Severity: string(a.Severity), SeveritySource: a.SeveritySource, CvssVersion: a.CVSSVersion, CvssVector: a.CVSSVector, CvssScore: a.CVSSScore,
		Refs: refs, CweIds: nn(a.CWEs), RecordSha256: a.RecordSHA256,
	})
}

// referenceObjects is the shape vuln_advisory."references" holds for a record of the OSV sync.
func referenceObjects(urls []string) []map[string]string {
	out := make([]map[string]string, 0, len(urls))
	for _, u := range urls {
		out = append(out, map[string]string{"type": "WEB", "url": u})
	}
	return out
}

// KnownExploited returns the identifiers among ids that the catalog lists.
func (t *ImageTx) KnownExploited(ctx context.Context, ids []string) (map[string]bool, error) {
	rows, err := t.qs.VulnImageKnownExploited(ctx, nn(ids))
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(rows))
	for _, id := range rows {
		out[id] = true
	}
	return out, nil
}

// Findings returns the findings of one manifest, resolved ones too.
func (t *ImageTx) Findings(ctx context.Context, repoID int64, name, version string) ([]Finding, error) {
	rows, err := t.qs.VulnImageFindings(ctx, q.VulnImageFindingsParams{RepoID: repoID, Name: name, Version: version})
	if err != nil {
		return nil, err
	}
	out := make([]Finding, len(rows))
	for i, r := range rows {
		out[i] = findingOf(q.VulnFindingsOfRepoNamesRow(r))
	}
	return out, nil
}

// PackageID returns the package row of an image name, 0 for none.
func (t *ImageTx) PackageID(ctx context.Context, repoID int64, name string) (int64, error) {
	rows, err := t.qs.VulnImagePackageID(ctx, q.VulnImagePackageIDParams{RepoID: repoID, Name: name})
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	return rows[0], nil
}

// SetComponents stores the packages of the image the advisory affects, when they differ.
func (t *ImageTx) SetComponents(ctx context.Context, repoID int64, name, version, advisoryID string, components json.RawMessage) error {
	return t.qs.VulnImageSetComponents(ctx, q.VulnImageSetComponentsParams{Components: components, RepoID: repoID, Name: name, Version: version, AdvisoryID: advisoryID})
}

// Scanned records a finished detection.
func (t *ImageTx) Scanned(ctx context.Context, a ImageApplied) error {
	return t.qs.VulnImageScanned(ctx, q.VulnImageScannedParams{
		RepoID: a.RepoID, Name: a.Name, Digest: a.Digest[:], DbDigest: a.DBDigest, OsFamily: a.OSFamily, OsName: a.OSName, OsEosl: a.EOSL,
		Packages: int32(a.Packages), //nolint:gosec // bounded by the analyser's report
	})
}

// WithComponents adds the affected packages to the rows that are findings of an image; the other rows are returned as they are.
func (a *API) WithComponents(ctx context.Context, rows []Row) ([]Row, error) {
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.Ecosystem == advisory.EcosystemOCI {
			ids = append(ids, r.ID)
		}
	}
	if len(ids) == 0 {
		return rows, nil
	}
	found := make(map[string]json.RawMessage, len(ids))
	err := a.db.Read(ctx, func(db q.DBTX) error {
		list, err := q.New(db).VulnImageComponentsOf(ctx, ids)
		if err != nil {
			return err
		}
		for _, r := range list {
			found[r.ID] = r.Components
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if c, ok := found[rows[i].ID]; ok {
			rows[i].Components = c
		}
	}
	return rows, nil
}
