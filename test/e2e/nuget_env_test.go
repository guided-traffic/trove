//go:build e2e

package e2e

// The harness of the NuGet acceptance tests (M3, R-NUGET-027): the local repositories of the installation, a publisher
// with tokens, the clients of nuget_clients_test.go driven through `kubectl exec`, and the fixture packages, which the
// real .NET SDK packs (`dotnet pack`, as R-NUGET-027 asks). The scenarios are in nuget_local_test.go (the write path
// and the clients' reads, E3.1 Linux part, E3.5), nuget_replicas_test.go (E3.6, the documents on all replicas, the dropped
// answer), nuget_remote_test.go, nuget_virtual_test.go and nuget_collapse_test.go (E3.2) against the counting test upstream
// (nuget_upstream_test.go), nuget_endpoint_test.go (E3.7) and nuget_closing_test.go (Valkey stopped). docs/developer/e2e.md
// describes the whole.
//
// Everything a client needs beyond the base URL (a cache directory, no telemetry) goes through the environment of the
// command; the registry, the source and the credentials come from a nuget.config of the scenario's directory, written
// the way the setup snippets of R-NUGET-026 print it (`<clear />`, `%TROVE_TOKEN%`).

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	nugetAuthKey = "nuget-e2e"       // requireAuthentication: true; the repository of the clients with credentials
	nugetOpenKey = "nuget-e2e-open"  // anonymous read: the repository a client reaches with the API key alone
	nugetHardKey = "nuget-e2e-hard"  // deleteMode delete: the protocol DELETE removes the version
	nugetCustKey = "nuget-customers" // the repository the customers endpoint exposes through rules (E3.7)
	nugetPubSA   = "e2e-nuget"       // the publisher: read, deploy and delete on the four repositories
)

// nugetFixture is the installation the NuGet tests share: created once per run, idempotent over runs.
type nugetFixture struct {
	adm   *api
	token string // the publisher's token
	run   string // six hex digits, part of every package ID this run publishes
}

var (
	nugetFxOnce sync.Once
	nugetFxVal  *nugetFixture
	nugetFxErr  error
)

// nugetFx puts the cluster into the scatter profile and creates the repositories and the publisher. The profile is the
// one R-NUGET-027 names for R-NUGET-030, and every test of this file runs in it: several packages replicas, every request to
// another one.
func nugetFx(t testing.TB) *nugetFixture {
	t.Helper()
	useScatter(t)
	nugetFxOnce.Do(func() {
		nugetFxErr = fmt.Errorf("the fixture did not finish (see the first failure)")
		nugetFxVal, nugetFxErr = buildNuGetFixture(t)
	})
	if nugetFxErr != nil {
		t.Fatalf("NuGet fixture: %v", nugetFxErr)
	}
	return nugetFxVal
}

func buildNuGetFixture(t testing.TB) (*nugetFixture, error) {
	adm := adminAPI(t)
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	fx := &nugetFixture{adm: adm, run: hex.EncodeToString(b)}
	for _, body := range []map[string]any{
		{"key": nugetAuthKey, "class": "local", "format": "nuget", "requireAuthentication": true},
		{"key": nugetOpenKey, "class": "local", "format": "nuget"},
		{"key": nugetHardKey, "class": "local", "format": "nuget", "requireAuthentication": true, "nuget": map[string]any{"deleteMode": "delete"}},
		{"key": nugetCustKey, "class": "local", "format": "nuget", "requireAuthentication": true},
	} {
		r, err := adm.send(http.MethodPost, reposRoute, body)
		if err != nil {
			return nil, err
		}
		if r.Status != 201 && r.Status != 409 {
			return nil, fmt.Errorf("create %v: %s", body["key"], r)
		}
	}
	ensureSA(t, adm, nugetPubSA)
	ensurePolicy(t, adm, nugetPubSA, []string{nugetAuthKey, nugetOpenKey, nugetHardKey, nugetCustKey}, [2]any{"sa:" + nugetPubSA, []string{"read", "deploy", "delete"}})
	ensurePolicy(t, adm, nugetPubSA+"-anonymous", []string{nugetOpenKey}, [2]any{"anonymous", []string{"read"}})
	fx.token = freshSAToken(t, adm, nugetPubSA, 4*3600, map[string]any{"inherit": true}).Token
	return fx, nil
}

// nugetFeed is the address of a repository as the setup snippets print it (R-NUGET-026): the edge's address and the
// service index, never a v2 address.
func nugetFeed(t testing.TB, host, key string) string {
	return "https://" + host + ":" + edgePort(t) + nugetBase + key + "/v3/index.json"
}

// pkgID is a package ID of this run: unique per scenario group and run, so a rerun on the same cluster never meets a
// version that exists (and a tombstone never meets a push).
func (fx *nugetFixture) pkgID(group string) string { return "E2e.Nuget" + fx.run + "." + group }

// withAnonymousRead turns the runtime switch anonymousAccess on (R-AUTH-040) for the test and puts the previous value
// back at its end. The repository nuget-e2e-open grants `read` to anonymous; nothing else of the installation does, and
// the other repositories of this suite force authentication.
func (fx *nugetFixture) withAnonymousRead(t *testing.T) {
	t.Helper()
	cur := fx.adm.get("/api/v1/settings/auth").want(t, "read the authentication settings", 200)
	var s struct {
		AnonymousAccess bool `json:"anonymousAccess"`
	}
	cur.JSON(t, &s)
	if s.AnonymousAccess {
		return
	}
	set := func(on bool) reply {
		g := fx.adm.get("/api/v1/settings/auth").want(t, "read the authentication settings", 200)
		return fx.adm.do(http.MethodPatch, "/api/v1/settings/auth", map[string]any{"anonymousAccess": on}, "If-Match", g.Header.Get("ETag"), "Content-Type", "application/merge-patch+json")
	}
	set(true).want(t, "anonymous access on", 200)
	t.Cleanup(func() { set(false).want(t, "anonymous access off", 200) })
	// the switch is in force on every replica within the revocation bound: wait until all three answer the anonymous read
	feed := nugetBase + nugetOpenKey + "/v3/index.json"
	seen := map[string]bool{}
	waitFor(t, "anonymous read of the open feed on every packages replica", time.Minute, time.Second, func() bool {
		anon := newAPI(t)
		for range 6 {
			r, err := anon.send(http.MethodGet, feed, nil)
			if err != nil || r.Status != 200 {
				clear(seen)
				return false
			}
			seen[r.Replica()] = true
		}
		return len(seen) >= 3
	})
}

// ── the clients ──────────────────────────────────────────────────────────────

// nugetClient is one scenario in one clients pod. Everything it writes lives below /work/<tag> in the pod: the
// scenario's files, NUGET_PACKAGES and NUGET_HTTP_CACHE_PATH (Q-NUGET-35), so no scenario meets the cache of another.
type nugetClient struct {
	t   testing.TB
	fx  *nugetFixture
	pod int
	tag string
	// token is what %TROVE_TOKEN% expands to in the scenario's nuget.config.
	token string
}

// client returns the scenario of the name in the pod i%pods, with the publisher's token.
func (fx *nugetFixture) client(t testing.TB, pod int, name string) *nugetClient {
	t.Helper()
	return &nugetClient{t: t, fx: fx, pod: pod, tag: name + "-" + fx.run, token: fx.token}
}

func (c *nugetClient) dir() string      { return "/work/" + c.tag }
func (c *nugetClient) packages() string { return c.dir() + "/packages" }

// argv wraps a command: its working directory is the scenario's, its environment the scenario's caches and token.
func (c *nugetClient) argv(cmd ...string) []string {
	return append([]string{
		"env", "-C", c.dir(),
		"NUGET_PACKAGES=" + c.packages(),
		"NUGET_HTTP_CACHE_PATH=" + c.dir() + "/httpcache",
		"NUGET_PLUGINS_CACHE_PATH=" + c.dir() + "/plugins",
		"TROVE_TOKEN=" + c.token,
	}, cmd...)
}

// exec runs a command that must succeed and returns its standard output.
func (c *nugetClient) exec(cmd ...string) string {
	c.t.Helper()
	c.mkdir()
	return nugetClientExec(c.t, c.pod, c.argv(cmd...)...)
}

// try runs a command and returns its output (both streams) and the error of a non-zero exit.
func (c *nugetClient) try(cmd ...string) (string, error) {
	c.t.Helper()
	c.mkdir()
	return nugetClientTry(c.t, c.pod, c.argv(cmd...)...)
}

func (c *nugetClient) mkdir() {
	c.t.Helper()
	nugetClientExec(c.t, c.pod, "mkdir", "-p", c.dir())
}

// write puts a file below the scenario's directory.
func (c *nugetClient) write(rel string, content []byte) {
	c.t.Helper()
	nugetClientExec(c.t, c.pod, "sh", "-c", `mkdir -p "$(dirname "$1")" && printf %s "$2" | base64 -d >"$1"`,
		"sh", c.dir()+"/"+rel, base64.StdEncoding.EncodeToString(content))
}

// config writes nuget.config the way the snippets print it. A repository that is not anonymously readable gets the
// credentials section: user name token, password %TROVE_TOKEN%.
func (c *nugetClient) config(feed string, credentials bool) {
	c.t.Helper()
	creds := ""
	if credentials {
		creds = `
  <packageSourceCredentials>
    <trove>
      <add key="Username" value="token" />
      <add key="ClearTextPassword" value="%TROVE_TOKEN%" />
    </trove>
  </packageSourceCredentials>`
	}
	c.write("nuget.config", []byte(`<?xml version="1.0" encoding="utf-8"?>
<configuration>
  <packageSources>
    <clear />
    <add key="trove" value="`+feed+`" protocolVersion="3" />
  </packageSources>`+creds+`
</configuration>
`))
}

// consumer writes a project that references one package, and returns its file name.
func (c *nugetClient) consumer(name, id, version string) string {
	c.t.Helper()
	file := name + ".csproj"
	c.write(file, []byte(`<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup><TargetFramework>net10.0</TargetFramework><NuGetAudit>false</NuGetAudit></PropertyGroup>
  <ItemGroup><PackageReference Include="`+id+`" Version="`+version+`" /></ItemGroup>
</Project>
`))
	return file
}

// hasPackage says whether the scenario's NUGET_PACKAGES holds the package: the restore extracted it.
func (c *nugetClient) hasPackage(id, version string) bool {
	c.t.Helper()
	low := strings.ToLower
	_, err := c.try("test", "-f", c.packages()+"/"+low(id)+"/"+low(version)+"/"+low(id)+"."+low(version)+".nupkg")
	return err == nil
}

// ── the fixture packages ─────────────────────────────────────────────────────

type packed struct{ nupkg, snupkg []byte }

var (
	packMu    sync.Mutex
	packCache = map[string]packed{}
)

// nugetPack builds a package with the pod's .NET SDK (`dotnet pack` of a project with one class) and returns the
// archive. The same arguments return the same bytes (a replay needs them); another description gives other bytes of
// the same version (a conflict needs them).
func (fx *nugetFixture) nugetPack(t testing.TB, id, version, description string) []byte {
	t.Helper()
	return fx.pack(t, 0, id, version, description, false).nupkg
}

// nugetPackSymbols is nugetPack on clients pod `pod` with the symbol package next to it (the snupkg format):
// `dotnet nuget push` pushes a .snupkg that lies next to the .nupkg by itself (R-NUGET-020). Packs of different
// pods run side by side; the cache is locked only for the lookup and the store.
func (fx *nugetFixture) nugetPackSymbols(t testing.TB, pod int, id, version, description string) (nupkg, snupkg []byte) {
	t.Helper()
	p := fx.pack(t, pod, id, version, description, true)
	return p.nupkg, p.snupkg
}

func (fx *nugetFixture) pack(t testing.TB, pod int, id, version, description string, symbols bool) packed {
	t.Helper()
	key := fmt.Sprintf("%s|%s|%s|%t", id, version, description, symbols)
	packMu.Lock()
	if b, ok := packCache[key]; ok {
		packMu.Unlock()
		return b
	}
	n := len(packCache)
	packMu.Unlock()
	c := fx.client(t, pod, "pack-"+strings.ToLower(strings.ReplaceAll(id, ".", "-"))+"-"+version+"-"+fmt.Sprint(n)+"-"+fmt.Sprint(time.Now().UnixNano()%100000))
	extra := ""
	if symbols {
		extra = "\n    <IncludeSymbols>true</IncludeSymbols>\n    <SymbolPackageFormat>snupkg</SymbolPackageFormat>\n    <DebugType>portable</DebugType>"
	}
	c.write("pack.csproj", []byte(`<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <TargetFramework>net10.0</TargetFramework>
    <PackageId>`+id+`</PackageId>
    <Version>`+version+`</Version>
    <Authors>trove end-to-end</Authors>
    <Description>`+xmlText(description)+`</Description>
    <NuGetAudit>false</NuGetAudit>`+extra+`
  </PropertyGroup>
</Project>
`))
	c.write("Marker.cs", []byte(`namespace E2e { public static class Marker { public const string Id = "`+id+`"; } }`))
	c.write("nuget.config", []byte(`<?xml version="1.0" encoding="utf-8"?><configuration><packageSources><clear /></packageSources></configuration>`))
	if out, err := c.try("dotnet", "pack", "pack.csproj", "-c", "Release", "-o", "out", "--nologo"); err != nil {
		t.Fatalf("dotnet pack of %s %s: %v\n%s", id, version, err, out)
	}
	read := func(ext string) []byte {
		raw := c.exec("base64", "-w0", "out/"+id+"."+version+ext)
		b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
		if err != nil || len(b) == 0 {
			t.Fatalf("dotnet pack of %s %s: no %s (%v)", id, version, ext, err)
		}
		return b
	}
	out := packed{nupkg: read(".nupkg")}
	if symbols {
		out.snupkg = read(".snupkg")
	}
	packMu.Lock()
	defer packMu.Unlock()
	if b, ok := packCache[key]; ok {
		return b // another goroutine packed the same package first: its bytes are the ones the run uses
	}
	packCache[key] = out
	return out
}

// place writes the package into the scenario's directory and returns its file name.
func (c *nugetClient) place(id, version string, pkg []byte) string {
	c.t.Helper()
	name := id + "." + version + ".nupkg"
	c.write(name, pkg)
	return name
}

// ── the answers ──────────────────────────────────────────────────────────────

// pushed says that a push command reported success; dotnet answers "Your package was pushed.", nuget.exe "Your package
// was pushed." as well.
var pushedRE = regexp.MustCompile(`(?i)your package was pushed`)

// mustPush runs a push command that has to be accepted.
func (c *nugetClient) mustPush(what string, cmd ...string) {
	c.t.Helper()
	out, err := c.try(cmd...)
	if err != nil || !pushedRE.MatchString(out) {
		c.t.Fatalf("%s: %v\n%s", what, err, out)
	}
}

// mustRefuse runs a command that has to fail with the HTTP status.
func (c *nugetClient) mustRefuse(what, status string, cmd ...string) string {
	c.t.Helper()
	out, err := c.try(cmd...)
	if err == nil || pushedRE.MatchString(out) {
		c.t.Fatalf("%s: want a failure with %s, the command succeeded:\n%s", what, status, out)
	}
	if !strings.Contains(out, status) {
		c.t.Fatalf("%s: want %s in the output, got:\n%s", what, status, out)
	}
	return out
}

// xmlText escapes a text for an element of the fixture project.
func xmlText(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
