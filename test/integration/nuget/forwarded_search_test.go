//go:build integration

package nuget

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/nugetstub"
)

// The forwarded search of a remote repository (R-NUGET-016) and a remote member's part of the
// merged search of a virtual repository (R-NUGET-018) against the integration stack and the
// counting test upstream: the engine resolves the upstream's service index, sends the query to its
// search resource, keeps the answer per replica for nuget.search.remote_result_ttl, and the format
// filters and rewrites it on every use. The criteria that need a real client (dotnet package
// search, Visual Studio) and the two-replica timing with Valkey stopped run in e2e.

// Repositories of the forwarded search beside the ones of newRemoteEnv.
const (
	fNoForward = "nuget-noforward" // a remote with forwardSearch false
	fExclude   = "nuget-cexcl"     // a remote with excludePatterns ["Contoso.*"]
	fAll       = "nuget-all"       // nuget-local, then nuget-org
)

// searchCounter counts the requests that reach the upstream's search resource, by q.
type searchCounter struct {
	mu  sync.Mutex
	byQ map[string]int
}

func (c *searchCounter) wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/query" {
			c.mu.Lock()
			c.byQ[r.URL.Query().Get("q")]++
			c.mu.Unlock()
		}
		h.ServeHTTP(w, r)
	})
}

// n is the number of search requests for q; total over every q.
func (c *searchCounter) n(q string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.byQ[q]
}

func (c *searchCounter) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, v := range c.byQ {
		n += v
	}
	return n
}

// fenv is newRemoteEnv with the repositories above, the claim acme.* of nuget-local, and
// packages at the upstream whose names the claim cases need.
type fenv struct {
	*renv
	searches *searchCounter
}

func newForwardEnv(t *testing.T, env ...string) *fenv {
	t.Helper()
	c := &searchCounter{byQ: map[string]int{}}
	e := &fenv{renv: newRemoteEnv(t, remoteOptions{Valkey: true, Wrap: c.wrap, Env: env}), searches: c}
	content := []any{map[string]any{"pattern": "localhost", "sendCredentials": false}}
	e.st.Repository(fNoForward, "remote", "nuget", withMember(t, e.remoteConfig(300, content, nil), "nuget", map[string]any{"forwardSearch": false}))
	e.st.Repository(fExclude, "remote", "nuget", e.remoteConfig(300, content, []string{"Contoso.*"}))
	e.st.Repository(fAll, "virtual", "nuget", `{}`)
	for i, m := range []string{rLocal, rRemote} {
		e.st.Exec(`INSERT INTO virtual_members (virtual_id, position, member_id)
			SELECT v.id, $2, m.id FROM repositories v, repositories m WHERE v.key = $1 AND m.key = $3`, fAll, i, m)
	}
	e.st.Exec(`INSERT INTO namespace_claims (repo_id, pattern, source, scope) SELECT id, 'acme.*', 'manual', 'members' FROM repositories WHERE key = $1`, rLocal)
	for _, id := range []string{"Acme.Payments", "Partner.Widgets", "Contoso.Secret"} {
		if err := e.stub.Publish(id, "1.0.0", nil); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

// withMember sets a member of a repository document.
func withMember(t *testing.T, doc, key string, value any) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(doc), &m); err != nil {
		t.Fatal(err)
	}
	m[key] = value
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// forwardedResult is the search document with the members a forwarded result keeps.
type forwardedResult struct {
	TotalHits int `json:"totalHits"`
	Data      []struct {
		Registration   string   `json:"registration"`
		ID             string   `json:"id"`
		Version        string   `json:"version"`
		Owners         []string `json:"owners"`
		Verified       *bool    `json:"verified"`
		TotalDownloads int64    `json:"totalDownloads"`
		Versions       []struct {
			ID        string `json:"@id"`
			Version   string `json:"version"`
			Downloads int64  `json:"downloads"`
		} `json:"versions"`
	} `json:"data"`
}

func (r forwardedResult) ids() []string {
	var out []string
	for _, d := range r.Data {
		out = append(out, d.ID)
	}
	return out
}

// search sends a search to a repository as the administrator and decodes the answer, which must
// be 200.
func (e *fenv) search(t *testing.T, a *testenv.Replica, repo, query string) forwardedResult {
	t.Helper()
	ans := e.st.Do(a, http.MethodGet, "/api/nuget/"+repo+"/v3/search?"+query, e.tokAdmin, nil)
	if ans.Status != http.StatusOK {
		t.Fatalf("%s search %s: %d %s", repo, query, ans.Status, ans.Body)
	}
	var r forwardedResult
	if err := json.Unmarshal(ans.Body, &r); err != nil {
		t.Fatalf("%s search %s: %v: %.300s", repo, query, err, ans.Body)
	}
	return r
}

// TestR_NUGET_016_ARemoteForwardsItsSearchAndFallsBackToItsCache: a remote addressed directly
// forwards a query to the upstream's search resource and answers with the upstream's download
// counts, owners and verified flag under its own addresses; it keeps the answer for the next
// identical query; with forwardSearch false it lists only what it has cached; a query an exclude
// pattern protects is not forwarded and a result the pattern excludes is removed with totalHits
// reduced; with the upstream down it answers 200 from its cache.
func TestR_NUGET_016_ARemoteForwardsItsSearchAndFallsBackToItsCache(t *testing.T) {
	e := newForwardEnv(t)
	a := e.start(1)[0]
	corpus := nugetstub.Default()
	newton, _ := corpus.Get(nugetstub.NewtonsoftID)

	t.Run("R-NUGET-016 a forwarded result carries the upstream's counts under the remote's addresses", func(t *testing.T) {
		r := e.search(t, a, rRemote, q("newtonsoft"))
		if !slices.Equal(r.ids(), []string{"Newtonsoft.Json"}) || r.TotalHits != 1 {
			t.Fatalf("results %v, totalHits %d", r.ids(), r.TotalHits)
		}
		d := r.Data[0]
		if want := feed(rRemote) + "registration/newtonsoft.json/index.json"; d.Registration != want {
			t.Errorf("registration %q, want %q", d.Registration, want)
		}
		var total int64
		for i := range newton.Versions {
			total += nugetstub.VersionDownloads(i)
		}
		if len(d.Versions) != 2 || d.Versions[1].Downloads != nugetstub.VersionDownloads(1) || d.TotalDownloads != total {
			t.Errorf("versions %+v, totalDownloads %d, want the upstream's %d", d.Versions, d.TotalDownloads, total)
		}
		if d.Versions[1].ID != feed(rRemote)+"registration/newtonsoft.json/13.0.3.json" {
			t.Errorf("leaf %q is not an address of the remote", d.Versions[1].ID)
		}
		if !slices.Equal(d.Owners, []string{nugetstub.SearchOwner}) || d.Verified == nil || !*d.Verified {
			t.Errorf("owners %v, verified %v", d.Owners, d.Verified)
		}
		semver2 := e.search(t, a, rRemote, q("newtonsoft")+"&semVerLevel=2.0.0")
		if want := feed(rRemote) + "registration-semver2/newtonsoft.json/index.json"; semver2.Data[0].Registration != want {
			t.Errorf("registration with semVerLevel 2.0.0: %q, want %q", semver2.Data[0].Registration, want)
		}
		before := e.searches.n("newtonsoft")
		e.search(t, a, rRemote, q("newtonsoft"))
		if got := e.searches.n("newtonsoft"); got != before {
			t.Errorf("an identical query reached the upstream again (%d, then %d)", before, got)
		}
	})

	t.Run("R-NUGET-016 with forwardSearch false the remote lists what it has cached", func(t *testing.T) {
		before := e.searches.total()
		if r := e.search(t, a, fNoForward, q("newtonsoft")); len(r.Data) != 0 || r.TotalHits != 0 {
			t.Fatalf("an empty cache lists %v", r.ids())
		}
		if pkg := e.st.Do(a, http.MethodGet, "/api/nuget/"+fNoForward+"/"+nupkgPath(nugetstub.NewtonsoftID, "13.0.3"), e.tokAdmin, nil); pkg.Status != 200 {
			t.Fatalf("restore: %d %s", pkg.Status, pkg.Body)
		}
		if r := e.search(t, a, fNoForward, q("newtonsoft")); !slices.Equal(r.ids(), []string{"Newtonsoft.Json"}) {
			t.Errorf("after a restore: %v", r.ids())
		}
		if got := e.searches.total(); got != before {
			t.Errorf("the upstream saw %d searches", got-before)
		}
	})

	t.Run("R-NUGET-016 an exclude pattern withholds its names and removes its results", func(t *testing.T) {
		for _, text := range []string{"contoso.secret", "contoso", "packageid:Contoso.Secret"} {
			e.search(t, a, fExclude, q(text))
			if n := e.searches.n(text); n != 0 {
				t.Errorf("%q was forwarded %d times", text, n)
			}
		}
		r := e.search(t, a, fExclude, q("json"))
		if e.searches.n("json") != 1 {
			t.Fatalf("an unprotected query was not forwarded")
		}
		if !slices.Equal(r.ids(), []string{"Newtonsoft.Json"}) || r.TotalHits != 1 {
			t.Errorf("results %v, totalHits %d, want Newtonsoft.Json alone and 1", r.ids(), r.TotalHits)
		}
	})

	t.Run("R-NUGET-016 with the upstream down the remote answers 200 from its cache", func(t *testing.T) {
		if pkg := e.st.Do(a, http.MethodGet, "/api/nuget/"+rRemote+"/"+nupkgPath(nugetstub.NewtonsoftID, "13.0.3"), e.tokAdmin, nil); pkg.Status != 200 {
			t.Fatalf("restore: %d %s", pkg.Status, pkg.Body)
		}
		if err := e.stub.SetMode(nugetstub.Mode{Down: nugetstub.DownUnavailable}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = e.stub.SetMode(nugetstub.Mode{}) })
		for range 2 { // the second meets the failure count or the open circuit
			r := e.search(t, a, rRemote, q("newtonsoft.json"))
			if !slices.Equal(r.ids(), []string{"Newtonsoft.Json"}) || len(r.Data[0].Owners) != 0 {
				t.Errorf("results %v, owners %v, want the cached package without upstream members", r.ids(), r.Data)
			}
		}
		if e.searches.n("newtonsoft.json") == 0 {
			t.Error("the query was not sent to the upstream at all")
		}
	})
}

// TestR_NUGET_018_AVirtualRepositoryForwardsThroughItsRemoteMember: through nuget-all, a query with
// a name nuget-local claims is answered without a search at the upstream, and an unprotected one is
// forwarded and listed after the local packages; a claim added after an answer was cached removes
// its packages on the next use on both replicas, with no new search at the upstream (R-NUGET-016,
// R-NUGET-018 steps 1, 2 and 6).
func TestR_NUGET_018_AVirtualRepositoryForwardsThroughItsRemoteMember(t *testing.T) {
	e := newForwardEnv(t, "TROVE_NUGET_SEARCH_REMOTE_RESULT_TTL=1h")
	reps := e.start(2)
	a := reps[0]
	for _, p := range []pkgSpec{{id: "Acme.Widgets", version: "1.0.0"}, {id: "Json.Tools", version: "1.0.0"}} {
		body, ct := form(t, nupkg(t, p))
		if ans := e.st.Do(a, http.MethodPut, "/api/nuget/"+rLocal+"/v3/package", e.tokAdmin, body, "Content-Type", ct); ans.Status != 201 {
			t.Fatalf("push %s: %d %s", p.id, ans.Status, ans.Body)
		}
	}

	t.Run("R-NUGET-016 a query with a claimed name is not forwarded through the virtual repository", func(t *testing.T) {
		for _, text := range []string{"acme", "acme.payments", "packageid:Acme.Widgets"} {
			r := e.search(t, a, fAll, q(text))
			if slices.Contains(r.ids(), "Acme.Payments") {
				t.Errorf("%q lists the upstream's Acme.Payments", text)
			}
		}
		if r := e.search(t, a, fAll, q("acme")); !slices.Contains(r.ids(), "Acme.Widgets") {
			t.Errorf("acme lists %v, want the local Acme.Widgets", r.ids())
		}
		if n := e.searches.total(); n != 0 {
			t.Errorf("the upstream recorded %d searches", n)
		}
		r := e.search(t, a, fAll, q("newtonsoft"))
		if e.searches.n("newtonsoft") != 1 || !slices.Contains(r.ids(), "Newtonsoft.Json") {
			t.Errorf("newtonsoft: %d searches, results %v", e.searches.n("newtonsoft"), r.ids())
		}
	})

	t.Run("R-NUGET-018 the remote member's results follow the local ones under the virtual repository's addresses", func(t *testing.T) {
		r := e.search(t, a, fAll, q("json"))
		if len(r.Data) < 2 || r.Data[0].ID != "Json.Tools" || !slices.Contains(r.ids(), "Newtonsoft.Json") {
			t.Fatalf("results %v, want Json.Tools first and Newtonsoft.Json after it", r.ids())
		}
		for _, d := range r.Data {
			if !strings.HasPrefix(d.Registration, feed(fAll)) {
				t.Errorf("%s: registration %q is not an address of the virtual repository", d.ID, d.Registration)
			}
			if d.ID == "Newtonsoft.Json" && !slices.Equal(d.Owners, []string{nugetstub.SearchOwner}) {
				t.Errorf("the remote's owners are lost in the merge: %v", d.Owners)
			}
		}
		if r.TotalHits < len(r.Data) {
			t.Errorf("totalHits %d below the %d results", r.TotalHits, len(r.Data))
		}
		if first := e.search(t, a, fAll, q("packageid:newtonsoft.json")); len(first.Data) == 0 || first.Data[0].ID != "Newtonsoft.Json" {
			t.Errorf("packageid:newtonsoft.json lists %v", first.ids())
		}
	})

	t.Run("R-NUGET-016 a claim added after a cached answer removes its packages on the next use", func(t *testing.T) {
		for _, rep := range reps {
			for _, text := range []string{"partner", "widgets"} {
				if r := e.search(t, rep, fAll, q(text)); !slices.Contains(r.ids(), "Partner.Widgets") {
					t.Fatalf("%s before the claim: %v", text, r.ids())
				}
			}
		}
		partner, widgets := e.searches.n("partner"), e.searches.n("widgets")
		cur := e.st.Do(a, http.MethodGet, "/api/v1/repositories/"+rLocal+"/claims", e.tokAdmin, nil)
		if cur.Status != 200 {
			t.Fatalf("claims: %d %s", cur.Status, cur.Body)
		}
		put := e.st.Do(a, http.MethodPut, "/api/v1/repositories/"+rLocal+"/claims", e.tokAdmin,
			map[string]any{"claims": []any{map[string]any{"pattern": "Acme.*"}, map[string]any{"pattern": "Partner.*"}}},
			"If-Match", cur.Header.Get("ETag"))
		if put.Status != 200 {
			t.Fatalf("put claims: %d %s", put.Status, put.Body)
		}
		for _, rep := range reps {
			waitFor(t, "the claim reaches "+rep.Name, func() bool {
				for _, text := range []string{"partner", "widgets"} {
					if slices.Contains(e.search(t, rep, fAll, q(text)).ids(), "Partner.Widgets") {
						return false
					}
				}
				return true
			})
		}
		if e.searches.n("partner") != partner || e.searches.n("widgets") != widgets {
			t.Errorf("the upstream saw new searches (partner %d to %d, widgets %d to %d): the cached answers were not used",
				partner, e.searches.n("partner"), widgets, e.searches.n("widgets"))
		}
	})
}

// waitFor polls a condition until it holds; the deadline only keeps a broken change from hanging
// the run and is no bound the test asserts (docs/developer/build-and-test.md).
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s: the condition did not hold", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestR_NUGET_016_AForwardedResultCarriesNoAddressOfTheUpstreamInItsAdvisories: the test upstream's
// result for Fabrikam.Legacy carries a deprecation and two advisories in nuget.org's JSON-LD form,
// one advisory a file:// address. Through the remote and through nuget-all the result keeps the
// reasons, the alternate package and the severities, keeps the https advisory as a link for people,
// and drops every @id, @type and the file:// address (Q-NUGET-85).
func TestR_NUGET_016_AForwardedResultCarriesNoAddressOfTheUpstreamInItsAdvisories(t *testing.T) {
	e := newForwardEnv(t)
	a := e.start(1)[0]
	for _, repo := range []string{rRemote, fAll} {
		ans := e.st.Do(a, http.MethodGet, "/api/nuget/"+repo+"/v3/search?"+q("packageid:fabrikam.legacy"), e.tokAdmin, nil)
		if ans.Status != http.StatusOK {
			t.Fatalf("%s: %d %s", repo, ans.Status, ans.Body)
		}
		body := string(ans.Body)
		if !strings.Contains(body, `"id":"`+nugetstub.LegacyID+`"`) {
			t.Fatalf("%s lists no %s: %s", repo, nugetstub.LegacyID, body)
		}
		for _, bad := range []string{"file://", `"@type":"deprecation"`, `"@type":"alternatePackage"`, `"@type":"Vulnerability"`, "#deprecation", "#vulnerability"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s: the result carries %q: %s", repo, bad, body)
			}
		}
		wantVuln := `"vulnerabilities":[{"advisoryUrl":"` + nugetstub.Advisory + `","severity":"3"},{"severity":"1"}]`
		wantDep := `"deprecation":{"alternatePackage":{"id":"` + nugetstub.NewtonsoftID + `","range":"[13.0.3, )"},"message":"Use Newtonsoft.Json.","reasons":["Legacy"]}`
		if !strings.Contains(body, wantVuln) || !strings.Contains(body, wantDep) {
			t.Errorf("%s: want %s and %s in %s", repo, wantDep, wantVuln, body)
		}
	}
}
