//go:build e2e

package e2e

// Visibility levels with the real npm client (R-AUTHZ-030, R-AUTHZ-033, R-AUTHZ-034, T-18): a repository with public artifacts and an
// internal wiki is installed from without a credential and its package document carries no readme for an anonymous caller, while an
// internal user's token gets the README; a user flagged external sees nothing that is only internal, and reads it again when the flag
// is cleared.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// visibilityRepo creates a local npm repository with the given levels, grants the publisher on it and waits until every replica knows it.
func (fx *npmFixture) visibilityRepo(t *testing.T, label string, levels map[string]any) string {
	t.Helper()
	key := "npm-" + label + "-" + fx.run
	createRepoAt(t, fx.adm, map[string]any{"key": key, "class": "local", "format": "npm"}, levels)
	t.Cleanup(func() { deleteRepository(t, fx.adm, key) })
	policy := npmPublisher + "-" + label + "-" + fx.run
	ensurePolicy(t, fx.adm, policy, []string{key}, [2]any{"sa:" + npmPublisher, []string{"read", "deploy", "delete"}})
	t.Cleanup(func() { fx.adm.do(http.MethodDelete, "/api/v1/permission-policies/"+policy, nil) })
	awaitReadableOnEveryReplica(t, fx.token, key)
	return key
}

// anonymousProject is a project directory of a client that sends no credential: only the registry line, no token.
func (c *npmClient) anonymousProject(t *testing.T, name string) string {
	t.Helper()
	c.token = ""
	d := c.project(name, nil)
	c.write(d+"/.npmrc", "registry="+registryURL(t, c.hostName(), c.registryKey)+"\n")
	return d
}

func TestNPMVisibility_R_AUTHZ_034(t *testing.T) {
	fx := npmFx(t)
	requireScatter(t, fx)
	key := fx.visibilityRepo(t, "vis", map[string]any{"artifacts": "public", "wiki": "internal"})
	name := fx.pkgName("vis-"+fx.run, "readme", false)
	fx.clientOn(t, "npm11", 0, key, "vispub").publish(name, "1.0.0")
	anon := func() *api { return newAPI(t) }
	awaitStatusOnEveryReplica(t, "the anonymous read of the package on every replica", anon, npmAddr(key)+"/"+url.PathEscape(name), 200)

	t.Run("R-AUTHZ-034 an anonymous npm install from a repository with public artifacts works", func(t *testing.T) {
		c := fx.clientOn(t, "npm11", 1, key, "visanon")
		d := c.anonymousProject(t, "consume")
		c.mustInstall(d, name)
		if v := c.read(d + "/node_modules/" + name + "/package.json"); !strings.Contains(v, `"version": "1.0.0"`) {
			t.Fatalf("npm installed %s without version 1.0.0:\n%s", name, v)
		}
	})

	t.Run("R-AUTHZ-034 an anonymous package document has no readme and npm view prints none", func(t *testing.T) {
		r := anon().get(npmAddr(key)+"/"+url.PathEscape(name)).want(t, "the anonymous package document", 200)
		// the wiki members are `readme` and `readmeFilename` (R-AUTHZ-034, R-NPM-010), at the top level and in every version manifest
		var doc struct {
			Top      map[string]json.RawMessage
			Versions map[string]map[string]json.RawMessage `json:"versions"`
		}
		r.JSON(t, &doc.Top)
		r.JSON(t, &doc)
		for _, member := range []string{"readme", "readmeFilename"} {
			if _, ok := doc.Top[member]; ok {
				t.Fatalf("the anonymous package document carries the top-level member %s", member)
			}
			for v, manifest := range doc.Versions {
				if _, ok := manifest[member]; ok {
					t.Fatalf("the anonymous package document carries %s in the manifest of version %s", member, v)
				}
			}
		}
		one := anon().get(npmAddr(key)+"/"+url.PathEscape(name)+"/1.0.0").want(t, "the anonymous version document", 200)
		if strings.Contains(string(one.Body), `"readme`) {
			t.Fatalf("the anonymous version document carries a readme member:\n%s", one)
		}
		c := fx.clientOn(t, "npm11", 2, key, "visview")
		d := c.anonymousProject(t, "view")
		out := c.must(d, "view", name, "readme")
		if strings.Contains(out.Out, "Published by the trove end-to-end tests") {
			t.Fatalf("npm view printed the README to an anonymous caller:\n%s", out.Out)
		}
	})

	t.Run("R-AUTHZ-034 an internal user's token gets the README", func(t *testing.T) {
		tok := userToken(t, "bob")
		awaitStatusOnEveryReplica(t, "the internal user's read on every replica", func() *api { return newAPI(t).withToken(tok) },
			npmAddr(key)+"/"+url.PathEscape(name), 200)
		r := newAPI(t).withToken(tok).get(npmAddr(key)+"/"+url.PathEscape(name)).want(t, "the package document with an internal user's token", 200)
		var doc map[string]json.RawMessage
		r.JSON(t, &doc)
		if _, ok := doc["readme"]; !ok {
			t.Fatalf("the package document of an internal user has no top-level readme member:\n%s", r)
		}
		c := fx.clientOn(t, "npm11", 0, key, "visview2")
		c.token = tok
		d := c.project("view", nil)
		c.configure(d)
		out := c.must(d, "view", name, "readme")
		if !strings.Contains(out.Out, "Published by the trove end-to-end tests") {
			t.Fatalf("npm view with an internal user's token did not print the README:\n%s", out.Out)
		}
	})
}

func TestNPMExternalUser_R_AUTHZ_030(t *testing.T) {
	fx := npmFx(t)
	requireScatter(t, fx)
	internalKey := fx.visibilityRepo(t, "int", map[string]any{"artifacts": "internal", "wiki": "internal"})
	publicKey := fx.visibilityRepo(t, "pub", map[string]any{"artifacts": "public", "wiki": "public"})
	inName := fx.pkgName("ext-"+fx.run, "internal", false)
	pubName := fx.pkgName("ext-"+fx.run, "public", false)
	fx.clientOn(t, "npm11", 0, internalKey, "extpubi").publish(inName, "1.0.0")
	fx.clientOn(t, "npm11", 0, publicKey, "extpubp").publish(pubName, "1.0.0")
	tok := userToken(t, "bob")
	t.Cleanup(func() { setExternal(t, fx.adm, "bob", false) })
	bob := func() *api { return newAPI(t).withToken(tok) }
	view := func(i int, key, name, tag string) npmRun {
		c := fx.clientOn(t, "npm11", i, key, tag)
		c.token = tok
		d := c.project("view", nil)
		c.configure(d)
		return c.run(d, "view", name, "version")
	}

	awaitStatusOnEveryReplica(t, "the internal read on every replica", bob, npmAddr(internalKey)+"/"+url.PathEscape(inName), 200)
	if r := view(0, internalKey, inName, "extbefore"); r.Code != 0 || !strings.Contains(r.Out, "1.0.0") {
		t.Fatalf("an internal user's npm view of the internal repository failed:\n%s", r.Out)
	}

	setExternal(t, fx.adm, "bob", true)
	t.Run("R-AUTHZ-030 R-AUTH-044 an external user gets E404 on an internal repository within the revocation bound and reads the public one", func(t *testing.T) {
		awaitStatusOnEveryReplica(t, "the external flag on every replica", bob, npmAddr(internalKey)+"/"+url.PathEscape(inName), 404)
		c := fx.clientOn(t, "npm11", 1, internalKey, "extread")
		c.token = tok
		d := c.project("view", nil)
		c.configure(d)
		c.expectRefusal(t, "npm view of an internal repository as an external user", c.run(d, "view", inName, "version"), "E404", 30*time.Second)
		if r := view(2, publicKey, pubName, "extpub"); r.Code != 0 || !strings.Contains(r.Out, "1.0.0") {
			t.Fatalf("an external user's npm view of the public repository failed:\n%s", r.Out)
		}
	})

	t.Run("R-AUTHZ-030 R-AUTH-044 the repository list of an external user holds no internal repository", func(t *testing.T) {
		r := bob().get("/api/v1/repositories?limit=200").want(t, "the repository list", 200)
		if strings.Contains(string(r.Body), `"`+internalKey+`"`) {
			t.Fatalf("the list of an external user names %s:\n%s", internalKey, r)
		}
	})

	setExternal(t, fx.adm, "bob", false)
	t.Run("R-AUTH-044 cleared, the flag lets the user read the internal repository again", func(t *testing.T) {
		awaitStatusOnEveryReplica(t, "the cleared flag on every replica", bob, npmAddr(internalKey)+"/"+url.PathEscape(inName), 200)
		if r := view(0, internalKey, inName, "extafter"); r.Code != 0 || !strings.Contains(r.Out, "1.0.0") {
			t.Fatalf("npm view of the internal repository after the flag was cleared failed:\n%s", r.Out)
		}
	})
}
