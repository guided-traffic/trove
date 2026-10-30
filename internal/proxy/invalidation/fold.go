package invalidation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

const (
	// FoldKind is the job kind and the task name of the fold (R-REPO-045, Q-OPS-153).
	FoldKind = "proxy-invalidation-fold"
	// DefaultFoldSchedule is the schedule of the task when maintenance.tasks does not
	// name it: every five minutes.
	DefaultFoldSchedule = "*/5 * * * *"
	// NegativeRetention is how long a row of scope negative or all stays: the largest
	// notFoundTtlSeconds (86,400 s) plus the clock allowance (5 s) (R-REPO-045).
	NegativeRetention = 86405 * time.Second
	// DefaultMetadataGrace is how long a folded row of scope metadata stays before
	// the task checks it a last time and deletes it (Q-REPO-103).
	DefaultMetadataGrace = time.Hour

	defaultFoldBatch = 5000
	rowsPerQuery     = 100
)

// JobSpec is the registration of the kind. The composition root registers it with
// the handler of its Folder, because the handler needs the Folder and this package
// holds no package-level state (R-ARCH-032). The task is maintenance work of which
// at most one run exists (R-OPS-019).
func JobSpec(h jobs.Handler) jobs.KindSpec {
	return jobs.KindSpec{Handler: h, Queue: "maintenance", DedupMode: jobs.Exclusive, MaxAttempts: 5}
}

// TaskSpec is the task of the fold with the given schedule.
func TaskSpec(schedule string) jobs.TaskSpec { return jobs.TaskSpec{Schedule: schedule} }

// FolderOptions configure [NewFolder].
type FolderOptions struct {
	Runner   meta.Runner
	Notifier Notifier
	// Clock is the clock of the retention checks; nil is time.Now. A test moves it.
	Clock func() time.Time
	// BatchSize is the number of entries one fold transaction updates; it defaults
	// to 5000, which keeps a transaction short and its locks few.
	BatchSize int
	// MetadataGrace defaults to [DefaultMetadataGrace].
	MetadataGrace time.Duration
	Log           *slog.Logger
}

// Folder folds the invalidations into the metadata entries and deletes the rows no
// reader needs any more (R-REPO-045).
type Folder struct {
	o   FolderOptions
	log *slog.Logger
}

// NewFolder creates a Folder.
func NewFolder(o FolderOptions) (*Folder, error) {
	if o.Runner == nil || o.Notifier == nil {
		return nil, errors.New("invalidation: the folder needs a runner and a notifier")
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	if o.BatchSize <= 0 {
		o.BatchSize = defaultFoldBatch
	}
	if o.MetadataGrace <= 0 {
		o.MetadataGrace = DefaultMetadataGrace
	}
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	return &Folder{o: o, log: log}, nil
}

// FoldResult is what one run did; it is the result of the job.
type FoldResult struct {
	// FoldedRows are the invalidations whose fold finished (folded_at set).
	FoldedRows int `json:"foldedRows"`
	// ExpiredEntries are the metadata entries that got an earlier expiry.
	ExpiredEntries int64 `json:"expiredEntries"`
	// DeletedRows are the rows deleted.
	DeletedRows int `json:"deletedRows"`
}

// Run is the handler of the job kind [FoldKind].
func (f *Folder) Run(ctx context.Context, run *jobs.Run) error {
	res, err := f.Fold(ctx)
	if err != nil {
		return err
	}
	return run.SetResult(res)
}

// Fold runs the task once: it folds every unfolded row, then deletes the rows whose
// time has come. It is idempotent and resumable by the columns alone, so a run that
// is interrupted or repeated loses nothing.
func (f *Folder) Fold(ctx context.Context) (FoldResult, error) {
	var res FoldResult
	for {
		rows, err := f.listUnfolded(ctx)
		if err != nil {
			return res, err
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			n, err := f.foldRow(ctx, r)
			res.ExpiredEntries += n
			if err != nil {
				return res, fmt.Errorf("folding invalidation %d: %w", r.id, err)
			}
			if err := f.markFolded(ctx, r.id); err != nil {
				return res, err
			}
			res.FoldedRows++
		}
	}
	for {
		n, entries, err := f.deleteDue(ctx)
		res.ExpiredEntries += entries
		if err != nil {
			return res, err
		}
		if n == 0 {
			return res, nil
		}
		res.DeletedRows += n
	}
}

func (f *Folder) listUnfolded(ctx context.Context) ([]row, error) {
	var out []row
	err := f.o.Runner.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rs, err := qs.InvalidationListUnfolded(ctx, rowsPerQuery)
		for _, r := range rs {
			out = append(out, row{id: r.ID, repoID: r.RepoID, prefix: r.Prefix, scope: r.Scope, createdUs: r.CreatedUs})
		}
		return err
	})
	return out, err
}

// foldRow expires the metadata entries one row covers, batch by batch, each in a
// transaction of its own. A row of scope negative has nothing to fold: negative
// entries are in Valkey and the readers apply the row.
func (f *Folder) foldRow(ctx context.Context, r row) (int64, error) {
	if r.scope == ScopeNegative {
		return 0, nil
	}
	var total int64
	for {
		var n int64
		err := f.o.Runner.InTx(ctx, func(tx meta.Tx) error {
			qs, err := queries(tx)
			if err != nil {
				return err
			}
			n, err = qs.InvalidationFoldBatch(ctx, q.InvalidationFoldBatchParams{
				RepoID: r.repoID, Prefix: r.prefix, CreatedUs: r.createdUs, MaxRows: int32(f.o.BatchSize), //nolint:gosec // G115: a configured batch size
			})
			return err
		})
		total += n
		if err != nil {
			return total, err
		}
		if n < int64(f.o.BatchSize) {
			return total, nil
		}
	}
}

func (f *Folder) markFolded(ctx context.Context, id int64) error {
	return f.o.Runner.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		_, err = qs.InvalidationMarkFolded(ctx, q.InvalidationMarkFoldedParams{ID: id, AtUs: f.o.Clock().UnixMicro()})
		return err
	})
}

// deleteDue deletes one batch of the rows that no reader needs any more: a folded
// row of scope metadata after its grace, a negative or all row 86,405 s after its
// creation. Before it deletes a row it folds it once more, which finds an entry
// that a fill validated before the invalidation committed after the first fold
// (Q-REPO-103). It returns the number of rows deleted and of entries expired.
func (f *Folder) deleteDue(ctx context.Context) (int, int64, error) {
	now := f.o.Clock()
	var due []row
	err := f.o.Runner.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rs, err := qs.InvalidationListDeletable(ctx, q.InvalidationListDeletableParams{
			MetadataBeforeUs: now.Add(-f.o.MetadataGrace).UnixMicro(),
			NegativeBeforeUs: now.Add(-NegativeRetention).UnixMicro(),
			MaxRows:          rowsPerQuery,
		})
		for _, r := range rs {
			due = append(due, row{id: r.ID, repoID: r.RepoID, prefix: r.Prefix, scope: r.Scope, createdUs: r.CreatedUs})
		}
		return err
	})
	if err != nil || len(due) == 0 {
		return 0, 0, err
	}
	var entries int64
	ids := make([]int64, len(due))
	for i, r := range due {
		n, err := f.foldRow(ctx, r)
		entries += n
		if err != nil {
			return 0, entries, fmt.Errorf("folding invalidation %d before its deletion: %w", r.id, err)
		}
		ids[i] = r.id
	}
	err = f.o.Runner.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		repos, err := qs.InvalidationDelete(ctx, ids)
		if err != nil {
			return err
		}
		// Every replica drops what the database no longer holds (R-REPO-045).
		seen := map[int64]bool{}
		var distinct []int64
		for _, id := range repos {
			if !seen[id] {
				seen[id] = true
				distinct = append(distinct, id)
			}
		}
		sort.Slice(distinct, func(i, j int) bool { return distinct[i] < distinct[j] })
		for _, id := range distinct {
			if err := f.o.Notifier.Notify(tx, cluster.AreaCacheInvalidation, PayloadVersion, Payload{Kind: KindDelete, Repo: id}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, entries, err
	}
	return len(ids), entries, nil
}
