//go:build e2e

package e2e

// Visibility levels with the real .NET SDK (R-AUTHZ-033, R-AUTHZ-034, T-18): a feed with public artifacts and an internal wiki restores
// without a credential while its README route challenges an anonymous caller and answers an internal user's token; a feed with internal
// artifacts challenges the restore without credentials and serves the restore with the credentials of nuget.config.

import (
	"net/http"
	"strings"
	"testing"
)

// visibilityFeed creates a local NuGet repository with the given levels, grants the publisher on it and waits until every replica knows it.
func (fx *nugetFixture) visibilityFeed(t *testing.T, label string, levels map[string]any) string {
	t.Helper()
	key := "nuget-" + label + "-" + fx.run
	createRepoAt(t, fx.adm, map[string]any{"key": key, "class": "local", "format": "nuget"}, levels)
	t.Cleanup(func() { deleteRepository(t, fx.adm, key) })
	policy := nugetPubSA + "-" + label + "-" + fx.run
	ensurePolicy(t, fx.adm, policy, []string{key}, [2]any{"sa:" + nugetPubSA, []string{"read", "deploy", "delete"}})
	t.Cleanup(func() { fx.adm.do(http.MethodDelete, "/api/v1/permission-policies/"+policy, nil) })
	awaitStatusOnEveryReplica(t, "the publisher's read of "+key+" on every replica", func() *api { return newAPI(t).withToken(fx.token) },
		nugetAddr(key)+"/v3/index.json", 200)
	return key
}

func (fx *nugetFixture) pushReadme(t *testing.T, key, id string) {
	t.Helper()
	c := fx.client(t, 0, "vispush-"+strings.ToLower(id[len(id)-3:]))
	feed := nugetFeed(t, "localhost", key)
	c.config(feed, true) // the publisher's credentials: the service index of an internal feed is refused without them
	file := c.place(id, "1.0.0", fx.nugetPackReadme(t, id, "1.0.0", "a package with a README"))
	out, err := c.dotnetPush(file, feed, fx.token)
	if err != nil || !pushedRE.MatchString(out) {
		t.Fatalf("push of %s to %s: %v\n%s", id, key, err, out)
	}
}

func TestNuGetVisibility_R_AUTHZ_034(t *testing.T) {
	fx := nugetFx(t)
	pub := fx.visibilityFeed(t, "vispub", map[string]any{"artifacts": "public", "wiki": "internal"})
	internal := fx.visibilityFeed(t, "visint", map[string]any{"artifacts": "internal", "wiki": "internal"})
	pubID, intID := fx.pkgID("VisPub"), fx.pkgID("VisInt")
	fx.pushReadme(t, pub, pubID)
	fx.pushReadme(t, internal, intID)
	anon := func() *api { return newAPI(t) }
	awaitStatusOnEveryReplica(t, "the anonymous read of the public feed on every replica", anon, nugetAddr(pub)+"/v3/index.json", 200)
	readme := func(key, id string) string {
		return nugetAddr(key) + "/v3/flatcontainer/" + strings.ToLower(id) + "/1.0.0/readme"
	}

	t.Run("R-AUTHZ-033 dotnet restore without credentials from a public feed succeeds", func(t *testing.T) {
		c := fx.client(t, 1, "visanon")
		c.token = ""
		c.config(nugetFeed(t, "localhost", pub), false)
		proj := c.consumer("consume", pubID, "1.0.0")
		if out, err := c.try("dotnet", "restore", proj, "--no-cache"); err != nil || !c.hasPackage(pubID, "1.0.0") {
			t.Fatalf("anonymous restore from %s: %v\n%s", pub, err, out)
		}
	})

	t.Run("R-AUTHZ-034 the README route answers 401 anonymously and lets an internal user through the wiki gate", func(t *testing.T) {
		r := anon().get(readme(pub, pubID)).want(t, "the anonymous README request", 401)
		if !strings.Contains(strings.ToLower(r.Header.Get("WWW-Authenticate")), "basic") {
			t.Errorf("the anonymous README refusal has no Basic challenge: %v", r.Header)
		}
		tok := userToken(t, "bob")
		// The README handler of NuGet is the P1 stub of R-NUGET-024 (embedded files): a caller who may read the wiki gets 501
		// nuget_not_implemented, and only the gate of R-AUTHZ-034 works (401 or 404 for a denied caller). 501 for the internal user, and
		// not 401, 403 or 404, is the proof of the visibility decision; the README bytes are asserted with the handler (R-NUGET-024).
		awaitStatusOnEveryReplica(t, "the internal user's README read passes the wiki gate on every replica", func() *api { return newAPI(t).withToken(tok) }, readme(pub, pubID), http.StatusNotImplemented)
	})

	t.Run("R-AUTHZ-033 an internal feed challenges the restore without credentials, and the credentials of nuget.config restore", func(t *testing.T) {
		awaitStatusOnEveryReplica(t, "the challenge of the internal feed on every replica", anon, nugetAddr(internal)+"/v3/index.json", 401)
		c := fx.client(t, 2, "visno")
		c.token = ""
		c.config(nugetFeed(t, "localhost", internal), false)
		proj := c.consumer("consume", intID, "1.0.0")
		if out, err := c.try("dotnet", "restore", proj, "--no-cache"); err == nil || c.hasPackage(intID, "1.0.0") {
			t.Fatalf("restore from the internal feed without credentials succeeded:\n%s", out)
		}
		c2 := fx.client(t, 2, "visyes")
		c2.token = userToken(t, "bob")
		awaitStatusOnEveryReplica(t, "the internal user's read of the internal feed", func() *api { return newAPI(t).withToken(c2.token) }, nugetAddr(internal)+"/v3/index.json", 200)
		c2.config(nugetFeed(t, "localhost", internal), true)
		proj = c2.consumer("consume", intID, "1.0.0")
		if out, err := c2.try("dotnet", "restore", proj, "--no-cache"); err != nil || !c2.hasPackage(intID, "1.0.0") {
			t.Fatalf("restore from the internal feed with credentials: %v\n%s", err, out)
		}
	})
}
