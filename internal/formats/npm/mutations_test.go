package npm

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
)

// TestR_NPM_015_TarballDownload: the file name names the version, the item at the
// canonical path is served with the headers of an immutable file, and a name that does
// not match or a version that is not canonical is 404.
func TestR_NPM_015_TarballDownload(t *testing.T) {
	rf := newRepo(t)
	rf.publish("@acme/ui", "1.2.0")
	want := rf.get("@acme%2fui/-/ui-1.2.0.tgz")
	if want.Code != 200 || want.Body.Len() == 0 {
		t.Fatalf("%d %s", want.Code, want.Body.String())
	}
	t.Run("both spellings of the scoped name serve the same file", func(t *testing.T) {
		got := rf.get("@acme/ui/-/ui-1.2.0.tgz")
		if got.Code != 200 || !bytesEqual(got.Body.Bytes(), want.Body.Bytes()) || got.Header().Get("ETag") != want.Header().Get("ETag") {
			t.Fatalf("%d etag %q", got.Code, got.Header().Get("ETag"))
		}
	})
	t.Run("the response is immutable and private, and nothing is public", func(t *testing.T) {
		if cc := want.Header().Get("Cache-Control"); cc != "private, max-age=31536000, immutable" {
			t.Fatalf("Cache-Control %q", cc)
		}
		if want.Header().Get("ETag") == "" {
			t.Fatal("no ETag")
		}
	})
	t.Run("HEAD answers the headers", func(t *testing.T) {
		r := rf.do(http.MethodHead, "@acme%2fui/-/ui-1.2.0.tgz", "")
		if r.Code != 200 || r.Body.Len() != 0 || r.Header().Get("Content-Length") == "" {
			t.Fatalf("%d %d", r.Code, r.Body.Len())
		}
	})
	t.Run("a file name that is not <basename>-<version>.tgz of this package is 404", func(t *testing.T) {
		for _, p := range []string{
			"@acme%2fui/-/other-1.2.0.tgz", "@acme%2fui/-/ui-1.2.tgz", "@acme%2fui/-/ui-v1.2.0.tgz", "@acme%2fui/-/ui-1.2.0+build.tgz",
			"@acme%2fui/-/ui-1.2.0.tar.gz", "@acme%2fui/-/@acme-ui-1.2.0.tgz", "@acme%2fui/-/ui-1.3.0.tgz",
		} {
			if r := rf.get(p); r.Code != 404 {
				t.Errorf("%s: %d", p, r.Code)
			}
		}
	})
	t.Run("R-AUTHZ-016 a package the caller may not read is the 404 of a missing one", func(t *testing.T) {
		rf := newRepo(t, func(o *formattest.HostOptions) {
			o.Allow = func(tg formats.Target) error {
				if tg.Package == "@acme/ui" {
					return authz.NotFound()
				}
				return nil
			}
		})
		hidden := rf.get("@acme%2fui/-/ui-1.2.0.tgz")
		missing := rf.get("lodash/-/lodash-1.0.0.tgz")
		if hidden.Code != 404 || hidden.Body.String() != missing.Body.String() {
			t.Fatalf("%d %q / %d %q", hidden.Code, hidden.Body.String(), missing.Code, missing.Body.String())
		}
	})
}

func bytesEqual(a, b []byte) bool { return string(a) == string(b) }

// TestR_NPM_047_ServeCheckRefusesTombstonedAndWithheldVersions: a version with a
// tombstone is not served by the tarball route nor by /files (ServeCheck), though its
// item and its bytes are stored, which is the state of a version a restore's replay
// withheld.
func TestR_NPM_047_ServeCheckRefusesTombstonedAndWithheldVersions(t *testing.T) {
	rf := newRepo(t)
	rf.publish("lodash", "4.17.20")
	rf.publish("lodash", "4.17.21")
	ctx := context.Background()
	itemOf := func(version string) formats.ItemRef {
		it, ok, err := rf.h.Items().Stat(ctx, rf.h.Ref(), formats.Path{Path: "lodash/-/lodash-" + version + ".tgz"})
		if err != nil || !ok {
			t.Fatalf("item of %s: %v %v", version, ok, err)
		}
		return it.Ref
	}
	if err := rf.f.ServeCheck(ctx, rf.h, rf.h.Ref(), itemOf("4.17.20"), formats.ServeStored); err != nil {
		t.Fatalf("a live version is refused: %v", err)
	}
	// the replay's state: stored, and tombstoned with source 'journal'
	rf.st.MarkTombstoned(rf.h.Ref().ID, "lodash", "4.17.20")
	for _, mode := range []formats.ServeMode{formats.ServeStored, formats.ServeMayRevalidate} {
		err := rf.f.ServeCheck(ctx, rf.h, rf.h.Ref(), itemOf("4.17.20"), mode)
		if e, ok := errs.As(err); !ok || e.Kind != errs.NotFound {
			t.Fatalf("mode %d: %v", mode, err)
		}
	}
	if err := rf.f.ServeCheck(ctx, rf.h, rf.h.Ref(), itemOf("4.17.21"), formats.ServeStored); err != nil {
		t.Fatalf("another version of the package is refused: %v", err)
	}
	t.Run("the tarball route answers 404 and the item stays stored", func(t *testing.T) {
		if r := rf.get("lodash/-/lodash-4.17.20.tgz"); r.Code != 404 {
			t.Fatalf("%d", r.Code)
		}
		if r := rf.get("lodash/-/lodash-4.17.21.tgz"); r.Code != 200 {
			t.Fatalf("%d", r.Code)
		}
		if _, ok, _ := rf.h.Items().Stat(ctx, rf.h.Ref(), formats.Path{Path: "lodash/-/lodash-4.17.20.tgz"}); !ok {
			t.Fatal("the item was removed")
		}
	})
	t.Run("the document omits the version and latest is not left on it", func(t *testing.T) {
		// the replay applies the tag rule when it tombstones; the document honours the tombstone
		doc := jsonOf(t, rf.get("lodash"))
		if vs := versionsOf(doc); len(vs) != 1 || vs[0] != "4.17.21" {
			t.Fatalf("versions %v", vs)
		}
	})
	t.Run("the check does not apply to what is not a file of a version", func(t *testing.T) {
		for _, p := range []string{"lodash", "-/ping", "lodash/-/readme.txt"} {
			if err := rf.f.ServeCheck(ctx, rf.h, rf.h.Ref(), formats.ItemRef{Path: p}, formats.ServeStored); err != nil {
				t.Errorf("%s: %v", p, err)
			}
		}
	})
	t.Run("a bundle of a withheld version is refused as well", func(t *testing.T) {
		err := rf.f.ServeCheck(ctx, rf.h, rf.h.Ref(), formats.ItemRef{Path: "lodash/-/lodash-4.17.20.sigstore"}, formats.ServeStored)
		if e, ok := errs.As(err); !ok || e.Kind != errs.NotFound {
			t.Fatalf("%v", err)
		}
	})
}

// TestR_NPM_016_DistTags: the read, the writes and their refusals.
func TestR_NPM_016_DistTags(t *testing.T) {
	rf := newRepo(t)
	rf.publish("@acme/ui", "1.0.0")
	rf.publish("@acme/ui", "1.1.0")
	rf.publishTag("@acme/ui", "2.0.0-rc.1", "next")
	tagsOf := func() map[string]any {
		r := rf.get("-/package/@acme%2fui/dist-tags")
		if r.Code != 200 {
			t.Fatalf("%d %s", r.Code, r.Body.String())
		}
		return jsonOf(t, r)
	}
	if tags := tagsOf(); tags["latest"] != "1.1.0" || tags["next"] != "2.0.0-rc.1" || len(tags) != 2 {
		t.Fatalf("tags %v", tags)
	}
	put := func(tag, body string) *formattest.Response {
		return rf.do(http.MethodPut, "-/package/@acme%2fui/dist-tags/"+tag, body)
	}
	t.Run("a tag is set to a live version and the revision rises", func(t *testing.T) {
		before := rf.rev("@acme/ui")
		if r := put("stable", `"1.0.0"`); r.Code != 200 || jsonOf(t, r)["ok"] != true {
			t.Fatalf("%d %s", r.Code, r.Body.String())
		}
		if tagsOf()["stable"] != "1.0.0" || rf.rev("@acme/ui") == before {
			t.Fatalf("tags %v", tagsOf())
		}
		if got := rf.st.TagChanges; len(got) == 0 || got[len(got)-1] != "set stable" {
			t.Fatalf("changes %v", got)
		}
	})
	t.Run("a request that changes nothing raises nothing", func(t *testing.T) {
		before := rf.rev("@acme/ui")
		if r := put("stable", `"1.0.0"`); r.Code != 200 || rf.rev("@acme/ui") != before {
			t.Fatalf("%d, rev %s -> %s", r.Code, before, rf.rev("@acme/ui"))
		}
	})
	t.Run("latest moves to any live version, an older one included", func(t *testing.T) {
		if r := put("latest", `"1.0.0"`); r.Code != 200 || tagsOf()["latest"] != "1.0.0" {
			t.Fatalf("%d %s", r.Code, r.Body.String())
		}
		put("latest", `"1.1.0"`)
	})
	t.Run("R-NPM-016 the refusals", func(t *testing.T) {
		for _, c := range []struct{ name, tag, body, code string }{
			{"a tag that is a version range", "1.x", `"1.0.0"`, "npm_invalid_tag"},
			{"a tag that is a version", "1.2.3", `"1.0.0"`, "npm_invalid_tag"},
			{"a tag that starts with a dash", "-x", `"1.0.0"`, "npm_invalid_tag"},
			{"a version that is not published", "beta", `"9.9.9"`, "npm_invalid_tag"},
			{"a version that is not canonical", "beta", `"v1.0.0"`, "npm_invalid_version"},
			{"a body that is no string", "beta", `{"version":"1.0.0"}`, "npm_invalid_version"},
			{"an empty body", "beta", ``, "npm_invalid_version"},
		} {
			r := put(c.tag, c.body)
			if r.Code != 400 || errorBodyOf(t, r.ResponseRecorder).Code != c.code {
				t.Errorf("%s: %d %s", c.name, r.Code, r.Body.String())
			}
		}
		if r := rf.do(http.MethodPut, "-/package/nope/dist-tags/beta", `"1.0.0"`); r.Code != 404 {
			t.Errorf("an unknown package: %d", r.Code)
		}
		if r := rf.get("-/package/nope/dist-tags"); r.Code != 404 {
			t.Errorf("the tags of an unknown package: %d", r.Code)
		}
	})
	t.Run("a tag is removed; latest cannot be; an unknown tag is 404", func(t *testing.T) {
		del := func(tag string) *formattest.Response {
			return rf.do(http.MethodDelete, "-/package/@acme%2fui/dist-tags/"+tag, "")
		}
		before := rf.rev("@acme/ui")
		if r := del("stable"); r.Code != 200 || tagsOf()["stable"] != nil || rf.rev("@acme/ui") == before {
			t.Fatalf("%d %s", r.Code, r.Body.String())
		}
		if r := del("latest"); r.Code != 400 || errorBodyOf(t, r.ResponseRecorder).Code != "npm_invalid_tag" || tagsOf()["latest"] == nil {
			t.Fatalf("latest: %d %s", r.Code, r.Body.String())
		}
		if r := del("stable"); r.Code != 404 {
			t.Fatalf("a tag that is gone: %d", r.Code)
		}
	})
	t.Run("R-AUTHZ-018 every write asks the ownership question under the lock, as deploy", func(t *testing.T) {
		n := len(rf.h.Claims())
		put("beta", `"1.0.0"`)
		rf.do(http.MethodDelete, "-/package/@acme%2fui/dist-tags/beta", "")
		claims := rf.h.Claims()[n:]
		if len(claims) != 2 {
			t.Fatalf("%d claims", len(claims))
		}
		for _, c := range claims {
			if c.Write.Publish || c.Write.FirstVersion || c.Target.Package != "@acme/ui" || len(c.Target.Actions) != 1 || c.Target.Actions[0] != formats.ActionDeploy {
				t.Fatalf("%+v", c)
			}
		}
	})
	t.Run("an owner check that refuses stops the write", func(t *testing.T) {
		deny := newRepo(t, func(o *formattest.HostOptions) {
			o.Claim = func(_ formats.Target, w formats.OwnershipWrite) error {
				if w.Publish {
					return nil
				}
				return errs.E(errs.Forbidden, errs.CodePackageOwnedByOtherOwner, "owned by someone else")
			}
		})
		deny.publish("p", "1.0.0")
		deny.publish("p", "1.1.0")
		before := deny.rev("p")
		r := deny.do(http.MethodPut, "-/package/p/dist-tags/beta", `"1.0.0"`)
		if r.Code != 403 || errorBodyOf(t, r.ResponseRecorder).Code != errs.CodePackageOwnedByOtherOwner {
			t.Fatalf("%d %s", r.Code, r.Body.String())
		}
		if r := deny.do(http.MethodDelete, "-/package/p/dist-tags/latest", ""); r.Code != 403 {
			t.Fatalf("%d", r.Code)
		}
		if deny.rev("p") != before || len(deny.st.TagChanges) != 0 {
			t.Fatal("a refused write changed the package")
		}
	})
}

// TestR_NPM_017_Unpublish: the three requests of npm's unpublish, the tombstone, the
// tags and the rules around a stale revision.
func TestR_NPM_017_Unpublish(t *testing.T) {
	setup := func(t *testing.T) *repoFixture {
		rf := newRepo(t)
		rf.publish("@acme/ui", "1.0.0")
		rf.publish("@acme/ui", "1.1.0")
		rf.publish("@acme/ui", "2.0.0-rc.1")
		rf.do(http.MethodPut, "-/package/@acme%2fui/dist-tags/next", `"2.0.0-rc.1"`)
		rf.do(http.MethodPut, "-/package/@acme%2fui/dist-tags/old", `"1.0.0"`)
		return rf
	}
	body := func(keep ...string) string {
		var parts []string
		for _, v := range keep {
			parts = append(parts, `"`+v+`":{}`)
		}
		return `{"_id":"@acme/ui","name":"@acme/ui","versions":{` + strings.Join(parts, ",") + `},"dist-tags":{"latest":"x"},"description":"ignored"}`
	}
	t.Run("the PUT removes the versions absent from the document and only those", func(t *testing.T) {
		rf := setup(t)
		rev := rf.rev("@acme/ui")
		r := rf.do(http.MethodPut, "@acme%2fui/-rev/"+rev, body("1.0.0", "2.0.0-rc.1"))
		if r.Code != 200 {
			t.Fatalf("%d %s", r.Code, r.Body.String())
		}
		ans := jsonOf(t, r)
		if ans["ok"] != true || ans["rev"] != rf.rev("@acme/ui") || ans["rev"] == rev {
			t.Fatalf("answer %v, rev %s -> %s", ans, rev, rf.rev("@acme/ui"))
		}
		doc := jsonOf(t, rf.get("@acme%2fui"))
		if vs := versionsOf(doc); len(vs) != 2 {
			t.Fatalf("versions %v", vs)
		}
		tags := doc["dist-tags"].(map[string]any)
		if tags["latest"] != "1.0.0" || tags["old"] != "1.0.0" || tags["next"] != "2.0.0-rc.1" {
			t.Fatalf("latest moved to the highest remaining release: %v", tags)
		}
		if doc["description"] == "ignored" {
			t.Fatal("a field of the body other than versions was applied")
		}
		t.Run("the files and the version row are gone, the tombstone stays", func(t *testing.T) {
			if _, ok, _ := rf.h.Items().Stat(context.Background(), rf.h.Ref(), formats.Path{Path: "@acme/ui/-/ui-1.1.0.tgz"}); ok {
				t.Fatal("the tarball item is stored")
			}
			if len(rf.st.Tombstones) != 1 || rf.st.Tombstones[0].Version != "1.1.0" || rf.st.Tombstones[0].Source != "unpublish" || rf.st.Tombstones[0].By != alice().ID {
				t.Fatalf("%+v", rf.st.Tombstones)
			}
		})
		t.Run("the number is never accepted again", func(t *testing.T) {
			err := serveOK(rf.h, http.MethodPut, "@acme%2fui", pubBody(t, "@acme/ui", "1.1.0", "latest", nil), 201)
			if err == nil || !strings.Contains(err.Error(), "npm_version_unpublished") {
				t.Fatalf("%v", err)
			}
			if err := serveOK(rf.h, http.MethodPut, "@acme%2fui", pubBody(t, "@acme/ui", "1.1.1", "latest", nil), 201); err != nil {
				t.Fatal(err)
			}
		})
		t.Run("the tarball of the version is 404 and the second request of npm is a no-op 200", func(t *testing.T) {
			if r := rf.get("@acme%2fui/-/ui-1.1.0.tgz"); r.Code != 404 {
				t.Fatalf("%d", r.Code)
			}
			r := rf.do(http.MethodDelete, "@acme%2fui/-/ui-1.1.0.tgz/-rev/"+rev, "") // the stale rev changes nothing
			if r.Code != 200 || jsonOf(t, r)["ok"] != true {
				t.Fatalf("%d %s", r.Code, r.Body.String())
			}
		})
	})
	t.Run("the DELETE of a tarball whose version is still published is 400", func(t *testing.T) {
		rf := setup(t)
		r := rf.do(http.MethodDelete, "@acme%2fui/-/ui-1.0.0.tgz/-rev/"+rf.rev("@acme/ui"), "")
		if r.Code != 400 || errorBodyOf(t, r.ResponseRecorder).Code != "npm_bad_publish" {
			t.Fatalf("%d %s", r.Code, r.Body.String())
		}
	})
	t.Run("R-NPM-009 a stale revision is 409, unless the request changes nothing", func(t *testing.T) {
		rf := setup(t)
		stale := rf.rev("@acme/ui")
		rf.publish("@acme/ui", "1.2.0") // a newer commit
		r := rf.do(http.MethodPut, "@acme%2fui/-rev/"+stale, body("1.1.0", "1.2.0", "2.0.0-rc.1"))
		if r.Code != 409 || errorBodyOf(t, r.ResponseRecorder).Code != "npm_stale_rev" {
			t.Fatalf("%d %s", r.Code, r.Body.String())
		}
		if len(versionsOf(jsonOf(t, rf.get("@acme%2fui")))) != 4 {
			t.Fatal("a stale request removed a version")
		}
		// the retry of a request whose answer was lost: its effect is in force already
		cur := rf.rev("@acme/ui")
		if r := rf.do(http.MethodPut, "@acme%2fui/-rev/"+cur, body("1.1.0", "1.2.0", "2.0.0-rc.1")); r.Code != 200 {
			t.Fatalf("%d %s", r.Code, r.Body.String())
		}
		after := rf.rev("@acme/ui")
		r = rf.do(http.MethodPut, "@acme%2fui/-rev/"+cur, body("1.1.0", "1.2.0", "2.0.0-rc.1"))
		if r.Code != 200 || jsonOf(t, r)["rev"] != after || rf.rev("@acme/ui") != after {
			t.Fatalf("the retry: %d %s (rev %s -> %s)", r.Code, r.Body.String(), after, rf.rev("@acme/ui"))
		}
	})
	t.Run("a document that names a version the package does not hold, or none, is 400", func(t *testing.T) {
		rf := setup(t)
		rev := rf.rev("@acme/ui")
		for _, b := range []string{body("1.0.0", "9.9.9"), `{"name":"@acme/ui","maintainers":[{"name":"bob"}]}`, `[]`, `{"versions":`, ``} {
			r := rf.do(http.MethodPut, "@acme%2fui/-rev/"+rev, b)
			if r.Code != 400 || errorBodyOf(t, r.ResponseRecorder).Code != "npm_bad_publish" {
				t.Errorf("%q: %d %s", b, r.Code, r.Body.String())
			}
		}
		if len(versionsOf(jsonOf(t, rf.get("@acme%2fui")))) != 3 {
			t.Fatal("a refused request removed a version")
		}
	})
	t.Run("the DELETE of the package removes every version and every tag", func(t *testing.T) {
		rf := setup(t)
		r := rf.do(http.MethodDelete, "@acme%2fui/-rev/"+rf.rev("@acme/ui"), "")
		if r.Code != 200 {
			t.Fatalf("%d %s", r.Code, r.Body.String())
		}
		if r := rf.get("@acme%2fui"); r.Code != 404 {
			t.Fatalf("the document: %d", r.Code)
		}
		if r := rf.get("-/package/@acme%2fui/dist-tags"); r.Code != 404 {
			t.Fatalf("the tags: %d", r.Code)
		}
		if len(rf.st.Tombstones) != 3 {
			t.Fatalf("%d tombstones", len(rf.st.Tombstones))
		}
		if cur := rf.rev("@acme/ui"); cur == "" {
			t.Fatal("the package row went")
		}
		// stale, and every version is gone already: the retry of the DELETE
		if r := rf.do(http.MethodDelete, "@acme%2fui/-rev/1-"+strings.Repeat("0", 32), ""); r.Code != 200 {
			t.Fatalf("the retry: %d %s", r.Code, r.Body.String())
		}
	})
	t.Run("R-AUTHZ-018 the write asks the ownership question under the lock, as delete", func(t *testing.T) {
		rf := setup(t)
		n := len(rf.h.Claims())
		rf.do(http.MethodPut, "@acme%2fui/-rev/"+rf.rev("@acme/ui"), body("1.0.0", "1.1.0"))
		claims := rf.h.Claims()[n:]
		if len(claims) != 1 || claims[0].Write.Publish || len(claims[0].Target.Actions) != 1 || claims[0].Target.Actions[0] != formats.ActionDelete {
			t.Fatalf("%+v", claims)
		}
	})
	t.Run("an owner check that refuses stops the unpublish and removes nothing", func(t *testing.T) {
		rf := setup(t)
		rf.opts.Claim = nil
		deny := newRepo(t, func(o *formattest.HostOptions) {
			o.Claim = func(_ formats.Target, w formats.OwnershipWrite) error {
				if w.Publish {
					return nil
				}
				return errs.E(errs.Forbidden, errs.CodePackageOwnedByOtherOwner, "owned by someone else")
			}
		})
		deny.publish("p", "1.0.0")
		deny.publish("p", "1.1.0")
		r := deny.do(http.MethodPut, "p/-rev/"+deny.rev("p"), `{"versions":{"1.0.0":{}}}`)
		if r.Code != 403 || errorBodyOf(t, r.ResponseRecorder).Code != errs.CodePackageOwnedByOtherOwner {
			t.Fatalf("%d %s", r.Code, r.Body.String())
		}
		if len(versionsOf(jsonOf(t, deny.get("p")))) != 2 || len(deny.st.Tombstones) != 0 {
			t.Fatal("a refused unpublish changed the package")
		}
	})
	t.Run("a package that does not exist is 404 and is not created", func(t *testing.T) {
		rf := newRepo(t)
		if r := rf.do(http.MethodPut, "nope/-rev/1-abc", `{"versions":{}}`); r.Code != 404 {
			t.Fatalf("%d", r.Code)
		}
		if r := rf.do(http.MethodDelete, "nope/-rev/1-abc", ""); r.Code != 404 {
			t.Fatalf("%d", r.Code)
		}
		if _, ok, _ := rf.h.Packages().Package(context.Background(), rf.h.Ref(), "nope"); ok {
			t.Fatal("a package row was created")
		}
	})
}

// TestR_NPM_017_RawDeleteOfATarballUnpublishesTheVersion: a delete of the tarball's item
// through the item store (/files/) takes the same road as the unpublish route.
func TestR_NPM_017_RawDeleteOfATarballUnpublishesTheVersion(t *testing.T) {
	rf := newRepo(t)
	rf.publish("lodash", "4.17.20")
	rf.publish("lodash", "4.17.21")
	rq, _ := rf.h.NewRequest(context.Background(), http.MethodGet, "lodash", nil)
	before := rf.rev("lodash")
	if err := rf.h.Remove(context.Background(), rq, "delete", "lodash/-/lodash-4.17.21.tgz"); err != nil {
		t.Fatal(err)
	}
	if rf.rev("lodash") == before {
		t.Fatal("the revision did not rise")
	}
	doc := jsonOf(t, rf.get("lodash"))
	tags := doc["dist-tags"].(map[string]any)
	if vs := versionsOf(doc); len(vs) != 1 || tags["latest"] != "4.17.20" || len(tags) != 1 {
		t.Fatalf("versions %v tags %v", vs, tags)
	}
	if len(rf.st.Tombstones) != 1 || rf.st.Tombstones[0].Version != "4.17.21" {
		t.Fatalf("%+v", rf.st.Tombstones)
	}
	if err := serveOK(rf.h, http.MethodPut, "lodash", pubBody(t, "lodash", "4.17.21", "latest", nil), 201); err == nil {
		t.Fatal("the number was accepted again")
	}
	t.Run("the notification evicts merged documents early", func(t *testing.T) {
		got := rf.h.Notifications()
		last := got[len(got)-1]
		if last.Area != "metadata_changed" || last.Payload != formats.PackageChanged(rf.h.Ref().ID, "lodash") {
			t.Fatalf("%+v", last)
		}
	})
}

// TestR_NPM_008_RestoreBringsBackTheOriginalBytes: a restore removes the tombstone of a
// version that is stored again with the integrity the tombstone holds, and a package
// that came back from nothing gets its latest tag (the trash can is P1; the hook is
// driven through the host the way the item store would).
func TestR_NPM_008_RestoreBringsBackTheOriginalBytes(t *testing.T) {
	rf := newRepo(t)
	rf.publish("lodash", "4.17.21")
	ctx := context.Background()
	rq, _ := rf.h.NewRequest(ctx, http.MethodGet, "lodash", nil)
	rq.Subject = alice()
	if err := rf.h.Remove(ctx, rq, "trash", "lodash/-/lodash-4.17.21.tgz"); err != nil {
		t.Fatal(err)
	}
	if len(rf.st.Tombstones) != 1 || rf.st.Tombstones[0].Source != "unpublish" {
		t.Fatalf("%+v", rf.st.Tombstones)
	}
	if r := rf.get("lodash"); r.Code != 404 {
		t.Fatalf("a package whose only version is in the trash: %d", r.Code)
	}
	before := rf.rev("lodash")
	if err := rf.h.Restore(ctx, rq, "lodash/-/lodash-4.17.21.tgz"); err != nil {
		t.Fatal(err)
	}
	if len(rf.st.Cleared) != 1 || rf.rev("lodash") == before {
		t.Fatalf("cleared %v", rf.st.Cleared)
	}
	doc := jsonOf(t, rf.get("lodash"))
	if tags, _ := doc["dist-tags"].(map[string]any); tags["latest"] != "4.17.21" || len(versionsOf(doc)) != 1 {
		t.Fatalf("%v", doc)
	}
	t.Run("a restore does not make a version live that was never tombstoned", func(t *testing.T) {
		before := len(rf.st.Cleared)
		if err := rf.h.Restore(ctx, rq, "lodash/-/lodash-4.17.21.tgz"); err != nil {
			t.Fatal(err)
		}
		if len(rf.st.Cleared) != before {
			t.Fatal("a tombstone was cleared twice")
		}
	})
}
