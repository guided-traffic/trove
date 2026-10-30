package repository

import (
	"log/slog"
	"maps"
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// fixture builds repositories the way the snapshot would: with a numeric id, the
// compiled admission lists, and for a virtual repository its resolved members.
type routeFixture struct {
	t     *testing.T
	cat   *catalog.Registry
	repos map[string]Repository
	keys  map[int64]string
	rows  []q.ListNamespaceClaimsRow
	next  int64
}

func newRouteFixture(t *testing.T) *routeFixture {
	return &routeFixture{t: t, cat: npmCatalog(), repos: map[string]Repository{}, keys: map[int64]string{}}
}

type repoOpt func(*Repository, *admLists)

type admLists struct{ include, exclude []string }

func disabled() repoOpt { return func(r *Repository, _ *admLists) { r.Disabled = true } }
func deleting() repoOpt { return func(r *Repository, _ *admLists) { r.Lifecycle = Deleting } }

func priority() repoOpt           { return func(r *Repository, _ *admLists) { r.PriorityMember = true } }
func include(p ...string) repoOpt { return func(_ *Repository, a *admLists) { a.include = p } }
func exclude(p ...string) repoOpt { return func(_ *Repository, a *admLists) { a.exclude = p } }

func (f *routeFixture) repo(key string, class Class, format string, opts ...repoOpt) Repository {
	f.t.Helper()
	f.next++
	r := Repository{ID: key, Key: key, Class: class, Format: format, Lifecycle: Active, rid: f.next, autoClaim: true}
	var lists admLists
	for _, o := range opts {
		o(&r, &lists)
	}
	info, _ := f.cat.Lookup(format)
	adm, err := NewAdmission(info.PatternSubject, lists.include, lists.exclude)
	if err != nil {
		f.t.Fatal(err)
	}
	r.adm = adm
	f.repos[key] = r
	f.keys[r.rid] = key
	return r
}

// claim adds a claim of a repository to the index the routing reads.
func (f *routeFixture) claim(repoKey, pattern, scope string) {
	f.rows = append(f.rows, q.ListNamespaceClaimsRow{
		RepoID: f.repos[repoKey].rid, Format: f.repos[repoKey].Format, Pattern: pattern, Source: SourceManual, Scope: scope,
	})
}

func (f *routeFixture) virtual(key, format string, memberKeys []string, rules []PinRule, opts ...repoOpt) Repository {
	f.t.Helper()
	v := f.repo(key, Virtual, format, opts...)
	st := &virtualState{
		settings:  VirtualSettings{Members: memberKeys, Pinning: Pinning{Rules: rules}},
		memberIDs: map[int64]bool{},
	}
	for _, k := range memberKeys {
		m := f.repos[k]
		st.members = append(st.members, m)
		st.memberIDs[m.rid] = true
	}
	st.pins = newPinIndex(f.cat, format, rules, slog.New(slog.DiscardHandler), key)
	v.virt = st
	f.repos[key] = v
	return v
}

func (f *routeFixture) route(v Repository, name string) Route {
	f.t.Helper()
	idx := buildClaimIndex(f.rows, slog.New(slog.DiscardHandler))
	return routeOf(f.cat, idx, func(id int64) string { return f.keys[id] }, v, RouteRequest{Name: name})
}

func memberKeys(r Route) []string {
	out := make([]string, len(r.Members))
	for i, m := range r.Members {
		out[i] = m.Key
	}
	return out
}

func TestRouteWithoutClaimsOrPins(t *testing.T) {
	f := newRouteFixture(t)
	f.repo("local", Local, "npmlike")
	f.repo("remote", Remote, "npmlike")
	v := f.virtual("all", "npmlike", []string{"local", "remote"}, nil)
	t.Run("R-REPO-019 step 2 every member takes part in list order", func(t *testing.T) {
		r := f.route(v, "left-pad")
		if !r.Admitted || !slices.Equal(memberKeys(r), []string{"local", "remote"}) || r.Decision.Kind != "" || len(r.Dropped) != 0 {
			t.Fatalf("route = %+v", r)
		}
	})
	t.Run("R-REPO-019 a request without a name takes part with every member", func(t *testing.T) {
		if r := f.route(v, ""); !slices.Equal(memberKeys(r), []string{"local", "remote"}) {
			t.Fatalf("route = %+v", r)
		}
	})
	t.Run("R-REPO-019 a repository that is not virtual routes nothing", func(t *testing.T) {
		if r := f.route(f.repos["local"], "x"); len(r.Members) != 0 || r.Admitted {
			t.Fatalf("route = %+v", r)
		}
	})
}

func TestRouteDropsMembers(t *testing.T) {
	f := newRouteFixture(t)
	f.repo("off", Local, "npmlike", disabled())
	f.repo("going", Local, "npmlike", deleting())
	f.repo("scoped", Local, "npmlike", include("@acme/*"))
	f.repo("closed", Remote, "npmlike", exclude("@acme/*"))
	f.repo("plain", Remote, "npmlike")
	v := f.virtual("all", "npmlike", []string{"off", "going", "scoped", "closed", "plain"}, nil)
	r := f.route(v, "@acme/util")
	if want := []string{"scoped", "plain"}; !slices.Equal(memberKeys(r), want) {
		t.Fatalf("members = %v, want %v", memberKeys(r), want)
	}
	wantDropped := map[string]string{"off": DropDisabled, "going": DropDisabled, "closed": DropNotAdmitted}
	if !maps.Equal(r.Dropped, wantDropped) {
		t.Fatalf("dropped = %v, want %v", r.Dropped, wantDropped)
	}
	t.Run("R-REPO-005 a member is consulted only for admitted requests", func(t *testing.T) {
		r := f.route(v, "left-pad")
		if want := []string{"closed", "plain"}; !slices.Equal(memberKeys(r), want) {
			t.Fatalf("members = %v, want %v", memberKeys(r), want)
		}
		if r.Dropped["scoped"] != DropNotAdmitted {
			t.Fatalf("dropped = %v", r.Dropped)
		}
	})
	t.Run("R-REPO-005 a package-less request is admitted by every pattern list", func(t *testing.T) {
		if r := f.route(v, ""); !slices.Equal(memberKeys(r), []string{"scoped", "closed", "plain"}) {
			t.Fatalf("members = %v", memberKeys(r))
		}
	})
	t.Run("R-REPO-019 step 1 the virtual repository's own patterns decide Admitted", func(t *testing.T) {
		g := newRouteFixture(t)
		g.repo("m", Local, "npmlike")
		w := g.virtual("narrow", "npmlike", []string{"m"}, nil, include("@acme/*"))
		if g.route(w, "left-pad").Admitted || !g.route(w, "@acme/x").Admitted || !g.route(w, "").Admitted {
			t.Fatal("Admitted does not follow the patterns of the virtual repository")
		}
	})
}

func TestRoutePathSubject(t *testing.T) {
	f := newRouteFixture(t)
	f.repo("releases", Local, "pathfmt", include("releases/**"))
	f.repo("any", Local, "pathfmt")
	v := f.virtual("all", "pathfmt", []string{"releases", "any"}, nil)
	idx := buildClaimIndex(nil, slog.New(slog.DiscardHandler))
	route := func(path string) Route {
		return routeOf(f.cat, idx, func(int64) string { return "" }, v, RouteRequest{Path: path})
	}
	if got := memberKeys(route("releases/1.0/app.zip")); !slices.Equal(got, []string{"releases", "any"}) {
		t.Fatalf("members = %v", got)
	}
	if got := memberKeys(route("snapshots/app.zip")); !slices.Equal(got, []string{"any"}) {
		t.Fatalf("members = %v", got)
	}
}

func TestRouteClaims(t *testing.T) {
	f := newRouteFixture(t)
	f.repo("team-a", Local, "npmlike")
	f.repo("team-b", Local, "npmlike")
	f.repo("sandbox", Local, "npmlike")
	f.repo("remote", Remote, "npmlike")
	all := f.virtual("all", "npmlike", []string{"team-a", "team-b", "remote"}, nil)
	f.claim("team-a", "@acme/*", ScopeMembers)
	f.claim("team-b", "@acme/billing-*", ScopeMembers)
	f.claim("sandbox", "react", ScopeMembers)
	f.claim("sandbox", "@sandbox/*", ScopeGlobal)

	t.Run("R-REPO-023 a members claim of a member decides: only the claiming local member is consulted", func(t *testing.T) {
		r := f.route(all, "@acme/util")
		if !slices.Equal(memberKeys(r), []string{"team-a"}) || r.Decision.Kind != DecidedByClaim ||
			r.Decision.Pattern != "@acme/*" || !slices.Equal(r.Decision.ClaimedBy, []string{"team-a"}) {
			t.Fatalf("route = %+v", r)
		}
		if r.Dropped["team-b"] != DropClaimedElsewhere || r.Dropped["remote"] != DropClaimedElsewhere {
			t.Fatalf("dropped = %v", r.Dropped)
		}
	})
	t.Run("R-REPO-023 the most specific claim wins: team-b takes @acme/billing-* from the broader claim", func(t *testing.T) {
		r := f.route(all, "@acme/billing-core")
		if !slices.Equal(memberKeys(r), []string{"team-b"}) || r.Decision.Pattern != "@acme/billing-*" {
			t.Fatalf("route = %+v", r)
		}
	})
	t.Run("R-REPO-023 a members claim of a repository that V does not contain is not in effect in V", func(t *testing.T) {
		r := f.route(all, "react")
		if !slices.Equal(memberKeys(r), []string{"team-a", "team-b", "remote"}) || r.Decision.Kind != "" {
			t.Fatalf("route = %+v", r)
		}
	})
	t.Run("R-REPO-023 a global claim of a repository outside V is in effect: nothing is consulted, the answer is 404", func(t *testing.T) {
		r := f.route(all, "@sandbox/tool")
		if len(r.Members) != 0 || r.Decision.Kind != DecidedByClaim || !slices.Equal(r.Decision.ClaimedBy, []string{"sandbox"}) {
			t.Fatalf("route = %+v", r)
		}
		for _, k := range []string{"team-a", "team-b", "remote"} {
			if r.Dropped[k] != DropClaimedElsewhere {
				t.Fatalf("dropped = %v", r.Dropped)
			}
		}
	})
	t.Run("R-REPO-023 remote members are not asked for a claimed name", func(t *testing.T) {
		if slices.Contains(memberKeys(f.route(all, "@acme/anything")), "remote") {
			t.Fatal("the remote member was consulted for a claimed name")
		}
	})
	t.Run("R-REPO-023 a claimant that is disabled keeps the name from falling through to the remote", func(t *testing.T) {
		g := newRouteFixture(t)
		g.repo("internal", Local, "npmlike", disabled())
		g.repo("remote", Remote, "npmlike")
		w := g.virtual("all", "npmlike", []string{"internal", "remote"}, nil)
		g.claim("internal", "left-pad", ScopeMembers)
		r := g.route(w, "left-pad")
		if len(r.Members) != 0 || r.Dropped["internal"] != DropDisabled || r.Dropped["remote"] != DropClaimedElsewhere {
			t.Fatalf("route = %+v", r)
		}
	})
	t.Run("R-REPO-023 equal claims of two members: both are consulted", func(t *testing.T) {
		g := newRouteFixture(t)
		g.repo("a", Local, "npmlike")
		g.repo("b", Local, "npmlike")
		g.repo("remote", Remote, "npmlike")
		w := g.virtual("all", "npmlike", []string{"a", "b", "remote"}, nil)
		g.claim("a", "@shared/*", ScopeMembers)
		g.claim("b", "@shared/*", ScopeMembers)
		if got := memberKeys(g.route(w, "@shared/x")); !slices.Equal(got, []string{"a", "b"}) {
			t.Fatalf("members = %v", got)
		}
	})
	t.Run("R-REPO-023 R-NPM-033 a bare * claims unscoped names only", func(t *testing.T) {
		g := newRouteFixture(t)
		g.repo("local", Local, "npmlike")
		g.repo("remote", Remote, "npmlike")
		w := g.virtual("all", "npmlike", []string{"local", "remote"}, nil)
		g.claim("local", "*", ScopeGlobal)
		if got := memberKeys(g.route(w, "left-pad")); !slices.Equal(got, []string{"local"}) {
			t.Fatalf("unscoped: members = %v", got)
		}
		if got := memberKeys(g.route(w, "@partner/sdk")); !slices.Equal(got, []string{"local", "remote"}) {
			t.Fatalf("scoped: members = %v", got)
		}
	})
}

func TestRoutePins(t *testing.T) {
	f := newRouteFixture(t)
	f.repo("local", Local, "npmlike")
	f.repo("remote", Remote, "npmlike")
	f.repo("partner", Remote, "npmlike")
	rules := []PinRule{
		{Packages: []string{"@partner/*"}, Members: []string{"partner"}},
		{Packages: []string{"@partner/internal-*"}, Members: []string{"local"}},
		{Packages: []string{"dead-end"}, Members: []string{}},
	}
	all := f.virtual("all", "npmlike", []string{"local", "remote", "partner"}, rules)
	f.claim("local", "@partner/*", ScopeMembers)
	f.claim("local", "@acme/*", ScopeGlobal)

	t.Run("R-REPO-023 a pin rule takes precedence over a claim: the remote that owns a scope is reachable", func(t *testing.T) {
		r := f.route(all, "@partner/sdk")
		if !slices.Equal(memberKeys(r), []string{"partner"}) || r.Decision.Kind != DecidedByPin || r.Decision.Pattern != "@partner/*" {
			t.Fatalf("route = %+v", r)
		}
		if r.Dropped["local"] != DropPinnedOut || r.Dropped["remote"] != DropPinnedOut {
			t.Fatalf("dropped = %v", r.Dropped)
		}
	})
	t.Run("R-REPO-023 the most specific pattern over all rules selects the rule", func(t *testing.T) {
		if got := memberKeys(f.route(all, "@partner/internal-tool")); !slices.Equal(got, []string{"local"}) {
			t.Fatalf("members = %v", got)
		}
	})
	t.Run("R-REPO-023 a rule without members answers 404 for its names", func(t *testing.T) {
		r := f.route(all, "dead-end")
		if len(r.Members) != 0 || r.Decision.Kind != DecidedByPin {
			t.Fatalf("route = %+v", r)
		}
	})
	t.Run("R-REPO-023 a name no rule selects falls to the claims", func(t *testing.T) {
		r := f.route(all, "@acme/x")
		if !slices.Equal(memberKeys(r), []string{"local"}) || r.Decision.Kind != DecidedByClaim {
			t.Fatalf("route = %+v", r)
		}
	})
	t.Run("R-REPO-023 a pinned member that is disabled is dropped as disabled", func(t *testing.T) {
		g := newRouteFixture(t)
		g.repo("partner", Remote, "npmlike", disabled())
		g.repo("remote", Remote, "npmlike")
		w := g.virtual("all", "npmlike", []string{"partner", "remote"}, []PinRule{{Packages: []string{"@p/*"}, Members: []string{"partner"}}})
		r := g.route(w, "@p/x")
		if len(r.Members) != 0 || r.Dropped["partner"] != DropDisabled || r.Dropped["remote"] != DropPinnedOut {
			t.Fatalf("route = %+v", r)
		}
	})
}

func TestRoutePriorityOrder(t *testing.T) {
	f := newRouteFixture(t)
	f.repo("first", Local, "npmlike")
	f.repo("second", Remote, "npmlike", priority())
	f.repo("third", Local, "npmlike")
	f.repo("fourth", Remote, "npmlike", priority())
	v := f.virtual("all", "npmlike", []string{"first", "second", "third", "fourth"}, nil)
	t.Run("R-REPO-019 step 5 R-REPO-022 priority members first, list order within each group", func(t *testing.T) {
		if got := memberKeys(f.route(v, "x")); !slices.Equal(got, []string{"second", "fourth", "first", "third"}) {
			t.Fatalf("members = %v", got)
		}
	})
	t.Run("R-REPO-022 a format without priority members keeps the list order", func(t *testing.T) {
		g := newRouteFixture(t)
		g.cat = testCatalogNoPriority()
		g.repo("a", Local, "plain")
		g.repo("b", Remote, "plain", priority())
		w := g.virtual("all", "plain", []string{"a", "b"}, nil)
		if got := memberKeys(g.route(w, "x")); !slices.Equal(got, []string{"a", "b"}) {
			t.Fatalf("members = %v", got)
		}
	})
}

func testCatalogNoPriority() *catalog.Registry {
	r := catalog.NewRegistry()
	r.Register(catalog.Entry{Facts: catalog.Facts{
		ID: "plain", Classes: []Class{Local, Remote, Virtual}, Layout: "plain", ProtocolBasePath: "/api/plain/{key}/",
		PatternSubject: SubjectName, PackageNames: true, PinsAndClaims: true,
	}, Names: npmNames{}})
	return r
}

// TestRoutingRunsTheMatcherTable runs the name-pattern table of R-AUTHZ-005 through
// the routing code path: a member whose include list is the pattern is consulted
// for exactly the names the matcher matches (R-REPO-005 "a unit test runs the
// R-AUTHZ-005 matcher's test table through the routing code path").
func TestRoutingRunsTheMatcherTable(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"@acme/*", "@acme/ui", true},
		{"@acme/*", "@acme-tools/ui", false},
		{"@acme/*", "left-pad", false},
		{"*", "left-pad", true},
		{"*", "@acme/ui", false},
		{"@*/*", "@acme/ui", true},
		{"@*/*", "b", false},
		{"**", "@acme/ui", true},
		{"acme-*", "acme-core", true},
		{"acme-*", "ACME-ui", false},
		{"a?c", "abc", true},
		{"a?c", "a/c", false},
		{`a\*b`, "a*b", true},
		{`a\*b`, "ab", false},
		{`@acme/ui\*`, "@acme/ui-kit", false},
		{`\a`, "a", true},
	}
	for _, c := range cases {
		t.Run("R-REPO-005 R-AUTHZ-005 "+c.pattern+" vs "+c.name, func(t *testing.T) {
			f := newRouteFixture(t)
			f.repo("m", Local, "npmlike", include(c.pattern))
			v := f.virtual("v", "npmlike", []string{"m"}, nil)
			got := slices.Contains(memberKeys(f.route(v, c.name)), "m")
			if got != c.want {
				t.Fatalf("consulted = %v, want %v", got, c.want)
			}
			// The exclude list is the same matcher with the opposite effect.
			g := newRouteFixture(t)
			g.repo("m", Local, "npmlike", exclude(c.pattern))
			w := g.virtual("v", "npmlike", []string{"m"}, nil)
			if got := slices.Contains(memberKeys(g.route(w, c.name)), "m"); got == c.want {
				t.Fatalf("with the pattern as exclude: consulted = %v", got)
			}
		})
	}
	t.Run("R-REPO-005 excluding * on an npm-like repository still reaches the scoped names", func(t *testing.T) {
		f := newRouteFixture(t)
		f.repo("remote", Remote, "npmlike", exclude("*"))
		v := f.virtual("v", "npmlike", []string{"remote"}, nil)
		if len(f.route(v, "lodash").Members) != 0 || len(f.route(v, "@partner/sdk").Members) != 1 {
			t.Fatal(`exclude ["*"] must drop unscoped names and keep scoped ones`)
		}
		g := newRouteFixture(t)
		g.repo("remote", Remote, "npmlike", exclude("**"))
		w := g.virtual("v", "npmlike", []string{"remote"}, nil)
		if len(g.route(w, "lodash").Members) != 0 || len(g.route(w, "@partner/sdk").Members) != 0 {
			t.Fatal(`exclude ["**"] must drop every name`)
		}
	})
}

func TestRemoteRefusedAndOwnClaim(t *testing.T) {
	f := newRouteFixture(t)
	f.repo("local", Local, "npmlike")
	remote := f.repo("remote", Remote, "npmlike")
	other := f.repo("other-remote", Remote, "npmlike")
	f.virtual("v", "npmlike", []string{"local", "remote"}, []PinRule{{Packages: []string{"@partner/*"}, Members: []string{"remote"}}})
	f.claim("local", "@acme/*", ScopeGlobal)
	f.claim("local", "@partner/*", ScopeGlobal)
	f.claim("local", "internal-*", ScopeMembers)
	idx := buildClaimIndex(f.rows, slog.New(slog.DiscardHandler))
	// The index is read through the cache in production; the decision itself is
	// pure, so the test calls the same function with the index in hand.
	refused := func(r Repository, name string, via *Repository) bool {
		return remoteRefused(f.cat, idx, r, name, via)
	}
	via := f.repos["v"]
	t.Run("R-REPO-023 remote refusal: a global claim makes a remote repository answer 404, also when addressed directly", func(t *testing.T) {
		if !refused(remote, "@acme/new-thing", nil) || !refused(other, "@acme/new-thing", &via) {
			t.Fatal("a global claim did not refuse")
		}
	})
	t.Run("R-REPO-023 a members claim is not refused at the remote", func(t *testing.T) {
		if refused(remote, "internal-tool", nil) {
			t.Fatal("a members claim refused at the remote")
		}
	})
	t.Run("R-REPO-023 a name no claim covers is served", func(t *testing.T) {
		if refused(remote, "left-pad", nil) {
			t.Fatal("refused an unclaimed name")
		}
	})
	t.Run("R-REPO-023 a pin rule of the virtual repository that names the remote lifts the refusal", func(t *testing.T) {
		if refused(remote, "@partner/sdk", &via) {
			t.Fatal("the pin rule did not lift the refusal")
		}
		if !refused(other, "@partner/sdk", &via) {
			t.Fatal("the pin rule lifted the refusal for a remote it does not name")
		}
		if !refused(remote, "@partner/sdk", nil) {
			t.Fatal("addressed directly, the remote must refuse a globally claimed name")
		}
	})
	t.Run("R-REPO-023 proxy.claims.remote_refusal off disables the refusal", func(t *testing.T) {
		off := &Routing{cat: f.cat, refusal: func() bool { return false }}
		if got, err := off.RemoteRefused(t.Context(), remote, "@acme/x", nil); err != nil || got {
			t.Fatalf("RemoteRefused with the switch off = %v, %v", got, err)
		}
	})
	t.Run("R-REPO-048 a repository is covered by its own claim of either scope", func(t *testing.T) {
		local := f.repos["local"]
		for name, want := range map[string]bool{"@acme/x": true, "internal-tool": true, "left-pad": false} {
			if got := coveredByOwn(f.cat, idx, local, name); got != want {
				t.Errorf("covered(%q) = %v, want %v", name, got, want)
			}
		}
	})
}
