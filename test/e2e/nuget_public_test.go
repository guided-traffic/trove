//go:build e2e

package e2e

// The read-only subset against nuget.org of the run that closes the phase (roadmap M3, E3.1; R-NUGET-027's row of the
// closing run against nuget.org; Q-NUGET-91, Q-NUGET-92): a remote repository of nuget.org's service index and a
// virtual repository with nuget-e2e and that remote, read by the real clients and by the test. Nothing is pushed, and
// nothing changes upstream. The test belongs to the set nuget-closing and fails, never skips, without
// TROVE_E2E_NUGET_CLOSING=1, as the subset of TestNPMClosing against the public npm registry does.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
)

const (
	nugetOrgIndex = "https://api.nuget.org/v3/index.json"
	// Pinned versions without a dependency for net10.0, so a restore reads exactly these IDs. Newtonsoft.Json 13.0.3 is
	// the version R-NUGET-015 names; nuget.org inlines the pages of its registration.
	nugetOrgOne        = "Newtonsoft.Json"
	nugetOrgOneVersion = "13.0.3"
	// More than 128 versions upstream (602 on 2026-11-02), in registration pages nuget.org does not inline: what the
	// merge of a remote member reads for this ID is the upstream's paging, which trove has to follow (R-NUGET-017).
	nugetOrgMany        = "Serilog"
	nugetOrgManyVersion = "4.3.0"
)

// nugetPeopleLinks are the members whose addresses stay the upstream's: the web links shown to people (R-NUGET-015's
// table) and a vulnerability's advisory page, which Q-NUGET-85 keeps as a link for people (Q-NUGET-92).
var nugetPeopleLinks = map[string]bool{"projectUrl": true, "licenseUrl": true, "iconUrl": true, "advisoryUrl": true}

// addressCheck is what foreignAddresses saw in a document.
type addressCheck struct {
	foreign []string // the addresses outside the prefix, each with its JSON path
	checked int      // the addresses compared with the prefix
	links   int      // the members of nugetPeopleLinks left out
}

// foreignAddresses compares the addresses of a NuGet JSON document with prefix: every string member that is an absolute
// http or https URL, except the members of nugetPeopleLinks and the JSON-LD @context, whose IRIs name vocabularies a
// client never requests (Q-NUGET-92).
func foreignAddresses(raw []byte, prefix string) (addressCheck, error) {
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return addressCheck{}, err
	}
	var c addressCheck
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				switch {
				case k == "@context":
				case nugetPeopleLinks[k]:
					c.links++
				default:
					walk(path+"."+k, e)
				}
			}
		case []any:
			for i, e := range x {
				walk(fmt.Sprintf("%s[%d]", path, i), e)
			}
		case string:
			if strings.HasPrefix(x, "https://") || strings.HasPrefix(x, "http://") {
				c.checked++
				if !strings.HasPrefix(x, prefix) {
					c.foreign = append(c.foreign, path+" = "+x)
				}
			}
		}
	}
	walk("$", doc)
	slices.Sort(c.foreign)
	return c, nil
}

// requireTroveAddresses fails the test when a document names an address outside the repository; it lists the first
// ten, which is enough to see the member and the host.
func requireTroveAddresses(t *testing.T, what string, raw []byte, prefix string) {
	t.Helper()
	c, err := foreignAddresses(raw, prefix)
	if err != nil {
		t.Fatalf("%s is no JSON (%v): %.300s", what, err, raw)
	}
	if len(c.foreign) > 0 {
		t.Errorf("%s names %d of its %d addresses outside %s (R-NUGET-015), the first:\n%s", what, len(c.foreign), c.checked, prefix, strings.Join(c.foreign[:min(10, len(c.foreign))], "\n"))
		return
	}
	if c.checked == 0 {
		t.Errorf("%s names no address at all: %.300s", what, raw)
	}
	t.Logf("%s: %d addresses, all below %s; %d web links left out", what, c.checked, prefix, c.links)
}

// repoPrefix is the start of every address a repository's documents may name: <base>/api/nuget/<key>/v3/.
func repoPrefix(key string) string {
	return strings.TrimRight(cfg.edgeURL, "/") + nugetAddr(key) + "/v3/"
}

// registrationLeaves reads the SemVer 2.0.0 registration of an ID through a repository the way a client finds it (the
// service index's RegistrationsBaseUrl/3.6.0), with every page that is not inlined, checks that each document names
// only the repository's addresses, and returns the versions it lists.
func registrationLeaves(t *testing.T, a *api, key, lowerID string) []string {
	t.Helper()
	prefix := repoPrefix(key)
	idx := a.get(nugetAddr(key)+"/v3/index.json").want(t, "the service index of "+key, 200)
	requireTroveAddresses(t, "the service index of "+key, idx.Body, prefix)
	var service struct {
		Resources []struct {
			ID   string `json:"@id"`
			Type string `json:"@type"`
		} `json:"resources"`
	}
	idx.JSON(t, &service)
	base := ""
	for _, r := range service.Resources {
		if r.Type == "RegistrationsBaseUrl/3.6.0" {
			base = r.ID
		}
	}
	if base == "" {
		t.Fatalf("the service index of %s has no RegistrationsBaseUrl/3.6.0: %s", key, idx)
	}
	type leaf struct {
		CatalogEntry struct {
			Version string `json:"version"`
		} `json:"catalogEntry"`
	}
	type page struct {
		ID    string `json:"@id"`
		Items []leaf `json:"items"`
	}
	r := a.get(base+lowerID+"/index.json").want(t, "the registration index of "+lowerID+" through "+key, 200)
	requireTroveAddresses(t, "the registration index of "+lowerID+" through "+key, r.Body, prefix)
	var index struct {
		Items []page `json:"items"`
	}
	r.JSON(t, &index)
	var versions []string
	byAddress := 0
	for _, p := range index.Items {
		if len(p.Items) == 0 { // a page listed by its address
			byAddress++
			pr := a.get(p.ID).want(t, "the registration page "+p.ID, 200)
			requireTroveAddresses(t, "the registration page "+p.ID, pr.Body, prefix)
			pr.JSON(t, &p)
		}
		for _, l := range p.Items {
			versions = append(versions, l.CatalogEntry.Version)
		}
	}
	t.Logf("the registration of %s through %s: %d versions in %d pages, %d of them listed by address", lowerID, key, len(versions), len(index.Items), byAddress)
	return versions
}

// dotnetSearchHits reads the packages of `dotnet package search --format json`.
func dotnetSearchHits(t *testing.T, out string) map[string]int64 {
	t.Helper()
	var r struct {
		SearchResult []struct {
			Packages []struct {
				ID             string `json:"id"`
				TotalDownloads int64  `json:"totalDownloads"`
			} `json:"packages"`
		} `json:"searchResult"`
	}
	if err := json.Unmarshal([]byte(out[max(0, strings.Index(out, "{")):]), &r); err != nil {
		t.Fatalf("dotnet package search printed no JSON (%v):\n%s", err, tailLines(out, 30))
	}
	hits := map[string]int64{}
	for _, s := range r.SearchResult {
		for _, p := range s.Packages {
			hits[strings.ToLower(p.ID)] = p.TotalDownloads
		}
	}
	return hits
}

func TestNuGetClosingPublic(t *testing.T) {
	requireNuGetClosingRun(t)
	fx := nugetFx(t)
	remote, all := "nuget-public-"+fx.run+"-remote", "nuget-public-"+fx.run+"-all"
	// No extraHosts: a remote of nuget.org's index admits *.nuget.org by default, where nuget.org serves its search and
	// its package content (R-NUGET-014).
	fx.adm.do(http.MethodPost, reposRoute, map[string]any{
		"key": remote, "class": "remote", "format": "nuget", "requireAuthentication": true,
		"remote": map[string]any{"url": nugetOrgIndex},
	}).want(t, "create "+remote, 201)
	t.Cleanup(func() { deleteRepository(t, fx.adm, remote) })
	fx.adm.do(http.MethodPost, reposRoute, map[string]any{
		"key": all, "class": "virtual", "format": "nuget", "requireAuthentication": true,
		"virtual": map[string]any{"members": []string{nugetAuthKey, remote}, "deployTarget": nugetAuthKey},
	}).want(t, "create "+all, 201)
	t.Cleanup(func() { deleteRepository(t, fx.adm, all) }) // before the remote: the virtual repository names it
	ensurePolicy(t, fx.adm, "e2e-nuget-public-"+fx.run, []string{remote, all}, [2]any{"sa:" + nugetPubSA, []string{"read"}})
	nugetReadableOnEveryReplica(t, fx.token, remote, all)
	remoteFeed, allFeed := nugetFeed(t, "localhost", remote), nugetFeed(t, "localhost", all)
	roles := map[string]string{remote: "the remote", all: "the virtual repository"}

	// First, while the remote has cached nothing: a remote that searched only its cache would list nothing, so a hit
	// with nuget.org's download count is the forwarded search (R-NUGET-016).
	t.Run("R-NUGET-016 R-NUGET-027 E3.1 dotnet package search Newtonsoft through a remote of nuget.org that has cached nothing lists Newtonsoft.Json with nuget.org's download count and trove's addresses", func(t *testing.T) {
		c := fx.client(t, 0, "public-search")
		c.config(remoteFeed, true)
		out := c.exec("dotnet", "package", "search", "Newtonsoft", "--source", remoteFeed, "--format", "json", "--take", "20")
		hits := dotnetSearchHits(t, out)
		downloads, ok := hits[strings.ToLower(nugetOrgOne)]
		if !ok {
			t.Fatalf("the search through %s does not list %s; it listed %v", remote, nugetOrgOne, sortedKeys(hits))
		}
		// billions on nuget.org; a cache-only search has no download count of the upstream
		if downloads < 1_000_000 {
			t.Errorf("%s is listed with %d downloads, want nuget.org's count (R-NUGET-016 keeps the download counts)", nugetOrgOne, downloads)
		}
		a := newAPI(t).withToken(fx.token)
		r := a.get(nugetAddr(remote)+"/v3/search?q=newtonsoft&take=20&semVerLevel=2.0.0").want(t, "search through "+remote, 200)
		requireTroveAddresses(t, "the search answer of "+remote, r.Body, repoPrefix(remote))
	})

	t.Run("R-NUGET-014 R-NUGET-015 R-NUGET-027 E3.1 nuget.exe installs Newtonsoft.Json 13.0.3 through the remote of nuget.org", func(t *testing.T) {
		c := fx.client(t, 1, "public-exe")
		c.config(remoteFeed, true)
		if out, err := c.try("nuget", "install", nugetOrgOne, "-Version", nugetOrgOneVersion, "-Source", remoteFeed, "-ConfigFile", "nuget.config",
			"-OutputDirectory", "installed", "-NonInteractive", "-DirectDownload", "-NoCache"); err != nil {
			t.Fatalf("nuget install through %s: %v\n%s", remote, err, tailLines(out, 30))
		}
		name := nugetOrgOne + "." + nugetOrgOneVersion
		if _, err := c.try("test", "-f", c.dir()+"/installed/"+name+"/"+name+".nupkg"); err != nil {
			t.Fatalf("nuget install did not extract %s", name)
		}
	})

	t.Run("R-NUGET-014 R-NUGET-017 R-NUGET-027 E3.1 dotnet restore through the virtual repository of Newtonsoft.Json 13.0.3 and Serilog 4.3.0, an ID with more than 128 versions on nuget.org", func(t *testing.T) {
		c := fx.client(t, 2, "public-restore")
		c.config(allFeed, true)
		c.write("p.csproj", []byte(`<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup><TargetFramework>net10.0</TargetFramework><NuGetAudit>false</NuGetAudit></PropertyGroup>
  <ItemGroup>
    <PackageReference Include="`+nugetOrgOne+`" Version="`+nugetOrgOneVersion+`" />
    <PackageReference Include="`+nugetOrgMany+`" Version="`+nugetOrgManyVersion+`" />
  </ItemGroup>
</Project>
`))
		if out, err := c.try("dotnet", "restore", "p.csproj", "--no-cache"); err != nil {
			t.Fatalf("dotnet restore through %s: %v\n%s", all, err, tailLines(out, 30))
		}
		for id, v := range map[string]string{nugetOrgOne: nugetOrgOneVersion, nugetOrgMany: nugetOrgManyVersion} {
			if !c.hasPackage(id, v) {
				t.Errorf("the restore through %s did not extract %s %s", all, id, v)
			}
		}
	})

	// R-NUGET-015's criterion on nuget.org: every address of the registration index but the web links is the serving
	// repository's; through the virtual repository the merge renders it (R-NUGET-017).
	for _, key := range []string{remote, all} {
		t.Run("R-NUGET-015 R-NUGET-017 E3.1 the registration of newtonsoft.json through "+roles[key]+" answers 200 and every address in it but the web links is the repository's", func(t *testing.T) {
			versions := registrationLeaves(t, newAPI(t).withToken(fx.token), key, strings.ToLower(nugetOrgOne))
			if !slices.Contains(versions, nugetOrgOneVersion) {
				t.Errorf("the registration of %s through %s lists %d versions, not %s", nugetOrgOne, key, len(versions), nugetOrgOneVersion)
			}
		})
	}
	for _, key := range []string{remote, all} {
		t.Run("R-NUGET-009 R-NUGET-015 R-NUGET-017 E3.1 the registration of serilog through "+roles[key]+" lists its more than 128 versions in pages with the repository's addresses", func(t *testing.T) {
			versions := registrationLeaves(t, newAPI(t).withToken(fx.token), key, strings.ToLower(nugetOrgMany))
			if len(versions) <= 128 || !slices.Contains(versions, nugetOrgManyVersion) {
				t.Errorf("the registration of %s through %s lists %d versions (with %s: %t), want more than 128 and that one", nugetOrgMany, key, len(versions), nugetOrgManyVersion, slices.Contains(versions, nugetOrgManyVersion))
			}
		})
	}
}
