package mirror

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/proxy"
	"github.com/guided-traffic/trove/internal/repository"
)

// Enqueuer enqueues jobs: *jobs.Queue implements it.
type Enqueuer interface {
	EnqueueNow(ctx context.Context, e jobs.EnqueueSpec) (jobs.Result, error)
}

// ResultSetter sets the result of the job that runs a pass: *jobs.Run implements it.
type ResultSetter interface {
	SetResult(v any) error
}

// Options configure New.
type Options struct {
	// Store is the persistence of the state and the pending names.
	Store Store
	// Repos is the repository snapshot: the dispatch lists it, a pass reads its repository from it before each package.
	Repos repository.Reader
	// Host is what the engine needs from the remote engine and the formats (host.go).
	Host Host
	// Queue enqueues the passes.
	Queue Enqueuer
	// Audit records the pass events; nil records none.
	Audit audit.Recorder
	// Metrics are the collectors; nil records none.
	Metrics *Metrics
	// Holder is the instance id of this process, a UUID: the second half of a pass's fence.
	Holder string
	// Clock and Jitter are the seams of the engine's tests; nil is the system's.
	Clock  Clock
	Jitter Jitter
	// GlobalOffline reports the global offline switch (R-REPO-031); nil is never.
	GlobalOffline func() bool
	// NormalizeName normalizes a package name with a format's rule; nil is the catalog's.
	NormalizeName func(format, name string) (string, bool)
	Log           *slog.Logger
}

// Engine runs the dispatch and the passes of the mirrors of one process (docs/developer/mirror.md).
type Engine struct {
	o Options

	mu sync.Mutex
	// notified is when a repository's mirror.rate_limited event was last written by this process (R-REPO-054: at most once
	// per pass and hour).
	notified map[int64]time.Time
}

// New creates the engine.
func New(o Options) (*Engine, error) {
	switch {
	case o.Store == nil:
		return nil, errors.New("mirror: no store")
	case o.Repos == nil:
		return nil, errors.New("mirror: no repository reader")
	case o.Host == nil:
		return nil, errors.New("mirror: no host")
	case o.Holder == "":
		return nil, errors.New("mirror: no instance id")
	}
	if o.Clock == nil {
		o.Clock = SystemClock{}
	}
	if o.Jitter == nil {
		o.Jitter = SystemJitter{}
	}
	if o.GlobalOffline == nil {
		o.GlobalOffline = func() bool { return false }
	}
	if o.NormalizeName == nil {
		o.NormalizeName = catalog.Default().NormalizeName
	}
	if o.Audit == nil {
		o.Audit = audit.Nop{}
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Engine{o: o, notified: map[int64]time.Time{}}, nil
}

// repoID returns the numeric id of a repository of the snapshot.
func repoID(r repository.Repository) (int64, error) {
	id, err := strconv.ParseInt(r.ID, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("mirror: repository id %q: %w", r.ID, err)
	}
	return id, nil
}

// mirrored reports whether the repository is a remote in mode mirror that is in service.
func mirrored(r repository.Repository) (repository.RemoteSettings, bool) {
	s, ok := r.Remote()
	if !ok || !s.Mirrored() || r.Lifecycle == repository.Deleting {
		return repository.RemoteSettings{}, false
	}
	return s, true
}

// DispatchResult is the result of a dispatch, the result of its job.
type DispatchResult struct {
	Repositories int `json:"repositories"`
	Enqueued     int `json:"enqueued"`
}

// Dispatch is the task mirror-dispatch (R-REPO-053): it ensures a mirror_state row for every remote repository in mode
// mirror of the snapshot, clears the pass of a remote repository of the snapshot that left the mode (a snapshot that lists
// nothing clears nothing), and enqueues a mirror-pass under PassKey
// for every repository that is due: not paused, next_pass_at passed or a pass requested, no rate-limit wait pending. The
// exclusive dedup key keeps one pass per repository, whichever process dispatches.
func (e *Engine) Dispatch(ctx context.Context) (DispatchResult, error) {
	list, err := e.o.Repos.List(ctx)
	if err != nil {
		return DispatchResult{}, err
	}
	byID := map[int64]repository.Repository{}
	var ids, left []int64
	for _, r := range list {
		if r.Class != repository.Remote {
			continue
		}
		id, err := repoID(r)
		if err != nil {
			e.o.Log.Warn("a repository of the snapshot has no numeric id", "repository", r.Key, "error", err)
			continue
		}
		if _, ok := mirrored(r); !ok {
			left = append(left, id)
			continue
		}
		byID[id] = r
		ids = append(ids, id)
	}
	res := DispatchResult{Repositories: len(ids)}
	if err := e.o.Store.EnsureStates(ctx, ids); err != nil {
		return res, fmt.Errorf("mirror: ensure the state rows: %w", err)
	}
	if err := e.o.Store.ClearInactive(ctx, left); err != nil {
		return res, fmt.Errorf("mirror: clear the passes of repositories that left the mode: %w", err)
	}
	due, err := e.o.Store.Due(ctx, ids, e.o.Clock.Now())
	if err != nil {
		return res, fmt.Errorf("mirror: read the due passes: %w", err)
	}
	for _, id := range due {
		r := byID[id]
		s, _ := mirrored(r)
		if r.Disabled || s.Mirror.Paused {
			continue
		}
		if _, ok := e.o.Host.Lister(r.Format); !ok {
			continue
		}
		if e.o.Queue == nil {
			return res, errors.New("mirror: no job queue")
		}
		out, err := e.o.Queue.EnqueueNow(ctx, jobs.EnqueueSpec{
			Kind: KindPass, DedupKey: PassKey(id), Args: PassArgs{Repository: r.Key, RepositoryID: id},
			TargetKind: "repository", TargetKey: r.Key,
		})
		if err != nil {
			return res, fmt.Errorf("mirror: enqueue the pass of %s: %w", r.Key, err)
		}
		if out.Created {
			res.Enqueued++
		}
	}
	return res, nil
}

// rateLimitEvent reports whether this process may write the rate-limit event of a repository now, and notes that it did.
func (e *Engine) rateLimitEvent(repoID int64, now time.Time) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if last, ok := e.notified[repoID]; ok && now.Sub(last) < NotifyEvery {
		return false
	}
	e.notified[repoID] = now
	return true
}

// background is the background share B of R-REPO-055 for a repository's settings.
func background(s repository.RemoteSettings) int {
	return proxy.BackgroundShare(s.Limits.MaxConcurrentFetches, s.Mirror.Concurrency)
}

// notFound reports whether err says that the repository does not exist.
func notFound(err error) bool { return errs.KindOf(err) == errs.NotFound }

// passSummary is the summary of a pass the status shows as lastCompletePass (R-REPO-056).
type passSummary struct {
	Kind              string  `json:"kind"`
	Method            *string `json:"method"`
	StartedAt         string  `json:"startedAt"`
	FinishedAt        string  `json:"finishedAt"`
	Outcome           string  `json:"outcome"`
	Packages          int64   `json:"packages"`
	PackagesFailed    int64   `json:"packagesFailed"`
	ItemsStored       int64   `json:"itemsStored"`
	BytesStored       int64   `json:"bytesStored"`
	DiscoveryComplete bool    `json:"discoveryComplete"`
	DiscoveryNote     *string `json:"discoveryNote"`
}

func (s passSummary) json() json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}
