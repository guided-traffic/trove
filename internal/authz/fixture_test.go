package authz

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Principal IDs of the unit fixtures. They are plain strings: the index treats an
// ID as an opaque key.
const (
	idAlice  = "user-alice"
	idBob    = "user-bob"
	idCarol  = "user-carol"
	idDevs   = "group-developers"
	idWeb    = "group-team-web"
	idBot    = "sa-ci-bot"
	idOps    = "group-platform-admins"
	idAudit  = "user-auditor"
	idMapAcm = "ci-gitlab-internal-acme"
)

// fixture assembles a Snapshot the way the database would.
type fixture struct {
	s      Snapshot
	repoID map[string]int64
	next   int64
}

func newFixture() *fixture {
	f := &fixture{repoID: map[string]int64{}}
	f.s.Label = Label{AuthzEpoch: 1, AuthzAt: time.Unix(1000, 0), RepoEpoch: 1, RepoAt: time.Unix(1000, 0)}
	f.s.Started = time.Now()
	return f
}

// repo adds a repository; cfg is the JSON document or "".
func (f *fixture) repo(key, class, format, cfg string) *fixture {
	f.next++
	f.repoID[key] = f.next
	if cfg == "" {
		cfg = "{}"
	}
	f.s.Repositories = append(f.s.Repositories, q.AuthzBuildRepositoriesRow{ID: f.next, Key: key, Class: class, Format: format, Config: []byte(cfg)})
	return f
}

// policyDef describes one policy.
type policyDef struct {
	name      string
	selectors []string
	pkgInc    []string
	pkgExc    []string
	pathInc   []string
	pathExc   []string
	// entries maps a principal ID to its actions (presets allowed).
	entries map[string][]string
}

func (f *fixture) policy(d policyDef) *fixture {
	id := "policy-" + d.name
	f.s.Policies = append(f.s.Policies, q.AuthzBuildPoliciesRow{ID: id, Name: d.name, ResourceType: "repository"})
	for i, text := range d.selectors {
		sel, err := parseSelector(text)
		if err != nil {
			panic(err)
		}
		row := q.AuthzBuildSelectorsRow{PolicyID: id, Ordinal: int16(i), Kind: sel.kind, Format: sel.format}
		switch sel.kind {
		case selKey:
			row.RepositoryID = f.repoID[sel.key]
			if row.RepositoryID == 0 {
				panic("unknown repository " + sel.key)
			}
		case selPattern:
			row.KeyPattern = sel.pattern
		case selClass:
			row.RepoClass = sel.class
		}
		f.s.Selectors = append(f.s.Selectors, row)
	}
	add := func(dim, mode string, list []string) {
		for _, p := range list {
			f.s.Patterns = append(f.s.Patterns, q.AuthzBuildPatternsRow{PolicyID: id, Dimension: dim, Mode: mode, Pattern: p})
		}
	}
	add("package", "include", d.pkgInc)
	add("package", "exclude", d.pkgExc)
	add("path", "include", d.pathInc)
	add("path", "exclude", d.pathExc)
	for pid, acts := range d.entries {
		set, bad := ParseActions(acts)
		if len(bad) > 0 {
			panic(fmt.Sprint("bad actions ", bad))
		}
		kind := dbGroup
		switch pid {
		case AnonymousID:
			kind = dbAnonymous
		case AuthenticatedID:
			kind = dbAuthenticated
		case idBot:
			kind = dbServiceAcct
		case idAlice, idBob, idCarol, idAudit:
			kind = dbUser
		case idMapAcm:
			kind = dbCIMapping
		}
		name := strings.TrimPrefix(pid, "group-")
		if kind == dbCIMapping {
			name = "gitlab-internal/acme"
		}
		f.s.Entries = append(f.s.Entries, q.AuthzBuildEntriesRow{PolicyID: id, PrincipalID: pid, PrincipalKind: kind, PrincipalName: name, Actions: set.Strings()})
	}
	return f
}

func (f *fixture) bind(role Role, principalID string) *fixture {
	f.s.Bindings = append(f.s.Bindings, q.AuthzBuildBindingsRow{Role: string(role), PrincipalID: principalID})
	return f
}

// fakeSource serves a snapshot that tests replace; Epochs reads its label.
type fakeSource struct {
	mu       sync.Mutex
	snap     *Snapshot
	err      error
	builds   int
	epochs   int
	buildGap time.Duration
}

func (s *fakeSource) set(snap Snapshot) {
	s.mu.Lock()
	cp := snap
	s.snap = &cp
	s.mu.Unlock()
}

func (s *fakeSource) Snapshot(ctx context.Context) (*Snapshot, error) {
	s.mu.Lock()
	gap := s.buildGap
	s.mu.Unlock()
	if gap > 0 {
		time.Sleep(gap)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.builds++
	if s.err != nil {
		return nil, s.err
	}
	cp := *s.snap
	cp.Started = time.Now()
	return &cp, nil
}

func (s *fakeSource) Epochs(ctx context.Context) (Label, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.epochs++
	if s.err != nil {
		return Label{}, s.err
	}
	return s.snap.Label, nil
}

// fakeChanges stands in for cluster.Changes.
type fakeChanges struct {
	mu        sync.Mutex
	listeners []cluster.Listener
	authz     struct {
		epoch int64
		at    time.Time
	}
	repo struct {
		epoch int64
		at    time.Time
	}
	polled time.Time
}

func (c *fakeChanges) AddListener(l cluster.Listener) { c.listeners = append(c.listeners, l) }

func (c *fakeChanges) AreaState(area string) (int64, time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch area {
	case AreaAuthz:
		return c.authz.epoch, c.authz.at
	case AreaRepoConfig:
		return c.repo.epoch, c.repo.at
	}
	return 0, time.Time{}
}

func (c *fakeChanges) FreshWithin(d time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.polled.IsZero() && time.Since(c.polled) < d
}

func (c *fakeChanges) Notify(meta.Tx, string, int, any) error { return nil }

// poll records a successful poll that applied the given label.
func (c *fakeChanges) poll(l Label) {
	c.mu.Lock()
	c.authz.epoch, c.authz.at = l.AuthzEpoch, l.AuthzAt
	c.repo.epoch, c.repo.at = l.RepoEpoch, l.RepoAt
	c.polled = time.Now()
	c.mu.Unlock()
}

func (c *fakeChanges) apply(area string) {
	for _, l := range c.listeners {
		l.ApplyChange(area, 1, json.RawMessage(`{}`))
	}
}

// testConfig is the authz section with its defaults.
func testConfig() config.Authz {
	return config.Authz{
		HideExistence: true,
		Cache:         config.AuthzCache{MaxStaleness: 10 * time.Second, SafetyTTL: 10 * time.Minute},
		Audit:         config.AuthzAudit{Reads: config.AuthzReadsDenied},
	}
}

// newTestEngine builds an engine over a fixture and installs its index without
// the background loops.
func newTestEngine(t testing.TB, f *fixture, mod func(*Options)) *Engine {
	t.Helper()
	src := &fakeSource{}
	src.set(f.s)
	o := Options{
		Source: src, Changes: &fakeChanges{}, Config: testConfig(), RetryBudget: time.Second, Formats: testRegistry(),
		AnonymousAccess: func(context.Context) (bool, error) { return false, nil },
	}
	if mod != nil {
		mod(&o)
	}
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	return e
}

func userSub(id, name string, groups ...string) Subject {
	s := Subject{Kind: principal.KindUser, ID: id, Name: name}
	for _, g := range groups {
		s.Groups = append(s.Groups, principal.GroupRef{ID: g, Name: strings.TrimPrefix(g, "group-")})
	}
	return s
}
