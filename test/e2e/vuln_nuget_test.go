//go:build e2e

package e2e

// The NuGet tests of the vulnerability scanning (R-VULN-029, E4b.4, E4b.5): `dotnet restore` of the .NET 8, 9 and 10 SDKs of
// the clients image reports the audit warnings NU1903 and NU1904 from trove's own data, `NuGetAuditSuppress` removes the warning,
// and a restore of a version blocked in triage fails and succeeds again after the lift.

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
)

// vulnSDK is one .NET SDK of the clients image: global.json picks the newest feature band of its major, the project targets
// its framework (nothing is compiled: a restore reads the project's package references).
type vulnSDK struct{ label, tfm string }

var vulnSDKs = []vulnSDK{{"8", "net8.0"}, {"9", "net9.0"}, {"10", "net10.0"}}

const (
	vulnNuGetAdvisory = "https://github.com/advisories/GHSA-aaaa-aaaa-0101" // the url of the advisory of Newtonsoft.Json 13.0.1 (R-VULN-018)
	vulnNuGetMalURL   = "https://osv.dev/vulnerability/MAL-2026-1301"       // no GHSA identifier: the OSV page
)

// vulnConfig writes nuget.config with the sources and their credentials (the setup snippet's form, one credentials element per
// source key).
func (c *nugetClient) vulnConfig(sources ...[2]string) {
	c.t.Helper()
	var src, creds strings.Builder
	for _, s := range sources {
		fmt.Fprintf(&src, "    <add key=%q value=%q protocolVersion=\"3\" />\n", s[0], s[1])
		fmt.Fprintf(&creds, "    <%s>\n      <add key=\"Username\" value=\"token\" />\n      <add key=\"ClearTextPassword\" value=\"%%TROVE_TOKEN%%\" />\n    </%s>\n", s[0], s[0])
	}
	c.write("nuget.config", []byte("<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<configuration>\n  <packageSources>\n    <clear />\n"+src.String()+
		"  </packageSources>\n  <packageSourceCredentials>\n"+creds.String()+"  </packageSourceCredentials>\n</configuration>\n"))
}

// vulnProject writes global.json and a project that references one package; extra is added to its item group.
func (c *nugetClient) vulnProject(sdk vulnSDK, id, version, extra string) {
	c.t.Helper()
	c.write("global.json", []byte(`{"sdk": {"version": "`+sdk.label+`.0.100", "rollForward": "latestFeature"}}`))
	c.write("p.csproj", []byte(`<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <TargetFramework>`+sdk.tfm+`</TargetFramework>
    <DisableImplicitFrameworkReferences>true</DisableImplicitFrameworkReferences>
    <NuGetAudit>true</NuGetAudit>
    <NuGetAuditMode>direct</NuGetAuditMode>
    <NuGetAuditLevel>low</NuGetAuditLevel>
  </PropertyGroup>
  <ItemGroup>
    <PackageReference Include="`+id+`" Version="`+version+`" />
`+extra+`  </ItemGroup>
</Project>
`))
}

// restore runs `dotnet restore` with every cache of the scenario bypassed and returns its output and exit error.
func (c *nugetClient) restore() (string, error) {
	c.t.Helper()
	return c.try("dotnet", "restore", "p.csproj", "--no-cache", "--force")
}

// clientFor is the scenario `name` of a .NET SDK, in the pod that the SDK's index picks.
func (e *vulnEnv) clientFor(t testing.TB, sdkIndex int, name string) *nugetClient {
	t.Helper()
	pods := nugetClientCount(t)
	return e.nuget.client(t, sdkIndex%pods, name)
}

func nugetClientCount(t testing.TB) int {
	t.Helper()
	n, err := strconv.Atoi(os.Getenv("TROVE_E2E_NUGET_CLIENTS_PODS"))
	if err != nil || n < 1 {
		t.Fatal("TROVE_E2E_NUGET_CLIENTS_PODS is not set; run the vulnerability sets of deploy/dev/e2e.sh")
	}
	return n
}

// TestVuln_R_VULN_018_NuGetRestoreAudit is E4b.5 for NuGet: with every SDK, the restore of a public test version with a high
// advisory reports NU1903 with trove's url through nuget-all, and through nuget-remote with nuget-local as a second source while
// nuget-remote answers no vulnerability information itself; a malicious version reports NU1904; the suppression removes the
// warning.
func TestVuln_R_VULN_018_NuGetRestoreAudit(t *testing.T) {
	e := vulnFx(t)
	feed := func(key string) [2]string { return [2]string{key, nugetFeed(t, "localhost", key)} }
	for i, sdk := range vulnSDKs {
		t.Run(".NET "+sdk.label, func(t *testing.T) {
			t.Parallel()
			t.Run("R-VULN-018 NU1903 through nuget-all", func(t *testing.T) {
				c := e.clientFor(t, i, "vuln-all-"+sdk.label)
				c.vulnConfig(feed(vulnNuGetAll))
				c.vulnProject(sdk, vulnNuGetHi, "13.0.1", "")
				out, err := c.restore()
				if err != nil {
					t.Fatalf("restore through %s: %v\n%s", vulnNuGetAll, err, tailLines(out, 30))
				}
				requireAll(t, ".NET "+sdk.label+" restore through "+vulnNuGetAll, out, "NU1903", vulnNuGetAdvisory)
			})
			t.Run("R-VULN-018 NU1903 through nuget-remote with nuget-local as a second source", func(t *testing.T) {
				c := e.clientFor(t, i, "vuln-two-"+sdk.label)
				c.vulnConfig(feed(vulnNuGetRemote), feed(vulnNuGetLocal))
				c.vulnProject(sdk, vulnNuGetHi, "13.0.1", "")
				out, err := c.restore()
				if err != nil {
					t.Fatalf("restore through %s and %s: %v\n%s", vulnNuGetRemote, vulnNuGetLocal, err, tailLines(out, 30))
				}
				requireAll(t, ".NET "+sdk.label+" restore through "+vulnNuGetRemote+" and "+vulnNuGetLocal, out, "NU1903", vulnNuGetAdvisory)
			})
			t.Run("R-VULN-018 nuget-remote alone reports nothing: it answers no vulnerability information", func(t *testing.T) {
				c := e.clientFor(t, i, "vuln-off-"+sdk.label)
				c.vulnConfig(feed(vulnNuGetRemote))
				c.vulnProject(sdk, vulnNuGetHi, "13.0.1", "")
				out, err := c.restore()
				if err != nil {
					t.Fatalf("restore through %s: %v\n%s", vulnNuGetRemote, err, tailLines(out, 30))
				}
				if strings.Contains(out, "NU1903") {
					t.Fatalf("a restore through %s alone reported an advisory, which it has no data for:\n%s", vulnNuGetRemote, tailLines(out, 20))
				}
			})
			t.Run("R-VULN-018 NU1904 for the malicious test version", func(t *testing.T) {
				// nuget-remote-open does not block a malicious version, so the restore completes and the audit warns about it
				c := e.clientFor(t, i, "vuln-mal-"+sdk.label)
				c.vulnConfig(feed(vulnNuGetRemoteOpen))
				c.vulnProject(sdk, vulnNuGetBad, "1.0.0", "")
				out, err := c.restore()
				if err != nil {
					t.Fatalf("restore of the malicious version through %s: %v\n%s", vulnNuGetRemoteOpen, err, tailLines(out, 30))
				}
				requireAll(t, ".NET "+sdk.label+" restore of "+vulnNuGetBad, out, "NU1904", vulnNuGetMalURL)
			})
			t.Run("R-VULN-018 NuGetAuditSuppress removes the warning", func(t *testing.T) {
				c := e.clientFor(t, i, "vuln-suppress-"+sdk.label)
				c.vulnConfig(feed(vulnNuGetAll))
				c.vulnProject(sdk, vulnNuGetHi, "13.0.1", `    <NuGetAuditSuppress Include="`+vulnNuGetAdvisory+`" />`+"\n")
				out, err := c.restore()
				if err != nil {
					t.Fatalf("restore with the suppression: %v\n%s", err, tailLines(out, 30))
				}
				if strings.Contains(out, "NU1903") {
					t.Fatalf(".NET %s still reports the suppressed advisory:\n%s", sdk.label, tailLines(out, 20))
				}
			})
		})
	}
}

// TestVuln_R_VULN_015_NuGetBlockedRestoreInTriage is E4b.4 for NuGet: a version cached in nuget-remote with a finding that is
// confirmed and blocked is refused by the restore of every SDK with the 403 of R-VULN-016, the same version in nuget-local
// keeps downloading, and the restore succeeds after the block is lifted.
func TestVuln_R_VULN_015_NuGetBlockedRestoreInTriage(t *testing.T) {
	e := vulnFx(t)
	remoteFeed := nugetFeed(t, "localhost", vulnNuGetRemote)
	for i, sdk := range vulnSDKs {
		t.Run(".NET "+sdk.label, func(t *testing.T) {
			t.Parallel()
			id := e.nugetTri[sdk.label]
			c := e.clientFor(t, i, "vuln-tri-"+sdk.label)
			c.vulnConfig([2]string{vulnNuGetRemote, remoteFeed})
			c.vulnProject(sdk, id, "1.0.0", "")
			if out, err := c.restore(); err != nil {
				t.Fatalf("the first restore of %s: %v\n%s", id, err, tailLines(out, 30))
			}
			f := e.finding(t, vulnNuGetRemote, strings.ToLower(id), "1.0.0")
			if f.Blocked || f.Status == "blocked" || f.Status == "dismissed" {
				t.Fatalf("the finding of %s starts as %+v, want an open finding that does not block", id, f)
			}
			e.block(t, e.adm, f.ID)
			c.exec("rm", "-rf", c.packages(), c.dir()+"/obj", c.dir()+"/httpcache")
			out, err := c.restore()
			if err == nil || !strings.Contains(out, "403") {
				t.Fatalf("restore of %s while it is blocked in triage: err %v, want a failure with the 403:\n%s", id, err, tailLines(out, 30))
			}
			e.lift(t, e.adm, f.ID)
			c.exec("rm", "-rf", c.packages(), c.dir()+"/obj", c.dir()+"/httpcache")
			if out, err := c.restore(); err != nil {
				t.Fatalf("restore of %s after the lift: %v\n%s", id, err, tailLines(out, 30))
			}
		})
	}
	t.Run("R-VULN-016 the refusal on the wire: one line, no-store", func(t *testing.T) {
		id := e.nugetTri["replicas"]
		a := e.adm.withToken(e.nuget.token)
		path := nugetBase + vulnNuGetRemote + "/v3/flatcontainer/" + strings.ToLower(id) + "/1.0.0/" + strings.ToLower(id) + ".1.0.0.nupkg"
		a.get(path).want(t, "the first download fills the cache", 200)
		f := e.finding(t, vulnNuGetRemote, strings.ToLower(id), "1.0.0")
		e.block(t, e.adm, f.ID)
		r := a.get(path).want(t, "download of a blocked version", 403)
		if cc := r.Header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
			t.Errorf("Cache-Control of the refusal = %q, want no-store", cc)
		}
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
			t.Errorf("Content-Type of the refusal = %q, want text/plain", ct)
		}
		body := strings.TrimSpace(string(r.Body))
		if !strings.HasPrefix(body, "vulnerability_blocked: trove blocked "+strings.ToLower(id)+"@1.0.0 in "+vulnNuGetRemote+": blocked in triage ("+e.advisoryOf[id]+")") || strings.Contains(body, "\n") {
			t.Errorf("the refusal body = %q, want the one line of R-VULN-016", body)
		}
		e.lift(t, e.adm, f.ID)
		a.get(path).want(t, "download after the lift", 200)
	})
}

// TestVuln_R_VULN_018_NuGetVulnerabilityInfoOnTheWire reads the resource off the wire: nuget-remote (off) answers 404 and does not
// list it, nuget-local and nuget-all list VulnerabilityInfo/6.7.0 with the one entry `trove`, the page is keyed by lowercased
// IDs, carries the severity and url of the rule, is the same on every replica (ETag) and answers 304.
func TestVuln_R_VULN_018_NuGetVulnerabilityInfoOnTheWire(t *testing.T) {
	e := vulnFx(t)
	addrs, stop := packagesForwards(t)
	defer stop()
	var etags []string
	for i, addr := range addrs {
		a := apiAtPod(t, addr, e.nuget.token)
		idx := a.get(nugetBase+vulnNuGetRemote+"/v3/index.json").want(t, "service index of nuget-remote", 200)
		if strings.Contains(string(idx.Body), "VulnerabilityInfo") {
			t.Errorf("replica %d: the service index of nuget-remote lists the vulnerability resource although the mode is off", i)
		}
		a.get(nugetBase+vulnNuGetRemote+"/v3/vulnerabilities/index.json").want(t, "vulnerabilities of nuget-remote (off)", 404)
		for _, key := range []string{vulnNuGetLocal, vulnNuGetAll} {
			si := a.get(nugetBase+key+"/v3/index.json").want(t, "service index of "+key, 200)
			requireAll(t, "the service index of "+key, string(si.Body), "VulnerabilityInfo/6.7.0")
			ix := a.get(vulnResourcePath(t, si, "VulnerabilityInfo/6.7.0")).want(t, "vulnerability index of "+key, 200)
			var entries []struct {
				Name string `json:"@name"`
				ID   string `json:"@id"`
			}
			ix.JSON(t, &entries)
			if len(entries) != 1 || entries[0].Name != "trove" {
				t.Fatalf("the vulnerability index of %s = %s, want one entry named trove", key, ix.Body)
			}
		}
		pagePath := vulnPagePath(t, a, vulnNuGetAll)
		page := a.get(pagePath).want(t, "vulnerability page", 200)
		var doc map[string][]struct {
			Severity int    `json:"severity"`
			URL      string `json:"url"`
			Versions string `json:"versions"`
		}
		page.JSON(t, &doc)
		hi := doc["newtonsoft.json"]
		if len(hi) != 1 || hi[0].Severity != 2 || hi[0].URL != vulnNuGetAdvisory || hi[0].Versions != "(, 13.0.3)" {
			t.Errorf("replica %d: the entry of Newtonsoft.Json = %+v, want severity 2, the GHSA url, (, 13.0.3)", i, hi)
		}
		mal := doc["contoso.json.codecs"]
		if len(mal) != 1 || mal[0].Severity != 3 || mal[0].Versions != "[1.0.0]" {
			t.Errorf("replica %d: the entry of the malicious package = %+v, want severity 3, [1.0.0]", i, mal)
		}
		etag := page.Header.Get("ETag")
		if etag == "" {
			t.Fatalf("replica %d: the page has no ETag", i)
		}
		etags = append(etags, etag)
		a.do(http.MethodGet, pagePath, nil, "If-None-Match", etag).want(t, "conditional request for the page", 304)
	}
	for _, et := range etags[1:] {
		if et != etags[0] {
			t.Errorf("the page's ETag differs between replicas: %v", etags)
		}
	}
}

// vulnResourcePath is the path of a resource the service index lists, by its @type.
func vulnResourcePath(t testing.TB, serviceIndex reply, resourceType string) string {
	t.Helper()
	var si struct {
		Resources []struct {
			ID   string `json:"@id"`
			Type string `json:"@type"`
		} `json:"resources"`
	}
	serviceIndex.JSON(t, &si)
	for _, r := range si.Resources {
		if r.Type == resourceType {
			u, err := url.Parse(r.ID)
			if err != nil {
				t.Fatal(err)
			}
			return u.RequestURI()
		}
	}
	t.Fatalf("the service index lists no %s: %s", resourceType, serviceIndex)
	return ""
}

// vulnPagePath is the path of the vulnerability page of a repository, found the way a client finds it: the service index lists the
// resource, the resource's index lists the page.
func vulnPagePath(t testing.TB, a *api, key string) string {
	t.Helper()
	si := a.get(nugetBase+key+"/v3/index.json").want(t, "service index of "+key, 200)
	ix := a.get(vulnResourcePath(t, si, "VulnerabilityInfo/6.7.0")).want(t, "vulnerability index of "+key, 200)
	var entries []struct {
		ID string `json:"@id"`
	}
	ix.JSON(t, &entries)
	if len(entries) != 1 {
		t.Fatalf("the vulnerability index of %s = %s, want one entry", key, ix.Body)
	}
	u, err := url.Parse(entries[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	return u.RequestURI()
}
