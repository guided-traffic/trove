//go:build integration

package npm

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/npmstub/corpus"
)

// The endpoint fixture of R-EP-012 on a VIRTUAL repository with a remote member
// (R-EP-018, R-EP-019, R-NPM-048, H-295): customers exposes npm-customers, whose only member is
// npm-remote, and the rule hides one version of an upstream package. The endpoint does not
// fill caches, so what it serves is what the default endpoint cached.
func TestR_EP_018_ACustomersEndpointOnAVirtualRepositoryWithARemoteMember(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{})
	rep := r.start(1)[0]
	p := upstreamPackage(t, 5)
	// shown is the dist-tag latest, a release; the rule hides pre-releases and one more version.
	shown := p.DistTags["latest"]
	hidden, other := "", ""
	for _, v := range p.Versions {
		switch {
		case corpus.IsPrerelease(v.Version) || v.Version == shown:
		case hidden == "":
			hidden = v.Version
		case other == "":
			other = v.Version
		}
	}
	if hidden == "" || other == "" {
		t.Fatalf("%s has too few releases", p.Name)
	}
	rule := `{"name":"upstream-sdk","repository":"` + rCustomers + `","enabled":true,
		"packages":{"include":["` + p.Name + `"],"exclude":[]},
		"versions":{"include":[],"exclude":["` + hidden + `"],"range":">=0.0.0"},"prereleases":false,
		"distTags":{"include":["latest"],"exclude":[]}}`
	a := r.st.Do(rep, http.MethodPost, "/api/v1/endpoints/customers/exposure-rules", r.opsPAT, rule)
	if a.Status != http.StatusCreated {
		t.Fatalf("create the rule: %d %s", a.Status, a.Body)
	}
	epoch := a.Header.Get("Trove-Authz-Min-Epoch")
	if epoch == "" {
		epoch = a.Header.Get("Trove-Authz-Epoch")
	}
	onCustomers := func(path string) testenv.Answer {
		return r.st.DoAt(rep.Addr("customers"), customersHost, http.MethodGet, "/api/npm/"+rCustomers+"/"+path, r.acmeSAT, nil, "Trove-Authz-Min-Epoch", epoch)
	}
	onDefault := func(path string) testenv.Answer {
		return r.st.Do(rep, http.MethodGet, "/api/npm/"+rCustomers+"/"+path, r.tokA, nil)
	}
	file := func(v string) string { return p.Name + "/-/" + corpus.TarballFile(p.Name, v) }

	t.Run("R-EP-019 customers does not fill the cache: an uncached package is a miss and no request goes upstream", func(t *testing.T) {
		if got := onCustomers(p.Name); got.Status != 404 {
			t.Fatalf("%d %.200s", got.Status, got.Body)
		}
		if got := onCustomers(file(shown)); got.Status != 404 {
			t.Fatalf("an uncached version: %d", got.Status)
		}
		if n := r.stub.Counts().Total(); n != 0 {
			t.Fatalf("customers made the upstream see %d requests", n)
		}
	})

	t.Run("R-EP-019 the same request through default fills the cache", func(t *testing.T) {
		doc := onDefault(p.Name)
		if doc.Status != 200 || len(versionsOf(t, doc)) != len(p.Versions) {
			t.Fatalf("default: %d %.200s", doc.Status, doc.Body)
		}
		if got := r.install(rep, tarballsOf(t, doc)[shown], r.tokA); got.Status != 200 {
			t.Fatalf("default install: %d", got.Status)
		}
		if n := r.stub.Counts().Tarballs(p.Name); n != 1 {
			t.Fatalf("%d tarball requests", n)
		}
	})

	t.Run("R-NPM-048 a document lists what is exposed, latest moves, and URLs are the endpoint's", func(t *testing.T) {
		r.stub.Reset()
		for _, accept := range []string{"application/json", abbreviatedAccept} {
			a := r.st.DoAt(rep.Addr("customers"), customersHost, http.MethodGet, "/api/npm/"+rCustomers+"/"+p.Name, r.acmeSAT, nil, "Trove-Authz-Min-Epoch", epoch, "Accept", accept)
			if a.Status != 200 {
				t.Fatalf("%s: %d %.300s %v", accept, a.Status, a.Body, a.Header)
			}
			vs := versionsOf(t, a)
			if slices.Contains(vs, hidden) || !slices.Contains(vs, shown) {
				t.Fatalf("%s: versions %v", accept, vs)
			}
			if tags := tagsOf(t, a); tags["latest"] == "" || tags["latest"] == hidden {
				t.Fatalf("%s: dist-tags %v", accept, tags)
			}
			body := string(a.Body)
			for _, secret := range []string{rRemote, rLocal, "Trove-Resolved-From", r.run.Addr, testenv.DefaultHost} {
				if strings.Contains(body, secret) {
					t.Fatalf("%s: the body names %q: %.300s", accept, secret, body)
				}
			}
			for _, addr := range tarballsOf(t, a) {
				if !strings.HasPrefix(addr, "https://"+customersHost+"/api/npm/"+rCustomers+"/"+p.Name+"/-/") {
					t.Fatalf("%s: tarball %s", accept, addr)
				}
			}
			for k, v := range a.Header {
				if h := strings.Join(v, " "); strings.Contains(h, rRemote) || strings.Contains(h, testenv.DefaultHost) {
					t.Fatalf("header %s names a member or the default endpoint: %s", k, h)
				}
			}
		}
		tags := onCustomers("-/package/" + p.Name + "/dist-tags")
		if tags.Status != 200 || strings.Contains(string(tags.Body), hidden) {
			t.Errorf("dist-tags: %d %s", tags.Status, tags.Body)
		}
		if one := onCustomers(p.Name + "/" + hidden); one.Status != 404 {
			t.Errorf("a hidden version document: %d", one.Status)
		}
		if one := onCustomers(p.Name + "/" + shown); one.Status != 200 {
			t.Errorf("an exposed version document: %d", one.Status)
		}
		if n := r.stub.Counts().Total(); n != 0 {
			t.Errorf("customers reached the upstream %d times", n)
		}
	})

	t.Run("R-NPM-048 an exposed cached version installs; a hidden or an uncached one is missing, and no header names a member", func(t *testing.T) {
		got := onCustomers(file(shown))
		if got.Status != 200 || integrityOf(got.Body) != p.Versions[p.Find(shown)].Dist.Integrity {
			t.Fatalf("exposed cached version: %d", got.Status)
		}
		if got.Header.Get("Trove-Resolved-From") != "" {
			t.Errorf("Trove-Resolved-From = %q on an endpoint with rules", got.Header.Get("Trove-Resolved-From"))
		}
		for _, v := range []string{hidden, other} {
			if a := onCustomers(file(v)); a.Status != 404 {
				t.Errorf("%s: %d", v, a.Status)
			}
		}
		// default caches the hidden-on-customers version; customers still does not serve it.
		if d := r.install(rep, "http://trove.test/api/npm/"+rCustomers+"/"+file(hidden), r.tokA); d.Status != 200 {
			t.Fatalf("default install of the version customers hides: %d", d.Status)
		}
		if a := onCustomers(file(hidden)); a.Status != 404 {
			t.Errorf("a hidden version served after it was cached: %d", a.Status)
		}
		if n := r.stub.Counts().Total(); n != 1 {
			t.Errorf("the upstream saw %d requests: only default's install may have reached it", n)
		}
	})
}
