//go:build integration

package ociremote

import (
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The listing of the tags a remote repository holds (R-OCI-036, Q-OCI-215): the list API and the version list read the cache entries of the
// repository, through the same statements and the same read scope as a local repository's versions.

// listing is the packages of a repository as a caller sees them, by name.
func (e *renv) listing(r *testenv.Replica, token, repo string) map[string]map[string]any {
	e.t.Helper()
	a := e.st.Do(r, "GET", "/api/v1/packages?limit=100&repository="+url.QueryEscape(testenv.DefaultProject+"/"+repo), token, nil)
	if a.Status != 200 {
		e.t.Fatalf("list %s: %d %s", repo, a.Status, a.Body)
	}
	var items []any
	if v, ok := a.JSON(e.t)["items"].([]any); ok {
		items = v
	}
	out := map[string]map[string]any{}
	for _, it := range items {
		m := it.(map[string]any)
		out[m["name"].(string)] = m
	}
	return out
}

// tags is the version list of a package in a repository: the tag, its digest and its size, newest fetch first.
func (e *renv) tags(r *testenv.Replica, token, id, repo string) []map[string]any {
	e.t.Helper()
	a := e.st.Do(r, "GET", "/api/v1/packages/"+id+"/versions?include=digest&repository="+url.QueryEscape(testenv.DefaultProject+"/"+repo), token, nil)
	if a.Status != 200 {
		e.t.Fatalf("versions of %s in %s: %d %s", id, repo, a.Status, a.Body)
	}
	var out []map[string]any
	for _, it := range a.JSON(e.t)["items"].([]any) {
		out = append(out, it.(map[string]any))
	}
	return out
}

func tagNames(vs []map[string]any) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, v["version"].(string))
	}
	slices.Sort(out)
	return out
}

func keysOf(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// narrowed grants read on a repository for the image names matching one include pattern.
func (e *renv) narrowed(name, repo string, who testenv.Person, include string) {
	e.t.Helper()
	id := e.st.Text("INSERT INTO authz_policy (id, name) VALUES (gen_random_uuid(), $1) RETURNING id::text", name)
	e.st.Exec("INSERT INTO authz_policy_selector (policy_id, ordinal, kind, repository_id) SELECT $1::uuid, 0, 'key', id FROM repositories WHERE key = $2", id, repo)
	e.st.Exec("INSERT INTO authz_policy_pattern (policy_id, dimension, mode, pattern) VALUES ($1::uuid, 'package', 'include', $2)", id, include)
	e.st.Exec("INSERT INTO authz_policy_entry (policy_id, principal_id, actions) VALUES ($1::uuid, $2::uuid, ARRAY['read'])", id, who.ID)
}

func TestR_OCI_036_ARemoteListsTheImagesAndTagsItHasCachedAndOnlyToWhoMayReadThem(t *testing.T) {
	e := newEnv(t, options{})
	narrow, outsider := e.st.User("acme-only"), e.st.User("outsider")
	e.narrowed("acme-only", rAnon, narrow, "acme/*")
	e.st.Policy("elsewhere", []string{"read"}, []string{rBasic}, outsider)
	narrowTok, outsiderTok := e.st.Token(narrow, scope.Default()), e.st.Token(outsider, scope.Default())
	rep := e.start(1)[0]

	if got := e.listing(rep, e.reader, rAnon); len(got) != 0 {
		t.Fatalf("a remote that cached nothing lists %v", keysOf(got))
	}

	alpine := e.pull(rep, rAnon, "library/alpine", "3.19")
	e.pull(rep, rAnon, "acme/a", "latest")
	e.pull(rep, rAnon, "acme/a", "1.0")

	got := e.listing(rep, e.reader, rAnon)
	if names := keysOf(got); !slices.Equal(names, []string{"acme/a", "library/alpine"}) {
		t.Fatalf("the reader lists %v, want the two pulled images", names)
	}
	if n := got["library/alpine"]["versionCount"].(float64); n != 1 {
		t.Errorf("library/alpine counts %v tags, want 1", n)
	}
	if n := got["acme/a"]["versionCount"].(float64); n != 2 {
		t.Errorf("acme/a counts %v tags, want 2", n)
	}

	alpineID := got["library/alpine"]["id"].(string)
	vs := e.tags(rep, e.reader, alpineID, rAnon)
	if len(vs) != 1 || vs[0]["version"] != "3.19" {
		t.Fatalf("the tags of library/alpine: %v", vs)
	}
	if vs[0]["digest"] != alpine {
		t.Errorf("the tag lists the digest %v, the pull answered %s", vs[0]["digest"], alpine)
	}
	if size, _ := vs[0]["sizeBytes"].(float64); size <= 0 {
		t.Errorf("the tag lists the size %v, want the summed size of the image", vs[0]["sizeBytes"])
	}
	if at, _ := vs[0]["publishedAt"].(string); at == "" {
		t.Error("the tag lists no fetch time")
	}
	if n, _ := vs[0]["downloadCount"].(float64); n != 0 {
		t.Errorf("downloadCount = %v, want 0 for a tag of a remote", n)
	}

	// A caller whose grant names the image names acme/* lists those and no others; the image it may not read answers like one that does not exist.
	if names := keysOf(e.listing(rep, narrowTok, rAnon)); !slices.Equal(names, []string{"acme/a"}) {
		t.Errorf("the caller narrowed to acme/* lists %v", names)
	}
	hidden := e.st.Do(rep, "GET", "/api/v1/packages/"+alpineID, narrowTok, nil)
	if hidden.Status != 404 {
		t.Errorf("an image outside the pattern answers %d %s, want the 404 of a missing one", hidden.Status, hidden.Body)
	}
	if a := e.st.Do(rep, "GET", "/api/v1/packages/"+alpineID+"/versions/3.19", narrowTok, nil); a.Status != 404 {
		t.Errorf("a tag outside the pattern answers %d %s", a.Status, a.Body)
	}
	if names := keysOf(e.listing(rep, narrowTok, rAnon)); slices.Contains(names, "library/alpine") {
		t.Error("the search leaks an image outside the pattern")
	}
	if a := e.st.Do(rep, "GET", "/api/v1/packages?q=alpine&repository="+url.QueryEscape(testenv.DefaultProject+"/"+rAnon), narrowTok, nil); strings.Contains(string(a.Body), "library/alpine") {
		t.Errorf("a search of the narrowed caller finds the hidden image: %s", a.Body)
	}

	// A caller with no grant on the repository lists nothing.
	if names := keysOf(e.listing(rep, outsiderTok, rAnon)); len(names) != 0 {
		t.Errorf("a caller without a grant lists %v", names)
	}
	if a := e.st.Do(rep, "GET", "/api/v1/packages/"+alpineID+"/versions", outsiderTok, nil); a.Status != 404 {
		t.Errorf("the tags of an image the caller may not read answer %d", a.Status)
	}
}

func TestR_OCI_036_AMovedTagShowsTheNewDigestAndNoSecondVersion(t *testing.T) {
	e := newEnv(t, options{})
	rep := e.start(1)[0]
	first := e.pull(rep, rFast, "acme/a", "latest")
	list := e.listing(rep, e.reader, rFast)
	id := list["acme/a"]["id"].(string)
	before := e.tags(rep, e.reader, id, rFast)
	if len(before) != 1 || before[0]["digest"] != first {
		t.Fatalf("before the move: %v, want the digest %s", before, first)
	}

	moved, err := e.stub.MoveTag("acme/a", "latest")
	if err != nil {
		t.Fatal(err)
	}
	if moved == first {
		t.Fatal("the stub did not move the tag")
	}
	// The cache still holds the old document until a request revalidates the tag after its period: the list says what the cache holds.
	if now := e.tags(rep, e.reader, id, rFast); now[0]["digest"] != first {
		t.Fatalf("the list shows %v before any request revalidated the tag, want the cached digest %s", now[0]["digest"], first)
	}
	eventually(t, 20*time.Second, "the moved tag to be fetched and listed", func() bool {
		if a := e.get(rep, rFast, "acme/a/manifests/latest"); a.Status != 200 {
			return false
		}
		vs := e.tags(rep, e.reader, id, rFast)
		return len(vs) == 1 && vs[0]["digest"] == moved
	})
	after := e.tags(rep, e.reader, id, rFast)
	if len(after) != 1 {
		t.Fatalf("a moved tag lists %d versions, want the one tag", len(after))
	}
	if at, was := after[0]["publishedAt"].(string), before[0]["publishedAt"].(string); at <= was {
		t.Errorf("the moved tag is listed with %s, not later than %s: the time is that of the fetch that stored the new document", at, was)
	}
	if n := e.listing(rep, e.reader, rFast)["acme/a"]["versionCount"].(float64); n != 1 {
		t.Errorf("acme/a counts %v tags after the move", n)
	}
}

func TestR_OCI_036_AGoneOrEvictedTagIsNotListed(t *testing.T) {
	e := newEnv(t, options{})
	rep := e.start(1)[0]
	for _, tag := range []string{"latest", "1.0"} {
		e.pull(rep, rAnon, "acme/a", tag)
	}
	e.pull(rep, rAnon, "library/alpine", "3.19")
	id := e.listing(rep, e.reader, rAnon)["acme/a"]["id"].(string)
	if got := tagNames(e.tags(rep, e.reader, id, rAnon)); !slices.Equal(got, []string{"1.0", "latest"}) {
		t.Fatalf("the tags before: %v", got)
	}

	// The upstream answered 404 for the tag at revalidation: its document is marked gone and the remote address no longer serves it.
	e.st.Exec(`UPDATE remote_cache_entries c SET upstream_gone_at = now() FROM repositories r
		WHERE r.id = c.repo_id AND r.key = $1 AND c.path = 'acme/a/_tags/1.0'`, rAnon)
	if got := tagNames(e.tags(rep, e.reader, id, rAnon)); !slices.Equal(got, []string{"latest"}) {
		t.Errorf("the tags after the gone mark: %v, want latest only", got)
	}

	// The entry of the image's last tag is evicted: the image is not listed any more, though its manifest row stays.
	e.st.Exec(`DELETE FROM remote_cache_entries c USING repositories r WHERE r.id = c.repo_id AND r.key = $1 AND c.path = 'library/alpine/_tags/3.19'`, rAnon)
	got := e.listing(rep, e.reader, rAnon)
	if names := keysOf(got); !slices.Equal(names, []string{"acme/a"}) {
		t.Errorf("the images after the eviction: %v", names)
	}
	if n := got["acme/a"]["versionCount"].(float64); n != 1 {
		t.Errorf("acme/a counts %v tags after the gone mark, want 1", n)
	}
	if e.manifestRows(rAnon, "library/alpine") == 0 {
		t.Error("the manifest row went with the entry: the test did not evict only the tag document")
	}
}
