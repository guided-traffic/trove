package virtualtest

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/virtual"
)

// Router is a virtual.Router with configurable answers. By default a request is admitted and every
// member of the virtual repository takes part.
type Router struct {
	mu sync.Mutex
	// Dropped maps a member key to the reason it takes no part.
	Dropped map[string]string
	// NotAdmitted makes the virtual repository's own patterns refuse the request.
	NotAdmitted bool
	Decision    repository.RouteDecision
	// Covered lists "<local key>/<name>" pairs a claim of the local repository covers.
	Covered map[string]bool
	// Exposure is the exposure set by local key.
	Exposure map[string][]repository.Exposure
	// Refused lists the remote keys that refuse a name.
	Refused map[string]bool
	// Routes counts the calls of Route.
	Routes int
}

var _ virtual.Router = (*Router)(nil)

// Route implements virtual.Router.
func (r *Router) Route(_ context.Context, v repository.Repository, _ repository.RouteRequest) (repository.Route, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Routes++
	out := repository.Route{Admitted: !r.NotAdmitted, Dropped: map[string]string{}, Decision: r.Decision}
	for _, m := range v.Members() {
		if why, ok := r.Dropped[m.Key]; ok {
			out.Dropped[m.Key] = why
			continue
		}
		out.Members = append(out.Members, m)
	}
	return out, nil
}

// RemoteRefused implements virtual.Router.
func (r *Router) RemoteRefused(_ context.Context, remote repository.Repository, _ string, _ *repository.Repository) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Refused[remote.Key], nil
}

// CoveredByOwnClaim implements virtual.Router.
func (r *Router) CoveredByOwnClaim(_ context.Context, local repository.Repository, name string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Covered[local.Key+"/"+name], nil
}

// ExposureSet implements virtual.Router.
func (r *Router) ExposureSet(_ context.Context, local repository.Repository, _ string) ([]repository.Exposure, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Exposure[local.Key], nil
}

// Remote is a virtual.Remotes that keeps its documents, caches and states in memory and records
// every call. A document is configured per repository key and path.
type Remote struct {
	mu     sync.Mutex
	docs   map[string]*doc
	cached map[string]map[string]bool // repo key -> artifact path
	states map[string]virtual.EntryState
	probes map[string]virtual.ProbeResult
	ids    map[int64]string
	calls  []string
	// OnProbe runs at the start of every probe; the shadowing tests use it to look at what is open.
	OnProbe func(rc virtual.RemoteCall)
	// MetadataErr makes Metadata fail for a repository key whatever is configured; ProbeErr and
	// StateErr do the same for a probe and for EntryState.
	MetadataErr, ProbeErr, StateErr map[string]error
	// ProbeDelay holds a probe, to show that the probes of one publish run concurrently.
	ProbeDelay time.Duration
	inflight   int
	// MaxInflight is the most probes that were running at once.
	MaxInflight int
}

type doc struct {
	versions []string
	expires  time.Time
	fetched  bool
}

var _ virtual.Remotes = (*Remote)(nil)

// NewRemote returns an engine without documents.
func NewRemote() *Remote {
	return &Remote{
		docs: map[string]*doc{}, cached: map[string]map[string]bool{}, states: map[string]virtual.EntryState{},
		probes: map[string]virtual.ProbeResult{}, ids: map[int64]string{}, MetadataErr: map[string]error{}, ProbeErr: map[string]error{}, StateErr: map[string]error{},
	}
}

// Register tells the fake the id of a remote repository, which EntryState and CachedArtifacts take.
func (f *Remote) Register(r ...repository.Repository) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, x := range r {
		f.ids[idOf(x)] = x.Key
	}
}

func docKey(repo, path string) string { return repo + "|" + path }

// SetDoc stores the document of a package in a remote repository's cache. fetched says the call that
// serves it contacts the upstream.
func (f *Remote) SetDoc(repo, path string, versions []string, expires time.Time, fetched bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.docs[docKey(repo, path)] = &doc{versions: versions, expires: expires, fetched: fetched}
}

// DropDoc removes a document.
func (f *Remote) DropDoc(repo, path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.docs, docKey(repo, path))
}

// CacheArtifact stores an artifact path in a remote repository's cache.
func (f *Remote) CacheArtifact(repo, path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cached[repo] == nil {
		f.cached[repo] = map[string]bool{}
	}
	f.cached[repo][path] = true
}

// SetState sets what EntryState answers for a repository (all paths).
func (f *Remote) SetState(repo string, st virtual.EntryState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states[repo] = st
}

// SetProbe sets what a probe of a repository answers.
func (f *Remote) SetProbe(repo string, r virtual.ProbeResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probes[repo] = r
}

// Calls returns the recorded calls: "metadata:<repo>:<path>:fill=<bool>", "probe:<repo>:<path>",
// "state:<repo>:<path>", "cached:<repo>:<path>", "artifacts:<repo>".
func (f *Remote) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// Count returns how many recorded calls start with a prefix.
func (f *Remote) Count(prefix string) int {
	n := 0
	for _, c := range f.Calls() {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (f *Remote) record(format string, args ...any) {
	f.calls = append(f.calls, fmt.Sprintf(format, args...))
}

// Cached implements virtual.Remotes.
func (f *Remote) Cached(_ context.Context, rc virtual.RemoteCall) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("cached:%s:%s", rc.Remote.Key, rc.Path)
	if f.cached[rc.Remote.Key][rc.Path] {
		return true, nil
	}
	_, ok := f.docs[docKey(rc.Remote.Key, rc.Path)]
	return ok, nil
}

// Metadata implements virtual.Remotes. A caller that may not fill gets the stored document,
// expired or not, and nothing else.
func (f *Remote) Metadata(_ context.Context, rc virtual.RemoteCall) (*virtual.RemoteDoc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("metadata:%s:%s:fill=%t", rc.Remote.Key, rc.Path, rc.MayFill)
	if err := f.MetadataErr[rc.Remote.Key]; err != nil {
		return nil, err
	}
	d := f.docs[docKey(rc.Remote.Key, rc.Path)]
	if d == nil {
		return nil, errs.E(errs.NotFound, errs.CodeNotFound, "no such package upstream")
	}
	body := fmt.Sprintf(`{"versions":[%s]}`, quoteAll(d.versions))
	digest := [32]byte{}
	copy(digest[:], body)
	return &virtual.RemoteDoc{
		Digest: digest, Bytes: func(context.Context) ([]byte, error) { return []byte(body), nil },
		ExpiresAt: d.expires, Stale: !d.expires.IsZero() && d.expires.Before(time.Now()), Fetched: d.fetched && rc.MayFill,
	}, nil
}

func quoteAll(vs []string) string {
	q := make([]string, len(vs))
	for i, v := range vs {
		q[i] = fmt.Sprintf("%q", v)
	}
	return strings.Join(q, ",")
}

// EntryState implements virtual.Remotes.
func (f *Remote) EntryState(_ context.Context, id int64, path string) (virtual.EntryState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := f.ids[id]
	f.record("state:%s:%s", key, path)
	if err := f.StateErr[key]; err != nil {
		return virtual.EntryState{}, err
	}
	return f.states[key], nil
}

// Probe implements virtual.Remotes.
func (f *Remote) Probe(_ context.Context, rc virtual.RemoteCall) (virtual.ProbeResult, error) {
	if f.OnProbe != nil {
		f.OnProbe(rc)
	}
	f.mu.Lock()
	f.record("probe:%s:%s", rc.Remote.Key, rc.Path)
	f.inflight++
	f.MaxInflight = max(f.MaxInflight, f.inflight)
	res, delay, perr := f.probes[rc.Remote.Key], f.ProbeDelay, f.ProbeErr[rc.Remote.Key]
	f.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	f.mu.Lock()
	f.inflight--
	f.mu.Unlock()
	if perr != nil {
		return virtual.ProbeResult{}, perr
	}
	if res.Outcome == 0 {
		res.Outcome = virtual.NotServed
	}
	return res, nil
}

// CachedArtifacts implements virtual.Remotes.
func (f *Remote) CachedArtifacts(_ context.Context, id int64, prefix string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := f.ids[id]
	f.record("artifacts:%s", key)
	var out []string
	for p := range f.cached[key] {
		if strings.HasPrefix(p, prefix) {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out, nil
}

// Artifacts is a virtual.Tiers over maps: the artifacts a local member holds, a remote member's cache
// and what its upstream would serve.
type Artifacts struct {
	mu sync.Mutex
	// LocalItems, CachedItems and UpstreamItems map a repository key to its artifacts, path to body.
	LocalItems, CachedItems, UpstreamItems map[string]map[string]string
	// FetchErr makes the fetch from a repository's upstream fail with the error.
	FetchErr map[string]error
	// FailAfterBytes makes the fetch of a repository write a first chunk and then fail.
	FailAfterBytes map[string]error
	calls          []string
}

var _ virtual.Tiers = (*Artifacts)(nil)

// NewArtifacts returns empty tiers.
func NewArtifacts() *Artifacts {
	return &Artifacts{
		LocalItems: map[string]map[string]string{}, CachedItems: map[string]map[string]string{}, UpstreamItems: map[string]map[string]string{},
		FetchErr: map[string]error{}, FailAfterBytes: map[string]error{},
	}
}

// Put stores a body in one of the maps.
func Put(m map[string]map[string]string, repo, path, body string) {
	if m[repo] == nil {
		m[repo] = map[string]string{}
	}
	m[repo][path] = body
}

// Calls returns the lookups made, in order: "local:<key>", "cached:<key>", "fetch:<key>".
func (a *Artifacts) Calls() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.calls)
}

func (a *Artifacts) hit(m map[string]map[string]string, tier string, mem virtual.Member, d virtual.Descriptor) (virtual.Hit, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, tier+":"+mem.Repo.Key)
	body, ok := m[mem.Repo.Key][d.Path]
	if !ok {
		return nil, false, nil
	}
	return func(w http.ResponseWriter) error {
		_, err := w.Write([]byte(body))
		return err
	}, true, nil
}

// Local implements virtual.Tiers.
func (a *Artifacts) Local(_ context.Context, mem virtual.Member, d virtual.Descriptor) (virtual.Hit, bool, error) {
	return a.hit(a.LocalItems, "local", mem, d)
}

// RemoteCached implements virtual.Tiers.
func (a *Artifacts) RemoteCached(_ context.Context, mem virtual.Member, d virtual.Descriptor) (virtual.Hit, bool, error) {
	return a.hit(a.CachedItems, "cached", mem, d)
}

// RemoteFetch implements virtual.Tiers.
func (a *Artifacts) RemoteFetch(_ context.Context, mem virtual.Member, d virtual.Descriptor, w http.ResponseWriter) (bool, error) {
	a.mu.Lock()
	a.calls = append(a.calls, "fetch:"+mem.Repo.Key)
	err, mid := a.FetchErr[mem.Repo.Key], a.FailAfterBytes[mem.Repo.Key]
	body, ok := a.UpstreamItems[mem.Repo.Key][d.Path]
	a.mu.Unlock()
	switch {
	case err != nil:
		return false, err
	case mid != nil:
		_, _ = w.Write([]byte("par"))
		return true, mid
	case !ok:
		return false, nil
	}
	_, werr := w.Write([]byte(body))
	return true, werr
}
