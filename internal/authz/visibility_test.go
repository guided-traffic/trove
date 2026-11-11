package authz

import (
	"testing"

	"github.com/guided-traffic/trove/internal/auth/principal"
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
