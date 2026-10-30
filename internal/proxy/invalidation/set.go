package invalidation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/meta"
)

// NegativeAllowance is the clock allowance of R-REPO-028 applied in the
// invalidation's favour: a negative entry written within 5 s after an invalidation
// is invalidated as well.
const NegativeAllowance = 5 * time.Second

// Matcher is what the proxy engine asks on a request. *Set implements it. Both
// methods answer from memory and are safe for concurrent use.
type Matcher interface {
	// MetadataValid reports whether a metadata entry of the repository that was
	// last validated at validatedAt still counts: no invalidation of scope metadata
	// or all with a prefix covering path is newer than validatedAt. An entry that
	// does not count is expired for the request, whatever its expires_at says; the
	// engine revalidates it, and serves it stale if the upstream fails.
	MetadataValid(repoID int64, path string, validatedAt time.Time) bool
	// NegativeValid reports whether a negative entry whose upstream request was
	// sent at sentAt still counts: no invalidation of scope negative or all with a
	// covering prefix was created later than sentAt minus [NegativeAllowance].
	NegativeValid(repoID int64, path string, sentAt time.Time) bool
}

// Changes is the part of *cluster.Changes the Set uses.
type Changes interface {
	AddListener(l cluster.Listener)
}

// SetOptions configure [NewSet].
type SetOptions struct {
	// Runner reads the rows; the Set never writes.
	Runner meta.Runner
	// Changes delivers the changes of the area cache_invalidation.
	Changes Changes
	// SafetyInterval is the period of the full reload that bounds every kind of
	// divergence, such as the rows a repository deletion removed by cascade without
	// a notification. Zero disables it.
	SafetyInterval time.Duration
	// RetryInterval is the wait before a failed reload is tried again; it defaults
	// to one second.
	RetryInterval time.Duration
	Log           *slog.Logger
}

// row is one invalidation as the matcher keeps it.
type row struct {
	id        int64
	repoID    int64
	prefix    string
	scope     string
	createdUs int64
}

// repoData is the immutable state of one repository. meta and neg map a prefix to
// the creation time of its newest row, so a lookup costs one map access per segment
// of the path, however many rows exist (Q-REPO-102).
type repoData struct {
	rows map[int64]row
	meta map[string]int64
	neg  map[string]int64
}

func buildRepo(rows map[int64]row) *repoData {
	d := &repoData{rows: rows, meta: map[string]int64{}, neg: map[string]int64{}}
	for _, r := range rows {
		if r.scope != ScopeNegative && r.createdUs > d.meta[r.prefix] {
			d.meta[r.prefix] = r.createdUs
		}
		if r.scope != ScopeMetadata && r.createdUs > d.neg[r.prefix] {
			d.neg[r.prefix] = r.createdUs
		}
	}
	return d
}

// newest returns the creation time of the newest row of m whose prefix covers path,
// 0 when none does. The prefixes that cover a path are the empty one, the path
// itself and the path up to each of its slashes.
func newest(m map[string]int64, path string) int64 {
	best := m[""]
	for i := 0; i < len(path); i++ {
		if path[i] == '/' {
			if v := m[path[:i]]; v > best {
				best = v
			}
		}
	}
	if v := m[path]; v > best {
		best = v
	}
	return best
}

type snapshot struct{ repos map[int64]*repoData }

// Set is the replica's copy of the rows of remote_cache_invalidations. It keeps
// every row the database holds: a row leaves the Set only when the reload after the
// fold task's deletion no longer finds it, so an all row whose fold is late never
// lets an unfolded entry count as fresh again (R-REPO-045). The Set is therefore
// bounded by the table, not by the number of invalidations ever made.
type Set struct {
	o   SetOptions
	log *slog.Logger
	cur atomic.Pointer[snapshot]

	mu      sync.Mutex // writers; guards loading and delta
	loading bool
	delta   []row // rows applied from a payload while a reload runs

	pmu     sync.Mutex // guards pend and all
	pend    map[int64]bool
	all     bool
	wake    chan struct{}
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	started atomic.Bool

	// load reads the rows; loadRows in production, replaced by the unit tests.
	load func(ctx context.Context, repo int64) ([]row, error)
}

var (
	_ Matcher          = (*Set)(nil)
	_ cluster.Listener = (*Set)(nil)
)

// NewSet creates a Set. It is empty and answers "valid" for everything until
// [Set.Start] has loaded the rows.
func NewSet(o SetOptions) (*Set, error) {
	if o.Runner == nil || o.Changes == nil {
		return nil, errors.New("invalidation: the set needs a runner and the change log")
	}
	if o.RetryInterval <= 0 {
		o.RetryInterval = time.Second
	}
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	s := &Set{o: o, log: log, pend: map[int64]bool{}, wake: make(chan struct{}, 1)}
	s.load = func(ctx context.Context, repo int64) ([]row, error) { return loadRows(ctx, o.Runner, repo) }
	s.cur.Store(&snapshot{repos: map[int64]*repoData{}})
	return s, nil
}

// Name implements the composition root's Component.
func (s *Set) Name() string { return "invalidations" }

// Start registers the Set for the changes of the area, loads every row and starts
// the reload loop. A load that fails stops the start-up: a replica must not serve
// cached metadata without knowing the invalidations (R-REPO-045). The listener is
// registered first, so a row committed during the load is not lost: a payload
// applied while the load runs is merged into its result.
func (s *Set) Start(ctx context.Context) error {
	if !s.started.CompareAndSwap(false, true) {
		return errors.New("invalidation: the set is started twice")
	}
	s.o.Changes.AddListener(s)
	if err := s.reload(ctx, 0); err != nil {
		return fmt.Errorf("loading the cache invalidations: %w", err)
	}
	lctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.cancel = cancel
	s.wg.Add(1)
	go s.loop(lctx)
	return nil
}

// Stop ends the reload loop.
func (s *Set) Stop(context.Context) error {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
	return nil
}

// Len is the number of rows the Set holds.
func (s *Set) Len() int {
	n := 0
	for _, d := range s.cur.Load().repos {
		n += len(d.rows)
	}
	return n
}

// MetadataValid implements [Matcher].
func (s *Set) MetadataValid(repoID int64, path string, validatedAt time.Time) bool {
	d := s.cur.Load().repos[repoID]
	return d == nil || newest(d.meta, path) <= validatedAt.UnixMicro()
}

// NegativeValid implements [Matcher].
func (s *Set) NegativeValid(repoID int64, path string, sentAt time.Time) bool {
	d := s.cur.Load().repos[repoID]
	return d == nil || newest(d.neg, path) <= sentAt.Add(-NegativeAllowance).UnixMicro()
}

// ApplyChange implements cluster.Listener. It is called from the change-log poll,
// from a Valkey message and from this replica's own commit, so it is idempotent
// and never blocks on the database: a new row is applied from its payload, a
// deletion or a payload it does not understand queues a reload.
func (s *Set) ApplyChange(area string, _ int, payload json.RawMessage) {
	if area != cluster.AreaCacheInvalidation {
		return
	}
	p, err := ParsePayload(payload)
	if err != nil {
		s.requestReload(0)
		return
	}
	switch p.Kind {
	case KindAdd:
		prefix, err := p.DecodePrefix()
		if err != nil || p.ID == 0 || p.Repo == 0 || !ValidScope(p.Scope) {
			s.requestReload(0)
			return
		}
		s.add(row{id: p.ID, repoID: p.Repo, prefix: prefix, scope: p.Scope, createdUs: p.CreatedUs})
	case KindDelete:
		s.requestReload(p.Repo)
	case KindVirtual:
		// The merged entries of a virtual repository are the business of its cache.
	default:
		s.requestReload(0)
	}
}

// FlushArea implements cluster.Listener: the poll found a gap, an unknown payload
// version or an epoch that went backwards, so every row is read again.
func (s *Set) FlushArea(area, _ string) {
	if area == cluster.AreaCacheInvalidation {
		s.requestReload(0)
	}
}

// add installs one row, replacing the repository's state.
func (s *Set) add(r row) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loading {
		s.delta = append(s.delta, r)
	}
	s.install(r)
}

// install adds r to the current snapshot; the caller holds mu.
func (s *Set) install(r row) {
	old := s.cur.Load()
	if d := old.repos[r.repoID]; d != nil {
		if _, ok := d.rows[r.id]; ok {
			return
		}
	}
	rows := map[int64]row{r.id: r}
	if d := old.repos[r.repoID]; d != nil {
		for id, x := range d.rows {
			rows[id] = x
		}
	}
	next := &snapshot{repos: make(map[int64]*repoData, len(old.repos)+1)}
	for id, d := range old.repos {
		next.repos[id] = d
	}
	next.repos[r.repoID] = buildRepo(rows)
	s.cur.Store(next)
}

// requestReload queues a reload of one repository, or of all of them for repo 0.
func (s *Set) requestReload(repo int64) {
	s.pmu.Lock()
	if repo == 0 {
		s.all = true
	} else {
		s.pend[repo] = true
	}
	s.pmu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Set) takePending() (all bool, repos []int64) {
	s.pmu.Lock()
	defer s.pmu.Unlock()
	all = s.all
	for r := range s.pend {
		repos = append(repos, r)
	}
	s.all, s.pend = false, map[int64]bool{}
	return all, repos
}

// loop runs the queued reloads one after another, and the full reload of the
// safety interval.
func (s *Set) loop(ctx context.Context) {
	defer s.wg.Done()
	var safety <-chan time.Time
	if s.o.SafetyInterval > 0 {
		t := time.NewTicker(s.o.SafetyInterval)
		defer t.Stop()
		safety = t.C
	}
	var retry <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-retry:
		case <-safety:
			s.requestReload(0)
		}
		retry = nil
		all, repos := s.takePending()
		if all {
			repos = []int64{0}
		}
		for i, repo := range repos {
			if err := s.reload(ctx, repo); err != nil {
				if ctx.Err() != nil {
					return
				}
				s.log.Warn("reloading the cache invalidations failed; trying again", "repository", repo, "error", err)
				for _, left := range repos[i:] {
					s.requestReload(left)
				}
				retry = time.After(s.o.RetryInterval)
				break
			}
		}
	}
}

// reload replaces the state of one repository (repo > 0) or of all of them with
// what the database holds, merged with the rows that arrived by payload while the
// query ran.
func (s *Set) reload(ctx context.Context, repo int64) error {
	s.mu.Lock()
	s.loading, s.delta = true, nil
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.loading, s.delta = false, nil
		s.mu.Unlock()
	}()
	rows, err := s.load(ctx, repo)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Every row applied from a payload since the load began is in delta; a row the
	// query already saw is in both and collapses on its id.
	byRepo := map[int64]map[int64]row{}
	for _, r := range append(rows, s.delta...) {
		if repo != 0 && r.repoID != repo {
			continue
		}
		if byRepo[r.repoID] == nil {
			byRepo[r.repoID] = map[int64]row{}
		}
		byRepo[r.repoID][r.id] = r
	}
	next := &snapshot{repos: map[int64]*repoData{}}
	if repo != 0 {
		for id, d := range s.cur.Load().repos {
			if id != repo {
				next.repos[id] = d
			}
		}
	}
	for id, rs := range byRepo {
		next.repos[id] = buildRepo(rs)
	}
	s.cur.Store(next)
	return nil
}
