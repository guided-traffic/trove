// Package imagescan scans the container images trove stores for known vulnerabilities (docs/spec/vulnerability-scanning.md R-VULN-033 to
// R-VULN-037). A scan has two steps, kept apart on purpose:
//
//	analysis   the layers of a manifest are read by the analyser and reduced to the list of what the image contains. It needs no database,
//	           runs at most once per manifest digest and analyser version, and its result is kept (vuln_image_sbom).
//	detection  the list is compared with the vulnerability database through the [Database] seam, again whenever the database changes. It
//	           reads no layer.
//
// The result enters the lifecycle of findings through the evaluation (evaluate.Service.ApplyImage), so an image's finding has the
// statuses, triage, report and blocking of a package's. Work lives in the table vuln_image_scan, not in a job's arguments, so a coalesced
// enqueue, a lost worker and a drain lose nothing.
package imagescan

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/internal/vuln/evaluate"
	vmetrics "github.com/guided-traffic/trove/internal/vuln/metrics"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// The working bounds of the job.
const (
	discoverBatch  = 1000
	orphanBatch    = 1000
	maxSBOMBytes   = int64(256) << 20
	maxSBOMStored  = 64 << 20
	maxReportBytes = int64(512) << 20
	stderrBytes    = 16 << 10
	// retryBackoff is the wait before the first retry of a scan that failed; it doubles per attempt.
	retryBackoff = time.Minute
	maxBackoff   = 6 * time.Hour
)

// Run is the part of a job's run handle the scan uses. *jobs.Run implements it.
type Run interface {
	SaveProgress(ctx context.Context, done, total int64, unit string) error
	SetResult(v any) error
}

var _ Run = (*jobs.Run)(nil)

// Options configure [New].
type Options struct {
	DB       meta.Runner
	Images   *store.Images
	Evaluate *evaluate.Service
	Blobs    BlobReader
	// Analyser and Database are the two halves of a scan; Database is the seam of Q-VULN-137.
	Analyser Analyser
	Database Database
	// States answers whether the database is complete, from the replica's cache.
	States vuln.States
	// Enqueue wakes the job; it runs in the caller's transaction.
	Enqueue func(ctx context.Context, tx meta.Tx) error
	// ScratchDir is the root of the working directories of the scans, on the worker's local disk.
	ScratchDir string
	// Concurrency is the number of images scanned at a time by one job.
	Concurrency int
	// RunSlice is how long one job keeps its slot before it hands the rest to the next run, so that an advisory sync waiting on the same
	// queue gets its turn.
	RunSlice time.Duration
	// MaxAttempts bounds the retries of a scan that failed for a reason that may pass.
	MaxAttempts int
	// Delay postpones a new manifest, so that a pull of a remote image has brought its layers.
	Delay   time.Duration
	Content ContentLimits
	Run     Limits
	Metrics *vmetrics.Metrics
	Log     *slog.Logger
	Now     func() time.Time
}

// Service runs the scans.
type Service struct {
	o   Options
	log *slog.Logger
}

// New returns the service.
func New(o Options) (*Service, error) {
	switch {
	case o.DB == nil || o.Images == nil || o.Evaluate == nil:
		return nil, errors.New("imagescan: the database and the evaluation are required")
	case o.Analyser == nil || o.Database == nil || o.Blobs == nil:
		return nil, errors.New("imagescan: the analyser, the database and the blob reader are required")
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Concurrency <= 0 {
		o.Concurrency = 1
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 5
	}
	return &Service{o: o, log: o.Log}, nil
}

// RunScan is the handler of vuln-image-scan (R-VULN-034): it adds the manifests that have no scan row, resolves the findings of manifests
// that are gone, and works off what is due. It returns when nothing is due or its time slice ended, the rest then in a job of its own. A
// context that ends stops the run; the rows say how far it came, so a released or lost job loses nothing.
func (s *Service) RunScan(ctx context.Context, run Run) error {
	if err := s.discover(ctx); err != nil {
		return err
	}
	if err := s.resolveOrphans(ctx); err != nil {
		return err
	}
	access, err := s.o.Database.Acquire(ctx)
	if errors.Is(err, ErrNoDatabase) {
		s.count("no_database")
		return run.SetResult(map[string]string{"skipped": "no vulnerability database has been imported yet"})
	}
	if err != nil {
		return fmt.Errorf("the vulnerability database: %w", err)
	}
	defer access.Release()
	total, err := s.o.Images.DueCount(ctx, access.Digest)
	if err != nil {
		return err
	}
	if _, err := s.o.Images.PruneSBOMs(ctx, s.o.Analyser.Version()); err != nil {
		s.log.WarnContext(ctx, "pruning the analyses failed", "error", err)
	}
	deadline := s.o.Now().Add(s.o.RunSlice)
	var done int64
	for s.o.RunSlice <= 0 || s.o.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return context.Cause(ctx)
		}
		due, err := s.o.Images.Due(ctx, access.Digest, s.o.MaxAttempts, s.o.Concurrency)
		if err != nil {
			return err
		}
		if len(due) == 0 {
			return run.SetResult(map[string]any{"done": done, "database": access.Digest})
		}
		if err := s.scanBatch(ctx, due, access); err != nil {
			return err
		}
		done += int64(len(due))
		if err := run.SaveProgress(ctx, done, max(total, done), "manifests"); err != nil {
			return err
		}
	}
	// The slice ended with work left: a job of its own continues it after whatever waits on the queue.
	if s.o.Enqueue != nil {
		if err := s.o.DB.InTx(ctx, func(tx meta.Tx) error { return s.o.Enqueue(ctx, tx) }); err != nil {
			return err
		}
	}
	return run.SetResult(map[string]any{"done": done, "database": access.Digest, "continued": true})
}

func (s *Service) discover(ctx context.Context) error {
	for {
		n, err := s.o.Images.Discover(ctx, s.o.Delay, discoverBatch)
		if err != nil {
			return fmt.Errorf("the discovery of manifests: %w", err)
		}
		if n < discoverBatch {
			return nil
		}
	}
}

func (s *Service) resolveOrphans(ctx context.Context) error {
	for {
		ids, err := s.o.Images.Orphans(ctx, orphanBatch)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		if err := s.o.DB.InTx(ctx, func(tx meta.Tx) error { return s.o.Evaluate.ResolveImageFindings(ctx, tx, ids) }); err != nil {
			return err
		}
		if len(ids) < orphanBatch {
			return nil
		}
	}
}

// scanBatch scans the manifests in parallel. An image whose scan fails is recorded and the run goes on; only an error of the worker's own
// (the database, a released context) ends it.
func (s *Service) scanBatch(ctx context.Context, due []store.ImageDue, access DatabaseAccess) error {
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	for _, d := range due {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.scanOne(ctx, d, access); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return firstErr
}

// scanOne works off one manifest. A [ScanError] and a detection that cannot be read are recorded on the row: the absence of findings of a
// manifest that could not be scanned must never read as clean.
func (s *Service) scanOne(ctx context.Context, d store.ImageDue, access DatabaseAccess) error {
	started := s.o.Now()
	ref := ImageRef{RepoID: d.RepoID, Name: d.Name, Digest: d.Digest}
	err := s.detect(ctx, ref, access)
	var se *ScanError
	switch {
	case err == nil:
		s.count("scanned")
		s.observe(started)
		return nil
	case ctx.Err() != nil:
		return context.Cause(ctx)
	case errors.As(err, &se):
		s.count(se.Code)
		return s.recordFailure(ctx, d, se.Code, se.Detail, se.Terminal)
	}
	s.count("error")
	s.log.ErrorContext(ctx, "an image could not be scanned", "repository", d.RepoID, "name", d.Name, "digest", ref.DigestString(), "error", err)
	return s.recordFailure(ctx, d, "scan_error", "the scan failed; the worker's log has the cause", false)
}

// recordFailure records a scan that did not finish. A terminal failure is not retried: it spends all its attempts at once.
func (s *Service) recordFailure(ctx context.Context, d store.ImageDue, code, detail string, terminal bool) error {
	backoff := min(retryBackoff<<min(d.Attempts, 10), maxBackoff)
	if terminal {
		backoff = 100 * maxBackoff
	}
	s.log.WarnContext(ctx, "an image could not be scanned", "repository", d.RepoID, "name", d.Name, "code", code)
	return s.o.Images.Failed(ctx, d.RepoID, d.Name, d.Digest, code, detail, backoff)
}

func (s *Service) count(result string) {
	if s.o.Metrics != nil {
		s.o.Metrics.ImageScans.WithLabelValues(result).Inc()
	}
}

func (s *Service) observe(started time.Time) {
	if s.o.Metrics != nil {
		s.o.Metrics.ImageScanDuration.Observe(s.o.Now().Sub(started).Seconds())
	}
}

// detect is one manifest: the analysis, once, and the detection.
func (s *Service) detect(ctx context.Context, ref ImageRef, access DatabaseAccess) error {
	if err := os.MkdirAll(s.o.ScratchDir, 0o700); err != nil {
		return err
	}
	scratch, err := os.MkdirTemp(s.o.ScratchDir, "scan-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	scoped, err := s.scoped(ctx, ref)
	if err != nil {
		return err
	}
	if !scoped {
		// Off for this repository: the row says the current database, so the job does not come back; R-VULN-001 shows not_covered and a
		// change of the setting re-arms the row.
		return s.recordDetection(ctx, ref, Detection{}, 0, access, false)
	}
	sbomPath := filepath.Join(scratch, "sbom.json")
	if err := s.analysis(ctx, ref, sbomPath, scratch); err != nil {
		return err
	}
	reportPath := filepath.Join(scratch, "report.json")
	if err := s.o.Analyser.Detect(ctx, sbomPath, reportPath, scratch, access, s.o.Run); err != nil {
		return err
	}
	f, err := os.Open(reportPath) //nolint:gosec // a path under the scan's own scratch directory
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	det, err := ParseReport(f, maxReportBytes)
	if err != nil {
		return &ScanError{Code: CodeAnalyserFailed, Detail: err.Error()}
	}
	return s.recordDetection(ctx, ref, det, packagesOf(sbomPath), access, true)
}

func (s *Service) scoped(ctx context.Context, ref ImageRef) (bool, error) {
	var ok bool
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) (err error) {
		ok, err = s.o.Evaluate.ImageScope(ctx, tx, ref.RepoID)
		return err
	})
	return ok, err
}

// analysis puts the list of what the image holds at sbomPath: from the stored analysis when one exists for this manifest and analyser, else
// by building a layout from the object store and running the analyser, then storing the result.
func (s *Service) analysis(ctx context.Context, ref ImageRef, sbomPath, scratch string) error {
	stored, found, err := s.o.Images.SBOM(ctx, ref.Digest, s.o.Analyser.Version())
	if err != nil {
		return err
	}
	if found {
		return gunzipTo(stored, sbomPath)
	}
	layout := filepath.Join(scratch, "layout")
	if err := os.MkdirAll(layout, 0o700); err != nil {
		return err
	}
	if err := buildLayout(ctx, s.o.Blobs, ref, layout, s.o.Content); err != nil {
		return err
	}
	if err := s.o.Analyser.Analyse(ctx, layout, sbomPath, scratch, s.o.Run); err != nil {
		return err
	}
	_ = os.RemoveAll(layout)
	gz, err := gzipFile(sbomPath, maxSBOMBytes)
	if err != nil {
		return err
	}
	if int64(len(gz)) > maxSBOMStored {
		s.log.WarnContext(ctx, "the analysis of an image is too large to keep; the next detection reads the layers again", "digest", ref.DigestString(), "bytes", len(gz))
		return nil
	}
	return s.o.Images.PutSBOM(ctx, ref.Digest, s.o.Analyser.Version(), gz)
}

// recordDetection applies a detection in one transaction: the advisories, the findings and the scan row.
func (s *Service) recordDetection(ctx context.Context, ref ImageRef, det Detection, packages int, access DatabaseAccess, scoped bool) error {
	return s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		images, err := store.ImageTxOf(tx)
		if err != nil {
			return err
		}
		if scoped {
			if err := s.applyFindings(ctx, tx, images, ref, det, access); err != nil {
				return err
			}
		}
		return images.Scanned(ctx, store.ImageApplied{
			RepoID: ref.RepoID, Name: ref.Name, Digest: ref.Digest, DBDigest: access.Digest, OSFamily: det.OSFamily, OSName: det.OSName, EOSL: det.EOSL, Packages: packages,
		})
	})
}

func (s *Service) applyFindings(ctx context.Context, tx meta.Tx, images *store.ImageTx, ref ImageRef, det Detection, access DatabaseAccess) error {
	ids := make([]string, 0, len(det.Advisories))
	for _, a := range det.Advisories {
		ids = append(ids, a.ID)
	}
	kev, err := images.KnownExploited(ctx, ids)
	if err != nil {
		return err
	}
	matches := make([]evaluate.ImageMatch, 0, len(det.Advisories))
	for _, a := range det.Advisories {
		if err := images.UpsertAdvisory(ctx, storedAdvisory(a)); err != nil {
			return err
		}
		m, err := matchOf(a, kev[a.ID])
		if err != nil {
			return err
		}
		matches = append(matches, m)
	}
	complete := s.o.States == nil || s.o.States.State(advisory.EcosystemOCI).Complete
	return s.o.Evaluate.ApplyImage(ctx, tx, evaluate.ImageApply{
		RepoID: ref.RepoID, Name: ref.Name, Digest: ref.DigestString(), Trigger: vuln.TriggerAdvisory, Matches: matches, DataComplete: complete && access.Digest != "",
	})
}

func matchOf(a Advisory, known bool) (evaluate.ImageMatch, error) {
	comps, err := json.Marshal(a.Components)
	if err != nil {
		return evaluate.ImageMatch{}, err
	}
	fixed := a.FixedVersions()
	return evaluate.ImageMatch{
		ID: a.ID, Severity: a.Severity, CVSS: a.CVSSScore, KnownExploited: known, ModifiedUs: microsOf(a.Modified),
		FixAvailable: len(fixed) > 0, Fixed: fixed, Components: comps,
	}, nil
}

func storedAdvisory(a Advisory) store.ImageAdvisory {
	sum := sha256.Sum256([]byte(a.ID + "\x00" + a.Title + "\x00" + a.Description + "\x00" + string(a.Severity) + "\x00" + a.CVSSVector + "\x00" + a.Modified.Format(time.RFC3339Nano)))
	src := "advisory"
	if a.CVSSScore >= 0 && a.SeveritySource == "" {
		src = "cvss"
	}
	return store.ImageAdvisory{
		ID: a.ID, Summary: a.Title, Details: a.Description, Modified: orEpoch(a.Modified), Published: a.Published, Severity: a.Severity, SeveritySource: src,
		CVSSVersion: a.CVSSVersion, CVSSVector: a.CVSSVector, CVSSScore: a.CVSSScore, References: a.References, CWEs: a.CWEs, RecordSHA256: sum[:],
	}
}

func orEpoch(t time.Time) time.Time {
	if t.IsZero() {
		return time.Unix(0, 0).UTC()
	}
	return t
}

func microsOf(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMicro()
}

func gzipFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // a path under the scan's own scratch directory
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := io.Copy(zw, io.LimitReader(f, limit)); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func gunzipTo(gz []byte, path string) error {
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return err
	}
	defer func() { _ = zr.Close() }()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // a path under the scan's own scratch directory
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = io.Copy(f, io.LimitReader(zr, maxSBOMBytes))
	return err
}

// packagesOf counts the packages in a CycloneDX document: how much an analysis found, which is what tells an image without a known
// distribution from one that is clean. A document that cannot be read counts as none.
func packagesOf(path string) int {
	f, err := os.Open(path) //nolint:gosec // a path under the scan's own scratch directory
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()
	var doc struct {
		Components []struct {
			Type string `json:"type"`
		} `json:"components"`
	}
	if err := json.NewDecoder(io.LimitReader(f, maxSBOMBytes)).Decode(&doc); err != nil {
		return 0
	}
	n := 0
	for _, c := range doc.Components {
		if c.Type == "library" || c.Type == "application" {
			n++
		}
	}
	return n
}

// Rescan is the image half of vuln-rescan (R-VULN-011): with the scanning off the repository's findings resolve, and otherwise every manifest
// of it is due again, whose analysis is kept.
func (s *Service) Rescan(ctx context.Context, repoID int64, enabled bool) error {
	if !enabled {
		return s.o.DB.InTx(ctx, func(tx meta.Tx) error { return s.o.Evaluate.ResolveImageRepository(ctx, tx, repoID) })
	}
	if _, err := s.o.Images.Rearm(ctx, repoID); err != nil {
		return err
	}
	if s.o.Enqueue != nil {
		return s.o.DB.InTx(ctx, func(tx meta.Tx) error { return s.o.Enqueue(ctx, tx) })
	}
	return nil
}
