package app

import (
	"context"
	"errors"
	"io"
	"log/slog"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/backup/fixup"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/formathost"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
)

// RestoreItems is the storage delete path of `trove backup restore` (R-BAK-008, R-BAK-021 step 7): the item store composed with
// the storage hooks of every format, over the restore's session, without a replica around it. It builds what the removal of
// cached files needs and nothing that serves, loops or talks to Valkey: the format data ports and their hooks, with signals that
// publish nothing (the restore holds no Valkey credential and no replica runs, R-BAK-022), a blob store that refuses every call
// (a removal releases references in the database; the objects of an emptied cache were never copied), and a job queue that
// refuses (removing files enqueues nothing). The vulnerability hook is left out: the advisory tables are empty after a restore,
// so nothing is evaluated (R-VULN-026).
func RestoreItems(cfg *config.Config, log *slog.Logger) func(ctx context.Context, db meta.Runner, rec audit.Recorder) (fixup.CacheEraser, error) {
	return func(_ context.Context, db meta.Runner, rec audit.Recorder) (fixup.CacheEraser, error) {
		ports, err := formatPorts(db, rec, nil, refusedJobs{})
		if err != nil {
			return nil, err
		}
		bindings, err := bindFormatPorts(formats.List(), ports)
		if err != nil {
			return nil, err
		}
		hooks := formathost.NewHooks(formathost.Deps{Bindings: bindings, Signals: silentSignals{}})
		return items.New(items.Options{
			Runner: db, Blobs: refusedBlobs{}, Recorder: rec, Hooks: hooks, Storage: cfg.Storage, Log: log.With("component", "restore-items"),
		})
	}
}

var errRestoreOnly = errors.New("the restore's item store only removes cached files")

// silentSignals publishes no change signal: no replica listens during a restore, and replicas that start afterwards read the
// database (R-ARCH-018).
type silentSignals struct{}

func (silentSignals) Notify(meta.Tx, string, string) {}

// refusedJobs is the job queue of the restore's item store: nothing it does enqueues a job.
type refusedJobs struct{}

func (refusedJobs) Enqueue(context.Context, meta.Tx, jobs.EnqueueSpec) (jobs.Result, error) {
	return jobs.Result{}, errRestoreOnly
}

// refusedBlobs is the blob store of the restore's item store: it reads and writes no object.
type refusedBlobs struct{}

func (refusedBlobs) Ingest(context.Context, io.Reader, blob.IngestOptions) (*blob.Spooled, error) {
	return nil, errRestoreOnly
}

func (refusedBlobs) CommitAll(context.Context, []*blob.Spooled, blob.LinkFunc) ([]blob.CommitResult, error) {
	return nil, errRestoreOnly
}

func (refusedBlobs) Open(context.Context, blob.Digest, int64, blob.OpenOptions) (io.ReadCloser, error) {
	return nil, errRestoreOnly
}
func (refusedBlobs) RecheckMissing(context.Context, blob.Digest, int64) bool { return false }
