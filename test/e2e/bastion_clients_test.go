//go:build e2e

package e2e

// The clients of the Bastion tests (R-BAS-049): npm, pnpm and Yarn 4 from the npm client pods and `dotnet restore` from the
// NuGet client pods, all configured with the snippets the main installation renders for the Bastion (R-BAS-007,
// client-setup?bastion=<name>), run from pods outside the Bastion's namespace through the Bastion's Ingress.

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// clientSnippet is one file of the answer of client-setup.
type clientSnippet struct {
	ID       string `json:"id"`
	Tab      string `json:"tab"`
	FileName string `json:"fileName"`
	Content  string `json:"content"`
}

// setupSnippets asks the main installation for the configure snippets of a client: for the Bastion (R-BAS-007) when the
// registration is named, for the main installation itself otherwise. The credential is the reference of the client's syntax
// (${TROVE_TOKEN}, %TROVE_TOKEN%), which the client expands from its environment.
func (e *basEnv) setupSnippets(t testing.TB, repo, client, bastion string) []clientSnippet {
	t.Helper()
	q := url.Values{"client": {client}, "credentials": {"env"}}
	if bastion != "" {
		q.Set("bastion", bastion)
	}
	var a struct {
		RegistryURL string          `json:"registryUrl"`
		Snippets    []clientSnippet `json:"snippets"`
	}
	e.adm.get(reposBase+repo+"/client-setup?"+q.Encode()).want(t, "client-setup of "+repo+" for "+client+" "+bastion, 200).JSON(t, &a)
	var out []clientSnippet
	for _, s := range a.Snippets {
		if s.Tab == "configure" && s.FileName != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		t.Fatalf("client-setup of %s for %s has no configure file: %+v", repo, client, a)
	}
	return out
}

// ── npm, pnpm, Yarn 4 ────────────────────────────────────────────────────────

var basNPMRows = map[string]string{"npm": "npm11", "pnpm": "pnpm12", "yarn4": "yarn4"}

// npmClient returns a client of the family in client pod `pod`; the tag keeps its directories apart.
func (e *basEnv) npmClient(t testing.TB, family string, pod int, tag string, token string) *npmClient {
	t.Helper()
	row := basNPMRows[family]
	return &npmClient{t: t, row: row, pod: pod, tag: row + "-" + tag + "-" + e.run, token: token, registryKey: basNPMRepo}
}

func setupClientName(family string) string {
	if family == "yarn4" {
		return "yarn"
	}
	return family
}

// configure writes the snippets of the Bastion (or of the main installation) into the project and the home of the client.
func (e *basEnv) configureNPM(t testing.TB, c *npmClient, dir, bastion string) {
	t.Helper()
	for _, s := range e.setupSnippets(t, basNPMRepo, setupClientName(c.family()), bastion) {
		target := dir + "/" + s.FileName
		if rest, user := strings.CutPrefix(s.FileName, "~/"); user {
			target = c.home() + "/" + rest
		}
		c.write(target, s.Content)
	}
}

// lockCmd writes the lockfile without fetching a tarball.
func (c *npmClient) lockCmd() []string {
	switch c.family() {
	case "pnpm":
		return []string{"install", "--lockfile-only"}
	case "yarn4":
		return []string{"install", "--mode=update-lockfile"}
	}
	return []string{"install", "--package-lock-only"}
}

// basNPMProject creates the project of a set, with its lockfile written against the main installation (the host of the
// lockfile is the main installation's, so the Bastion's `replace-registry-host=always` is on the way), and configures the
// client for the site.
func (e *basEnv) basNPMProject(t testing.TB, c *npmClient, set *basNPMSet, name string, site *basSite) string {
	t.Helper()
	dir := c.project(name, map[string]any{"dependencies": map[string]string{set.root: "1.0.0"}})
	e.configureNPM(t, c, dir, "")
	c.must(dir, c.lockCmd()...)
	if site != nil {
		e.configureNPM(t, c, dir, site.reg)
	}
	return dir
}

// install is the frozen install with an empty client cache; the packages are checked by loading them with Node.js.
func (c *npmClient) basInstall(t *testing.T, dir string, set *basNPMSet) npmRun {
	t.Helper()
	c.wipeClientState(dir)
	// pnpm 11 and later ignore npm_config_store_dir: its store is below the home directory
	c.sh(dir, `rm -rf "`+c.home()+`/.local"`)
	r := c.run(dir, c.frozenInstall()...)
	if r.Code != 0 {
		basDumpLogs(t, t.Name())
		t.Fatalf("%s %s exited %d:\n%s", c.row, strings.Join(c.frozenInstall(), " "), r.Code, tailLines(r.Out, 30))
	}
	// pnpm does not hoist a dependency into the project's node_modules: the root is what the project loads, and the install
	// would have failed had a tarball of its dependencies been missing
	c.assertMarkers(t, dir, map[string]string{set.root: set.root + "@1.0.0"})
	if c.family() != "pnpm" {
		c.assertMarkers(t, dir, map[string]string{set.deps[0]: set.deps[0] + "@1.0.0"})
	}
	return r
}

// ── dotnet restore ───────────────────────────────────────────────────────────

// nugetClient returns a scenario in NuGet client pod `pod`.
func (e *basEnv) nugetClient(t testing.TB, pod int, name, token string) *nugetClient {
	t.Helper()
	return &nugetClient{t: t, pod: pod, tag: "bas-" + name + "-" + e.run, token: token}
}

// configureNuGet writes nuget.config from the snippet of the main installation for the Bastion (or for itself).
func (e *basEnv) configureNuGet(t testing.TB, c *nugetClient, bastion string) {
	t.Helper()
	snips := e.setupSnippets(t, basNuGetRepo, "dotnet", bastion)
	for _, s := range snips {
		if s.FileName == "nuget.config" {
			c.write("nuget.config", []byte(s.Content))
			return
		}
	}
	t.Fatalf("client-setup for dotnet has no nuget.config: %+v", snips)
}

// project writes a project that references every package of the set.
func (set *basNuGetSet) project(c *nugetClient, name string) string {
	var refs strings.Builder
	for _, id := range set.ids {
		fmt.Fprintf(&refs, "    <PackageReference Include=%q Version=%q />\n", id, set.version)
	}
	file := name + ".csproj"
	c.write(file, []byte("<Project Sdk=\"Microsoft.NET.Sdk\">\n  <PropertyGroup><TargetFramework>net10.0</TargetFramework><NuGetAudit>false</NuGetAudit></PropertyGroup>\n  <ItemGroup>\n"+refs.String()+"  </ItemGroup>\n</Project>\n"))
	return file
}

// restore runs `dotnet restore` with an empty client cache and checks that every package was extracted.
func (set *basNuGetSet) restore(t testing.TB, c *nugetClient, project string) {
	t.Helper()
	c.mkdir()
	if out, err := c.try("sh", "-c", "rm -rf packages httpcache obj plugins"); err != nil {
		t.Fatalf("wipe the NuGet client cache: %v\n%s", err, out)
	}
	if out, err := c.try("dotnet", "restore", project, "--nologo"); err != nil {
		t.Fatalf("dotnet restore: %v\n%s", err, tailLines(out, 30))
	}
	for _, id := range set.ids {
		if !c.hasPackage(id, set.version) {
			t.Fatalf("dotnet restore did not extract %s %s", id, set.version)
		}
	}
}

// buildNuGet packs and pushes the packages of the run to the main installation.
func (e *basEnv) buildNuGet(t testing.TB) {
	t.Helper()
	fx := &nugetFixture{adm: e.adm, run: e.run}
	set := &basNuGetSet{version: "1.0.0"}
	for i := 1; i <= 6; i++ {
		set.ids = append(set.ids, fmt.Sprintf("Bas%s.Pkg%02d", e.run, i))
	}
	e.nuget = set
	feed := nugetFeed(t, "localhost", basNuGetRepo)
	var wg sync.WaitGroup
	errc := make(chan string, len(set.ids))
	// one pod runs one dotnet at a time: two at once in a fresh pod race for the runtime's shared memory directory
	for pod := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i, id := range set.ids {
				if i%2 != pod {
					continue
				}
				c := e.nugetClient(t, pod, fmt.Sprintf("pub%d", i), e.pub)
				c.config(feed, true)
				file := c.place(id, set.version, fx.pack(t, pod, id, set.version, "bastion end-to-end "+id, false).nupkg)
				out, err := c.dotnetPush(file, feed, e.pub)
				if err != nil || !pushedRE.MatchString(out) {
					errc <- fmt.Sprintf("push %s: %v\n%s", id, err, tailLines(out, 20))
				}
			}
		}()
	}
	wg.Wait()
	close(errc)
	for msg := range errc {
		t.Fatal(msg)
	}
	// every package that was pushed is listed by the flat container of the main installation
	for _, id := range set.ids {
		path := nugetBase + basNuGetRepo + "/v3/flatcontainer/" + strings.ToLower(id) + "/index.json"
		waitFor(t, "the main installation to list "+id+" after its push", 30*time.Second, time.Second, func() bool {
			return e.adm.get(path).Status == 200
		})
	}
}

// ── plain requests ───────────────────────────────────────────────────────────

// npmDocURL is the path of a package document at a Bastion.
func npmDocPath(name string) string {
	return npmBase + basNPMRepo + "/" + strings.ReplaceAll(name, "/", "%2f")
}

// getAt is a GET of the Bastion from this machine with a token; a transport error is a failure.
func (s *basSite) get(t testing.TB, token, path string, h ...string) reply {
	t.Helper()
	return s.api(t, token).do(http.MethodGet, path, nil, h...)
}
