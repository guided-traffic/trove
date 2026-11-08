//go:build integration

package oci

import (
	"strings"
	"testing"
)

// pathPatterns: a policy scoped by path decides on the canonical path of the item a protocol route acts on, as the files routes do
// (R-AUTHZ-005, Q-OCI-140): a path the policy excludes is neither pulled nor pushed, a path outside an include list is not reachable,
// the tags list is decided on the package document `<name>/_taglist`, each tag on its own path and a referrers request on the
// manifest path of its subject.
func pathPatterns(t *testing.T, p *proto) {
	refused := func(status int, what string, body []byte) {
		t.Helper()
		if status != 403 && status != 404 {
			t.Fatalf("%s: %d %s", what, status, body)
		}
	}

	secret, plain, team := newImage("p-secret", []byte("secret layer")), newImage("p-plain"), newImage("p-team", []byte("team layer"))
	want(t, p.pushImage(p.as("pusher"), repoLocal, "secret/app", "1", secret), 201, "")

	t.Run("an exclude list", func(t *testing.T) {
		want(t, p.pushImage(p.as("pscoped"), repoLocal, "app", "1", plain), 201, "")
		want(t, p.as1("pscoped", "GET", repo(repoLocal, "app")+"/manifests/1", nil), 200, "")
		a := p.as1("pscoped", "PUT", repo(repoLocal, "secret/app")+"/manifests/2", secret.manifest, "Content-Type", manifestType)
		refused(a.Status, "push to an excluded path", a.Body)
		a = p.as1("pscoped", "PUT", repo(repoLocal, "secret/app")+"/manifests/"+secret.digest(), secret.manifest, "Content-Type", manifestType)
		refused(a.Status, "push by digest to an excluded path", a.Body)
		for name, path := range map[string]string{
			"pull by tag":    "/manifests/1",
			"pull by digest": "/manifests/" + secret.digest(),
			"pull of a blob": "/blobs/" + digestOf(secret.layers[0]),
		} {
			a = p.as1("pscoped", "GET", repo(repoLocal, "secret/app")+path, nil)
			refused(a.Status, name+" of an excluded path", a.Body)
		}
		want(t, p.as1("pusher", "GET", repo(repoLocal, "secret/app")+"/manifests/1", nil), 200, "")
	})
	t.Run("an include list", func(t *testing.T) {
		want(t, p.pushImage(p.as("pteam"), repoLocal, "team/app", "1", team), 201, "")
		want(t, p.as1("pteam", "GET", repo(repoLocal, "team/app")+"/manifests/1", nil), 200, "")
		a := p.as1("pteam", "GET", repo(repoLocal, "app")+"/manifests/1", nil)
		refused(a.Status, "pull outside the include list", a.Body)
		a = p.as1("pteam", "PUT", repo(repoLocal, "app")+"/manifests/2", plain.manifest, "Content-Type", manifestType)
		refused(a.Status, "push outside the include list", a.Body)
		a = p.as1("pteam", "PUT", repo(repoLocal, "other/app")+"/manifests/"+plain.digest(), plain.manifest, "Content-Type", manifestType)
		refused(a.Status, "push by digest outside the include list", a.Body)
		want(t, p.as1("pteam", "GET", repo(repoLocal, "team/app")+"/tags/list", nil), 200, "")
		want(t, p.as1("pteam", "GET", repo(repoLocal, "team/app")+"/referrers/"+team.digest(), nil), 200, "")
		a = p.as1("pteam", "GET", repo(repoLocal, "app")+"/tags/list", nil)
		refused(a.Status, "tags list outside the include list", a.Body)
		a = p.as1("pteam", "GET", repo(repoLocal, "app")+"/referrers/"+plain.digest(), nil)
		refused(a.Status, "referrers outside the include list", a.Body)
	})
	t.Run("an exclude of some tag paths leaves the list and hides those tags", func(t *testing.T) {
		want(t, p.putManifest(p.next(), p.as("pusher"), repoLocal, "team/app", "internal-x", manifestType, team.manifest), 201, "")
		res := p.as1("phide", "GET", repo(repoLocal, "team/app")+"/tags/list", nil)
		want(t, res, 200, "")
		if !strings.Contains(string(res.Body), `"1"`) || strings.Contains(string(res.Body), "internal-x") {
			t.Fatalf("tags list: %s", res.Body)
		}
		want(t, p.as1("phide", "GET", repo(repoLocal, "team/app")+"/manifests/1", nil), 200, "")
		a := p.as1("phide", "GET", repo(repoLocal, "team/app")+"/manifests/internal-x", nil)
		refused(a.Status, "pull of the excluded tag", a.Body)
	})
	t.Run("a path set that covers the item folders and not the package document", func(t *testing.T) {
		m2 := newImage("p-team-2", []byte("layer two"))
		want(t, p.pushImage(p.as("pfolders"), repoLocal, "team/app", "2", m2), 201, "")
		want(t, p.putManifest(p.next(), p.as("pfolders"), repoLocal, "team/app", m2.digest(), manifestType, m2.manifest), 201, "")
		want(t, p.as1("pfolders", "GET", repo(repoLocal, "team/app")+"/manifests/1", nil), 200, "")
		want(t, p.as1("pfolders", "GET", repo(repoLocal, "team/app")+"/blobs/"+digestOf(team.config), nil), 200, "")
		a := p.as1("pfolders", "GET", repo(repoLocal, "team/app")+"/tags/list", nil)
		refused(a.Status, "tags list", a.Body)
	})
	t.Run("a grant for digests only lists the referrers of a subject", func(t *testing.T) {
		want(t, p.as1("pdigests", "GET", repo(repoLocal, "team/app")+"/referrers/"+team.digest(), nil), 200, "")
		a := p.as1("pdigests", "GET", repo(repoLocal, "team/app")+"/tags/list", nil)
		refused(a.Status, "tags list", a.Body)
	})
}

// pathPeople are the people of pathPatterns: they are created before the replicas start.
func pathPeople(p *proto) {
	rw := []string{"read", "deploy"}
	p.pathUser("pscoped", rw, nil, []string{"secret/**"})
	p.pathUser("pteam", rw, []string{"team/**"}, nil)
	p.pathUser("phide", rw, nil, []string{"team/app/_tags/internal-*"})
	p.pathUser("pfolders", rw, []string{"team/app/_tags/**", "team/app/_manifests/**", "team/app/_blobs/**"}, nil)
	p.pathUser("pdigests", []string{"read"}, []string{"**/_manifests/**", "**/_blobs/**"}, nil)
	p.scopedToken("catscope", []string{"team/*"})
}
