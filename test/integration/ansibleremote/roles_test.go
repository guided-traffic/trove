//go:build integration

package ansibleremote

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/ansiblestub"
	"github.com/guided-traffic/trove/test/ansiblestub/ansiblegen"
)

func roleNames(t *testing.T, doc map[string]any) []string {
	t.Helper()
	var out []string
	for _, v := range list(doc["results"]) {
		out = append(out, obj(v)["name"].(string))
	}
	return out
}

func TestR_ANS_020_ARoleIsInstalledByTranslationFromTheGitHubArchive(t *testing.T) {
	e := newEnv(t, options{})
	r := e.start(1)[0]
	repo := e.fresh()

	look := e.get(r, repo, "v1/roles/?owner__username=geerlingguy&name=pip")
	if look.Status != http.StatusOK {
		t.Fatalf("lookup: %d %s", look.Status, look.Body)
	}
	ld := look.JSON(t)
	role := obj(list(ld["results"])[0])
	if role["id"] != float64(16) || role["github_user"] != "geerlingguy" || role["url"] != baseURL+base(repo)+"v1/roles/16/" {
		t.Errorf("role = %v", role)
	}
	// Only the owner and the name pass to the upstream: another parameter changes nothing and costs no request.
	e.get(r, repo, "v1/roles/?owner__username=geerlingguy&name=pip&page_size=1000&foo=bar")
	if got := e.stub.Counts().Query(http.MethodGet, "/api/v1/roles/", "owner__username=geerlingguy&name=pip"); got != 1 {
		t.Errorf("the stub saw the lookup %d times, want 1", got)
	}

	vs := e.get(r, repo, "v1/roles/16/versions/?page_size=50")
	if vs.Status != http.StatusOK {
		t.Fatalf("versions: %d %s", vs.Status, vs.Body)
	}
	vd := vs.JSON(t)
	if got := strings.Join(roleNames(t, vd), " "); got != "1.0.0 1.1.0 2.0.0" {
		t.Fatalf("versions = %s", got)
	}
	var url string
	for _, v := range list(vd["results"]) {
		if obj(v)["name"] == "2.0.0" {
			url = obj(v)["download_url"].(string)
		}
		if d := obj(v)["download_url"].(string); !strings.HasPrefix(d, baseURL+base(repo)+"v1/roles/archives/16/") {
			t.Errorf("download_url %q is not on the repository", d)
		}
	}

	// The archive comes from the GitHub stand-in through its redirect, once, whatever the number of installs.
	want, err := ansiblegen.Role(ansiblegen.RoleSpec{User: "geerlingguy", Repo: "ansible-role-pip", Tag: "2.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		a := e.get(r, repo, e.follow(repo, url))
		if a.Status != http.StatusOK || !bytes.Equal(a.Body, want) {
			t.Fatalf("role archive: %d, %d bytes (want %d)", a.Status, len(a.Body), len(want))
		}
	}
	c := e.stub.Counts()
	if github, code := c.Requests(http.MethodGet, "/geerlingguy/ansible-role-pip/archive/2.0.0.tar.gz"), c.Requests(http.MethodGet, "/geerlingguy/ansible-role-pip/tar.gz/2.0.0"); github != 1 || code != 1 {
		t.Errorf("two installs made %d requests to the GitHub stand-in and %d to its redirect target, want 1 and 1", github, code)
	}
	if n := e.st.Number(`SELECT count(*) FROM items WHERE repo_id = $1 AND path = 'roles/16/2.0.0.tar.gz' AND trashed_at IS NULL`, e.repoID(repo)); n != 1 {
		t.Errorf("%d items for the role archive, want 1", n)
	}
}

func TestR_ANS_020_ARolesVersionListIsPagedAndAVersionOnAnUnlistedHostIsDropped(t *testing.T) {
	e := newEnv(t, options{})
	r := e.start(1)[0]
	repo := e.fresh()

	// 60 tags are two pages of 50, joined by next_link on the repository's address.
	if id := obj(list(e.get(r, repo, "v1/roles/?owner__username=many&name=tags").JSON(t)["results"])[0])["id"]; id != float64(18) {
		t.Fatalf("role id = %v", id)
	}
	p1 := e.get(r, repo, "v1/roles/18/versions/?page_size=50").JSON(t)
	next, _ := p1["next_link"].(string)
	if len(list(p1["results"])) != 50 || !strings.HasPrefix(next, baseURL+base(repo)+"v1/roles/18/versions/?") {
		t.Fatalf("first page: %d results, next_link %q", len(list(p1["results"])), next)
	}
	p2 := e.get(r, repo, e.follow(repo, next)).JSON(t)
	if len(list(p2["results"])) != 10 || p2["next_link"] != nil {
		t.Errorf("second page: %d results, next_link %v", len(list(p2["results"])), p2["next_link"])
	}

	// acme.rolex has a version on a host the repository does not list: it is not offered and not fetched, and the metric counts it.
	e.get(r, repo, "v1/roles/?owner__username=acme&name=rolex")
	vd := e.get(r, repo, "v1/roles/17/versions/?page_size=50").JSON(t)
	if got := strings.Join(roleNames(t, vd), " "); got != "1.0.0" {
		t.Errorf("versions of a role with a version on an unlisted host = %q, want 1.0.0 alone", got)
	}
	if a := e.get(r, repo, "v1/roles/archives/17/0.9.0.tar.gz"); a.Status != http.StatusNotFound {
		t.Errorf("the archive of the dropped version: %d", a.Status)
	}
	if n := e.stub.Counts().OnHost(ansiblestub.DefaultUnlistedHost); n != 0 {
		t.Errorf("the stub's unlisted host saw %d requests", n)
	}
	eventually(t, 10*time.Second, "the dropped-versions metric", func() bool {
		return strings.Contains(r.Metrics(t), `trove_ansible_role_versions_dropped_total{repo="`+project+`/`+repo+`"} 1`)
	})

	// A repository whose extra hosts do not hold the GitHub hosts offers no version of a GitHub role.
	e.get(r, rNoRoles, "v1/roles/?owner__username=geerlingguy&name=pip")
	vd = e.get(r, rNoRoles, "v1/roles/16/versions/?page_size=50").JSON(t)
	if len(list(vd["results"])) != 0 {
		t.Errorf("versions offered without a GitHub host: %v", roleNames(t, vd))
	}
}

func TestR_ANS_020_ALookupNeedsAnOwnerAndAName(t *testing.T) {
	e := newEnv(t, options{})
	r := e.start(1)[0]
	repo := e.fresh()
	for _, q := range []string{"", "?name=pip", "?owner__username=geerlingguy", "?owner__username=a/b&name=pip", "?owner__username=x&name=" + strings.Repeat("n", 65)} {
		a := e.get(r, repo, "v1/roles/"+q)
		if a.Status != http.StatusBadRequest || a.JSON(t)["default"] == nil {
			t.Errorf("lookup %q: %d %s", q, a.Status, a.Body)
		}
	}
	if a := e.get(r, repo, "v1/roles/archives/16/bad%20ref.tar.gz"); a.Status != http.StatusBadRequest {
		t.Errorf("a version name with a space: %d", a.Status)
	}
	if got := e.stub.Counts().Total(); got != 0 {
		t.Errorf("malformed requests made %d requests to the stub", got)
	}
}

func TestR_ANS_020_ARoleArchiveAnnouncesNoDigestAndARequiringRepositoryRefusesIt(t *testing.T) {
	e := newEnv(t, options{})
	r := e.start(1)[0]
	e.get(r, rRequire, "v1/roles/?owner__username=geerlingguy&name=pip")
	e.get(r, rRequire, "v1/roles/16/versions/")
	a := e.get(r, rRequire, "v1/roles/archives/16/2.0.0.tar.gz")
	if a.Status != http.StatusBadGateway || !strings.Contains(string(a.Body), "digest") {
		t.Errorf("checksumPolicy require: %d %s", a.Status, a.Body)
	}
}

func TestR_ANS_006_TheCredentialsOfTheRoutes(t *testing.T) {
	e := newEnv(t, options{})
	r := e.start(1)[0]
	repo := e.fresh()
	const doc = "v3/collections/acme/tools/versions/2.0.0/"
	e.get(r, repo, "v1/roles/?owner__username=geerlingguy&name=pip")
	e.get(r, repo, "v1/roles/16/versions/")
	const archive = "v1/roles/archives/16/2.0.0.tar.gz"

	// No credential: 401 with the challenge a client that waits for one answers.
	for _, p := range []string{"", doc, archive} {
		a := e.as(r, "", repo, p)
		if a.Status != http.StatusUnauthorized || !strings.HasPrefix(a.Header.Get("WWW-Authenticate"), "Basic") {
			t.Errorf("%q without a credential: %d %q", p, a.Status, a.Header.Get("WWW-Authenticate"))
		}
	}
	// The scheme Token of the Galaxy clients, Bearer and Basic with the token as password (what ~/.netrc does) are the same token.
	for name, auth := range map[string]string{"Token": "Token " + e.reader, "Bearer": "Bearer " + e.reader, "Basic": basic(e.reader)} {
		for _, p := range []string{doc, archive} {
			if a := e.as(r, auth, repo, p); a.Status != http.StatusOK {
				t.Errorf("%s on %s: %d %s", name, p, a.Status, a.Body)
			}
		}
	}
	if a := e.as(r, "Token not-a-token", repo, doc); a.Status != http.StatusUnauthorized {
		t.Errorf("a token nobody issued: %d", a.Status)
	}
}
