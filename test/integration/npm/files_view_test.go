//go:build integration

package npm

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The raw view of an npm repository: /files/ and the items API decide an npm item on the
// coordinate the format gives its path (R-EP-014, R-AUTHZ-005), apply the repository's name
// patterns to its package (R-REPO-005), ask the ownership question for every package whose
// files a delete removes (R-AUTHZ-018), and take no deploy (R-STOR-007).

// problemCode is the code of a problem body (R-API-009), the error shape of /files/ and the
// management API.
func problemCode(t *testing.T, a testenv.Answer) string {
	t.Helper()
	var p struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(a.Body, &p); err != nil || p.Code == "" {
		t.Fatalf("answer %d is no problem body: %v\n%s", a.Status, err, a.Body)
	}
	return p.Code
}

// sameMissing reports whether a hidden answer is the answer of a missing resource: the
// same status, headers that matter and problem body, but for the members that name the
// request (its instance and its request ID).
func sameMissing(t *testing.T, hidden, missing testenv.Answer) bool {
	t.Helper()
	strip := func(a testenv.Answer) string {
		var p map[string]any
		if err := json.Unmarshal(a.Body, &p); err != nil {
			t.Fatalf("answer %d is no problem body: %s", a.Status, a.Body)
		}
		delete(p, "instance")
		delete(p, "requestId")
		raw, _ := json.Marshal(p)
		return a.Header.Get("Content-Type") + "|" + string(raw)
	}
	return hidden.Status == http.StatusNotFound && missing.Status == http.StatusNotFound && strip(hidden) == strip(missing)
}

// listed are the names of the entries of a listing: the JSON directory index of /files/
// and a page of the children operation share the shape.
func listed(t *testing.T, a testenv.Answer) []string {
	t.Helper()
	if a.Status != http.StatusOK {
		t.Fatalf("listing: %d %s", a.Status, a.Body)
	}
	var page struct {
		Items []struct {
			Path string `json:"path"`
		} `json:"items"`
	}
	if err := json.Unmarshal(a.Body, &page); err != nil {
		t.Fatalf("listing: %v\n%s", err, a.Body)
	}
	out := make([]string, 0, len(page.Items))
	for _, it := range page.Items {
		out = append(out, it.Path[strings.LastIndex(it.Path, "/")+1:])
	}
	slices.Sort(out)
	return out
}

// excludeOnly grants actions on a repository for every package but those an exclude
// pattern names.
func (e *env) excludeOnly(name, repo string, who testenv.Person, exclude string, actions ...string) {
	e.t.Helper()
	id := e.st.Text("INSERT INTO authz_policy (id, name) VALUES (gen_random_uuid(), $1) RETURNING id::text", name)
	e.st.Exec("INSERT INTO authz_policy_selector (policy_id, ordinal, kind, repository_id) SELECT $1::uuid, 0, 'key', id FROM repositories WHERE key = $2", id, repo)
	e.st.Exec("INSERT INTO authz_policy_pattern (policy_id, dimension, mode, pattern) VALUES ($1::uuid, 'package', 'exclude', $2)", id, exclude)
	e.st.Exec("INSERT INTO authz_policy_entry (policy_id, principal_id, actions) VALUES ($1::uuid, $2::uuid, $3)", id, who.ID, actions)
}

// TestR_AUTHZ_005_FilesDecideNpmItemsOnTheirPackage: a grant that excludes a scope keeps
// that scope's tarballs out of /files/ and the items API, for a download, a listing and a
// delete, while it reaches every other package (R-AUTHZ-005, R-EP-014, Q-AUTHZ-160).
func TestR_AUTHZ_005_FilesDecideNpmItemsOnTheirPackage(t *testing.T) {
	e := newEnv(t)
	dave := e.st.User("dave")
	e.excludeOnly("no-secrets", "npm-local", dave, "@secret/*", "read", "delete")
	tokD := e.st.Token(dave, scope.Default())
	a := e.replica("web-a")
	e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "@secret/x", Version: "1.0.0"})
	e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "@acme/ui", Version: "1.0.0"})
	get := func(path string) testenv.Answer {
		return e.st.Do(a, "GET", path, tokD, nil, "Accept", "application/json")
	}

	t.Run("R-AUTHZ-005 R-EP-014 the excluded package's tarball is the 404 of a missing one; another package's is served", func(t *testing.T) {
		hidden, missing := get("/files/npm-local/@secret/x/-/x-1.0.0.tgz"), get("/files/npm-local/@secret/x/-/x-9.9.9.tgz")
		if !sameMissing(t, hidden, missing) {
			t.Fatalf("hidden %d %s, missing %d %s", hidden.Status, hidden.Body, missing.Status, missing.Body)
		}
		if ans := get("/files/npm-local/@acme/ui/-/ui-1.0.0.tgz"); ans.Status != 200 {
			t.Fatalf("@acme/ui: %d %s", ans.Status, ans.Body)
		}
		if ans := get("/api/v1/repositories/npm-local/items?path=@secret/x/-/x-1.0.0.tgz"); ans.Status != 404 {
			t.Fatalf("items API: %d %s", ans.Status, ans.Body)
		}
		if ans := get("/api/v1/repositories/npm-local/items?path=@acme/ui/-/ui-1.0.0.tgz"); ans.Status != 200 {
			t.Fatalf("items API @acme/ui: %d %s", ans.Status, ans.Body)
		}
	})
	t.Run("R-STOR-017 R-EP-014 the excluded package's folder is not listed, another package's is", func(t *testing.T) {
		for _, path := range []string{"/files/npm-local/@secret/x/-/", "/api/v1/repositories/npm-local/children?path=@secret/x/-"} {
			if ans := get(path); ans.Status != 404 {
				t.Fatalf("%s: %d %s", path, ans.Status, ans.Body)
			}
		}
		for _, path := range []string{"/files/npm-local/@acme/ui/-/", "/api/v1/repositories/npm-local/children?path=@acme/ui/-"} {
			if got := listed(t, get(path)); !slices.Equal(got, []string{"ui-1.0.0.tgz"}) {
				t.Fatalf("%s lists %v", path, got)
			}
		}
		got := listed(t, get("/api/v1/repositories/npm-local/children?recursive=true&folders=false"))
		if !slices.Equal(got, []string{"ui-1.0.0.tgz"}) {
			t.Fatalf("the recursive listing holds %v", got)
		}
	})
	t.Run("Q-AUTHZ-160 a path the format places in no package, a scope folder, is closed to a grant that narrows names", func(t *testing.T) {
		if ans := get("/files/npm-local/@acme/"); ans.Status != 404 {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
		if ans := e.st.Do(a, "GET", "/files/npm-local/@acme/", e.tokC, nil, "Accept", "application/json"); ans.Status != 200 {
			t.Fatalf("a grant without patterns lists the scope: %d %s", ans.Status, ans.Body)
		}
	})
	t.Run("R-STOR-018 R-AUTHZ-005 the excluded package cannot be deleted through either path", func(t *testing.T) {
		for _, path := range []string{"/files/npm-local/@secret/x/-/x-1.0.0.tgz", "/api/v1/repositories/npm-local/items?path=@secret/x/-/x-1.0.0.tgz", "/files/npm-local/@secret/"} {
			ans := e.st.Do(a, "DELETE", path, tokD, nil)
			if ans.Status != 403 || problemCode(t, ans) != "forbidden" {
				t.Fatalf("%s: %d %s", path, ans.Status, ans.Body)
			}
		}
		if e.versions("npm-local", "@secret/x") != 1 || e.itemCount("npm-local", "@secret/x/-/x-1.0.0.tgz") != 1 {
			t.Fatal("the excluded package lost its version")
		}
	})
}

// TestR_AUTHZ_018_RawRemovalsAskOwnership: a folder delete through /files/ and a delete
// through the items API ask the ownership question for every package whose files they
// remove, and refuse the whole removal when one package is another owner's (R-AUTHZ-018,
// R-NPM-017, Q-AUTHZ-161).
func TestR_AUTHZ_018_RawRemovalsAskOwnership(t *testing.T) {
	e := newEnv(t)
	ops := e.st.User("ops")
	e.st.Bind("admin", ops)
	tokOps := e.st.Token(ops, scope.Scope{Inherit: true, Admin: true})
	a := e.replica("web-a")
	e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "@team/x", Version: "1.0.0"})
	e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "@team/y", Version: "1.0.0"})
	e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "@mixed/a", Version: "1.0.0"})
	e.mustPublish(a, "npm-local", e.tokB, pkg{Name: "@mixed/b", Version: "1.0.0"})

	t.Run("R-AUTHZ-018 a folder that holds another owner's package is refused whole", func(t *testing.T) {
		for _, path := range []string{"/files/npm-local/@mixed/", "/api/v1/repositories/npm-local/items?path=@mixed"} {
			ans := e.st.Do(a, "DELETE", path, e.tokB, nil)
			if ans.Status != 403 || problemCode(t, ans) != "package_owned_by_other_owner" {
				t.Fatalf("%s: %d %s", path, ans.Status, ans.Body)
			}
		}
		if e.versions("npm-local", "@mixed/a") != 1 || e.versions("npm-local", "@mixed/b") != 1 || e.tombstones("npm-local", "@mixed/b") != 0 {
			t.Fatal("a refused folder delete removed a version")
		}
	})
	t.Run("R-NPM-017 the owner of every package of a folder removes it, and each version is unpublished", func(t *testing.T) {
		if ans := e.st.Do(a, "DELETE", "/files/npm-local/@team/", e.tokA, nil); ans.Status != 204 {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
		for _, n := range []string{"@team/x", "@team/y"} {
			if e.versions("npm-local", n) != 0 || e.tombstones("npm-local", n) != 1 {
				t.Fatalf("%s: versions %d, tombstones %d", n, e.versions("npm-local", n), e.tombstones("npm-local", n))
			}
		}
	})
	t.Run("R-AUTHZ-018 content:bypass passes the question", func(t *testing.T) {
		if ans := e.st.Do(a, "DELETE", "/files/npm-local/@mixed/", tokOps, nil); ans.Status != 204 {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
		if e.versions("npm-local", "@mixed/a")+e.versions("npm-local", "@mixed/b") != 0 {
			t.Fatal("the folder was not removed")
		}
	})
}

// TestR_STOR_018_ARawRemovalAboveTheSyncLimitAsksOwnershipBeforeItsJob: a subtree removed
// by the job items-delete is asked about in the transaction that queues the job, so a
// refusal queues nothing, and the job the owner queues unpublishes every version
// (R-STOR-018, R-AUTHZ-018, Q-AUTHZ-161).
func TestR_STOR_018_ARawRemovalAboveTheSyncLimitAsksOwnershipBeforeItsJob(t *testing.T) {
	e := newEnv(t)
	web := e.replica("web-a", "TROVE_STORAGE_DELETE_MAX_SYNC_ITEMS=2")
	e.st.Start(t, testenv.ReplicaOptions{Name: "worker-a", Role: "worker"})
	e.mustPublish(web, "npm-local", e.tokA, pkg{Name: "@big/p", Version: "1.0.0"})
	e.mustPublish(web, "npm-local", e.tokA, pkg{Name: "@big/p", Version: "1.1.0"})
	jobs := func() int64 { return e.st.Number(`SELECT count(*) FROM job WHERE kind = 'items-delete'`) }

	t.Run("R-AUTHZ-018 a deleter who does not own the package is refused and no job is queued", func(t *testing.T) {
		ans := e.st.Do(web, "DELETE", "/files/npm-local/@big/p/", e.tokB, nil)
		if ans.Status != 403 || problemCode(t, ans) != "package_owned_by_other_owner" || jobs() != 0 {
			t.Fatalf("%d %s, jobs %d", ans.Status, ans.Body, jobs())
		}
	})
	t.Run("R-STOR-018 the owner's delete is a job that unpublishes every version", func(t *testing.T) {
		ans := e.st.Do(web, "DELETE", "/files/npm-local/@big/p/", e.tokA, nil)
		if ans.Status != 202 || jobs() != 1 {
			t.Fatalf("%d %s, jobs %d", ans.Status, ans.Body, jobs())
		}
		testenv.WaitFor(t, "the job", 30*time.Second, func() bool {
			return e.st.Number(`SELECT count(*) FROM job WHERE kind = 'items-delete' AND state = 'succeeded'`) == 1
		})
		if e.versions("npm-local", "@big/p") != 0 || e.tombstones("npm-local", "@big/p") != 2 {
			t.Fatalf("versions %d, tombstones %d", e.versions("npm-local", "@big/p"), e.tombstones("npm-local", "@big/p"))
		}
	})
}

// TestR_REPO_005_NameExcludeOfAnNpmRepositoryUnderFiles: a name pattern of the repository
// applies to the package a raw path belongs to, so an excluded package is a missing one
// under /files/ (R-REPO-005), and the items API deletes it for content:bypass only, as it
// does in a disabled repository (Q-AUTHZ-162).
func TestR_REPO_005_NameExcludeOfAnNpmRepositoryUnderFiles(t *testing.T) {
	e := newEnv(t)
	ops := e.st.User("ops")
	e.st.Bind("admin", ops)
	tokOps := e.st.Token(ops, scope.Scope{Inherit: true, Admin: true})
	a := e.replica("web-a")
	e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "@hidden/x", Version: "1.0.0"})
	e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "@hidden/y", Version: "1.0.0"})
	e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "@shown/x", Version: "1.0.0"})
	patch := func(body string) {
		t.Helper()
		g := e.st.Do(a, "GET", "/api/v1/repositories/npm-local", tokOps, nil)
		ans := e.st.Do(a, "PATCH", "/api/v1/repositories/npm-local", tokOps, []byte(body),
			"Content-Type", "application/merge-patch+json", "If-Match", g.Header.Get("ETag"))
		if ans.Status != 200 {
			t.Fatalf("patch: %d %s", ans.Status, ans.Body)
		}
	}
	patch(`{"excludePatterns":["@hidden/*"]}`)
	testenv.WaitFor(t, "the pattern", 10*time.Second, func() bool {
		return e.st.Do(a, "GET", "/files/npm-local/@hidden/x/-/x-1.0.0.tgz", e.tokA, nil).Status == 404
	})

	t.Run("R-REPO-005 the excluded package is a missing one under /files/, another package is served", func(t *testing.T) {
		for _, path := range []string{"/files/npm-local/@hidden/x/-/x-1.0.0.tgz", "/files/npm-local/@hidden/x/-/"} {
			if ans := e.st.Do(a, "GET", path, e.tokA, nil, "Accept", "application/json"); ans.Status != 404 {
				t.Fatalf("%s: %d %s", path, ans.Status, ans.Body)
			}
		}
		if ans := e.st.Do(a, "GET", "/files/npm-local/@shown/x/-/x-1.0.0.tgz", e.tokA, nil); ans.Status != 200 {
			t.Fatalf("@shown/x: %d %s", ans.Status, ans.Body)
		}
		if got := listed(t, e.st.Do(a, "GET", "/files/npm-local/@hidden/", e.tokA, nil, "Accept", "application/json")); len(got) != 0 {
			t.Fatalf("the scope lists %v", got)
		}
	})
	t.Run("R-REPO-005 Q-AUTHZ-162 the items API deletes an excluded package for content:bypass only", func(t *testing.T) {
		path := "/api/v1/repositories/npm-local/items?path=@hidden/x/-/x-1.0.0.tgz"
		if ans := e.st.Do(a, "GET", path, e.tokA, nil); ans.Status != 200 {
			t.Fatalf("the items API shows the item: %d %s", ans.Status, ans.Body)
		}
		for _, p := range []string{path, "/files/npm-local/@hidden/x/-/x-1.0.0.tgz"} {
			if ans := e.st.Do(a, "DELETE", p, e.tokA, nil); ans.Status != 404 {
				t.Fatalf("the owner without content:bypass, %s: %d %s", p, ans.Status, ans.Body)
			}
		}
		if ans := e.st.Do(a, "DELETE", path, tokOps, nil); ans.Status != 204 {
			t.Fatalf("content:bypass: %d %s", ans.Status, ans.Body)
		}
		if e.versions("npm-local", "@hidden/x") != 0 || e.tombstones("npm-local", "@hidden/x") != 1 {
			t.Fatal("the cleanup did not unpublish the version")
		}
	})
	t.Run("Q-AUTHZ-162 a disabled repository's items are deleted for content:bypass only", func(t *testing.T) {
		patch(`{"disabled":true}`)
		path := "/api/v1/repositories/npm-local/items?path=@hidden/y/-/y-1.0.0.tgz"
		testenv.WaitFor(t, "the disabled flag", 10*time.Second, func() bool {
			ans := e.st.Do(a, "DELETE", path, e.tokA, nil)
			return ans.Status == 404 && strings.Contains(string(ans.Body), "repository_disabled")
		})
		if ans := e.st.Do(a, "DELETE", path, tokOps, nil); ans.Status != 204 {
			t.Fatalf("content:bypass: %d %s", ans.Status, ans.Body)
		}
	})
}

// TestR_STOR_007_NoRawDeployIntoAnNpmRepository: /files/ takes no file and no folder into an
// npm repository, before the body is read (R-STOR-007, R-API-025, Q-ARCH-223): a folder at a
// version's tarball path would block the owner's publish of that version.
func TestR_STOR_007_NoRawDeployIntoAnNpmRepository(t *testing.T) {
	e := newEnv(t)
	a := e.replica("web-a")
	for _, path := range []string{"/files/npm-local/@acme/ui/-/ui-2.0.0.tgz/", "/files/npm-local/@acme/ui/-/ui-2.0.0.tgz", "/files/npm-local/readme.txt"} {
		ans, err := rawPutTo(a.Addr("default"), path, e.tokB, strings.NewReader(""), 0, false)
		if err != nil {
			t.Fatal(err)
		}
		if ans.Status != 409 || problemCode(t, ans) != "conflict" || !strings.Contains(string(ans.Body), "/api/npm/npm-local/") {
			t.Fatalf("%s: %d %s", path, ans.Status, ans.Body)
		}
	}
	t.Run("R-STOR-007 a refused upload sends no 100 Continue and reads no body", func(t *testing.T) {
		ans := rawPut(t, a, "/files/npm-local/@acme/ui/-/ui-2.0.0.tgz", e.tokB, strings.NewReader(strings.Repeat("x", 1<<20)), 1<<20, true)
		if ans.Status != 409 {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
	})
	if n := e.st.Number(`SELECT count(*) FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = 'npm-local'`); n != 0 {
		t.Fatalf("%d items stored", n)
	}
	e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "@acme/ui", Version: "2.0.0"})
}

// TestR_EP_014_FilesOnAnEndpointWithRules: on an endpoint whose surface holds files and
// api, an npm tarball under /files/ and an item of the items API pass the ceiling with the
// package and the version the format gives the path, so a version the rule hides is a
// missing file and is not listed (R-EP-014, R-EP-012, Q-EP-59).
func TestR_EP_014_FilesOnAnEndpointWithRules(t *testing.T) {
	endpoints := `{"customers":{"listen":"127.0.0.1:0","base_url":"https://` + customersHost + `","surface":["packages","files","api"]}}`
	x := newExposureEnvOn(t, 1, endpoints)
	r := x.reps[0]
	on := func(path string) testenv.Answer {
		return x.st.DoAt(r.Addr("customers"), customersHost, "GET", path, x.acmeSAT, nil, "Trove-Authz-Min-Epoch", x.epoch, "Accept", "application/json")
	}
	t.Run("R-EP-014 an exposed version's tarball is served; a hidden one is the 404 of a missing file", func(t *testing.T) {
		if ans := on("/files/npm-customers/@acme/sdk/-/sdk-2.1.0.tgz"); ans.Status != 200 {
			t.Fatalf("2.1.0: %d %s", ans.Status, ans.Body)
		}
		missing := on("/files/npm-customers/@acme/sdk/-/sdk-9.9.9.tgz")
		for _, v := range []string{"1.9.0", "2.0.1", "2.1.0-rc.1"} {
			ans := on("/files/npm-customers/@acme/sdk/-/sdk-" + v + ".tgz")
			if !sameMissing(t, ans, missing) {
				t.Fatalf("%s: %d %s", v, ans.Status, ans.Body)
			}
		}
		for _, path := range []string{"/files/npm-customers/@acme/billing/-/billing-3.0.0.tgz", "/files/npm-customers/@acme/sdk-internal/-/sdk-internal-1.0.0.tgz"} {
			if ans := on(path); ans.Status != 404 {
				t.Fatalf("%s: %d %s", path, ans.Status, ans.Body)
			}
		}
	})
	t.Run("R-EP-014 a listing holds the exposed versions only", func(t *testing.T) {
		want := []string{"sdk-2.0.0.tgz", "sdk-2.1.0.tgz"}
		for _, path := range []string{"/files/npm-customers/@acme/sdk/-/", "/api/v1/repositories/npm-customers/children?path=@acme/sdk/-"} {
			if got := listed(t, on(path)); !slices.Equal(got, want) {
				t.Fatalf("%s lists %v, want %v", path, got, want)
			}
		}
	})
	t.Run("R-EP-014 the items API reads an exposed version and hides another", func(t *testing.T) {
		if ans := on("/api/v1/repositories/npm-customers/items?path=@acme/sdk/-/sdk-2.0.0.tgz"); ans.Status != 200 {
			t.Fatalf("2.0.0: %d %s", ans.Status, ans.Body)
		}
		if ans := on("/api/v1/repositories/npm-customers/items?path=@acme/sdk/-/sdk-1.9.0.tgz"); ans.Status != 404 {
			t.Fatalf("1.9.0: %d %s", ans.Status, ans.Body)
		}
	})
}
