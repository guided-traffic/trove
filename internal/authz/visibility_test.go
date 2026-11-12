package authz

import (
	"testing"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/qname"
)

// TestR_AUTHZ_033_TheLevelOrder: the levels are ordered from the most restrictive, spelled as the API and the database spell
// them, and a name that is none of them is refused.
func TestR_AUTHZ_033_TheLevelOrder(t *testing.T) {
	if LevelPrivate >= LevelInternal || LevelInternal >= LevelPublic {
		t.Fatal("the order is private < internal < public")
	}
	for _, c := range []struct {
		name string
		l    Level
	}{{"private", LevelPrivate}, {"internal", LevelInternal}, {"public", LevelPublic}} {
		got, ok := ParseLevel(c.name)
		if !ok || got != c.l || c.l.String() != c.name {
			t.Errorf("%q parses to %v (%v) and renders %q", c.name, got, ok, c.l.String())
		}
	}
	for _, bad := range []string{"", "Private", "authenticated", "anonymous", "open"} {
		if l, ok := ParseLevel(bad); ok || l != LevelPrivate {
			t.Errorf("%q parses to %v (%v), want the most restrictive level and not ok", bad, l, ok)
		}
	}
	if Level(9).String() != "private" {
		t.Error("a value outside the order renders as the most restrictive level")
	}
}

// TestR_AUTHZ_030_WhoIsInternal: a user through a session or a personal token who is not flagged external and did not come
// through a Bastion; never a service account, a CI token, a Bastion registration or the anonymous subject.
func TestR_AUTHZ_030_WhoIsInternal(t *testing.T) {
	user := Subject{Kind: principal.KindUser, ID: "u1", Name: "alice"}
	external, viaBastion := user, user
	external.External = true
	viaBastion.ViaBastion = true
	for _, c := range []struct {
		name string
		sub  Subject
		want bool
	}{
		{"a user", user, true},
		{"a user through a session", session(user), true},
		{"a user through a personal token", bearer(user), true},
		{"the break-glass administrator", Subject{Kind: principal.KindBreakGlass, ID: "bg", Name: "root"}, true},
		{"a user flagged external", external, false},
		{"a user through a Bastion", viaBastion, false},
		{"a service account", Subject{Kind: principal.KindServiceAccount, ID: "s1", Name: "bot"}, false},
		{"a CI token", Subject{Kind: principal.KindCI, Mappings: []string{"ci:gitlab/acme"}}, false},
		{"a Bastion registration", Subject{Kind: principal.KindBastion, ID: "b1", Name: "dmz"}, false},
		{"the anonymous subject", Subject{Anonymous: true, Kind: principal.KindAnonymous}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := c.sub.Internal(); got != c.want {
				t.Errorf("Internal() = %v, want %v", got, c.want)
			}
		})
	}
}

// TestR_AUTHZ_034_WikiIsAPartOfTheRead: a coordinate names its part; the wiki is read only, and the question form of a wiki
// read follows the grants of the artifacts until a level opens one of them apart.
func TestR_AUTHZ_034_WikiIsAPartOfTheRead(t *testing.T) {
	f := newFixture().repo("npm-internal", "local", "npm", "")
	f.policy(policyDef{name: "devs", selectors: []string{"npm-internal"}, entries: map[string][]string{idDevs: {"read", "deploy"}}})
	e := newTestEngine(t, f, nil)
	idx, err := e.Begin(t.Context(), aliceSub)
	if err != nil {
		t.Fatal(err)
	}
	wiki := func(c Coordinate) Coordinate { c.Part = PartWiki; return c }
	if PartArtifacts != 0 {
		t.Fatal("the zero value of Part is the artifacts, so every existing coordinate keeps its meaning")
	}
	if d := e.DecideOn(t.Context(), idx, aliceSub, wiki(read("npm-internal", "x", ""))); d.Outcome != Allow {
		t.Errorf("a wiki read by a grant of read: %+v", d)
	}
	for _, a := range []Action{Deploy, Delete, Manage} {
		d := e.DecideOn(t.Context(), idx, aliceSub, wiki(write("npm-internal", "x", "", a)))
		if d.Outcome == Allow || d.Reason != ReasonWiki {
			t.Errorf("a wiki coordinate asking %s was not refused as a programming error: %+v", a, d)
		}
	}
	if !e.WikiReadableOn(idx, aliceSub, read("npm-internal", "x", "")) {
		t.Error("the member of a group that reads the repository cannot read its wiki")
	}
	if e.WikiReadableOn(idx, carolSub, read("npm-internal", "x", "")) {
		t.Error("a user without a grant reads a wiki")
	}
	if e.WikiReadableOn(idx, anonSub, read("npm-internal", "x", "")) {
		t.Error("an anonymous caller reads a wiki no level has opened")
	}
}

// TestR_AUTHZ_033_TheIndexHoldsTheLevels: the grant index carries the levels of every repository, a wiki never above its
// artifacts, a value that is no level as the most restrictive one, and says whether some artifacts are public.
func TestR_AUTHZ_033_TheIndexHoldsTheLevels(t *testing.T) {
	f := newFixture().repo("npm-a", "local", "npm", "").repo("npm-b", "local", "npm", "").repo("npm-c", "local", "npm", "")
	f.levels("npm-a", "public", "internal").levels("npm-b", "internal", "public").levels("npm-c", "bogus", "internal")
	idx := newTestEngine(t, f, nil).Index()
	for _, c := range []struct {
		repo           string
		artifacts, wki Level
	}{
		{"npm-a", LevelPublic, LevelInternal},
		{"npm-b", LevelInternal, LevelInternal}, // a state that breaks the order never reads wider than the artifacts
		{"npm-c", LevelPrivate, LevelPrivate},
	} {
		a, w, ok := idx.Levels(qr(c.repo))
		if !ok || a != c.artifacts || w != c.wki {
			t.Errorf("%s: %v %v (%v), want %v %v", c.repo, a, w, ok, c.artifacts, c.wki)
		}
	}
	if _, _, ok := idx.Levels(qr("ghost")); ok {
		t.Error("a repository the index does not know has levels")
	}
	if !idx.PublicContent() {
		t.Error("npm-a has public artifacts and PublicContent is false")
	}
	f2 := newFixture().repo("npm-a", "local", "npm", "").levels("npm-a", "internal", "internal")
	if newTestEngine(t, f2, nil).Index().PublicContent() {
		t.Error("PublicContent is true while no artifacts are public")
	}
}

// levelWorld is the fixture of the worked rows of R-AUTHZ-035: project it, npm-public (artifacts public, wiki internal), npm-internal
// (both internal), npm-private (both private) and the virtual npm-all (internal) over the three. Erin is external with read on npm-private,
// Frank internal without grants, and the service account holds nothing.
func levelWorld(t *testing.T) (*Engine, map[string]Subject) {
	t.Helper()
	f := newFixture().
		repo("npm-public", "local", "npm", "").repo("npm-internal", "local", "npm", "").repo("npm-private", "local", "npm", "").
		repo("npm-all", "virtual", "npm", `{"virtual":{"members":["npm-public","npm-internal","npm-private"]}}`)
	f.levels("npm-public", "public", "internal").levels("npm-internal", "internal", "internal").levels("npm-all", "internal", "internal")
	f.policy(policyDef{name: "erin-reads", selectors: []string{"npm-private"}, entries: map[string][]string{"user-erin": {"read"}}})
	erin := external(userSub("user-erin", "erin"))
	frank := userSub("user-frank", "frank")
	mobile := scope.Scope{Repositories: []scope.Grant{{Repositories: []string{"mobile/*"}, Actions: []string{"read"}}}}
	return newTestEngine(t, f, nil), map[string]Subject{
		"anonymous": anonSub, "erin": erin, "frank": frank, "scoped frank": tokenScop(frank, mobile),
		"partner": {Kind: principal.KindServiceAccount, ID: "sa-partner", Name: "web/partner"},
	}
}

// TestR_AUTHZ_035_WorkedRows: the eight rows of the table of R-AUTHZ-035, in project web in the spec and project it here.
func TestR_AUTHZ_035_WorkedRows(t *testing.T) {
	e, who := levelWorld(t)
	for _, c := range []struct {
		name    string
		sub     string
		repo    string
		want    Outcome
		reason  string
		sources []string
	}{
		{"1 anonymous reads npm-public", "anonymous", "npm-public", Allow, ReasonAllowVisibility, []string{"visibility:public"}},
		{"2 anonymous through npm-all is refused at the gate", "anonymous", "npm-all", DenyUnauthenticated, ReasonUnauthenticated, nil},
		{"3 Frank through npm-all passes the gate by internal", "frank", "npm-all", Allow, ReasonAllowVisibility, []string{"visibility:internal"}},
		{"4 Erin reads npm-private by her grant", "erin", "npm-private", Allow, ReasonAllowGrant, []string{"erin-reads"}},
		{"5 Erin, external, does not read npm-internal", "erin", "npm-internal", DenyHidden, ReasonNoGrant, nil},
		{"6 the service account reads the public npm-public", "partner", "npm-public", Allow, ReasonAllowVisibility, nil},
		{"7 a scope that refuses the repository refuses internal", "scoped frank", "npm-internal", DenyHidden, ReasonNoGrant, nil},
		{"8 a scope is not consulted for public", "scoped frank", "npm-public", Allow, ReasonAllowVisibility, nil},
	} {
		t.Run("R-AUTHZ-035 row "+c.name, func(t *testing.T) {
			d := e.Decide(t.Context(), who[c.sub], read(c.repo, "@web/button", ""))
			if d.Outcome != c.want || d.Reason != c.reason || (c.sources != nil && !equalStrings(d.Sources, c.sources)) {
				t.Fatalf("decision %+v, want outcome %d reason %q sources %v", d, c.want, c.reason, c.sources)
			}
		})
	}
	t.Run("R-AUTHZ-035 row 3 through npm-all Frank reads public and internal members and the private one is skipped", func(t *testing.T) {
		idx, err := e.Begin(t.Context(), who["frank"])
		if err != nil {
			t.Fatal(err)
		}
		for member, want := range map[string]bool{"npm-public": true, "npm-internal": true, "npm-private": false} {
			if got, _ := e.MemberUsableOn(t.Context(), idx, who["frank"], qr(member), read("npm-all", "@web/button", "")); got != want {
				t.Errorf("%s usable: %v, want %v", member, got, want)
			}
		}
	})
	t.Run("R-AUTHZ-035 a level never gives a write, a role or a capability", func(t *testing.T) {
		for _, a := range []Action{Annotate, Deploy, Overwrite, Delete, Manage} {
			if d := e.Decide(t.Context(), who["frank"], write("npm-public", "x", "", a)); d.Allowed() {
				t.Errorf("%s allowed by a level: %+v", a, d)
			}
			if d := e.Decide(t.Context(), who["anonymous"], write("npm-public", "x", "", a)); d.Outcome != DenyUnauthenticated {
				t.Errorf("anonymous %s: %+v", a, d)
			}
		}
		idx, _ := e.Begin(t.Context(), who["frank"])
		if e.HoldsOn(idx, who["frank"], string(SettingsRead)) {
			t.Error("a level gave a capability")
		}
	})
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestR_AUTHZ_034_TheWikiHasALevelOfItsOwn: npm-public has public artifacts and an internal wiki, so the anonymous subject reads the
// artifacts and not the wiki, and an internal user reads both (R-AUTHZ-034 acceptance, R-AUTHZ-035 rule 9).
func TestR_AUTHZ_034_TheWikiHasALevelOfItsOwn(t *testing.T) {
	e, who := levelWorld(t)
	idx, err := e.Begin(t.Context(), who["frank"])
	if err != nil {
		t.Fatal(err)
	}
	wiki := func(c Coordinate) Coordinate { c.Part = PartWiki; return c }
	c := read("npm-public", "@web/button", "")
	if d := e.DecideOn(t.Context(), idx, who["anonymous"], wiki(c)); d.Outcome != DenyUnauthenticated || d.Reason != ReasonWiki {
		t.Errorf("anonymous wiki: %+v, want 401 deny.wiki", d)
	}
	if d := e.DecideOn(t.Context(), idx, who["anonymous"], c); d.Outcome != Allow {
		t.Errorf("anonymous artifacts: %+v", d)
	}
	if d := e.DecideOn(t.Context(), idx, who["frank"], wiki(c)); d.Outcome != Allow || d.Reason != ReasonAllowVisibility {
		t.Errorf("Frank wiki: %+v", d)
	}
	for sub, want := range map[string]bool{"anonymous": false, "frank": true, "erin": false, "partner": false, "scoped frank": false} {
		if got := e.WikiReadableOn(idx, who[sub], c); got != want {
			t.Errorf("WikiReadableOn(%s) = %v, want %v", sub, got, want)
		}
	}
	rs, err := e.ReadScopeOn(t.Context(), idx, who["anonymous"], qname.Repo{}, []qname.Repo{qr("npm-public")})
	if err != nil {
		t.Fatal(err)
	}
	if !rs.MayRead(qr("npm-public"), "@web/button", "") || rs.MayReadWiki(qr("npm-public"), "@web/button", "") {
		t.Error("the read scope of the anonymous subject reads the artifacts and not the wiki")
	}
}

// TestR_AUTHZ_035_AReadShapeAllNeverMeetsARefusedRead: with every level on every repository, for every kind of subject, a repository the
// shape calls ReadAll is read by the decision for every package, and a ReadNone one by none (R-AUTHZ-035 rule 7).
func TestR_AUTHZ_035_AReadShapeAllNeverMeetsARefusedRead(t *testing.T) {
	levels := []string{"private", "internal", "public"}
	subjects := map[string]Subject{
		"anonymous": anonSub, "internal": aliceSub, "external": external(aliceSub), "bastion": viaBastion(aliceSub), "bot": botSub,
		"scoped":     tokenScop(aliceSub, scope.Scope{Repositories: []scope.Grant{{Repositories: []string{"it/other"}, Actions: []string{"read"}}}}),
		"inheriting": tokenScop(aliceSub, scope.Scope{Inherit: true}), "ci": ciSub,
	}
	for _, a := range levels {
		for _, w := range levels {
			if w > a {
				continue
			}
			f := newFixture().repo("r", "local", "npm", "").levels("r", a, w)
			e := newTestEngine(t, f, nil)
			for name, sub := range subjects {
				idx, err := e.Begin(t.Context(), sub, qr("r"))
				if err != nil {
					t.Fatal(err)
				}
				rs, err := e.ReadScopeOn(t.Context(), idx, sub, qname.Repo{}, []qname.Repo{qr("r")})
				if err != nil {
					t.Fatal(err)
				}
				for _, pkg := range []string{"x", "@a/y"} {
					d := e.DecideOn(t.Context(), idx, sub, Coordinate{Repository: qr("r"), Package: pkg, Kind: KindMetadata, Actions: []Action{Read}})
					switch rs.Shape(qr("r")) {
					case ReadAll:
						if !d.Allowed() {
							t.Errorf("%s on %s/%s: ReadAll and the read of %s is %+v", name, a, w, pkg, d)
						}
					case ReadNone:
						if d.Allowed() {
							t.Errorf("%s on %s/%s: ReadNone and the read of %s is allowed", name, a, w, pkg)
						}
					}
				}
			}
		}
	}
}
