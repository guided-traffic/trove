//go:build e2e

package e2e

// The documentation of Ansible collections with the real clients (R-ANS-046, R-ANS-033 to R-ANS-036, R-ANS-038, R-ANS-041, R-UI-060 to
// R-UI-064): a collection with a module, a Markdown file and a README is built in the pod, published with ansible-galaxy and installed;
// what trove then holds for the documentation is read back through the Galaxy route `docs-blob/` with the client's token and through
// the operations of the screens. The remote class does the same over the counting Galaxy test upstream, whose document carries script;
// the virtual class reads the winner's documentation. The browser journey (web/e2e/ansible-docs) runs from TestAnsibleDocsJourney. The
// wiki row of R-ANS-046 belongs to the task that adds the wiki part (R-ANS-037).

import (
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/ansiblestub"
)

// docsModule is the module the collection of the tests documents in full: an option with a suboption, a return value and examples.
const docsModule = `DOCUMENTATION = r'''
module: ping_host
short_description: Pings a host of the e2e collection
description:
  - Sends C(count) probes to I(host) and reports the round trip.
version_added: "1.0.0"
author:
  - trove test
options:
  host:
    description: The host to ping.
    type: str
    required: true
  count:
    description: How many probes.
    type: int
    default: 3
  proxy:
    description: A proxy to go through.
    type: dict
    suboptions:
      url:
        description: The proxy URL.
        type: str
'''

EXAMPLES = r'''
- name: ping
  e2e.docs.ping_host:
    host: example.com
'''

RETURN = r'''
rtt:
  description: Milliseconds of the round trip.
  returned: success
  type: float
  sample: 1.5
'''
`

// docsCollection is a collection with documentation: a README, a Markdown file below docs/ and the module.
func docsCollection(fx *ansibleFixture, tag string) collection {
	c := collection{ns: "e2e" + fx.run, name: "docs" + tag, version: "1.0.0"}
	c.extra = "mkdir -p $d/docs $d/plugins/modules\n" +
		"printf '%s\\n' " + shellQuoteWord("# Install\n\nRun ansible-galaxy collection install "+c.full()+".") + " >$d/docs/install.md\n" +
		"printf '%s' " + shellQuoteWord(docsModule) + " >$d/plugins/modules/ping_host.py\n"
	return c
}

// docsTree is the answer of getPackageVersionDocs.
type docsTree struct {
	State      string `json:"state"`
	Reason     string `json:"reason"`
	Source     string `json:"source"`
	Repository string `json:"repository"`
	MatchCount *int   `json:"matchCount"`
	Files      []struct {
		Name  string `json:"name"`
		Title string `json:"title"`
	} `json:"files"`
	Groups []struct {
		Category string `json:"category"`
		Count    int    `json:"count"`
		Types    []struct {
			Type    string `json:"type"`
			Count   int    `json:"count"`
			Entries []struct {
				Type, Name string
				HasDocs    bool
			} `json:"entries"`
		} `json:"types"`
	} `json:"groups"`
}

func (d docsTree) entries(typ string) []string {
	var out []string
	for _, g := range d.Groups {
		for _, ty := range g.Types {
			if ty.Type == typ {
				for _, e := range ty.Entries {
					out = append(out, e.Name)
				}
			}
		}
	}
	return out
}

func (d docsTree) groupCount(category string) int {
	for _, g := range d.Groups {
		if g.Category == category {
			return g.Count
		}
	}
	return -1
}

// packageID resolves a collection to its package id through the management API.
func packageID(t testing.TB, adm *api, name string) string {
	t.Helper()
	r := adm.get("/api/v1/packages/lookup?format=ansible&name="+url.QueryEscape(name)).want(t, "lookup of "+name, 200)
	var p struct {
		ID string `json:"id"`
	}
	r.JSON(t, &p)
	return p.ID
}

// awaitPackageID is packageID for a package a remote repository has just cached: the package is listed once its version is, which a job
// of the remote's fill does after the client's request.
func awaitPackageID(t testing.TB, adm *api, name string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		r := adm.get("/api/v1/packages/lookup?format=ansible&name=" + url.QueryEscape(name))
		if r.Status == 200 {
			var p struct {
				ID string `json:"id"`
			}
			r.JSON(t, &p)
			return p.ID
		}
		if time.Now().After(deadline) {
			t.Fatalf("the package %s was not listed after 30 seconds: %s", name, r)
		}
		time.Sleep(2 * time.Second)
	}
}

func docsPath(id, version string) string {
	return "/api/v1/packages/" + id + "/versions/" + version + "/docs"
}

func readTree(t testing.TB, adm *api, id, version, query string) docsTree {
	t.Helper()
	var d docsTree
	adm.get(docsPath(id, version)+query).want(t, "the documentation tree of "+id+"@"+version, 200).JSON(t, &d)
	return d
}

// awaitDocs waits until the tree of a version is available: a remote fetches the document by a job after the version document.
func awaitDocs(t testing.TB, adm *api, id, version, query string) docsTree {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for {
		r := adm.get(docsPath(id, version) + query)
		if r.Status == 200 {
			var d docsTree
			r.JSON(t, &d)
			if d.State == "available" {
				return d
			}
			if d.State == "unavailable" {
				t.Fatalf("the documentation of %s@%s is unavailable (%s)", id, version, d.Reason)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the documentation of %s@%s was not available after 3 minutes: %s", id, version, r)
		}
		time.Sleep(2 * time.Second)
	}
}

// hostileMarkup reports what no served document may contain.
func hostileMarkup(body string) []string {
	var found []string
	lower := strings.ToLower(body)
	for _, bad := range []string{"<script", "onerror", "javascript:"} {
		if strings.Contains(lower, bad) {
			found = append(found, bad)
		}
	}
	return found
}

func TestAnsibleDocsLocal(t *testing.T) {
	fx := ansibleFx(t)
	repo := fx.localRepo(t, "docs")
	cfg := ansibleCfg(ansibleRepoURL(t, repo))
	env := tokenEnvVar + "=" + fx.token
	c := docsCollection(fx, "local")
	fx.client(t, ansibleCore221).must(withCfg(cfg, c.publishScript()), env)
	for _, img := range ansibleImages {
		out := fx.client(t, img).must(withCfg(cfg, "ansible-galaxy collection install "+c.full()+" -p coll >/dev/null\nls coll/ansible_collections/"+c.ns+"/"+c.name+"/plugins/modules"), env)
		if !strings.Contains(out, "ping_host.py") {
			t.Errorf("%s: the installed collection lacks its module:\n%s", img.name, out)
		}
	}
	id := packageID(t, fx.adm, c.full())

	t.Run("R-ANS-046 R-ANS-035 the Galaxy route docs-blob/ answers the document with the client's token", func(t *testing.T) {
		r := fx.registry(t).get(ansibleRepoPath(repo)+"/v3/collections/"+c.ns+"/"+c.name+"/versions/1.0.0/docs-blob/").want(t, "docs-blob", 200)
		var doc struct {
			Blob struct {
				Readme struct {
					HTML string `json:"html"`
				} `json:"collection_readme"`
				Files []struct {
					Name string `json:"name"`
				} `json:"documentation_files"`
				Contents []struct {
					Name    string         `json:"content_name"`
					Type    string         `json:"content_type"`
					Strings map[string]any `json:"doc_strings"`
				} `json:"contents"`
			} `json:"docs_blob"`
		}
		r.JSON(t, &doc)
		if !strings.Contains(doc.Blob.Readme.HTML, "e2e collection "+c.full()) {
			t.Errorf("the README of the document: %s", doc.Blob.Readme.HTML)
		}
		if len(doc.Blob.Files) != 1 || doc.Blob.Files[0].Name != "install.md" {
			t.Errorf("the documentation files: %+v", doc.Blob.Files)
		}
		found := false
		for _, e := range doc.Blob.Contents {
			if e.Type == "module" && e.Name == "ping_host" && e.Strings != nil {
				found = true
			}
		}
		if !found {
			t.Errorf("the module ping_host with its doc_strings is not in the contents: %s", r)
		}
		// the route is behind the repository's read: no credential, no document
		newAPI(t).get(ansibleRepoPath(repo)+"/v3/collections/"+c.ns+"/"+c.name+"/versions/1.0.0/docs-blob/").want(t, "docs-blob without a token", 401, 403, 404)
	})

	t.Run("R-ANS-046 R-ANS-036 R-UI-060 the tree has the README first, the file and the module, and the first page is the README", func(t *testing.T) {
		d := readTree(t, fx.adm, id, "1.0.0", "")
		if d.State != "available" || d.Source != "generated" || d.Repository != repoRef(repo) {
			t.Fatalf("the tree: %+v", d)
		}
		if len(d.Files) != 2 || d.Files[0].Name != "README.md" || d.Files[1].Name != "install.md" {
			t.Errorf("the files: %+v", d.Files)
		}
		if got := d.entries("module"); len(got) != 1 || got[0] != "ping_host" || d.groupCount("module") != 1 {
			t.Errorf("the modules: %v (count %d)", got, d.groupCount("module"))
		}
		var f struct {
			HTML string `json:"html"`
		}
		fx.adm.get(docsPath(id, "1.0.0")+"/files/README.md").want(t, "the README page", 200).JSON(t, &f)
		if !strings.Contains(f.HTML, "e2e collection "+c.full()) {
			t.Errorf("the README page: %s", f.HTML)
		}
	})

	t.Run("R-ANS-046 R-ANS-036 R-UI-061 the page of the module has its parameters, the suboption, the return value and the examples", func(t *testing.T) {
		var e struct {
			FullName         string `json:"fullName"`
			ShortDescription string `json:"shortDescription"`
			HasDocs          bool   `json:"hasDocs"`
			Source           string `json:"source"`
			Options          []struct {
				Name       string `json:"name"`
				Required   bool   `json:"required"`
				Suboptions []struct {
					Name string `json:"name"`
				} `json:"suboptions"`
			} `json:"options"`
			ReturnValues []struct {
				Name string `json:"name"`
			} `json:"returnValues"`
			Examples string `json:"examples"`
		}
		fx.adm.get(docsPath(id, "1.0.0")+"/entries/module/ping_host").want(t, "the module page", 200).JSON(t, &e)
		if e.FullName != c.ns+"."+c.name+".ping_host" || !e.HasDocs || e.Source != "generated" || !strings.Contains(e.ShortDescription, "Pings a host") {
			t.Errorf("the head: %+v", e)
		}
		names := map[string]bool{}
		for _, o := range e.Options {
			names[o.Name] = true
			if o.Name == "proxy" && (len(o.Suboptions) != 1 || o.Suboptions[0].Name != "url") {
				t.Errorf("the suboptions of proxy: %+v", o.Suboptions)
			}
		}
		if !names["host"] || !names["count"] || !names["proxy"] {
			t.Errorf("the options: %+v", e.Options)
		}
		if len(e.ReturnValues) != 1 || e.ReturnValues[0].Name != "rtt" || !strings.Contains(e.Examples, "host: example.com") {
			t.Errorf("the return values and examples: %+v %q", e.ReturnValues, e.Examples)
		}
	})

	t.Run("R-ANS-046 R-ANS-036 R-UI-060 the search keeps the counts and narrows the entries", func(t *testing.T) {
		d := readTree(t, fx.adm, id, "1.0.0", "?q=probes")
		if d.MatchCount == nil || *d.MatchCount != 1 || d.groupCount("module") != 1 || len(d.entries("module")) != 1 {
			t.Errorf("q=probes: %+v", d)
		}
		d = readTree(t, fx.adm, id, "1.0.0", "?q=nothingmatchesthis")
		if d.MatchCount == nil || *d.MatchCount != 0 || d.groupCount("module") != 1 || len(d.entries("module")) != 0 {
			t.Errorf("q=nothingmatchesthis: %+v", d)
		}
		fx.adm.get(docsPath(id, "1.0.0")+"?q=a").want(t, "a search of one character", 422)
	})

	t.Run("R-UI-063 R-ANS-009 the import task of the version is listed for the version", func(t *testing.T) {
		r := fx.adm.get(repoAPI(repo)+"/ansible/imports?package="+c.full()+"&version=1.0.0").want(t, "the import tasks of the version", 200)
		if !strings.Contains(string(r.Body), `"state":"completed"`) || !strings.Contains(string(r.Body), `"version":"1.0.0"`) {
			t.Errorf("the tasks: %s", r)
		}
		r = fx.adm.get(repoAPI(repo)+"/ansible/imports?package="+c.full()+"&version=9.9.9").want(t, "the import tasks of another version", 200)
		if strings.Contains(string(r.Body), `"state"`) {
			t.Errorf("tasks of a version that does not exist: %s", r)
		}
	})
}

// awaitDocsBlob reads the Galaxy route docs-blob/ of a repository until it answers 200: a remote fetches the upstream's document by a job
// after the version document.
func awaitDocsBlob(t testing.TB, fx *ansibleFixture, repo, ns, name, version string) reply {
	t.Helper()
	path := ansibleRepoPath(repo) + "/v3/collections/" + ns + "/" + name + "/versions/" + version + "/docs-blob/"
	deadline := time.Now().Add(3 * time.Minute)
	for {
		r := fx.registry(t).get(path)
		if r.Status == 200 {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not answer 200 within 3 minutes: %s", path, r)
		}
		time.Sleep(2 * time.Second)
	}
}

func TestAnsibleDocsRemote(t *testing.T) {
	fx := ansibleFx(t)
	s := ansibleStub(t)
	s.resetAll(t)
	s.ansibleMode(t, ansiblestub.Mode{})
	t.Cleanup(func() { s.resetAll(t) })
	repo := fx.remoteRepo(t, "docsremote", nil)
	cfg := ansibleCfg(ansibleRepoURL(t, repo))
	env := tokenEnvVar + "=" + fx.token

	fx.client(t, ansibleCore221).must(withCfg(cfg, "ansible-galaxy collection install acme.tools -p coll >/dev/null"), env)
	r := awaitDocsBlob(t, fx, repo, "acme", "tools", "2.0.0")

	t.Run("R-ANS-046 R-ANS-035 R-ANS-038 the document the client route serves is the upstream's, sanitized", func(t *testing.T) {
		if bad := hostileMarkup(string(r.Body)); len(bad) > 0 {
			t.Errorf("the served document still holds %v: %.600s", bad, r.Body)
		}
		for _, want := range []string{"acme.tools", "ping_host", "is_cidr", "webserver", "install.md"} {
			if !strings.Contains(string(r.Body), want) {
				t.Errorf("the served document lost %q: %.600s", want, r.Body)
			}
		}
	})

	t.Run("R-ANS-046 R-ANS-042 reading the documentation costs the upstream no further request", func(t *testing.T) {
		before := s.ansibleCounts(t).OfKind(ansiblestub.ClassDocs, "GET")
		if before == 0 {
			t.Fatal("the upstream saw no request for a docs-blob")
		}
		for i := 0; i < 6; i++ {
			fx.registry(t).get(ansibleRepoPath(repo)+"/v3/collections/acme/tools/versions/2.0.0/docs-blob/").want(t, "docs-blob", 200)
		}
		if after := s.ansibleCounts(t).OfKind(ansiblestub.ClassDocs, "GET"); after != before {
			t.Errorf("the upstream saw %d docs requests after the reads, %d before", after, before)
		}
	})
}

// TestAnsibleDocsRemoteScreens reads the documentation of a collection a remote repository cached through the operations of the screens
// (R-ANS-036) and in the browser (journey 02). It needs the collection to be a package of the package API.
func TestAnsibleDocsRemoteScreens(t *testing.T) {
	fx := ansibleFx(t)
	s := ansibleStub(t)
	s.resetAll(t)
	s.ansibleMode(t, ansiblestub.Mode{})
	t.Cleanup(func() { s.resetAll(t) })
	repo := fx.remoteRepo(t, "docsscreens", nil)
	fx.client(t, ansibleCore221).must(withCfg(ansibleCfg(ansibleRepoURL(t, repo)), "ansible-galaxy collection install acme.tools:2.0.0 -p coll >/dev/null"), tokenEnvVar+"="+fx.token)
	id := awaitPackageID(t, fx.adm, "acme.tools")
	d := awaitDocs(t, fx.adm, id, "2.0.0", "")

	t.Run("R-ANS-046 R-ANS-035 the tree is the upstream's document: its module, its filter without documentation and its role", func(t *testing.T) {
		if d.Source != "upstream" || d.Repository != repoRef(repo) {
			t.Errorf("the source and repository: %s %s", d.Source, d.Repository)
		}
		if got := d.entries("module"); len(got) != 1 || got[0] != "ping_host" {
			t.Errorf("the modules: %v", got)
		}
		if got := d.entries("filter"); len(got) != 1 || got[0] != "is_cidr" {
			t.Errorf("the filters: %v", got)
		}
		if got := d.entries("role"); len(got) != 1 || got[0] != "webserver" {
			t.Errorf("the roles: %v", got)
		}
		if len(d.Files) != 2 || d.Files[0].Name != "README.md" {
			t.Errorf("the files: %+v", d.Files)
		}
	})

	t.Run("R-ANS-046 R-ANS-036 R-ANS-039 the pages hold no script: the README, the role and the module", func(t *testing.T) {
		for _, p := range []string{"/files/README.md", "/entries/role/webserver", "/entries/module/ping_host", "/files/install.md"} {
			r := fx.adm.get(docsPath(id, "2.0.0")+p).want(t, "page "+p, 200)
			if bad := hostileMarkup(string(r.Body)); len(bad) > 0 {
				t.Errorf("%s holds %v: %.600s", p, bad, r.Body)
			}
		}
		var e struct {
			HasDocs bool   `json:"hasDocs"`
			Source  string `json:"source"`
		}
		fx.adm.get(docsPath(id, "2.0.0")+"/entries/filter/is_cidr").want(t, "the filter page", 200).JSON(t, &e)
		if e.HasDocs || e.Source != "upstream" {
			t.Errorf("the filter without doc_strings: %+v", e)
		}
	})

	t.Run("R-UI-060 R-UI-061 R-UI-064 R-ANS-046 the browser reads the same documentation", func(t *testing.T) {
		runDocsJourney(t, "02-remote.spec.ts", "TROVE_E2E_ANSIBLE_DOCS_REMOTE_PACKAGE=acme.tools", "TROVE_E2E_ANSIBLE_DOCS_REMOTE_VERSION=2.0.0")
	})
}

func TestAnsibleDocsVirtual(t *testing.T) {
	fx := ansibleFx(t)
	s := ansibleStub(t)
	s.resetAll(t)
	s.ansibleMode(t, ansiblestub.Mode{})
	t.Cleanup(func() { s.resetAll(t) })
	local := fx.localRepo(t, "dlocal")
	remote := fx.remoteRepo(t, "dremote", nil)
	virtual := "ansible-dvirtual-" + fx.run
	fx.createRepo(t, virtual, map[string]any{
		"key": virtual, "class": "virtual", "format": "ansible", "requireAuthentication": true,
		"virtual": map[string]any{"members": []string{local, remote}, "deployTarget": local},
	})
	env := tokenEnvVar + "=" + fx.token
	cfg := ansibleCfg(ansibleRepoURL(t, virtual))
	c := docsCollection(fx, "virt")

	// the publish goes to the deployment target; the install of a remote member's collection goes through the same address
	cl := fx.client(t, ansibleCore221)
	cl.must(withCfg(cfg, c.publishScript()), env)
	cl.must(withCfg(cfg, "ansible-galaxy collection install "+c.full()+" other.stuff -p coll >/dev/null"), env)
	id := packageID(t, fx.adm, c.full())

	t.Run("R-ANS-041 R-ANS-046 the documentation of a collection published through the virtual repository is the deployment target's", func(t *testing.T) {
		d := readTree(t, fx.adm, id, "1.0.0", "?repository="+url.QueryEscape(repoRef(virtual)))
		if d.State != "available" || d.Source != "generated" || d.Repository != repoRef(local) || len(d.entries("module")) != 1 {
			t.Errorf("the tree through the virtual repository: %+v", d)
		}
		r := fx.registry(t).get(ansibleRepoPath(virtual)+"/v3/collections/"+c.ns+"/"+c.name+"/versions/1.0.0/docs-blob/").want(t, "docs-blob through the virtual address", 200)
		if !strings.Contains(string(r.Body), "ping_host") {
			t.Errorf("the document of the virtual address: %.400s", r.Body)
		}
	})

	t.Run("R-ANS-041 R-ANS-046 the document of a collection only a remote member serves is that member's, sanitized", func(t *testing.T) {
		r := awaitDocsBlob(t, fx, virtual, "other", "stuff", "1.0.0")
		if bad := hostileMarkup(string(r.Body)); len(bad) > 0 {
			t.Errorf("the document holds %v: %.400s", bad, r.Body)
		}
		if !strings.Contains(string(r.Body), "other.stuff") {
			t.Errorf("the document lost its text: %.400s", r.Body)
		}
	})
}

// runDocsJourney runs one file of the browser journeys of the documentation screens (web/e2e/ansible-docs, playwright.ansible-docs.config.ts,
// R-UI-060 to R-UI-064) with axe. The suite needs `npm ci` in web/ and `npx playwright install --with-deps chromium` on the host; without them
// the test fails and says what to run, it does not skip.
func runDocsJourney(t *testing.T, file string, extraEnv ...string) {
	t.Helper()
	dir := filepath.Join(cfg.repo, "web", "e2e")
	if _, err := os.Stat(filepath.Join(cfg.repo, "web", "node_modules", ".bin", "playwright")); err != nil {
		t.Fatal("R-UI-060: the Playwright suite is not installed; run `npm ci` in web/ and `npx playwright install --with-deps chromium`")
	}
	fxAdm := adminAPI(t)
	fxAdm.do(http.MethodPost, "/api/v1/users/admin/unlock", nil).want(t, "unlock break-glass", 204)
	args := append([]string{"playwright", "test", "--config", "playwright.ansible-docs.config.ts", file}, strings.Fields(os.Getenv("TROVE_E2E_PLAYWRIGHT_ARGS"))...)
	cmd := exec.Command("npx", args...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), extraEnv...),
		"TROVE_E2E_EDGE_URL="+cfg.edgeURL,
		"TROVE_E2E_KEYCLOAK_ADDR="+keycloakAddr(t),
		"NODE_EXTRA_CA_CERTS="+cfg.caFile,
		"TROVE_E2E_ALICE_PASSWORD="+secretValue(t, "trove-keycloak", "alice-password"),
		"TROVE_E2E_BOB_PASSWORD="+secretValue(t, "trove-keycloak", "bob-password"),
		"TROVE_E2E_BREAKGLASS_PASSWORD="+secretValue(t, "trove-break-glass", "password"),
		"TROVE_E2E_ADMIN_TOKEN="+adminToken(t),
	)
	reportFile := filepath.Join(cfg.repo, ".out", "playwright", "ansible-docs-"+strings.TrimSuffix(file, ".spec.ts")+".json")
	_ = os.Remove(reportFile)
	cmd.Env = append(cmd.Env, "PLAYWRIGHT_JSON_OUTPUT_NAME="+reportFile)
	out, err := cmd.CombinedOutput()
	_ = os.WriteFile(filepath.Join(cfg.out, "playwright-ansible-docs-"+strings.TrimSuffix(file, ".spec.ts")+".log"), out, 0o644)
	specs := playwrightSpecs(t, reportFile)
	for _, sp := range specs {
		t.Run(sp.Title, func(t *testing.T) {
			if !sp.OK {
				t.Errorf("journey %s failed (see the playwright-ansible-docs log and .out/playwright/report)", sp.File)
			}
		})
	}
	if err != nil {
		t.Fatalf("playwright: %v\n%s", err, tail(string(out), 6000))
	}
	if len(specs) == 0 {
		t.Fatalf("playwright ran no journey:\n%s", tail(string(out), 2000))
	}
	t.Logf("playwright: %d journeys passed\n%s", len(specs), tail(string(out), 1500))
}

// TestAnsibleDocsJourney is the browser journey of a collection published with ansible-galaxy into a local repository: the Documentation,
// Contents and Import log tabs, and the layout at 1440 and 390 px.
func TestAnsibleDocsJourney(t *testing.T) {
	fx := ansibleFx(t)
	local := fx.localRepo(t, "jlocal")
	c := docsCollection(fx, "journey")
	fx.client(t, ansibleCore221).must(withCfg(ansibleCfg(ansibleRepoURL(t, local)), c.publishScript()), tokenEnvVar+"="+fx.token)
	runDocsJourney(t, "01-ansible-docs.spec.ts",
		"TROVE_E2E_ANSIBLE_DOCS_LOCAL_PACKAGE="+c.full(),
		"TROVE_E2E_ANSIBLE_DOCS_LOCAL_VERSION="+c.version,
	)
}
