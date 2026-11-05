package mirror

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/mirror/discover"
	"github.com/guided-traffic/trove/internal/repository"
)

// The fakes of the engine's unit tests: a clock whose sleeps cost nothing and move its time, a randomness the test fixes, an
// in-memory Store with the semantics of the SQL (the fence, the counters, the attempts), a repository reader, a job queue
// with the exclusive dedup key, a host and a lister the test scripts. The PostgreSQL implementation is tested against the
// integration stack (test/integration/mirror).

// fakeClock is a clock that a Sleep moves: nothing waits in real time and every wait of the engine is visible in sleeps.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func newClock() *fakeClock { return &fakeClock{now: time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	c.mu.Unlock()
	return nil
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func (c *fakeClock) slept() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

// fixedJitter returns the same draw every time.
type fixedJitter float64

func (j fixedJitter) Float64() float64 { return float64(j) }

// memStore is a Store in memory.
type memStore struct {
	mu      sync.Mutex
	clock   *fakeClock
	states  map[int64]*State
	pending map[int64]map[string]*memPending
	held    map[int64][]string
	usage   func(repoID int64) Usage
	// beforeWrite runs before every fenced write; a test uses it to take the pass over in the middle of a run.
	beforeWrite func(op string)
	writes      []string
	lastPass    map[int64]json.RawMessage
}

type memPending struct {
	attempts  int
	notBefore time.Time
	lastError string
}

func newMemStore(c *fakeClock) *memStore {
	return &memStore{clock: c, states: map[int64]*State{}, pending: map[int64]map[string]*memPending{}, held: map[int64][]string{}, lastPass: map[int64]json.RawMessage{}}
}

func (s *memStore) fence(f Fence, op string) (*State, error) {
	if s.beforeWrite != nil {
		s.mu.Unlock()
		s.beforeWrite(op)
		s.mu.Lock()
	}
	st := s.states[f.RepoID]
	if st == nil || st.PassJobID != f.JobID || st.PassHolder != f.Holder {
		return nil, ErrFenceLost
	}
	s.writes = append(s.writes, op)
	return st, nil
}

func (s *memStore) names(id int64) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for n := range s.pending[id] {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (s *memStore) state(id int64) State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *s.states[id]
}

func (s *memStore) EnsureStates(_ context.Context, ids []int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		if s.states[id] == nil {
			s.states[id] = &State{RepoID: id, PassDiscoveryComplete: true}
		}
	}
	return nil
}

func (s *memStore) Due(_ context.Context, ids []int64, now time.Time) ([]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []int64
	for _, id := range ids {
		st := s.states[id]
		if st == nil {
			continue
		}
		due := st.NextPassAt.IsZero() || !st.NextPassAt.After(now) || !st.PassRequestedAt.IsZero()
		if due && (st.RateLimitedUntil.IsZero() || !st.RateLimitedUntil.After(now)) {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func (s *memStore) ClearInactive(_ context.Context, left []int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range left {
		if st := s.states[id]; st != nil {
			clearPass(st)
		}
		delete(s.pending, id)
	}
	return nil
}

func clearPass(st *State) {
	st.PassStartedAt, st.PassKind, st.PassMethod, st.PassJobID, st.PassHolder = time.Time{}, "", "", "", ""
	st.PassCursor, st.PassDiscoveryDone = nil, false
	st.PassNames, st.PassPackagesDone, st.PassPackagesFailed, st.PassItemsStored, st.PassBytesStored = 0, 0, 0, 0, 0
	st.PassDiscoveryComplete, st.PassDiscoveryNote = true, ""
}

func (s *memStore) State(_ context.Context, id int64) (State, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.states[id]
	if st == nil {
		return State{}, false, nil
	}
	return *st, true, nil
}

func (s *memStore) Claim(_ context.Context, c Claim) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.states[c.RepoID]
	if st == nil {
		return false, nil
	}
	if !st.InProgress() {
		kind := c.KindIfNew
		if st.PassRequestedFull || c.Full {
			kind = discover.PassFull
		}
		clearPass(st)
		st.PassKind, st.PassStartedAt = kind, c.Now
		for _, p := range s.pending[c.RepoID] {
			p.attempts, p.notBefore = 0, time.Time{}
		}
	}
	st.PassJobID, st.PassHolder = c.JobID, c.Holder
	st.PassRequestedAt, st.PassRequestedFull, st.RateLimitedUntil = time.Time{}, false, time.Time{}
	return true, nil
}

func (s *memStore) SetMethod(_ context.Context, f Fence, m discover.Method, k discover.PassKind) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.fence(f, "method")
	if err != nil {
		return err
	}
	st.PassMethod, st.PassKind = m, k
	return nil
}

func (s *memStore) countPending(id int64) int64 {
	var n int64
	for _, p := range s.pending[id] {
		if p.attempts < MaxPackageAttempts {
			n++
		}
	}
	return n
}

func (s *memStore) PendingCount(_ context.Context, id int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.countPending(id), nil
}

func (s *memStore) AddNames(_ context.Context, f Fence, n Names) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.fence(f, "names")
	if err != nil {
		return 0, err
	}
	if s.pending[f.RepoID] == nil {
		s.pending[f.RepoID] = map[string]*memPending{}
	}
	var added int64
	for _, name := range n.Names {
		if _, ok := s.pending[f.RepoID][name]; !ok {
			s.pending[f.RepoID][name] = &memPending{}
			added++
		}
	}
	st.PassNames += added
	if n.SetProgress {
		st.PassCursor, st.PassDiscoveryDone = n.Cursor, n.Done
		if n.Truncated {
			st.PassDiscoveryComplete, st.PassDiscoveryNote = false, n.Note
		}
	}
	return added, nil
}

func (s *memStore) HeldNames(_ context.Context, id int64, after string, limit int) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, n := range s.held[id] {
		if n > after && len(out) < limit {
			out = append(out, n)
		}
	}
	return out, nil
}

func (s *memStore) NextPending(_ context.Context, id int64, now time.Time, after string, limit int) ([]PendingName, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []PendingName
	for n, p := range s.pending[id] {
		if p.attempts < MaxPackageAttempts && (p.notBefore.IsZero() || !p.notBefore.After(now)) && n > after {
			out = append(out, PendingName{Name: n, Attempts: p.attempts})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *memStore) NextWake(_ context.Context, id int64, now time.Time) (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var wake time.Time
	for _, p := range s.pending[id] {
		if p.attempts < MaxPackageAttempts && p.notBefore.After(now) && (wake.IsZero() || p.notBefore.Before(wake)) {
			wake = p.notBefore
		}
	}
	return wake, nil
}

func (s *memStore) PackageDone(_ context.Context, f Fence, r PackageResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.fence(f, "done")
	if err != nil {
		return err
	}
	delete(s.pending[f.RepoID], r.Name)
	st.PassPackagesDone++
	st.PassItemsStored += r.Items
	st.PassBytesStored += r.Bytes
	return nil
}

func (s *memStore) PackageFailed(_ context.Context, f Fence, r PackageResult) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.fence(f, "failed")
	if err != nil {
		return 0, err
	}
	p := s.pending[f.RepoID][r.Name]
	p.attempts++
	p.notBefore = r.Now.Add(time.Duration(p.attempts) * PackageRetryStep)
	p.lastError = r.Error
	if p.attempts >= MaxPackageAttempts {
		st.PassPackagesFailed++
	}
	st.PassItemsStored += r.Items
	st.PassBytesStored += r.Bytes
	return p.attempts, nil
}

func (s *memStore) PackageKept(_ context.Context, f Fence, r PackageResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.fence(f, "kept")
	if err != nil {
		return err
	}
	st.PassItemsStored += r.Items
	st.PassBytesStored += r.Bytes
	return nil
}

func (s *memStore) RecordRateLimit(_ context.Context, f Fence, _ time.Time, _ time.Duration, _ int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.fence(f, "ratelimit")
	return err
}

func (s *memStore) Defer(_ context.Context, f Fence, until time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.fence(f, "defer")
	if err != nil {
		return err
	}
	st.RateLimitedUntil, st.NextPassAt = until, until
	return nil
}

func (s *memStore) EndPass(_ context.Context, f Fence, e End) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.fence(f, "end")
	if err != nil {
		return false, err
	}
	boundNew := st.BoundReached == ""
	if e.ClearPending {
		delete(s.pending, f.RepoID)
	}
	if e.Advance {
		st.FeedCursor, st.FeedMethod = st.PassCursor, st.PassMethod
		if st.PassKind == discover.PassFull {
			st.LastFullPassAt = e.Now
		}
		st.PatternsFingerprint = e.Fingerprint
	}
	s.lastPass[f.RepoID] = e.LastPass
	if !e.KeepPass {
		clearPass(st)
	}
	if e.ResetFailures {
		st.ConsecutiveFailures = 0
	}
	st.RateLimitedUntil, st.NextPassAt = time.Time{}, e.NextPassAt
	switch {
	case e.Bound != "":
		if st.BoundReached == "" {
			st.BoundReachedAt = e.Now
		}
		st.BoundReached = e.Bound
	case e.ClearBound:
		st.BoundReached, st.BoundReachedAt = "", time.Time{}
	}
	return boundNew && e.Bound != "", nil
}

func (s *memStore) RecordFailure(_ context.Context, f Fence, now time.Time, _, _ string, jitter float64) (Failure, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.fence(f, "failure")
	if err != nil {
		return Failure{}, err
	}
	st.ConsecutiveFailures++
	st.NextPassAt = now.Add(time.Duration(float64(FailureBackoff(st.ConsecutiveFailures)) * jitter))
	return Failure{Failures: st.ConsecutiveFailures, NextPassAt: st.NextPassAt}, nil
}

func (s *memStore) Usage(_ context.Context, id int64) (Usage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.usage == nil {
		return Usage{ReadAt: s.clock.Now()}, nil
	}
	u := s.usage(id)
	u.ReadAt = s.clock.Now()
	return u, nil
}

// memRepos is a repository reader the test changes.
type memRepos struct {
	mu    sync.Mutex
	repos map[string]repository.Repository
}

func (r *memRepos) ByKey(_ context.Context, key string) (repository.Repository, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if repo, ok := r.repos[key]; ok {
		return repo, nil
	}
	return repository.Repository{}, errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "no such repository")
}

func (r *memRepos) List(context.Context) ([]repository.Repository, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []repository.Repository
	for _, repo := range r.repos {
		out = append(out, repo)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (r *memRepos) set(repo repository.Repository) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.repos[repo.Key] = repo
}

func (r *memRepos) remove(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.repos, key)
}

// memQueue is a job queue with the exclusive dedup key.
type memQueue struct {
	mu     sync.Mutex
	active map[string]string
	specs  []jobs.EnqueueSpec
}

func (q *memQueue) EnqueueNow(_ context.Context, e jobs.EnqueueSpec) (jobs.Result, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.active == nil {
		q.active = map[string]string{}
	}
	if id, ok := q.active[e.DedupKey]; ok {
		return jobs.Result{Job: jobs.Job{ID: id}}, nil
	}
	id := fmt.Sprintf("job-%d", len(q.specs)+1)
	q.active[e.DedupKey] = id
	q.specs = append(q.specs, e)
	return jobs.Result{Job: jobs.Job{ID: id}, Created: true}, nil
}

// finish ends the job of a key, so that the next enqueue creates one.
func (q *memQueue) finish(key string) {
	q.mu.Lock()
	delete(q.active, key)
	q.mu.Unlock()
}

func (q *memQueue) count() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.specs)
}

// fakeUpstream is the upstream a pass lends the format; the fake lister does not use it.
type fakeUpstream struct{}

func (fakeUpstream) Base() *url.URL            { u, _ := url.Parse("https://registry.example.org/"); return u }
func (fakeUpstream) Settings() json.RawMessage { return nil }
func (fakeUpstream) Document(context.Context, string, string) ([]byte, bool, error) {
	return nil, false, nil
}

func (fakeUpstream) Get(context.Context, *url.URL, string, int64) (*discover.Response, error) {
	return &discover.Response{Status: http.StatusOK}, nil
}

// fakeLister is a scripted discover.Lister: its pages come in order, a plan per name, and an error script per call.
type fakeLister struct {
	mu      sync.Mutex
	methods []discover.Method
	pages   []discover.Page
	plans   map[string]discover.Plan
	// namesErr and planErr return the error of the n-th call (from 1); nil for none.
	namesErr func(call int) error
	planErr  func(name string, call int) error
	methodsE error

	namesCalls int
	requests   []discover.PageRequest
	planCalls  map[string]int
	planOrder  []string
}

func (l *fakeLister) MirrorMethods(context.Context, discover.Upstream) ([]discover.Method, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.methods, l.methodsE
}

func (l *fakeLister) MirrorNames(_ context.Context, _ discover.Upstream, rq discover.PageRequest) (discover.Page, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.namesCalls++
	l.requests = append(l.requests, rq)
	if l.namesErr != nil {
		if err := l.namesErr(l.namesCalls); err != nil {
			return discover.Page{}, err
		}
	}
	i := 0
	if len(rq.Cursor) > 0 {
		var c struct{ I int }
		_ = json.Unmarshal(rq.Cursor, &c)
		i = c.I
	}
	if i >= len(l.pages) {
		return discover.Page{Done: true}, nil
	}
	pg := l.pages[i]
	if !pg.Done && len(pg.Cursor) == 0 {
		pg.Cursor, _ = json.Marshal(struct{ I int }{i + 1})
	}
	return pg, nil
}

func (l *fakeLister) MirrorPlan(_ context.Context, _ discover.Upstream, name string) (discover.Plan, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.planCalls == nil {
		l.planCalls = map[string]int{}
	}
	l.planCalls[name]++
	l.planOrder = append(l.planOrder, name)
	if l.planErr != nil {
		if err := l.planErr(name, l.planCalls[name]); err != nil {
			return discover.Plan{}, err
		}
	}
	if p, ok := l.plans[name]; ok {
		return p, nil
	}
	return discover.Plan{Name: name}, nil
}

func (l *fakeLister) MirrorHosts(*url.URL) []string { return nil }

// fakeHost is a scripted Host.
type fakeHost struct {
	lister  *fakeLister
	noLists bool
	admit   func(string) bool
	// fill returns the result of the n-th fill of a path (from 1); nil stores the item with its size.
	fill func(item discover.Item, call int) (FillResult, error)

	mu       sync.Mutex
	fills    []string
	perPath  map[string]int
	have     map[string]bool
	upstream int
	ceilings []int
}

func (h *fakeHost) Lister(string) (discover.Lister, bool) {
	if h.noLists {
		return nil, false
	}
	return h.lister, true
}

func (h *fakeHost) Admits(context.Context, repository.Repository) (func(string) bool, error) {
	if h.admit != nil {
		return h.admit, nil
	}
	return func(string) bool { return true }, nil
}

func (h *fakeHost) Upstream(_ repository.Repository, p Pass) (discover.Upstream, error) {
	h.mu.Lock()
	h.upstream++
	h.ceilings = append(h.ceilings, p.Ceiling)
	h.mu.Unlock()
	return fakeUpstream{}, nil
}

func (h *fakeHost) Fill(_ context.Context, _ repository.Repository, _ Pass, item discover.Item) (FillResult, error) {
	h.mu.Lock()
	if h.perPath == nil {
		h.perPath = map[string]int{}
	}
	if h.have == nil {
		h.have = map[string]bool{}
	}
	if h.have[item.Path] {
		// Content addressing: what is stored costs no request.
		h.mu.Unlock()
		return FillResult{Present: true}, nil
	}
	h.perPath[item.Path]++
	call := h.perPath[item.Path]
	fn := h.fill
	h.mu.Unlock()
	if fn != nil {
		res, err := fn(item, call)
		if err == nil && !res.Present {
			h.record(item.Path)
		}
		return res, err
	}
	h.record(item.Path)
	return FillResult{Bytes: item.Size}, nil
}

func (h *fakeHost) record(path string) {
	h.mu.Lock()
	h.fills = append(h.fills, path)
	h.have[path] = true
	h.mu.Unlock()
}

func (h *fakeHost) stored() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.fills...)
}

// rateLimitErr is a rate-limit answer as the upstream's typed error reports it.
type rateLimitErr struct {
	status int
	after  time.Duration
	hinted bool
	reason string
}

func (e *rateLimitErr) Error() string                     { return "rate limited" }
func (e *rateLimitErr) RetryAfter() (time.Duration, bool) { return e.after, e.hinted }
func (e *rateLimitErr) RateLimitReason() string {
	if e.reason == "" {
		return "too_many_requests"
	}
	return e.reason
}

// recorder keeps the audit events.
type recorder struct {
	mu     sync.Mutex
	events []audit.Event
}

func (r *recorder) RecordTx(context.Context, meta.Tx, audit.Event) error { return nil }

func (r *recorder) Record(_ context.Context, ev audit.Event) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

func (r *recorder) of(typ string) []audit.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []audit.Event
	for _, e := range r.events {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

// result is the result a job set.
type result struct {
	mu sync.Mutex
	v  map[string]any
}

func (r *result) SetResult(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.v = map[string]any{}
	return json.Unmarshal(b, &r.v)
}

func (r *result) get(key string) any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.v[key]
}

// rig is one engine over the fakes, with one mirrored repository.
type rig struct {
	t      *testing.T
	clock  *fakeClock
	store  *memStore
	repos  *memRepos
	queue  *memQueue
	host   *fakeHost
	lister *fakeLister
	rec    *recorder
	eng    *Engine
	met    *Metrics
	reg    *prometheus.Registry
	repo   repository.Repository
	jobs   int
}

const rigRepoID = 7

// mirrorDoc is the document of a mirrored remote of format npm; extra members of remote.mirror and of the document are
// merged in by the test.
func mirrorDoc(t *testing.T, include []string, mirrorSettings string) []byte {
	t.Helper()
	if mirrorSettings == "" {
		mirrorSettings = "{}"
	}
	inc, _ := json.Marshal(include)
	doc := `{"includePatterns":` + string(inc) + `,"excludePatterns":[],"remote":{"url":"https://registry.example.org/","mode":"mirror",` +
		`"availability":{"mode":"online","failureThreshold":3,"circuitOpenSeconds":30},"limits":{"maxConcurrentFetches":16},"mirror":` + mirrorSettings + `}}`
	return []byte(doc)
}

func buildRepo(t *testing.T, key string, id int64, doc []byte) repository.Repository {
	t.Helper()
	r, err := repository.Build(id, key, repository.Remote, "npm", doc, nil)
	if err != nil {
		t.Fatalf("repository: %v", err)
	}
	return r
}

// newRig builds the engine. mirrorSettings is the JSON of remote.mirror ("" for the defaults); concurrency 1 keeps a pass in
// one worker unless the test says otherwise.
func newRig(t *testing.T, include []string, mirrorSettings string) *rig {
	t.Helper()
	if mirrorSettings == "" {
		mirrorSettings = `{"concurrency":1}`
	}
	clock := newClock()
	g := &rig{
		t: t, clock: clock, store: newMemStore(clock), repos: &memRepos{repos: map[string]repository.Repository{}}, queue: &memQueue{},
		lister: &fakeLister{methods: []discover.Method{discover.MethodFeed}}, rec: &recorder{},
	}
	g.host = &fakeHost{lister: g.lister}
	g.reg = prometheus.NewRegistry()
	m, err := NewMetrics(g.reg)
	if err != nil {
		t.Fatal(err)
	}
	g.met = m
	g.repo = buildRepo(t, "npm-mirror", rigRepoID, mirrorDoc(t, include, mirrorSettings))
	g.repos.set(g.repo)
	g.eng = g.newEngine("holder-a")
	return g
}

func (g *rig) newEngine(holder string) *Engine {
	e, err := New(Options{
		Store: g.store, Repos: g.repos, Host: g.host, Queue: g.queue, Audit: g.rec, Metrics: g.met, Holder: holder, Clock: g.clock, Jitter: fixedJitter(0.5),
		NormalizeName: func(_, n string) (string, bool) { return strings.ToLower(n), true },
	})
	if err != nil {
		g.t.Fatal(err)
	}
	return e
}

func (g *rig) update(include []string, mirrorSettings string) {
	g.repo = buildRepo(g.t, "npm-mirror", rigRepoID, mirrorDoc(g.t, include, mirrorSettings))
	g.repos.set(g.repo)
}

// run runs one attempt of the pass as a new job and returns the job's result and error.
func (g *rig) run(args ...PassArgs) (*result, error) {
	g.t.Helper()
	return g.runAs(g.eng, args...)
}

func (g *rig) runAs(e *Engine, args ...PassArgs) (*result, error) {
	g.t.Helper()
	g.jobs++
	a := PassArgs{Repository: "npm-mirror", RepositoryID: rigRepoID}
	if len(args) > 0 {
		a.Full = args[0].Full
	}
	b, _ := json.Marshal(a)
	res := &result{}
	err := e.RunPass(context.Background(), fmt.Sprintf("00000000-0000-0000-0000-%012d", g.jobs), b, res)
	return res, err
}

// plan scripts the plan of a package with versions newest first; sizes are 100 bytes.
func (g *rig) plan(name string, versions ...string) {
	if g.lister.plans == nil {
		g.lister.plans = map[string]discover.Plan{}
	}
	p := discover.Plan{Name: name}
	for _, v := range versions {
		pre := strings.Contains(v, "-")
		p.Items = append(p.Items, discover.Item{Version: v, Path: name + "/-/" + v + ".tgz", Size: 100, Prerelease: pre})
	}
	g.lister.plans[name] = p
}

// names scripts one page of names that ends the discovery.
func (g *rig) names(names ...string) {
	g.lister.pages = append(g.lister.pages, discover.Page{Names: names, Done: true})
}

func mustEqual[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

func mustContain(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

// newReleasable returns a context whose release ends it with the cause the job framework gives a handler at shutdown.
func newReleasable(parent context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	return ctx, func() { cancel(jobs.ErrReleased) }
}

// val reads the value of a counter or a gauge.
func val(m prometheus.Metric) float64 {
	var d dto.Metric
	if err := m.Write(&d); err != nil {
		panic(err)
	}
	if d.Counter != nil {
		return d.Counter.GetValue()
	}
	return d.Gauge.GetValue()
}
