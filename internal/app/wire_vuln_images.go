package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/imagescan"
	vmetrics "github.com/guided-traffic/trove/internal/vuln/metrics"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// The wiring of the scanning of container images (R-VULN-033 to R-VULN-037, docs/developer/vulnerabilities.md): the service behind the
// kinds vuln-image-scan and vuln-sync-trivy-db, and the wake-up in the commit path. A replica that runs no jobs builds only the wake-up.

// imageOutputBytes bounds a file the analyser leaves behind (an SBOM, a report).
const imageOutputBytes = 512 << 20

// wireVulnImages builds the area's image scanning. A worker without the analyser binary keeps running and logs why: every other part of the
// scanning works without it, and the images then show pending_scan rather than a false clean.
func (a *App) wireVulnImages(ctx context.Context, in infra, m *vmetrics.Metrics, out *vulnArea) error {
	cfg := a.cfg.Vulnerabilities
	if !cfg.Enabled || !cfg.Images.Enabled {
		return nil
	}
	if in.queue != nil {
		queue := in.queue
		out.wakeImages = func(ctx context.Context, tx meta.Tx) error {
			_, err := queue.Enqueue(ctx, tx, jobs.EnqueueSpec{Kind: vuln.KindImageScan, DedupKey: vuln.DedupImageScan})
			return err
		}
	}
	if !a.sub.JobLoops || in.blobs == nil {
		return nil
	}
	evaluation, ok := out.evaluation()
	if !ok {
		return nil
	}
	images := cfg.Images
	log := a.log.With("component", "vuln-images")
	var analyser imagescan.Analyser
	override := a.o.Vuln
	if override != nil && override.ImageAnalyser != nil {
		analyser = override.ImageAnalyser
	} else {
		trivy, err := imagescan.NewTrivy(ctx, images.Binary)
		if err != nil {
			log.WarnContext(ctx, "the image scanning is not available on this worker: images stay pending", "error", err)
			return nil
		}
		analyser = trivy
	}
	if err := os.MkdirAll(images.ScanDir, 0o700); err != nil {
		log.WarnContext(ctx, "the image scanning has no working directory: images stay pending", "dir", images.ScanDir, "error", err)
		return nil
	}
	cleanScanDir(ctx, images.ScanDir, log)
	rows := store.NewImages(in.db)
	var database imagescan.Database = &imagescan.LocalDatabase{Dir: filepath.Join(images.ScanDir, "db"), Blobs: dbBlobs{in.blobs}, Rows: rows}
	if override != nil && override.ImageDatabase != nil {
		database = override.ImageDatabase
	}
	reader := imageBlobReader{
		repos: func() repository.Reader { return part[repositoryArea](a.parts).reader },
		items: func() *items.Store { s, _ := a.itemStore.get(); return s },
	}
	svc, err := imagescan.New(imagescan.Options{
		DB: in.db, Images: rows, Evaluate: evaluation, Blobs: reader, Analyser: analyser, Database: database,
		States: out.states, Enqueue: out.wakeImages, ScratchDir: images.ScanDir, Concurrency: images.Concurrency, RunSlice: images.RunSlice,
		MaxAttempts: images.MaxAttempts, Delay: images.ScanDelay, Metrics: m, Log: log,
		Content: imagescan.ContentLimits{MaxLayers: images.MaxLayers, MaxImageBytes: int64(images.MaxImageSize), MaxUncompressedBytes: int64(images.MaxUncompressedSize), MaxEntries: images.MaxEntries},
		Run:     imagescan.Limits{Timeout: images.Timeout, MemoryBytes: int64(images.MemoryLimit), OutputBytes: imageOutputBytes, StderrBytes: 16 << 10},
	})
	if err != nil {
		return fmt.Errorf("the image scanning: %w", err)
	}
	out.images, out.dbCommit = svc, in.blobs
	return nil
}

// cleanScanDir removes what a worker that stopped mid-scan left: a container that restarts keeps its volume, and the unpacked database in it
// is the one thing worth keeping.
func cleanScanDir(ctx context.Context, dir string, log *slog.Logger) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.WarnContext(ctx, "the working directory of the image scanning cannot be read", "dir", dir, "error", err)
		return
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "scan-") {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				log.WarnContext(ctx, "a leftover of the image scanning cannot be removed", "path", e.Name(), "error", err)
			}
		}
	}
}

// imageBlobReader reads the manifests and blobs of an image from the item store of the repository that holds it.
type imageBlobReader struct {
	repos func() repository.Reader
	items func() *items.Store
}

func (r imageBlobReader) ref(ctx context.Context, ref imagescan.ImageRef) (items.RepoRef, *items.Store, error) {
	repo, err := r.repos().ByID(ctx, ref.RepoID)
	if err != nil {
		return items.RepoRef{}, nil, err
	}
	store := r.items()
	if store == nil {
		return items.RepoRef{}, nil, errs.E(errs.Unavailable, errs.CodeInternal, "The item store is not wired yet.")
	}
	return items.RepoOf(repo), store, nil
}

// ReadManifest implements [imagescan.BlobReader].
func (r imageBlobReader) ReadManifest(ctx context.Context, ref imagescan.ImageRef, limit int64) ([]byte, error) {
	rc, _, found, err := r.open(ctx, ref, ref.Name+"/_manifests/"+ref.DigestString())
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, &imagescan.ScanError{Code: imagescan.CodeLayerMissing, Detail: "the repository does not hold the manifest any more"}
	}
	defer func() { _ = rc.Close() }()
	return io.ReadAll(io.LimitReader(rc, limit))
}

// OpenBlob implements [imagescan.BlobReader].
func (r imageBlobReader) OpenBlob(ctx context.Context, ref imagescan.ImageRef, digest string) (io.ReadCloser, int64, bool, error) {
	return r.open(ctx, ref, ref.Name+"/_blobs/"+digest)
}

func (r imageBlobReader) open(ctx context.Context, ref imagescan.ImageRef, path string) (io.ReadCloser, int64, bool, error) {
	repo, st, err := r.ref(ctx, ref)
	if err != nil {
		return nil, 0, false, err
	}
	it, found, err := st.Live(ctx, repo, path)
	if err != nil || !found || it.Kind != items.KindFile {
		return nil, 0, false, err
	}
	rc, err := st.Open(ctx, it, nil)
	if err != nil {
		return nil, 0, false, err
	}
	return rc, it.Size, true, nil
}

// dbBlobs opens the archive of the database from the object store.
type dbBlobs struct{ store *blob.Store }

// Open implements [imagescan.DBBlobs].
func (b dbBlobs) Open(ctx context.Context, sha256 [32]byte, size int64) (io.ReadCloser, error) {
	return b.store.Open(ctx, blob.Digest(sha256), size, blob.OpenOptions{})
}

// registerVulnImageJobs registers the kinds and tasks of the image scanning. The scan coalesces: its work is in a table, so one run after
// another loses nothing, and the queue is the advisory sync's, whose slot a long run gives back through its time slice.
func (a *App) registerVulnImageJobs(r *jobs.Registry) {
	scan := func(ctx context.Context, run *jobs.Run) error {
		svc := part[vulnArea](a.parts).images
		if svc == nil {
			return run.SetResult(map[string]string{"skipped": "the image scanning is not available on this replica"})
		}
		return svc.RunScan(ctx, run)
	}
	db := func(ctx context.Context, run *jobs.Run) error {
		area := part[vulnArea](a.parts)
		if area.sync == nil || a.cfg.Vulnerabilities.Sources.Trivy.Mode == "off" {
			return run.SetResult(map[string]string{"skipped": "the vulnerability database is not synced on this replica"})
		}
		if area.dbCommit == nil {
			return fmt.Errorf("the vulnerability database: the blob store is not wired yet")
		}
		return area.sync.RunTrivyDB(ctx, run, area.dbCommit)
	}
	r.RegisterKind(vuln.KindImageScan, jobs.KindSpec{Queue: vuln.QueueVuln, DedupMode: jobs.Coalesce, MaxAttempts: 3, Handler: scan})
	r.RegisterKind(vuln.KindSyncTrivyDB, jobs.KindSpec{Queue: vuln.QueueVuln, DedupMode: jobs.Exclusive, MaxAttempts: 1, Handler: db})
	for _, td := range vuln.TaskDefaults() {
		if td.Name == vuln.TaskImageScan || td.Name == vuln.TaskSyncTrivyDB {
			r.RegisterTask(td.Name, a.vulnTaskSpec(td))
		}
	}
}
