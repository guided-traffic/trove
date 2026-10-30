package repository

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Names of the caches of this package (R-ARCH-018).
const (
	// CacheSnapshot is the repository snapshot.
	CacheSnapshot = "repositories"
	// CacheKeyMisses is the negative cache of unknown repository keys
	// (R-ARCH-011 step 2).
	CacheKeyMisses = "repo_key_misses"
)

// missSafetyTTL is the SafetyTTL of the negative cache of unknown keys
// (R-ARCH-011).
const missSafetyTTL = 60 * time.Second

// Poller triggers an immediate change-log poll and waits for it (R-ARCH-018,
// change-area step 3). *cluster.Changes implements it.
type Poller interface {
	PollNow(ctx context.Context) error
}

// ReaderOptions configure [NewReader].
type ReaderOptions struct {
	Runner meta.Runner
	// Catalog is the format catalog the model reads (R-REPO-003); nil is
	// catalog.Default.
	Catalog *catalog.Registry
	Caches  *cache.Registry
	// Poller is optional: without it a replica waits for the regular poll.
	Poller Poller
	// SafetyTTL bounds the age of the snapshot (cache.snapshot_safety_ttl).
	SafetyTTL time.Duration
	// UnknownKeys bounds the negative cache of unknown keys (cache.unknown_repo_keys);
	// zero turns it off.
	UnknownKeys int
	// LookupWait bounds the wait for the poll (repositories.lookup_wait).
	LookupWait time.Duration
	Log        *slog.Logger
}

// snapshot is the repository table as one replica holds it: immutable once built.
type snapshot struct {
	byKey map[string]Repository
	list  []Repository
}

// SnapshotReader answers [Reader] from a per-replica snapshot of the repository
// table (R-REPO-049: an in-process copy of PostgreSQL rows). Every change of the
// change area repo_config drops it, and the next call rebuilds it; the snapshot
// is rebuilt in full after cache.snapshot_safety_ttl besides. It works without
// Valkey: the PostgreSQL change log brings a change within
// cluster.epoch_poll_interval, Valkey's message only makes it faster.
type SnapshotReader struct {
	run    meta.Runner
	cat    *catalog.Registry
	snap   *cache.Cache[struct{}, *snapshot]
	misses *cache.Cache[string, struct{}]
	poll   Poller
	wait   time.Duration
	log    *slog.Logger
	flight flight
}

var _ Reader = (*SnapshotReader)(nil)

// NewReader registers the snapshot and, unless UnknownKeys is zero, the negative
// cache of unknown keys on the registry, both invalidated by repo_config.
func NewReader(o ReaderOptions) (*SnapshotReader, error) {
	switch {
	case o.Runner == nil:
		return nil, errors.New("repository: no transaction runner")
	case o.Caches == nil:
		return nil, errors.New("repository: no cache registry")
	case o.SafetyTTL <= 0:
		return nil, errors.New("repository: no snapshot safety TTL")
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.LookupWait <= 0 {
		o.LookupWait = 5 * time.Second
	}
	r := &SnapshotReader{
		run: o.Runner, cat: catalog.Or(o.Catalog), poll: o.Poller, wait: o.LookupWait, log: o.Log,
		flight: flight{calls: map[string]*flightCall{}},
	}
	areas := []string{cluster.AreaRepoConfig}
	r.snap = cache.Register(o.Caches, CacheSnapshot, cache.Options[struct{}, *snapshot]{
		MaxEntries: 1, SafetyTTL: o.SafetyTTL, Areas: areas,
	})
	if o.UnknownKeys > 0 {
		r.misses = cache.Register(o.Caches, CacheKeyMisses, cache.Options[string, struct{}]{
			MaxEntries: o.UnknownKeys, SafetyTTL: missSafetyTTL, Areas: areas,
		})
	}
	return r, nil
}

// ByKey implements [Reader]. A repository being deleted is returned with
// Lifecycle Deleting; its key is taken, so it is not "unknown", and the caller
// treats it as not addressable (R-REPO-013).
//
// A key the snapshot does not hold is handled so that a repository is usable on
// every replica as soon as its creation has answered (R-ARCH-011 step 2,
// R-API-052): a key that cannot be a key answers not found without a query; any
// other key is looked up in PostgreSQL, concurrent lookups of one key sharing one
// query; if it exists, the replica polls the change log at once and rebuilds the
// snapshot; if it does not, the miss is remembered until the next repo_config
// change.
func (r *SnapshotReader) ByKey(ctx context.Context, key string) (Repository, error) {
	if !ValidKeyShape(key) {
		return Repository{}, notFound(key)
	}
	s, err := r.snapshot(ctx)
	if err != nil {
		return Repository{}, err
	}
	if repo, ok := s.byKey[key]; ok {
		return repo, nil
	}
	return r.flight.do(key, func() (Repository, error) {
		// The lookup belongs to every waiting caller, so it does not end with the
		// first one's request.
		return r.lookup(context.WithoutCancel(ctx), key)
	})
}

// List implements [Reader]: every repository of the snapshot, sorted by key.
func (r *SnapshotReader) List(ctx context.Context) ([]Repository, error) {
	s, err := r.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Repository, len(s.list))
	copy(out, s.list)
	return out, nil
}

func (r *SnapshotReader) snapshot(ctx context.Context) (*snapshot, error) {
	return r.snap.Load(ctx, struct{}{}, r.load)
}

func (r *SnapshotReader) load(ctx context.Context) (*snapshot, error) {
	rows, err := readRows(ctx, r.run, func(qs *q.Queries) ([]q.Repository, error) { return qs.ListRepositories(ctx) })
	if err != nil {
		return nil, err
	}
	s := &snapshot{byKey: make(map[string]Repository, len(rows)), list: make([]Repository, 0, len(rows))}
	for _, row := range rows {
		repo, err := toRepository(row, r.cat)
		if err != nil {
			r.log.Error("repository: stored configuration does not load; the repository is disabled", "key", row.Key, "error", err)
		}
		s.byKey[repo.Key] = repo
		s.list = append(s.list, repo)
	}
	sort.Slice(s.list, func(i, j int) bool { return s.list[i].Key < s.list[j].Key })
	return s, nil
}

var errPresent = errors.New("repository: the key exists")

// lookup resolves a key the snapshot does not hold.
func (r *SnapshotReader) lookup(ctx context.Context, key string) (Repository, error) {
	var row q.Repository
	loader := func(ctx context.Context) (struct{}, error) {
		rows, err := readRows(ctx, r.run, func(qs *q.Queries) ([]q.Repository, error) { return qs.GetRepositoryByKey(ctx, key) })
		if err != nil {
			return struct{}{}, err
		}
		if len(rows) == 0 {
			return struct{}{}, nil // absent: Load remembers it under the fill rule
		}
		row = rows[0]
		return struct{}{}, errPresent
	}
	var err error
	if r.misses != nil {
		_, err = r.misses.Load(ctx, key, loader)
	} else {
		_, err = loader(ctx)
	}
	switch {
	case err == nil:
		return Repository{}, notFound(key)
	case !errors.Is(err, errPresent):
		return Repository{}, err
	}
	// PostgreSQL knows the key and this replica's snapshot does not: the change
	// has not been applied here yet. Apply it now.
	if r.poll != nil {
		pctx, cancel := context.WithTimeout(ctx, r.wait)
		err := r.poll.PollNow(pctx)
		cancel()
		if err != nil {
			return Repository{}, errs.E(errs.Unavailable, errs.CodeUnavailable,
				"the repository exists but this replica could not confirm it in time; retry",
				errs.WithRetryAfter(time.Second), errs.WithCause(err))
		}
	}
	r.snap.Flush("lookup")
	s, err := r.snapshot(ctx)
	if err != nil {
		return Repository{}, err
	}
	if repo, ok := s.byKey[key]; ok {
		return repo, nil
	}
	// The row has been deleted since, or the poll has not seen the change: the
	// row we read is the truth (PostgreSQL decides).
	repo, err := toRepository(row, r.cat)
	if err != nil {
		r.log.Error("repository: stored configuration does not load; the repository is disabled", "key", row.Key, "error", err)
	}
	return repo, nil
}

// flight shares the result of one lookup among concurrent callers of the same
// key, so that a flood of requests for one unknown key costs one query.
type flight struct {
	mu    sync.Mutex
	calls map[string]*flightCall
}

type flightCall struct {
	done chan struct{}
	val  Repository
	err  error
}

func (f *flight) do(key string, fn func() (Repository, error)) (Repository, error) {
	f.mu.Lock()
	if c, ok := f.calls[key]; ok {
		f.mu.Unlock()
		<-c.done
		return c.val, c.err
	}
	c := &flightCall{done: make(chan struct{})}
	f.calls[key] = c
	f.mu.Unlock()

	c.val, c.err = fn()

	f.mu.Lock()
	delete(f.calls, key)
	f.mu.Unlock()
	close(c.done)
	return c.val, c.err
}
