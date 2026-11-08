//go:build integration

package oci

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_AUTHZ_007_OCIActionMap proves the OCI table of R-AUTHZ-007 row by row against two replicas: for each client operation the
// caller who holds the row's action is served, the caller who lacks it is refused by the decision point, the token service grants
// the row's token scope action to the first and withholds it from the second, and a name owned by another owner is refused for
// the rows that mark ownership. People and their grants (global policies on the local and the other repository):
//
//	reader  read              deployonly  deploy                 pusher  read, deploy
//	mover   read, deploy, overwrite       deleter     read, delete   nobody  nothing
func TestR_AUTHZ_007_OCIActionMap(t *testing.T) {
	p := newProto(t, protoOptions{replicas: 2})
	img := newImage("map")
	// The shared state of the rows: an image of the name map/app owned by pusher, a second image to move a tag to, a blob in oci-other.
	moved := newImage("map-moved")
	want(t, p.pushImage(p.as("pusher"), repoLocal, "map/app", "1", img), 201, "")
	for _, b := range append([][]byte{moved.config}, moved.layers...) {
		want(t, p.uploadBlob(p.next(), p.as("pusher"), repoLocal, "map/app", b), 201, "")
	}
	base := []byte("action map base layer")
	want(t, p.uploadBlob(p.next(), p.as("pusher"), repoOther, "map/base", base), 201, "")

	granted := func(who, sc string, bit int) bool {
		t.Helper()
		g := grantOf(t, p.registryToken(p.next(), who, sc))
		for _, v := range g {
			if v&bit != 0 {
				return true
			}
		}
		return false
	}
	name := project + "/" + repoLocal + "/map/app"
	pullScope, pushScope, delScope := scopeOf(repoLocal, "map/app", "pull"), scopeOf(repoLocal, "map/app", "push"), scopeOf(repoLocal, "map/app", "delete")
	refused := func(t *testing.T, a testenv.Answer, what string) {
		t.Helper()
		if a.Status != http.StatusForbidden && a.Status != http.StatusNotFound {
			t.Fatalf("%s: %d %s", what, a.Status, a.Body)
		}
	}

	t.Run("version check", func(t *testing.T) {
		want(t, p.do(call{rep: 0, method: "GET", path: "/v2/"}), 401, "UNAUTHORIZED")
		for _, who := range []string{"nobody", "reader"} {
			if a := p.as1(who, "GET", "/v2/", nil); a.Status != 200 || string(a.Body) != "{}" {
				t.Fatalf("%s: %d %s", who, a.Status, a.Body)
			}
		}
	})
	t.Run("catalog", func(t *testing.T) {
		list := func(who string) (testenv.Answer, int) {
			a := p.do(call{rep: p.next(), method: "GET", path: "/v2/_catalog", cred: p.as(who)})
			return a, len(a.Body)
		}
		if a, _ := list("reader"); a.Status != 200 || !contains(catalogNames(t, a), name) {
			t.Fatalf("a caller with read: %d %s", a.Status, a.Body)
		}
		if a, _ := list("nobody"); a.Status != 200 || len(catalogNames(t, a)) != 0 {
			t.Fatalf("a caller without read lists %s", a.Body)
		}
		// The token scope action of the row is registry:catalog:*, granted by the coarse check of read.
		tok := p.registryToken(0, "reader", "registry:catalog:*")
		if a := p.do(call{rep: 1, method: "GET", path: "/v2/_catalog", cred: bearer(tok)}); a.Status != 200 || !contains(catalogNames(t, a), name) {
			t.Fatalf("a registry token with the catalog scope: %d %s", a.Status, a.Body)
		}
		noCatalog := p.registryToken(0, "reader", pullScope)
		if a := p.do(call{rep: 1, method: "GET", path: "/v2/_catalog", cred: bearer(noCatalog)}); a.Status != 401 {
			t.Fatalf("a registry token without the catalog scope: %d", a.Status)
		}
	})
	t.Run("pull: manifest and blob GET and HEAD, tags list, referrers", func(t *testing.T) {
		reads := map[string][2]string{
			"manifest GET":  {"GET", "/manifests/1"},
			"manifest HEAD": {"HEAD", "/manifests/1"},
			"blob GET":      {"GET", "/blobs/" + digestOf(img.config)},
			"blob HEAD":     {"HEAD", "/blobs/" + digestOf(img.config)},
			"tags list":     {"GET", "/tags/list"},
			"referrers":     {"GET", "/referrers/" + img.digest()},
		}
		for what, r := range reads {
			want(t, p.as1("reader", r[0], repo(repoLocal, "map/app")+r[1], nil), 200, "")
			refused(t, p.as1("nobody", r[0], repo(repoLocal, "map/app")+r[1], nil), what+" without read")
			refused(t, p.as1("deployonly", r[0], repo(repoLocal, "map/app")+r[1], nil), what+" with deploy only")
		}
		if !granted("reader", pullScope, pull) || granted("nobody", pullScope, pull) || granted("deployonly", pullScope, pull) {
			t.Fatal("the token service does not grant pull exactly to the caller with read")
		}
	})
	t.Run("upload: start, continue, finish, query, cancel", func(t *testing.T) {
		chunk := []byte("chunk of the action map")
		start := func(who string) testenv.Answer {
			return p.as1(who, "POST", repo(repoLocal, "map/app")+"/blobs/uploads/", nil)
		}
		refused(t, start("reader"), "start with read only")
		refused(t, start("nobody"), "start without a grant")
		a := start("deployonly")
		want(t, a, 202, "")
		loc := hdr(a, "Location")[len("http://"+testenv.DefaultHost):]
		for _, who := range []string{"reader", "nobody"} {
			refused(t, p.as1(who, "GET", loc, nil), "query by "+who)
			refused(t, p.as1(who, "PATCH", loc, chunk, "Content-Type", octet), "continue by "+who)
			refused(t, p.as1(who, "PUT", withDigest(loc, digestOf(chunk)), nil), "finish by "+who)
			refused(t, p.as1(who, "DELETE", loc, nil), "cancel by "+who)
		}
		// The session belongs to its starter: the pusher, who holds deploy as well, cannot continue it (another principal is a 404).
		if b := p.as1("pusher", "PATCH", loc, chunk, "Content-Type", octet); b.Status != 404 {
			t.Fatalf("another principal's session: %d %s", b.Status, b.Body)
		}
		if q := p.as1("deployonly", "GET", loc, nil); q.Status != http.StatusNoContent {
			t.Fatalf("query by the starter: %d %s", q.Status, q.Body)
		}
		b := p.as1("deployonly", "PATCH", loc, chunk, "Content-Type", octet)
		want(t, b, 202, "")
		loc = hdr(b, "Location")[len("http://"+testenv.DefaultHost):]
		want(t, p.as1("deployonly", "PUT", withDigest(loc, digestOf(chunk)), nil), 201, "")
		c := start("deployonly")
		want(t, c, 202, "")
		want(t, p.as1("deployonly", "DELETE", hdr(c, "Location")[len("http://"+testenv.DefaultHost):], nil), 204, "")
		if !granted("deployonly", pushScope, push) || !granted("pusher", pushScope, push) || granted("reader", pushScope, push) || granted("nobody", pushScope, push) {
			t.Fatal("the token service does not grant push exactly to the caller with deploy")
		}
	})
	t.Run("mount: deploy on the target, read on the source", func(t *testing.T) {
		d := digestOf(base)
		mount := func(who, to string) testenv.Answer {
			return p.as1(who, "POST", repo(repoLocal, to)+"/blobs/uploads/?mount="+d+"&from="+project+"/"+repoOther+"/map/base", nil)
		}
		want(t, mount("pusher", "map/mounted"), 201, "")
		want(t, mount("deployonly", "map/second"), 202, "") // no read on the source: an ordinary session
		refused(t, mount("reader", "map/third"), "a mount with read only")
		refused(t, mount("nobody", "map/third"), "a mount without a grant")
		// A registry token for the mount asks push on the target and pull on the source.
		tok := p.registryToken(0, "pusher", scopeOf(repoLocal, "map/fourth", "push"), scopeOf(repoOther, "map/base", "pull"))
		g := grantOf(t, tok)
		if g[project+"/"+repoLocal+"/map/fourth"] != push || g[project+"/"+repoOther+"/map/base"] != pull {
			t.Fatalf("grant of the mount token: %v", g)
		}
		tok = p.registryToken(0, "deployonly", scopeOf(repoLocal, "map/fifth", "push"), scopeOf(repoOther, "map/base", "pull"))
		g = grantOf(t, tok)
		if g[project+"/"+repoLocal+"/map/fifth"] != push || g[project+"/"+repoOther+"/map/base"] != 0 {
			t.Fatalf("the source's pull is granted to a caller without read: %v", g)
		}
	})
	t.Run("manifest push: a new tag, a digest, the same digest", func(t *testing.T) {
		fresh := newImage("map-fresh")
		for _, b := range append([][]byte{fresh.config}, fresh.layers...) {
			want(t, p.uploadBlob(p.next(), p.as("deployonly"), repoLocal, "map/new", b), 201, "")
		}
		want(t, p.putManifest(p.next(), p.as("deployonly"), repoLocal, "map/new", "1", manifestType, fresh.manifest), 201, "")
		want(t, p.putManifest(p.next(), p.as("deployonly"), repoLocal, "map/new", "1", manifestType, fresh.manifest), 201, "")
		want(t, p.putManifest(p.next(), p.as("deployonly"), repoLocal, "map/new", fresh.digest(), manifestType, fresh.manifest), 201, "")
		refused(t, p.putManifest(p.next(), p.as("reader"), repoLocal, "map/new", "2", manifestType, fresh.manifest), "a push with read only")
		refused(t, p.putManifest(p.next(), p.as("nobody"), repoLocal, "map/new", "2", manifestType, fresh.manifest), "a push without a grant")
	})
	t.Run("manifest push under a tag that points at another digest: deploy and overwrite", func(t *testing.T) {
		want(t, p.putManifest(p.next(), p.as("deployonly"), repoLocal, "map/app", "1", manifestType, moved.manifest), 403, "DENIED")
		if got := p.as1("reader", "GET", repo(repoLocal, "map/app")+"/manifests/1", nil); hdr(got, "Docker-Content-Digest") != img.digest() {
			t.Fatal("the tag moved without overwrite")
		}
		want(t, p.putManifest(p.next(), p.as("mover"), repoLocal, "map/app", "1", manifestType, moved.manifest), 201, "")
		if got := p.as1("reader", "GET", repo(repoLocal, "map/app")+"/manifests/1", nil); hdr(got, "Docker-Content-Digest") != moved.digest() {
			t.Fatal("the tag did not move")
		}
		// The token scope action is push for both; overwrite is decided again on the request.
		if !granted("mover", pushScope, push) {
			t.Fatal("push is not granted to a caller with deploy and overwrite")
		}
	})
	t.Run("delete: a tag, a manifest, a blob", func(t *testing.T) {
		del := func(who, path string) testenv.Answer {
			return p.as1(who, "DELETE", repo(repoLocal, "map/app")+path, nil)
		}
		for _, path := range []string{"/manifests/1", "/manifests/" + moved.digest(), "/blobs/" + digestOf(moved.config)} {
			want(t, del("pusher", path), 403, "DENIED")
			refused(t, del("reader", path), "delete by read only")
			refused(t, del("nobody", path), "delete without a grant")
		}
		want(t, del("deleter", "/manifests/1"), 202, "")
		want(t, p.as1("reader", "GET", repo(repoLocal, "map/app")+"/manifests/1", nil), 404, "MANIFEST_UNKNOWN")
		want(t, del("deleter", "/manifests/"+moved.digest()), 202, "")
		want(t, del("deleter", "/blobs/"+digestOf(moved.config)), 202, "")
		if !granted("deleter", delScope, drop) || granted("pusher", delScope, drop) || granted("reader", delScope, drop) {
			t.Fatal("the token service does not grant delete exactly to the caller with delete")
		}
	})
	t.Run("ownership: a name owned by another owner is refused for the rows that mark it", func(t *testing.T) {
		// In oci-owned (packageOwnership enforce) map/owned belongs to pusher by its first push; mover holds deploy and overwrite on the
		// repository and is no owner of the name.
		owned := newImage("map-owned")
		want(t, p.pushImage(p.as("pusher"), repoOwned, "map/owned", "1", owned), 201, "")
		ownedBy := func(what string, a testenv.Answer) {
			t.Helper()
			if a.Status != http.StatusForbidden || errorCode(a) != "DENIED" || !strings.Contains(string(a.Body), "owned by another owner") {
				t.Fatalf("%s: %d %s", what, a.Status, a.Body)
			}
		}
		ownedBy("a blob upload", p.uploadBlob(p.next(), p.as("mover"), repoOwned, "map/owned", []byte("x")))
		ownedBy("an upload start", p.as1("mover", "POST", repo(repoOwned, "map/owned")+"/blobs/uploads/", nil))
		ownedBy("a manifest push", p.putManifest(p.next(), p.as("mover"), repoOwned, "map/owned", "2", manifestType, owned.manifest))
		mountFrom := "&from=" + project + "/" + repoOther + "/map/base"
		ownedBy("a mount into the name", p.as1("mover", "POST", repo(repoOwned, "map/owned")+"/blobs/uploads/?mount="+digestOf(base)+mountFrom, nil))
		ownedBy("a delete", p.as1("deleter", "DELETE", repo(repoOwned, "map/owned")+"/manifests/1", nil))
		// The owner is not refused, and another name in the repository is open to the second caller.
		want(t, p.as1("pusher", "POST", repo(repoOwned, "map/owned")+"/blobs/uploads/", nil), 202, "")
		want(t, p.as1("mover", "POST", repo(repoOwned, "map/free")+"/blobs/uploads/", nil), 202, "")
	})
}

func catalogNames(t *testing.T, a testenv.Answer) []string {
	t.Helper()
	var b struct {
		Repositories []string `json:"repositories"`
	}
	if err := json.Unmarshal(a.Body, &b); err != nil {
		t.Fatalf("catalog body %q: %v", a.Body, err)
	}
	return b.Repositories
}
