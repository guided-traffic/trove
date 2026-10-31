//go:build e2e

package e2e

// The client matrix of R-NPM-045: the scenario list of the spec, run by every gate row against the scatter profile
// (E2.1). One table of scenarios, one table of rows; a cell a row's client cannot run is "not applicable" with
// the reason, never a silent skip: it is in the log line of the test, in the report of npmReport and in
// .out/e2e/npm-<run>.json.
//
// The registry is npm-all, the virtual repository over npm-local and npm-remote (npmFixture.registry). Everything
// here is configured only from the setup snippets of R-NPM-041 (npm's SetupTemplates, rendered the way the setup
// API does); what the clients need beyond them (a cache directory, no telemetry, no release-age quarantine) goes
// through the environment, never into the snippet files.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/npm"
	"github.com/guided-traffic/trove/test/npmstub"
	"github.com/guided-traffic/trove/test/npmstub/corpus"
)

// ── the snippets ─────────────────────────────────────────────────────────────

// setupSnippet renders the configure template of the row's client with the data the setup API would give it for the
// repository: the registry URL of the edge, the keyed credential form and the always-auth switches of a repository
// that is not anonymously readable. The credential stays the placeholder ${TROVE_TOKEN}, which the client expands
// from its environment.
func setupSnippet(t testing.TB, row, host, repo string) (file, content string) {
	t.Helper()
	id := map[string]string{
		"npm": "npm.npmrc.configure", "pnpm": "pnpm.npmrc.configure",
		"yarn4": "yarn.yarnrc.configure", "yarn1": "yarn-classic.npmrc.configure",
	}[familyOf(row)]
	regURL := registryURL(t, host, repo)
	data := formats.SetupData{
		RegistryURL: regURL, Repository: repo, Endpoint: "default",
		RegistryKey: strings.TrimPrefix(regURL, "https:"), RequiresAuth: true,
	}
	for _, tpl := range npm.New().SetupTemplates() {
		if tpl.ID != id {
			continue
		}
		tm, err := template.New(tpl.ID).Option("missingkey=error").Parse(tpl.Template)
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		if err := tm.Execute(&b, data); err != nil {
			t.Fatal(err)
		}
		return tpl.File, b.String()
	}
	t.Fatalf("npm has no setup template %s", id)
	return "", ""
}

func familyOf(row string) string {
	switch {
	case strings.HasPrefix(row, "pnpm"):
		return "pnpm"
	case strings.HasPrefix(row, "npm"):
		return "npm"
	default:
		return row
	}
}

// configure writes the snippet of the row to the file the snippet names and returns the content. A name that starts
// with ~/ is the user-level file (the HOME of the row); any other is a file of the project directory. The pnpm snippet
// names ~/.npmrc, because pnpm 11 and 12 ignore a credential with an environment variable in a project .npmrc
// (Q-NPM-47, R-NPM-041).
func (c *npmClient) configure(dir string) string {
	c.t.Helper()
	file, content := setupSnippet(c.t, c.row, c.hostName(), c.registryKey)
	target := dir + "/" + file
	if rest, user := strings.CutPrefix(file, "~/"); user {
		target = c.home() + "/" + rest
	}
	c.write(target, content)
	return content
}

// ── packages ─────────────────────────────────────────────────────────────────

// publishDir writes a small package into a directory of the pod.
func (c *npmClient) publishDir(name, version, marker string) string {
	c.t.Helper()
	dir := c.dir("pkg-" + strings.NewReplacer("@", "", "/", "-").Replace(name) + "-" + version)
	pkg, _ := json.MarshalIndent(map[string]any{
		"name": name, "version": version, "description": "trove end-to-end test package", "main": "index.js", "license": "MIT",
	}, "", "  ")
	c.sh("/work", `rm -rf "`+dir+`" && mkdir -p "`+dir+`"`)
	c.write(dir+"/package.json", string(pkg)+"\n")
	c.write(dir+"/index.js", fmt.Sprintf("module.exports = %q;\n", marker))
	c.write(dir+"/README.md", fmt.Sprintf("# %s\n\nPublished by the trove end-to-end tests, row %s.\n", name, c.row))
	c.configure(dir)
	if c.family() == "yarn4" {
		// `yarn npm publish` wants the package in the lockfile of its own project; an install with no dependencies
		// writes it and asks the registry for nothing.
		c.write(dir+"/yarn.lock", "")
		c.must(dir, "install")
	}
	return dir
}

// publishCmd is the row's publish command in the package directory.
func (c *npmClient) publishCmd(version string) []string {
	switch c.family() {
	case "pnpm":
		return []string{"publish", "--no-git-checks"}
	case "yarn4":
		return []string{"npm", "publish"}
	case "yarn1":
		return []string{"publish", "--non-interactive", "--new-version", version, "--no-git-tag-version"}
	default:
		return []string{"publish"}
	}
}

// publish publishes name@version with the row's client and returns the package directory.
func (c *npmClient) publish(name, version string) string {
	c.t.Helper()
	dir := c.publishDir(name, version, "marker:"+name+"@"+version)
	c.must(dir, c.publishCmd(version)...)
	return dir
}

// registryDoc reads the package document of the matrix registry directly (with the publisher's token), the way a
// check of the result that does not trust the client's report does.
func (fx *npmFixture) registryDoc(t testing.TB, key, name string) reply {
	t.Helper()
	return fx.adm.withToken(fx.token).get("/api/npm/" + key + "/" + url.PathEscape(name))
}

func (fx *npmFixture) versionsOf(t testing.TB, key, name string) (versions []string, tags map[string]string, doc map[string]any) {
	t.Helper()
	r := fx.registryDoc(t, key, name)
	r.want(t, "document of "+name, 200)
	var d struct {
		Versions map[string]json.RawMessage `json:"versions"`
		Tags     map[string]string          `json:"dist-tags"`
	}
	r.JSON(t, &d)
	_ = json.Unmarshal(r.Body, &doc)
	for v := range d.Versions {
		versions = append(versions, v)
	}
	return versions, d.Tags, doc
}

// ensurePublished publishes the row's scoped and unscoped package once.
func (c *npmClient) ensurePublished() (plain, scoped string) {
	c.t.Helper()
	plain, scoped = c.pkg("plain", false), c.pkg("scoped", true)
	if c.published {
		return plain, scoped
	}
	c.publish(plain, "1.0.0")
	c.publish(scoped, "1.0.0")
	c.published = true
	return plain, scoped
}

// requireReplicas asserts that the answers of the registry come from more than one replica over a few requests, so the
// matrix does run against the scatter profile (INV-18) and not against one pod.
func requireScatter(t testing.TB, fx *npmFixture) {
	t.Helper()
	seen := map[string]bool{}
	a := fx.adm.withToken(fx.token)
	for i := 0; i < 6 && len(seen) < 2; i++ {
		seen[a.get("/api/npm/"+fx.registry()+"/-/ping").Replica()] = true
	}
	if len(seen) < 2 {
		t.Fatalf("six consecutive requests reached %d replica(s): the cluster is not in the scatter profile", len(seen))
	}
}

// ── the scenario table ───────────────────────────────────────────────────────

type npmScenario struct {
	id   string // the name of the subtest after the row
	reqs string // requirement and exit-criterion IDs, in the test name
	// na is the reason the row's client cannot run the scenario, "" when it can.
	na  func(c *npmClient) string
	run func(t *testing.T, c *npmClient)
}

// naUnless returns a reason for every family but the listed ones.
func naUnless(reason string, families ...string) func(c *npmClient) string {
	return func(c *npmClient) string {
		for _, f := range families {
			if c.family() == f {
				return ""
			}
		}
		return reason
	}
}

var npmScenarios = []npmScenario{
	{id: "configure", reqs: "R-NPM-041 E2.8", run: scenarioConfigure},
	{id: "whoami", reqs: "R-NPM-019 R-NPM-045", na: naUnless("Yarn 1.22 has no whoami command", "npm", "pnpm", "yarn4"), run: scenarioWhoami},
	{id: "ping", reqs: "R-NPM-019 E2.7", na: naUnless("only npm and pnpm have a ping command", "npm", "pnpm"), run: scenarioPing},
	{id: "publish", reqs: "R-NPM-007 R-NPM-040 R-NPM-045", run: scenarioPublish},
	{id: "install-local", reqs: "R-NPM-015 R-NPM-040 R-NPM-045", run: scenarioInstallLocal},
	{id: "install-remote", reqs: "R-NPM-024 R-NPM-025 R-NPM-045", run: scenarioInstallRemote},
	{id: "dist-tag", reqs: "R-NPM-016 R-NPM-042 R-NPM-045", run: scenarioDistTag},
	{id: "deprecate", reqs: "R-NPM-018 R-NPM-045", na: naUnless("neither Yarn 1.22 nor Yarn 4 has a deprecate command", "npm", "pnpm"), run: scenarioDeprecate},
	{id: "unpublish", reqs: "R-NPM-017 R-NPM-045", na: naUnless("neither Yarn 1.22 nor Yarn 4 has an unpublish command", "npm", "pnpm"), run: scenarioUnpublish},
	{id: "login", reqs: "R-NPM-021 R-NPM-043 R-NPM-045", na: func(c *npmClient) string {
		if c.row == "yarn1" {
			return "yarn login only stores the user name and e-mail address in the client's configuration and sends no request to the registry (verified: the command exits 0 and the registry logs nothing)"
		}
		return ""
	}, run: scenarioLogin},
	{id: "audit", reqs: "R-NPM-038 R-NPM-043 R-NPM-045", na: func(c *npmClient) string {
		if c.row == "yarn1" {
			return "yarn audit sends its request to registry.yarnpkg.com whatever the .npmrc says (verified with --verbose), so it never reaches the registry under test"
		}
		return ""
	}, run: scenarioAudit},
	{id: "search", reqs: "R-NPM-037 R-NPM-043 R-NPM-045", na: naUnless("neither Yarn 1.22 nor Yarn 4 has a search command", "npm", "pnpm"), run: scenarioSearch},
	{id: "deferred-endpoints", reqs: "R-NPM-043", na: naUnless("the commands of the criterion (token, profile, org, access) are npm's", "npm"), run: scenarioDeferred},
	{id: "lockfile-reinstall", reqs: "R-NPM-040 R-NPM-045", run: scenarioLockfileReinstall},
	{id: "yarn4-lockfile", reqs: "R-NPM-042", na: naUnless("the conventional-URL lockfile is Yarn 2+ behaviour", "yarn4"), run: scenarioYarn4Lockfile},
	{id: "yarn4-switch-registry", reqs: "R-NPM-042", na: naUnless("the conventional-URL lockfile is Yarn 2+ behaviour", "yarn4"), run: scenarioYarn4Switch},
	{id: "pnpm-workspace", reqs: "R-NPM-040", na: naUnless("the workspace criterion is pnpm's", "pnpm"), run: scenarioPnpmWorkspace},
}

// TestNPMMatrix is E2.1: every scenario for every gate row in the scatter profile.
func TestNPMMatrix(t *testing.T) { runNPMMatrix(t, "matrix") }

// runNPMMatrix runs the scenario list for the eight gate rows; run names the report (the Valkey-stopped variant of
// the closing run uses another name).
func runNPMMatrix(t *testing.T, run string) {
	fx := npmFx(t)
	requireScatter(t, fx)
	pods := npmClientCount(t)
	// The rows run side by side (one pod serves two or three of them, each in a directory and a HOME of its own);
	// the scenarios of a row run one after the other, the later ones read what the earlier ones published. The
	// parent's cleanup runs after every row has finished.
	t.Run("rows", func(t *testing.T) {
		for i, row := range npmGateRows {
			t.Run("R-NPM-045 E2.1 "+row, func(t *testing.T) {
				t.Parallel()
				c := newNPMClient(t, fx, row, i%pods)
				for _, sc := range npmScenarios {
					t.Run(sc.id+" "+sc.reqs, func(t *testing.T) {
						defer func() { npmRep.record(run, row, sc.id, t) }()
						c.t = t // the row's client reports to the scenario that is running
						if sc.na != nil {
							if why := sc.na(c); why != "" {
								npmNotApplicable(t, why)
							}
						}
						start := time.Now()
						sc.run(t, c)
						t.Logf("%s %s: %s", row, sc.id, time.Since(start).Round(time.Millisecond))
					})
				}
			})
		}
	})
	npmRep.flush(t, run)
}

// ── the scenarios ────────────────────────────────────────────────────────────

// scenarioConfigure: the snippet, written as printed, makes the client use the registry (E2.8, R-NPM-041). The
// check does not request anything: it reads the configuration back the way each client reports it.
func scenarioConfigure(t *testing.T, c *npmClient) {
	dir := c.project("configure", nil)
	content := c.configure(dir)
	want := registryURL(t, c.hostName(), c.registryKey)
	if !strings.Contains(content, want) {
		t.Fatalf("the snippet does not name the registry %s:\n%s", want, content)
	}
	if strings.Contains(content, c.token) {
		t.Fatalf("the snippet contains the token, it must hold the variable only:\n%s", content)
	}
	var r npmRun
	switch c.family() {
	case "npm":
		r = c.must(dir, "config", "get", "registry")
	case "pnpm":
		r = c.must(dir, "config", "get", "registry")
	case "yarn4":
		r = c.must(dir, "config", "get", "npmRegistryServer")
	case "yarn1":
		r = c.must(dir, "config", "list")
	}
	if !strings.Contains(r.Out, want) {
		t.Fatalf("%s reads the registry from the snippet as:\n%s\nwant %s", c.row, r.Out, want)
	}
}

func scenarioWhoami(t *testing.T, c *npmClient) {
	dir := c.project("whoami", nil)
	c.configure(dir)
	var r npmRun
	if c.family() == "yarn4" {
		r = c.must(dir, "npm", "whoami")
	} else {
		r = c.must(dir, "whoami")
	}
	if !strings.Contains(r.Out, "sa:"+npmPublisher) {
		t.Fatalf("%s whoami printed %q, want the publisher sa:%s", c.row, strings.TrimSpace(r.Out), npmPublisher)
	}
}

// scenarioPing is E2.7: the ping of the registry succeeds. The dev profile serves TLS only (R-ARCH-035), so the
// URL is https; the roadmap's http URL is that of a profile without TLS.
func scenarioPing(t *testing.T, c *npmClient) {
	dir := c.project("ping", nil)
	c.configure(dir)
	r := c.must(dir, "ping")
	if !strings.Contains(r.Out, "PONG") && c.family() == "npm" {
		t.Fatalf("npm ping did not print PONG:\n%s", r.Out)
	}
}

// scenarioPublish: publish a scoped and an unscoped package; the registry then holds both, with the README of the
// package (the document of E2.3 and E2.8's package list read it).
func scenarioPublish(t *testing.T, c *npmClient) {
	plain, scoped := c.ensurePublished()
	for _, name := range []string{plain, scoped} {
		versions, tags, doc := c.fx.versionsOf(t, c.registryKey, name)
		if len(versions) != 1 || versions[0] != "1.0.0" || tags["latest"] != "1.0.0" {
			t.Fatalf("%s after publish: versions %v, dist-tags %v, want 1.0.0 as latest", name, versions, tags)
		}
		if readme, _ := doc["readme"].(string); !strings.Contains(readme, name) {
			t.Fatalf("%s: the document's readme is %q, want the README of the package", name, readme)
		}
	}
}

// scenarioInstallLocal: install the two packages into a project; the files are the published ones.
func scenarioInstallLocal(t *testing.T, c *npmClient) {
	plain, scoped := c.ensurePublished()
	dir := c.project("install-local", nil)
	c.configure(dir)
	c.mustInstall(dir, plain, scoped)
	c.assertMarkers(t, dir, map[string]string{plain: "marker:" + plain + "@1.0.0", scoped: "marker:" + scoped + "@1.0.0"})
}

// addCmd is the row's command that adds packages to a project and installs them.
func (c *npmClient) addCmd(specs ...string) []string {
	switch c.family() {
	case "npm":
		return append([]string{"install"}, specs...)
	default:
		return append([]string{"add"}, specs...)
	}
}

func (c *npmClient) mustInstall(dir string, specs ...string) npmRun {
	c.t.Helper()
	return c.must(dir, c.addCmd(specs...)...)
}

// assertMarkers loads each package in the project with Node.js and compares what it exports.
func (c *npmClient) assertMarkers(t *testing.T, dir string, want map[string]string) {
	t.Helper()
	for name, marker := range want {
		r := c.exec(dir, nil, "node", "-e", fmt.Sprintf("process.stdout.write(String(require(%q)))", name))
		if r.Code != 0 || strings.TrimSpace(r.Out) != marker {
			t.Fatalf("require(%q) in %s: exit %d, printed %q, want %q", name, dir, r.Code, strings.TrimSpace(r.Out), marker)
		}
	}
}

// scenarioInstallRemote: a package the remote repository serves is installed through the registry. Waits for WP2.5.
func scenarioInstallRemote(t *testing.T, c *npmClient) {
	dir := c.project("install-remote", nil)
	c.configure(dir)
	const plain, scoped = "@cobalt/dusky-gauge", "left-pad"
	c.mustInstall(dir, plain, scoped)
	for _, name := range []string{plain, scoped} {
		if r := c.exec(dir, nil, "node", "-e", fmt.Sprintf("require(%q)", name)); r.Code != 0 {
			t.Fatalf("require(%q): %s", name, r.Out)
		}
	}
}

// distTagCmds are the add, list and remove commands of the row for a package version and a tag.
func (c *npmClient) distTagCmds(name, version, tag string) (add, list, rm []string) {
	switch c.family() {
	case "yarn4":
		return []string{"npm", "tag", "add", name + "@" + version, tag}, []string{"npm", "tag", "list", name}, []string{"npm", "tag", "remove", name, tag}
	case "yarn1":
		return []string{"tag", "add", name + "@" + version, tag}, []string{"tag", "list", name}, []string{"tag", "remove", name, tag}
	default:
		return []string{"dist-tag", "add", name + "@" + version, tag}, []string{"dist-tag", "ls", name}, []string{"dist-tag", "rm", name, tag}
	}
}

func scenarioDistTag(t *testing.T, c *npmClient) {
	plain, _ := c.ensurePublished()
	dir := c.project("dist-tag", nil)
	c.configure(dir)
	add, list, rm := c.distTagCmds(plain, "1.0.0", "beta")
	c.must(dir, add...)
	if _, tags, _ := c.fx.versionsOf(t, c.registryKey, plain); tags["beta"] != "1.0.0" || tags["latest"] != "1.0.0" {
		t.Fatalf("after add: dist-tags %v, want beta and latest on 1.0.0", tags)
	}
	if r := c.must(dir, list...); !strings.Contains(r.Out, "beta") || !strings.Contains(r.Out, "1.0.0") {
		t.Fatalf("%s lists %q, want beta: 1.0.0", strings.Join(list, " "), r.Out)
	}
	c.must(dir, rm...)
	if _, tags, _ := c.fx.versionsOf(t, c.registryKey, plain); tags["beta"] != "" || tags["latest"] != "1.0.0" {
		t.Fatalf("after remove: dist-tags %v, want latest only", tags)
	}
}

// expectRefusal checks a command the registry answers with the npm error of R-NPM-043: it fails, the status is
// in the output, it ends fast, and the client did not retry.
func (c *npmClient) expectRefusal(t *testing.T, what string, r npmRun, status string, within time.Duration) {
	t.Helper()
	if r.Code == 0 {
		t.Fatalf("%s succeeded, want a failure with %s:\n%s", what, status, tailLines(r.Out, 20))
	}
	if !strings.Contains(r.Out, status) {
		t.Fatalf("%s failed without %s in its output:\n%s", what, status, tailLines(r.Out, 20))
	}
	if retryNotice.MatchString(r.Out) {
		t.Fatalf("%s retried:\n%s", what, tailLines(r.Out, 20))
	}
	if r.Dur > within {
		t.Fatalf("%s took %s, want a refusal within %s (the client retried a response it should not):\n%s", what, r.Dur.Round(time.Millisecond), within, tailLines(r.Out, 12))
	}
}

// scenarioDeprecate: deprecate is P1 (R-NPM-018). The registry refuses the metadata update the way R-NPM-043
// prescribes for a PUT, so npm does not retry it.
func scenarioDeprecate(t *testing.T, c *npmClient) {
	plain, _ := c.ensurePublished()
	dir := c.project("deprecate", nil)
	c.configure(dir)
	r := c.run(dir, "deprecate", plain+"@1.0.0", "use another")
	c.expectRefusal(t, c.row+" deprecate", r, "404", 20*time.Second)
	if _, _, doc := c.fx.versionsOf(t, c.registryKey, plain); fmt.Sprint(doc["versions"].(map[string]any)["1.0.0"].(map[string]any)["deprecated"]) != "<nil>" {
		t.Fatalf("the refused deprecate left a message on 1.0.0")
	}
}

// scenarioUnpublish: npm and pnpm unpublish one version; the registry then no longer lists it and the tarball is
// gone (R-NPM-017, E2.10's flow on one replica per request).
func scenarioUnpublish(t *testing.T, c *npmClient) {
	name := c.pkg("unpub", false)
	c.publish(name, "1.0.0")
	c.publish(name, "1.1.0")
	// pnpm 11 and 12 read the package with a plain GET and send its _rev back; through a virtual repository that _rev
	// is the merge's synthetic one, which R-NPM-009 says cannot be written through (only ?write=true returns the
	// deployment target's, which npm and pnpm 10, whose unpublish is npm's, use). Their unpublish therefore goes to
	// the deployment target itself (Q-NPM-49).
	if (c.row == "pnpm11" || c.row == "pnpm12") && c.registryKey != npmLocalKey {
		t.Logf("%s unpublishes at %s, the deployment target of %s (Q-NPM-49)", c.row, npmLocalKey, c.registryKey)
		viaTarget := *c
		viaTarget.registryKey = npmLocalKey
		c = &viaTarget
	}
	dir := c.project("unpublish", nil)
	c.configure(dir)
	args := []string{"unpublish", name + "@1.1.0"}
	if c.family() == "pnpm" {
		args = append(args, "--force")
	}
	c.must(dir, args...)
	versions, tags, _ := c.fx.versionsOf(t, c.registryKey, name)
	if len(versions) != 1 || versions[0] != "1.0.0" || tags["latest"] != "1.0.0" {
		t.Fatalf("after unpublish of 1.1.0: versions %v, dist-tags %v, want 1.0.0 as latest", versions, tags)
	}
	r := c.fx.adm.withToken(c.fx.token).get("/api/npm/" + c.registryKey + "/" + url.PathEscape(name) + "/-/" + name + "-1.1.0.tgz")
	if r.Status != 404 && r.Status != 410 {
		t.Fatalf("the tarball of the unpublished version answers %s, want 404 or 410", r)
	}
}

// scenarioLogin: login is P1 (R-NPM-020, R-NPM-021), so the registry answers its endpoints the way R-NPM-043
// prescribes and the client fails at once with the registry's error instead of hanging or retrying. The clients ask
// for the user name and the password only at a terminal, so they run under a pseudo-terminal with typed answers.
// The status the client reports depends on whether it sends its credential to the login endpoint: npm does and gets
// the 501 of the POST and the 404 of the PUT; pnpm, Yarn 2+ and Yarn 1 log in anonymously and, at a repository that
// requires authentication, get the 401 of the endpoint's authentication gate (R-NPM-004) first.
func scenarioLogin(t *testing.T, c *npmClient) {
	dir := c.project("login", nil)
	c.configure(dir)
	answers := []string{"someone", "not-a-token", "someone@example.com"}
	var runs []struct {
		what string
		r    npmRun
	}
	add := func(what string, r npmRun) {
		runs = append(runs, struct {
			what string
			r    npmRun
		}{what, r})
	}
	switch c.family() {
	case "npm":
		add("login (web)", c.run(dir, "login"))
		add("login --auth-type=legacy", c.execPTY(dir, answers, c.row, "login", "--auth-type=legacy"))
	case "pnpm":
		add("login", c.execPTY(dir, answers, c.row, "login"))
	case "yarn4":
		add("npm login", c.execPTY(dir, answers, c.row, "npm", "login"))
	case "yarn1":
		add("login", c.execPTY(dir, answers, c.row, "login"))
	}
	for _, x := range runs {
		what := c.row + " " + x.what
		if x.r.Code == 0 {
			t.Fatalf("%s succeeded, the registry has no login (P1):\n%s", what, tailLines(x.r.Out, 20))
		}
		if !loginRefusal.MatchString(x.r.Out) {
			t.Fatalf("%s failed without the registry's refusal (401, 404 or 501) in its output:\n%s", what, tailLines(x.r.Out, 20))
		}
		if retryNotice.MatchString(x.r.Out) || x.r.Dur > 30*time.Second {
			t.Fatalf("%s took %s or retried, want an immediate refusal:\n%s", what, x.r.Dur.Round(time.Millisecond), tailLines(x.r.Out, 20))
		}
	}
}

// retryNotice matches what the clients print when they repeat a request ("Will retry in 10 seconds", "retrying", npm's
// "attempt 2"). A Yarn error dump that lists its own retry settings is not one.
var retryNotice = regexp.MustCompile(`(?i)will retry|retrying|retry in|attempt \d`)

// loginRefusal matches what a client prints of the registry's refusal of a login: the status, or Yarn's text for it.
var loginRefusal = regexp.MustCompile(`\b(401|404|501)\b|Invalid authentication|Not Implemented|requires an authenticated`)

// scenarioAudit (R-NPM-038): the row's audit through npm-all reports the test upstream's advisories for the remote
// packages it installed (left-pad 1900001, @cobalt/dusky-gauge 1900003, test/npmstub/testdata/advisories.json), and the
// row's own package, which npm-local holds, never reaches the provider; npm prints the notice that names the withheld
// name. npm's audit against npm-local fails with the 501 of R-NPM-038 and its text, and the install there succeeds.
// pnpm 10 speaks npm 6's audit protocol (audits/quick, then audits), which trove answers with 501 (Q-NPM-65): pnpm
// retries a 501 after 10 seconds and a minute for each of the two, so the row reports the 501 after about 140 seconds.
func scenarioAudit(t *testing.T, c *npmClient) {
	plain, _ := c.ensurePublished()
	dir := c.project("audit", nil)
	c.configure(dir)
	c.mustInstall(dir, plain, "left-pad", "@cobalt/dusky-gauge")
	var r npmRun
	if c.family() == "yarn4" {
		r = c.run(dir, "npm", "audit")
	} else {
		r = c.run(dir, "audit")
	}
	if c.row == "pnpm10" {
		if r.Code == 0 || !strings.Contains(r.Out, "501") || !strings.Contains(r.Out, "bulk advisory endpoint") {
			t.Fatalf("%s audit: exit %d, want a failure that reports the registry's 501 for npm 6's endpoints:\n%s", c.row, r.Code, tailLines(r.Out, 15))
		}
		return
	}
	// Every client exits non-zero when it found an advisory.
	if r.Code == 0 || !strings.Contains(r.Out, "1900001") || !strings.Contains(r.Out, "1900003") {
		t.Fatalf("%s audit: exit %d, want the advisories 1900001 (left-pad) and 1900003 (@cobalt/dusky-gauge):\n%s", c.row, r.Code, tailLines(r.Out, 40))
	}
	if c.family() == "npm" && !strings.Contains(r.Out, "trove audited") {
		t.Errorf("%s printed no npm-notice of the withheld name %s:\n%s", c.row, plain, tailLines(r.Out, 20))
	}
	log := stub(t).audits(t)
	if npmstub.Asked(log, plain) || !npmstub.Asked(log, "left-pad") {
		t.Fatalf("the provider's log asked about %s: %v, about left-pad: %v; want only the remote's names", plain, npmstub.Asked(log, plain), npmstub.Asked(log, "left-pad"))
	}
	if c.family() != "npm" {
		return
	}
	local := *c
	local.registryKey = npmLocalKey
	ldir := local.project("audit-local", nil)
	local.configure(ldir)
	local.mustInstall(ldir, plain)
	lr := local.run(ldir, "audit")
	c.expectRefusal(t, c.row+" audit at npm-local", lr, "501", 30*time.Second)
	if !strings.Contains(lr.Out, "security audit is not available on repository "+npmLocalKey) {
		t.Fatalf("%s audit at npm-local does not print the text of R-NPM-038:\n%s", c.row, tailLines(lr.Out, 15))
	}
}

// scenarioSearch: search is P1 (R-NPM-037); the endpoint answers 404 as R-NPM-043 prescribes for a GET.
func scenarioSearch(t *testing.T, c *npmClient) {
	dir := c.project("search", nil)
	c.configure(dir)
	r := c.run(dir, "search", "e2e")
	c.expectRefusal(t, c.row+" search", r, "404", 20*time.Second)
}

// scenarioDeferred is the acceptance criterion of R-NPM-043: the commands of endpoints the registry does not
// implement fail without retries and within a few seconds (the criterion says one second of the registry's time; the
// harness adds the process start of the client and kubectl).
func scenarioDeferred(t *testing.T, c *npmClient) {
	plain, _ := c.ensurePublished()
	dir := c.project("deferred", nil)
	c.configure(dir)
	for _, cmd := range [][]string{
		{"token", "list"},
		{"profile", "get"},
		{"org", "ls", "acme"},
		{"access", "get", "status", plain},
		{"owner", "add", "someone", plain},
	} {
		r := c.run(dir, append(cmd, "--loglevel=verbose")...)
		c.expectRefusal(t, c.row+" "+strings.Join(cmd, " "), r, "40", 8*time.Second)
	}
}

// wipeClientState removes the row's installed packages and its caches: the "empty client cache" of R-NPM-045.
func (c *npmClient) wipeClientState(dir string) {
	c.t.Helper()
	h := c.home()
	c.sh(dir, `rm -rf node_modules .yarn/cache .yarn/install-state.gz "`+h+`/npm-cache" "`+h+`/pnpm-store" "`+h+`/pnpm-cache" "`+h+`/yarn-cache" "`+h+`/.cache"`)
}

// frozenInstall is the row's lockfile install.
func (c *npmClient) frozenInstall() []string {
	switch c.family() {
	case "npm":
		return []string{"ci"}
	case "pnpm":
		return []string{"install", "--frozen-lockfile"}
	case "yarn4":
		return []string{"install", "--immutable"}
	default:
		return []string{"install", "--frozen-lockfile"}
	}
}

// scenarioLockfileReinstall: install, delete the packages and every cache, install again from the lockfile alone.
func scenarioLockfileReinstall(t *testing.T, c *npmClient) {
	plain, scoped := c.ensurePublished()
	dir := c.project("lockfile", nil)
	c.configure(dir)
	c.mustInstall(dir, plain, scoped)
	c.wipeClientState(dir)
	c.must(dir, c.frozenInstall()...)
	c.assertMarkers(t, dir, map[string]string{plain: "marker:" + plain + "@1.0.0", scoped: "marker:" + scoped + "@1.0.0"})
}

// scenarioYarn4Lockfile (R-NPM-042): the tarball URLs of the registry are conventional, so yarn.lock holds no URL of
// the registry.
func scenarioYarn4Lockfile(t *testing.T, c *npmClient) {
	plain, scoped := c.ensurePublished()
	dir := c.project("yarn4-lockfile", nil)
	c.configure(dir)
	c.mustInstall(dir, plain, scoped)
	lock := c.read(dir + "/yarn.lock")
	for _, bad := range []string{"localhost", "/api/npm/", "https://"} {
		if strings.Contains(lock, bad) {
			t.Fatalf("yarn.lock names the registry (%q), tarball URLs are not conventional:\n%s", bad, lock)
		}
	}
	if !strings.Contains(lock, plain) || !strings.Contains(lock, scoped) {
		t.Fatalf("yarn.lock lacks the packages:\n%s", lock)
	}
}

// scenarioYarn4Switch (R-NPM-042): the lockfile of an install through npm-all works against npm-remote. Waits for WP2.5.
func scenarioYarn4Switch(t *testing.T, c *npmClient) {
	dir := c.project("yarn4-switch", nil)
	c.configure(dir)
	c.mustInstall(dir, "@cobalt/dusky-gauge")
	c.write(dir+"/.yarnrc.yml", strings.NewReplacer(npmAllKey, npmRemoteKey).Replace(c.read(dir+"/.yarnrc.yml")))
	c.wipeClientState(dir)
	c.must(dir, "install", "--immutable")
}

// scenarioPnpmWorkspace (R-NPM-040): a workspace of 3 internal and 300 public packages installs with an empty store
// and again with a warm store and --frozen-lockfile. Waits for WP2.5, the public packages are the remote's.
func scenarioPnpmWorkspace(t *testing.T, c *npmClient) {
	dir := c.workspace("workspace")
	c.must(dir, "install")
	c.must(dir, "install", "--frozen-lockfile")
	c.sh(dir, `rm -rf node_modules packages/*/node_modules`)
	c.must(dir, "install", "--frozen-lockfile")
}

// workspace creates the pnpm workspace of R-NPM-040 in the pod: three internal packages that depend on one another
// through workspace: ranges and 300 public packages of the test upstream's corpus, 100 for each.
func (c *npmClient) workspace(name string) string {
	c.t.Helper()
	dir := c.project(name, map[string]any{"name": "e2e-workspace-" + c.fx.run})
	c.configure(dir)
	names := corpus.Default().Names()
	if len(names) < 300 {
		c.t.Fatalf("the corpus has %d packages, the workspace needs 300", len(names))
	}
	c.write(dir+"/pnpm-workspace.yaml", "packages:\n  - 'packages/*'\n")
	internal := []string{"core", "web", "cli"}
	for i, in := range internal {
		deps := map[string]string{}
		if i > 0 {
			deps["@e2e/ws-"+c.fx.run+"-"+internal[i-1]] = "workspace:*"
		}
		for _, n := range names[i*100 : (i+1)*100] {
			p, _ := corpus.Default().Get(n)
			deps[n] = "^" + p.DistTags["latest"]
		}
		raw, _ := json.MarshalIndent(map[string]any{"name": "@e2e/ws-" + c.fx.run + "-" + in, "version": "1.0.0", "private": true, "dependencies": deps}, "", "  ")
		c.write(dir+"/packages/"+in+"/package.json", string(raw)+"\n")
	}
	return dir
}
