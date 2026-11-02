package nuget

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
	"github.com/guided-traffic/trove/internal/formats/nuget/version"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/packages"
)

// The recomputation of nuget_version.is_semver2 (F-30, Q-NUGET-93). Up to release 0.9.0 a push
// and a remote's fill classified a version by its own number alone; since then a bound of one of
// its dependency ranges counts as well (R-NUGET-004). The flag is stored, so the rows written
// before keep false although the rule now says true, and such a version still shows in the
// SemVer 1 hives and in a search without semVerLevel=2.0.0. This background data migration
// (R-HA-022) recomputes the stored rows once per installation with the rule the push applies
// (version.PackageIsSemver2), from the version and the dependency ranges the row holds: every
// row ever written carries its dependency groups in its metadata, so no .nuspec is read.

// Semver2JobKind is the kind of the recomputation, named data_migration_<name> as R-HA-022 names
// a background data migration. Queue maintenance; dedup mode coalesce, so that a run that waits
// for older replicas can queue the run after it, and two runs never run together.
const Semver2JobKind = "data_migration_nuget_semver2"

// Semver2StateKey is the system_state key (R-OPS-024) that records the completion: while it is
// absent, a worker that starts queues the recomputation. Its value is the [Semver2Result] of the
// run that reached the end.
const Semver2StateKey = "nuget_semver2_recomputed"

// Semver2LastOldRelease is the last release whose writers classified a version by its own
// number alone. The recomputation waits while a replica of it or an older one is live, because
// such a replica can still write a row the old way behind it (R-HA-022).
const Semver2LastOldRelease = "0.9.0"

// The states a run records.
const (
	// Semver2Done: the completion was recorded already; the run changed nothing.
	Semver2Done = "done"
	// Semver2Waiting: a replica of Semver2LastOldRelease or older is live; the run changed
	// nothing and queued the run after it.
	Semver2Waiting = "waiting"
	// Semver2Recomputed: the run read every row to the end and recorded the completion.
	Semver2Recomputed = "recomputed"
)

// Semver2Result is what a run records as its result (jobs.Run.SetResult) and, when it reached
// the end, as the value of Semver2StateKey.
type Semver2Result struct {
	State string `json:"state"`
	// Examined counts the rows read that said SemVer 1; Changed the versions set to SemVer 2;
	// Packages the packages whose revision was raised for them.
	Examined int64 `json:"examined"`
	Changed  int64 `json:"changed"`
	Packages int64 `json:"packages"`
	// Unreadable counts the rows whose version or metadata did not parse: they keep their flag.
	Unreadable int64 `json:"unreadable"`
	// WaitingFor names the replicas a waiting run waits for, "name (version)".
	WaitingFor []string `json:"waitingFor,omitempty"`
}

// semver2Checkpoint is the cursor a run saves after every batch: the version id of the last row
// read and the counts so far. A run released at shutdown resumes from it.
type semver2Checkpoint struct {
	After      int64 `json:"after"`
	Examined   int64 `json:"examined"`
	Changed    int64 `json:"changed"`
	Packages   int64 `json:"packages"`
	Unreadable int64 `json:"unreadable"`
}

// Signaler attaches a signal to a transaction, published after the commit. *cluster.Signals
// implements it.
type Signaler interface {
	Notify(tx meta.Tx, area, payload string)
}

// Semver2Options configure [NewSemver2].
type Semver2Options struct {
	// DB runs the reads and the transactions of the changes.
	DB meta.Runner
	// Jobs queues the recomputation at the start of a worker and the run after a waiting one.
	Jobs Jobs
	// Signals carries the signal that evicts merged metadata early after a raised revision
	// (metadata_changed, R-REPO-020), as the package writer of a request attaches it. nil sends
	// none: every consumer validates by revision.
	Signals Signaler
	// BatchSize and Pause are jobs.background_migrations.batch_size and pause_between_batches.
	BatchSize int
	Pause     time.Duration
	// StaleAfter is system.heartbeat_stale_after: a replica whose heartbeat is older is not live.
	StaleAfter time.Duration
	// Wait is how long a waiting run waits before the run after it; zero is one minute.
	Wait time.Duration
	Log  *slog.Logger
}

// Semver2 is the recomputation. One value per replica; its handler runs on a worker.
type Semver2 struct{ o Semver2Options }

// NewSemver2 creates the recomputation.
func NewSemver2(o Semver2Options) (*Semver2, error) {
	if o.DB == nil || o.Jobs == nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "nuget: the SemVer 2 recomputation needs a transaction runner and a job queue")
	}
	if o.BatchSize < 1 {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "nuget: the SemVer 2 recomputation needs a batch size of at least 1")
	}
	if o.Wait <= 0 {
		o.Wait = time.Minute
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Semver2{o: o}, nil
}

// Semver2JobSpec is the registration of the kind, with h as the handler: the composition root
// passes one that finds the replica's [Semver2] at run time.
func Semver2JobSpec(h jobs.Handler) jobs.KindSpec {
	return jobs.KindSpec{Queue: "maintenance", DedupMode: jobs.Coalesce, Handler: h}
}

// Enqueue queues the recomputation unless its completion is recorded, in the transaction that
// reads the record. A worker calls it when it starts; an enqueue that meets a waiting run
// returns that run. It reports whether a run is queued.
func (s *Semver2) Enqueue(ctx context.Context) (bool, error) {
	var queued bool
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		queued = false
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		st, err := qs.NuGetSemver2State(ctx, Semver2StateKey)
		if err != nil || len(st) > 0 {
			return err
		}
		if _, err := s.o.Jobs.Enqueue(ctx, tx, jobs.EnqueueSpec{Kind: Semver2JobKind}); err != nil {
			return err
		}
		queued = true
		return nil
	})
	return queued, err
}

// Run is the handler of the kind. It is idempotent: a recorded completion ends it at once, a
// row that says SemVer 2 is never read, and a version is set only while it still says SemVer 1,
// under the row lock of its package.
func (s *Semver2) Run(ctx context.Context, run *jobs.Run) error {
	done, err := s.done(ctx)
	if err != nil {
		return err
	}
	if done {
		return run.SetResult(Semver2Result{State: Semver2Done})
	}
	if waiting, err := s.waitFor(ctx, run); err != nil || waiting {
		return err
	}
	var cp semver2Checkpoint
	if _, err := run.LoadCheckpoint(&cp); err != nil {
		return err
	}
	for {
		batch, err := q.New(run.DB()).NuGetSemver2Batch(ctx, q.NuGetSemver2BatchParams{After: cp.After, Batch: int32(min(s.o.BatchSize, 1<<30))}) //nolint:gosec // bounded above
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}
		if err := s.apply(ctx, batch, &cp); err != nil {
			return err
		}
		if err := run.SaveCheckpoint(ctx, cp); err != nil {
			return err
		}
		if err := run.SaveProgress(ctx, cp.Examined, 0, "versions"); err != nil {
			return err
		}
		if len(batch) < s.o.BatchSize {
			break
		}
		if err := pause(ctx, s.o.Pause); err != nil {
			return err
		}
	}
	// A replica of the old release that came up while the run read the rows (a rollback) may
	// have written behind the cursor: the completion waits for it like the start did.
	if waiting, err := s.waitFor(ctx, run); err != nil || waiting {
		return err
	}
	res := Semver2Result{
		State: Semver2Recomputed, Examined: cp.Examined, Changed: cp.Changed, Packages: cp.Packages, Unreadable: cp.Unreadable,
	}
	if err := s.record(ctx, res); err != nil {
		return err
	}
	s.o.Log.Info("NuGet SemVer 2 classification recomputed for the versions stored before it counted dependency ranges",
		"examined", res.Examined, "changed", res.Changed, "packages", res.Packages, "unreadable", res.Unreadable)
	return run.SetResult(res)
}

// done reports whether the completion is recorded.
func (s *Semver2) done(ctx context.Context) (bool, error) {
	var done bool
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		st, err := qs.NuGetSemver2State(ctx, Semver2StateKey)
		done = len(st) > 0
		return err
	})
	return done, err
}

// waitFor checks for live replicas of Semver2LastOldRelease or older. With one, it queues the
// run after this one with a delay, records the waiting result and reports true.
func (s *Semver2) waitFor(ctx context.Context, run *jobs.Run) (bool, error) {
	old, err := postgres.ReplicasUpTo(ctx, run.DB(), Semver2LastOldRelease, s.o.StaleAfter)
	if err != nil || len(old) == 0 {
		return false, err
	}
	names := make([]string, len(old))
	for i, r := range old {
		names[i] = r.Name + " (" + r.Version + ")"
	}
	err = s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		_, err := s.o.Jobs.Enqueue(ctx, tx, jobs.EnqueueSpec{Kind: Semver2JobKind, Delay: s.o.Wait})
		return err
	})
	if err != nil {
		return false, err
	}
	s.o.Log.Info("the NuGet SemVer 2 recomputation waits for the replicas of an older release", "replicas", names, "next_run_in", s.o.Wait.String())
	return true, run.SetResult(Semver2Result{State: Semver2Waiting, WaitingFor: names})
}

// semver2Change is the versions of one package whose flag the rule sets.
type semver2Change struct {
	pkg      packages.Ref
	versions []int64
}

// apply classifies a batch and writes the versions it sets, one transaction per package, and
// advances the cursor and the counts.
func (s *Semver2) apply(ctx context.Context, batch []q.NuGetSemver2BatchRow, cp *semver2Checkpoint) error {
	changes := map[int64]*semver2Change{}
	for _, r := range batch {
		is, err := semver2Of(r.OriginalVersion, []byte(r.Metadata))
		if err != nil {
			cp.Unreadable++
			s.o.Log.Warn("a stored NuGet version cannot be classified; it keeps its SemVer 2 flag",
				"version_id", r.VersionID, "repository_id", r.RepositoryID, "error", err)
			continue
		}
		if !is {
			continue
		}
		c := changes[r.PackageID]
		if c == nil {
			c = &semver2Change{pkg: packages.Ref{ID: r.PackageID, RepoID: r.RepositoryID, Name: r.LowerID}}
			changes[r.PackageID] = c
		}
		c.versions = append(c.versions, r.VersionID)
	}
	ids := make([]int64, 0, len(changes))
	for id := range changes {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		n, err := s.mark(ctx, changes[id])
		if err != nil {
			return err
		}
		if n > 0 {
			cp.Changed += n
			cp.Packages++
		}
	}
	cp.Examined += int64(len(batch))
	cp.After = batch[len(batch)-1].VersionID
	return nil
}

// mark sets the versions of one package SemVer 2.0.0-specific under the package row lock and
// raises the package's revision the way any change of its rows does, so the rendered
// documents, their validators and the caches keyed by the revision follow (R-NUGET-030), and
// the signal of the raise evicts merged metadata early (R-REPO-020). A package that is gone, or
// whose versions say SemVer 2 already, is left as it is.
func (s *Semver2) mark(ctx context.Context, c *semver2Change) (int64, error) {
	var n int64
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		n = 0
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		locked, err := qs.NuGetSemver2LockPackage(ctx, c.pkg.ID)
		if err != nil || len(locked) == 0 {
			return err
		}
		if n, err = qs.NuGetSemver2Mark(ctx, c.versions); err != nil || n == 0 {
			return err
		}
		pw, err := packages.NewWriter(tx, "", packages.WithRaised(s.raised))
		if err != nil {
			return err
		}
		_, _, err = pw.BumpRevision(ctx, c.pkg)
		return err
	})
	return n, err
}

// raised attaches the signal of a raised package, as the host's package writer does
// (Q-ARCH-259).
func (s *Semver2) raised(_ context.Context, tx meta.Tx, pkg packages.Ref) error {
	if s.o.Signals != nil {
		s.o.Signals.Notify(tx, catalog.MetadataChangedArea, catalog.PackageChanged(pkg.RepoID, pkg.Name))
	}
	return nil
}

// record writes the completion.
func (s *Semver2) record(ctx context.Context, res Semver2Result) error {
	raw, err := json.Marshal(res)
	if err != nil {
		return err
	}
	return s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		return qs.NuGetSemver2Done(ctx, q.NuGetSemver2DoneParams{Key: Semver2StateKey, Value: string(raw)})
	})
}

// semver2Of is the classification of a stored version by the rule of a push
// (version.PackageIsSemver2): the version as the .nuspec spelled it (original_version) and the
// ranges of the dependency groups in nuget_version.metadata, which hold the range strings of
// the .nuspec verbatim (rows.Dependency.Version).
func semver2Of(original string, metadata []byte) (bool, error) {
	v, err := version.Parse(original)
	if err != nil {
		return false, fmt.Errorf("original_version: %w", err)
	}
	var m rows.Metadata
	if err := json.Unmarshal(metadata, &m); err != nil {
		return false, fmt.Errorf("metadata: %w", err)
	}
	var ranges []string
	for _, g := range m.DependencyGroups {
		for _, d := range g.Dependencies {
			ranges = append(ranges, d.Version)
		}
	}
	return version.PackageIsSemver2(v, ranges...), nil
}

// pause waits between two batches, or until the run ends.
func pause(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
