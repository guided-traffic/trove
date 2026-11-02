//go:build integration

package nuget

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The include and exclude patterns of R-REPO-005 in the NuGet search (Q-NUGET-77), against
// PostgreSQL and repositories as the snapshot compiles them:
//
//	nuget-pat:   local, excludes Acme.Secret*
//	nuget-plain: local, no patterns
//	nuget-pv:    virtual over nuget-pat and nuget-plain, excludes Acme.Hidden*
//
// whole reads everything (the store would count and page); rows reads everything but
// acme.other (a package exclude in its grant, so every version is decided row by row).

type patternEnv struct {
	t           *testing.T
	st          *testenv.Stack
	r           *testenv.Replica
	whole, rows string
}

func newPatternEnv(t *testing.T) *patternEnv {
	t.Helper()
	st := testenv.NewStack(t, testenv.StackOptions{Name: "nugetpat"})
	e := &patternEnv{t: t, st: st}
	whole, rows := st.User("whole"), st.User("rows")
	e.whole, e.rows = st.Token(whole, scope.Default()), st.Token(rows, scope.Default())
	st.Repository("nuget-pat", "local", "nuget", `{"excludePatterns":["Acme.Secret*"]}`)
	st.Repository("nuget-plain", "local", "nuget", "{}")
	st.Repository("nuget-pv", "virtual", "nuget", `{"excludePatterns":["Acme.Hidden*"]}`)
	for i, m := range []string{"nuget-pat", "nuget-plain"} {
		st.Exec(`INSERT INTO virtual_members (virtual_id, position, member_id)
			SELECT v.id, $2, m.id FROM repositories v, repositories m WHERE v.key = 'nuget-pv' AND m.key = $1`, m, i)
	}
	all := []string{"nuget-pat", "nuget-plain", "nuget-pv"}
	st.Policy("pat-whole", []string{"read"}, all, whole)
	st.Policy("pat-rows", []string{"read"}, all, rows)
	st.Exec(`INSERT INTO authz_policy_pattern (policy_id, dimension, mode, pattern) SELECT id, 'package', 'exclude', 'acme.other' FROM authz_policy WHERE name = 'pat-rows'`)
	insert(st, "nuget-pat", seedPackage{display: "Acme.Widgets", title: "Widgets", description: "Visible widgets", versions: vs("1.0.0")})
	insert(st, "nuget-pat", seedPackage{display: "Acme.Secret.Thing", title: "Secret", description: "Withdrawn widgets", versions: vs("1.0.0", "2.0.0")})
	insert(st, "nuget-plain", seedPackage{display: "Acme.Hidden.Tool", title: "Hidden", description: "Kept out of the virtual widgets", versions: vs("1.0.0")})
	insert(st, "nuget-plain", seedPackage{display: "Acme.Secret.Thing", title: "Secret elsewhere", description: "Admitted widgets", versions: vs("3.0.0")})
	e.r = st.Start(t, testenv.ReplicaOptions{Name: "web-pat"})
	return e
}

func (e *patternEnv) search(repo, token, query string) found {
	e.t.Helper()
	a := e.st.Do(e.r, "GET", "/api/nuget/"+repo+"/v3/search?"+query, token, nil)
	if a.Status != 200 {
		e.t.Fatalf("%s search?%s: %d %s", repo, query, a.Status, a.Body)
	}
	var f found
	if err := json.Unmarshal(a.Body, &f); err != nil {
		e.t.Fatalf("%v: %s", err, a.Body)
	}
	return f
}

// TestR_REPO_005_TheNuGetSearchOfALocalRepositoryAppliesItsPatterns: a package the repository's
// exclude pattern keeps out of its documents is not listed and not counted, for a caller who reads
// the repository whole and for one whose reads are decided row by row; the same ID in a repository
// without the pattern is listed (Q-NUGET-77).
func TestR_REPO_005_TheNuGetSearchOfALocalRepositoryAppliesItsPatterns(t *testing.T) {
	e := newPatternEnv(t)
	for name, tok := range map[string]string{"whole": e.whole, "row by row": e.rows} {
		t.Run(name, func(t *testing.T) {
			for _, query := range []string{q("acme"), q("secret"), q("packageid:acme.secret.thing"), ""} {
				got := e.search("nuget-pat", tok, query)
				if slices.Contains(got.ids(), "Acme.Secret.Thing") {
					t.Fatalf("search?%s lists the excluded package: %v (%d)", query, got.ids(), got.TotalHits)
				}
				if want := len(got.ids()); got.TotalHits != want {
					t.Fatalf("search?%s counts %d for %d results", query, got.TotalHits, want)
				}
			}
			if got := e.search("nuget-pat", tok, q("acme")); !slices.Equal(got.ids(), []string{"Acme.Widgets"}) {
				t.Fatalf("q=acme: %v", got.ids())
			}
			if a := e.st.Do(e.r, "GET", "/api/nuget/nuget-pat/v3/flatcontainer/acme.secret.thing/index.json", tok, nil); a.Status != 404 {
				t.Fatalf("the documents of the excluded package: %d", a.Status)
			}
			if got := e.search("nuget-plain", tok, q("secret")); !slices.Equal(got.ids(), []string{"Acme.Secret.Thing"}) {
				t.Fatalf("a repository without the pattern: %v", got.ids())
			}
		})
	}
}

// TestR_REPO_005_TheNuGetSearchOfAVirtualRepositoryAppliesItsAndItsMembersPatterns: through a
// virtual repository, a member contributes only the IDs its own patterns and the virtual
// repository's admit; an ID one member's pattern excludes still comes from a member that admits
// it, with that member's versions alone (Q-NUGET-77).
func TestR_REPO_005_TheNuGetSearchOfAVirtualRepositoryAppliesItsAndItsMembersPatterns(t *testing.T) {
	e := newPatternEnv(t)
	for name, tok := range map[string]string{"whole": e.whole, "row by row": e.rows} {
		t.Run(name, func(t *testing.T) {
			got := e.search("nuget-pv", tok, q("acme"))
			if want := []string{"Acme.Widgets", "Acme.Secret.Thing"}; !slices.Equal(got.ids(), want) || got.TotalHits != len(want) {
				t.Fatalf("q=acme through nuget-pv: %v (%d), want %v", got.ids(), got.TotalHits, want)
			}
			if v := got.Data[1].versions(); !slices.Equal(v, []string{"3.0.0"}) {
				t.Fatalf("Acme.Secret.Thing through nuget-pv shows %v, want the admitting member's 3.0.0 alone", v)
			}
			for _, query := range []string{q("hidden"), q("packageid:acme.hidden.tool")} {
				if got := e.search("nuget-pv", tok, query); got.TotalHits != 0 || len(got.Data) != 0 {
					t.Fatalf("search?%s through nuget-pv: %v (%d)", query, got.ids(), got.TotalHits)
				}
			}
			body := e.st.Do(e.r, "GET", "/api/nuget/nuget-pv/v3/search?"+q("acme")+"&take=50", tok, nil).Body
			if strings.Contains(string(body), "2.0.0") || strings.Contains(string(body), "Hidden") {
				t.Fatalf("the merged search names what the patterns keep out: %s", body)
			}
			if got := e.search("nuget-plain", tok, q("hidden")); !slices.Equal(got.ids(), []string{"Acme.Hidden.Tool"}) {
				t.Fatalf("the member searched on its own: %v", got.ids())
			}
		})
	}
}
