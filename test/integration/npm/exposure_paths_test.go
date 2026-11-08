//go:build integration

package npm

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_EP_018_PathRulesDecideAnNPMPackageOnItsDocumentPath: an exposure rule's path patterns on an npm repository are
// matched against the package document's path `<name>` by the format's view and by check-exposure, as the ceiling
// matches them against the path a protocol request states (R-EP-012, R-AUTHZ-005, Q-AUTHZ-251). An include of a
// package's folder exposes that package on the endpoint and nothing else; an exclude hides it, and check-exposure says so.
func TestR_EP_018_PathRulesDecideAnNPMPackageOnItsDocumentPath(t *testing.T) {
	x := newExposureEnv(t, 1)
	st := x.st
	st.Repository("npm-pinc", "local", "npm", `{"local":{"packageOwnership":"off"}}`)
	st.Repository("npm-pexc", "local", "npm", `{"local":{"packageOwnership":"off"}}`)
	// The fixture's policies cover the two repositories as well: the service account of the endpoint reads, the
	// publisher deploys.
	for _, policy := range []string{"customers-read", "customers-publish"} {
		st.Exec(`INSERT INTO authz_policy_selector (policy_id, ordinal, kind, repository_id)
			SELECT p.id, 10 + row_number() OVER (), 'key', r.id FROM authz_policy p, repositories r WHERE p.name = $1 AND r.key IN ('npm-pinc','npm-pexc')`, policy)
	}
	r := st.Start(t, testenv.ReplicaOptions{Name: "web-paths", Endpoints: customersEndpoint})
	for _, repo := range []string{"npm-pinc", "npm-pexc"} {
		x.mustPublish(r, repo, x.tokA, pkg{Name: "open-pkg", Version: "1.0.0"})
		x.mustPublish(r, repo, x.tokA, pkg{Name: "secret-pkg", Version: "1.0.0"})
	}
	for _, rule := range []string{
		`{"name":"pinc","repository":"it/npm-pinc","enabled":true,"paths":{"include":["open-pkg/**"],"exclude":[]}}`,
		`{"name":"pexc","repository":"it/npm-pexc","enabled":true,"paths":{"include":[],"exclude":["secret-pkg/**"]}}`,
	} {
		a := st.Do(r, "POST", "/api/v1/endpoints/customers/exposure-rules", x.opsPAT, []byte(rule))
		if a.Status != http.StatusCreated {
			t.Fatalf("rule: %d %s", a.Status, a.Body)
		}
		x.epoch = a.Header.Get("Trove-Authz-Epoch")
	}
	on := func(repo, path string) testenv.Answer {
		return st.DoAt(r.Addr("customers"), customersHost, "GET", "/api/npm/it/"+repo+"/"+path, x.acmeSAT, nil, "Trove-Authz-Min-Epoch", x.epoch)
	}
	reads := func(name string) []string {
		return []string{name, name + "/1.0.0", name + "/-/" + name + "-1.0.0.tgz", "-/package/" + name + "/dist-tags"}
	}
	for _, c := range []struct {
		repo, name string
		status     int
	}{
		{"npm-pinc", "open-pkg", http.StatusOK},
		{"npm-pinc", "secret-pkg", http.StatusNotFound},
		{"npm-pexc", "open-pkg", http.StatusOK},
		{"npm-pexc", "secret-pkg", http.StatusNotFound},
	} {
		for _, p := range reads(c.name) {
			if a := on(c.repo, p); a.Status != c.status {
				t.Errorf("customers %s %s: %d, want %d", c.repo, p, a.Status, c.status)
			}
		}
	}
	for _, c := range []struct {
		repo, name string
		exposed    bool
	}{
		{"it/npm-pinc", "open-pkg", true},
		{"it/npm-pinc", "secret-pkg", false},
		{"it/npm-pexc", "open-pkg", true},
		{"it/npm-pexc", "secret-pkg", false},
	} {
		a := st.Do(r, "POST", "/api/v1/endpoints/customers/check-exposure", x.opsPAT, map[string]any{"repository": c.repo, "package": c.name})
		if a.Status != http.StatusOK {
			t.Fatalf("check-exposure %s %s: %d %s", c.repo, c.name, a.Status, a.Body)
		}
		var res struct {
			PackageExposed bool `json:"packageExposed"`
			Versions       []struct {
				Version string `json:"version"`
				Exposed bool   `json:"exposed"`
			} `json:"versions"`
		}
		if err := json.Unmarshal(a.Body, &res); err != nil {
			t.Fatalf("%v: %s", err, a.Body)
		}
		if res.PackageExposed != c.exposed {
			t.Errorf("check-exposure %s %s: packageExposed %v, want %v", c.repo, c.name, res.PackageExposed, c.exposed)
		}
		for _, v := range res.Versions {
			if v.Exposed != c.exposed {
				t.Errorf("check-exposure %s %s@%s: exposed %v, want %v", c.repo, c.name, v.Version, v.Exposed, c.exposed)
			}
		}
	}
}

// TestR_EP_018_AVersionPathRuleHidesTheVersionFromNPMDocuments: an exposure rule whose path pattern names one version's
// tarball hides that version from the documents of the endpoint, as it hides the tarball, and check-exposure reports the
// version as hidden (R-EP-012, R-NPM-048, Q-EP-85): the view decides a version on its tarball's path.
func TestR_EP_018_AVersionPathRuleHidesTheVersionFromNPMDocuments(t *testing.T) {
	x := newExposureEnv(t, 1)
	st := x.st
	st.Repository("npm-pver", "local", "npm", `{"local":{"packageOwnership":"off"}}`)
	for _, policy := range []string{"customers-read", "customers-publish"} {
		st.Exec(`INSERT INTO authz_policy_selector (policy_id, ordinal, kind, repository_id)
			SELECT p.id, 10 + row_number() OVER (), 'key', r.id FROM authz_policy p, repositories r WHERE p.name = $1 AND r.key = 'npm-pver'`, policy)
	}
	r := st.Start(t, testenv.ReplicaOptions{Name: "web-pver", Endpoints: customersEndpoint})
	x.mustPublish(r, "npm-pver", x.tokA, pkg{Name: "tool", Version: "1.0.0"})
	x.mustPublish(r, "npm-pver", x.tokA, pkg{Name: "tool", Version: "2.0.0"})
	a := st.Do(r, "POST", "/api/v1/endpoints/customers/exposure-rules", x.opsPAT,
		[]byte(`{"name":"pver","repository":"it/npm-pver","enabled":true,"paths":{"include":[],"exclude":["tool/-/tool-2.0.0.tgz"]}}`))
	if a.Status != http.StatusCreated {
		t.Fatalf("rule: %d %s", a.Status, a.Body)
	}
	x.epoch = a.Header.Get("Trove-Authz-Epoch")
	on := func(path string, hdr ...string) testenv.Answer {
		return st.DoAt(r.Addr("customers"), customersHost, "GET", "/api/npm/it/npm-pver/"+path, x.acmeSAT, nil, append(hdr, "Trove-Authz-Min-Epoch", x.epoch)...)
	}
	if a := on("tool/-/tool-2.0.0.tgz"); a.Status != http.StatusNotFound {
		t.Errorf("the excluded tarball on customers: %d, want 404", a.Status)
	}
	for _, accept := range [][]string{nil, {"Accept", abbreviatedAccept}} {
		doc := document(t, on("tool", accept...))
		if got := versionNames(doc); len(got) != 1 || got[0] != "1.0.0" {
			t.Errorf("customers, package document %v lists %v, want [1.0.0]", accept, got)
		}
		if got := tagsOfDoc(doc); got["latest"] != "1.0.0" {
			t.Errorf("customers, dist-tags %v, want latest 1.0.0", got)
		}
	}
	if a := on("tool/2.0.0"); a.Status != http.StatusNotFound {
		t.Errorf("customers, document tool/2.0.0: %d, want 404", a.Status)
	}
	c := st.Do(r, "POST", "/api/v1/endpoints/customers/check-exposure", x.opsPAT, map[string]any{"repository": "it/npm-pver", "package": "tool"})
	var res struct {
		PackageExposed bool `json:"packageExposed"`
		Versions       []struct {
			Version string `json:"version"`
			Exposed bool   `json:"exposed"`
		} `json:"versions"`
	}
	if c.Status != http.StatusOK || json.Unmarshal(c.Body, &res) != nil {
		t.Fatalf("check-exposure: %d %s", c.Status, c.Body)
	}
	if !res.PackageExposed {
		t.Errorf("check-exposure: the package is reported hidden")
	}
	if len(res.Versions) != 2 {
		t.Errorf("check-exposure: %d versions judged, want 2: %s", len(res.Versions), c.Body)
	}
	for _, v := range res.Versions {
		if v.Exposed != (v.Version == "1.0.0") {
			t.Errorf("check-exposure: tool@%s exposed %v", v.Version, v.Exposed)
		}
	}
}
