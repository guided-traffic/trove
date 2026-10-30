package virtual_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/virtual"
	vt "github.com/guided-traffic/trove/internal/virtual/virtualtest"
)

// explained returns the decisions by member key.
func explained(t *testing.T, w *world, sub authz.Subject, d virtual.Descriptor) (virtual.Explanation, map[string]string) {
	t.Helper()
	ex, err := w.r.Explain(t.Context(), w.caller(sub), w.v, d)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, m := range ex.Members {
		got[m.Key] = m.Decision
	}
	return ex, got
}

func TestR_REPO_019_Explanation(t *testing.T) {
	// npm-all lists local, remote and remote2; alice reads all of them.
	three := func(o *worldOpts) { o.members = []repository.Repository{local, remote, remote2} }

	t.Run("R-REPO-019 hit and miss for a local member, hit for a cached remote member", func(t *testing.T) {
		w := newWorld(t, three)
		w.rows.AddItem(idLocal, "left-pad/-/1.0.0.tgz")
		w.remote.CacheArtifact("npm-remote", "left-pad/-/1.0.0.tgz")
		ex, got := explained(t, w, alice, artifactOf("left-pad", "1.0.0"))
		want := map[string]string{"npm-local": "hit", "npm-remote": "hit", "npm-remote2": "miss"}
		if !mapsEqual(got, want) {
			t.Fatalf("decisions = %v, want %v", got, want)
		}
		if ex.Repository != "npm-all" || ex.UnreadableMembers != 0 || ex.Members[0].Class != "local" || ex.Members[1].Class != "remote" {
			t.Fatalf("explanation = %+v", ex)
		}
		// The spec's shape.
		b, _ := json.Marshal(ex)
		for _, want := range []string{`"repository":"npm-all"`, `"descriptor":{"path":"left-pad/-/1.0.0.tgz","kind":"artifact","package":"left-pad"}`, `"unreadableMembers":0`} {
			if !strings.Contains(string(b), want) {
				t.Errorf("%s lacks %s", b, want)
			}
		}
	})

	t.Run("R-REPO-019 the reasons of the route: disabled, not_admitted, pinned-out, claimed-elsewhere", func(t *testing.T) {
		w := newWorld(t, three)
		w.router.Dropped = map[string]string{"npm-local": "disabled", "npm-remote": "pinned-out", "npm-remote2": "claimed-elsewhere"}
		_, got := explained(t, w, alice, metadataOf("left-pad"))
		want := map[string]string{"npm-local": "disabled", "npm-remote": "pinned-out", "npm-remote2": "claimed-elsewhere"}
		if !mapsEqual(got, want) {
			t.Fatalf("decisions = %v", got)
		}
		w.router.Dropped = map[string]string{"npm-local": "not_admitted"}
		if _, got = explained(t, w, alice, metadataOf("left-pad")); got["npm-local"] != "not_admitted" {
			t.Fatalf("decisions = %v", got)
		}
	})

	t.Run("R-REPO-019 the virtual repository's own patterns or state apply to every member", func(t *testing.T) {
		w := newWorld(t, three)
		w.router.NotAdmitted = true
		if _, got := explained(t, w, alice, metadataOf("p")); got["npm-local"] != "not_admitted" || got["npm-remote2"] != "not_admitted" {
			t.Fatalf("decisions = %v", got)
		}
		w.router.NotAdmitted = false
		w.v.Disabled = true
		if _, got := explained(t, w, alice, metadataOf("p")); got["npm-local"] != "disabled" {
			t.Fatalf("decisions = %v", got)
		}
	})

	t.Run("R-REPO-019 refused-claimed for a remote member whose name a global claim protects", func(t *testing.T) {
		w := newWorld(t, three)
		w.router.Refused = map[string]bool{"npm-remote": true}
		if _, got := explained(t, w, alice, metadataOf("p")); got["npm-remote"] != "refused-claimed" || got["npm-remote2"] != "miss" {
			t.Fatalf("decisions = %v", got)
		}
	})

	t.Run("R-REPO-019 the state of a remote member that has nothing stored: negative, offline, circuit-open, miss", func(t *testing.T) {
		w := newWorld(t, three)
		w.remote.SetState("npm-remote", virtual.EntryState{Negative: true})
		w.remote.SetState("npm-remote2", virtual.EntryState{Offline: true})
		if _, got := explained(t, w, alice, metadataOf("p")); got["npm-remote"] != "negative" || got["npm-remote2"] != "offline" {
			t.Fatalf("decisions = %v", got)
		}
		w.remote.SetState("npm-remote", virtual.EntryState{CircuitOpenFor: time.Minute})
		w.remote.SetState("npm-remote2", virtual.EntryState{})
		if _, got := explained(t, w, alice, metadataOf("p")); got["npm-remote"] != "circuit-open" || got["npm-remote2"] != "miss" {
			t.Fatalf("decisions = %v", got)
		}
		// What is stored is a hit whatever else is said of the member.
		w.remote.SetDoc("npm-remote", "p", []string{"1.0.0"}, future(w, time.Hour), false)
		if _, got := explained(t, w, alice, metadataOf("p")); got["npm-remote"] != "hit" {
			t.Fatalf("decisions = %v", got)
		}
	})

	t.Run("R-REPO-019 no-cache-fill: read without the cache-fill action on a frozen mirror", func(t *testing.T) {
		w := newWorld(t, func(o *worldOpts) { o.fill = "deploy"; three(o) })
		_, got := explained(t, w, alice, artifactOf("left-pad", "1.0.0"))
		if got["npm-remote"] != "no-cache-fill" || got["npm-remote2"] != "miss" {
			t.Fatalf("decisions = %v", got)
		}
		if _, got = explained(t, w, carol, artifactOf("left-pad", "1.0.0")); got["npm-remote"] != "miss" {
			t.Fatalf("carol may fill the mirror: %v", got)
		}
		cached := artifactOf("left-pad", "1.0.0")
		w.remote.CacheArtifact("npm-remote", cached.Path)
		if _, got = explained(t, w, alice, cached); got["npm-remote"] != "hit" {
			t.Fatalf("a cached path is a hit: %v", got)
		}
	})

	t.Run("R-REPO-019 the explanation transfers nothing and contacts no upstream", func(t *testing.T) {
		w := newWorld(t, three)
		w.rows.AddItem(idLocal, "p/-/1.0.0.tgz")
		explained(t, w, alice, artifactOf("p", "1.0.0"))
		explained(t, w, alice, metadataOf("p"))
		for _, c := range w.remote.Calls() {
			if strings.HasPrefix(c, "metadata:") || strings.HasPrefix(c, "probe:") {
				t.Fatalf("the explanation made a call that can reach the upstream: %s", c)
			}
		}
		if len(w.arts.Calls()) != 0 {
			t.Fatalf("the explanation fetched: %v", w.arts.Calls())
		}
	})

	t.Run("R-REPO-019 members the caller cannot read appear only as a count", func(t *testing.T) {
		w := newWorld(t, three)
		ex, got := explained(t, w, bob, metadataOf("p")) // bob reads npm-all and npm-local
		if ex.UnreadableMembers != 2 || len(ex.Members) != 1 || got["npm-local"] == "" {
			t.Fatalf("explanation = %+v", ex)
		}
		b, _ := json.Marshal(ex)
		if strings.Contains(string(b), "npm-remote") {
			t.Fatalf("a member key leaked: %s", b)
		}
	})

	t.Run("R-REPO-019 an unreadable member that routing dropped is a count too, not a key", func(t *testing.T) {
		w := newWorld(t, three)
		w.router.Dropped = map[string]string{"npm-remote": "pinned-out"}
		ex, _ := explained(t, w, bob, metadataOf("p"))
		b, _ := json.Marshal(ex)
		if ex.UnreadableMembers != 2 || strings.Contains(string(b), "npm-remote") {
			t.Fatalf("explanation = %s", b)
		}
	})

	t.Run("R-REPO-019 a holder of repositories:read-config sees the keys of unreadable members as no-read", func(t *testing.T) {
		gs := append(grants(), vt.Grant{Principal: "user-ops", Role: "repository-admin"},
			vt.Grant{Repos: []string{"npm-all", "npm-local"}, Principal: "user-ops", Actions: []string{"read"}})
		w := newWorld(t, three, func(o *worldOpts) { o.grants = gs })
		ops := subjectFor("user-ops")
		ex, got := explained(t, w, ops, metadataOf("p"))
		if ex.UnreadableMembers != 0 || got["npm-remote"] != "no-read" || got["npm-remote2"] != "no-read" || got["npm-local"] == "no-read" {
			t.Fatalf("explanation = %+v", ex)
		}
	})

	t.Run("R-REPO-019 decidedBy names the claim and only the claimants the caller can read", func(t *testing.T) {
		w := newWorld(t, three)
		w.router.Decision = repository.RouteDecision{Kind: repository.DecidedByClaim, Pattern: "@acme/*", ClaimedBy: []string{"npm-local", "npm-remote2"}}
		ex, _ := explained(t, w, bob, metadataOf("@acme/util"))
		if ex.DecidedBy == nil || ex.DecidedBy.Claim != "@acme/*" || !slices.Equal(ex.DecidedBy.ClaimedBy, []string{"npm-local"}) {
			t.Fatalf("decidedBy = %+v", ex.DecidedBy)
		}
		ex, _ = explained(t, w, alice, metadataOf("@acme/util"))
		if !slices.Equal(ex.DecidedBy.ClaimedBy, []string{"npm-local", "npm-remote2"}) {
			t.Fatalf("decidedBy = %+v", ex.DecidedBy)
		}
		w.router.Decision = repository.RouteDecision{Kind: repository.DecidedByPin, Pattern: "lodash"}
		if ex, _ = explained(t, w, alice, metadataOf("lodash")); ex.DecidedBy == nil || ex.DecidedBy.Pin != "lodash" || ex.DecidedBy.Claim != "" {
			t.Fatalf("decidedBy = %+v", ex.DecidedBy)
		}
		w.router.Decision = repository.RouteDecision{}
		if ex, _ = explained(t, w, alice, metadataOf("lodash")); ex.DecidedBy != nil {
			t.Fatalf("decidedBy = %+v", ex.DecidedBy)
		}
	})
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
