//go:build e2e

package e2e

// NuGet on a customer endpoint (exit criterion E3.7, R-NUGET-034, R-EP-018): the endpoint customers of the dev profile has
// its own Ingress and host (packages.trove.localhost) and exposes a repository only through rules. The clients reach it by
// that host, which their pods resolve to the loopback address of their forwarder (hostAliases in deploy/dev/e2e.sh), and
// use a service-account token bound to the endpoint as the Basic password. The rule exposes the package from 2.0 on,
// without pre-releases; the repository holds 1.9.0, 2.0.0, 2.1.0-beta.1 and 2.1.0 of it, and a second package that no rule names.

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// registrationVersions lists the versions a registration index of a hive names (the pages are inlined for a package with
// this few versions).
func registrationVersions(t testing.TB, a *api, repo, hive, id string) []string {
	t.Helper()
	r := a.get("/api/nuget/"+repo+"/v3/"+hive+"/"+strings.ToLower(id)+"/index.json").want(t, hive+" index", 200)
	var doc struct {
		Items []struct {
			Items []struct {
				CatalogEntry struct {
					Version string `json:"version"`
				} `json:"catalogEntry"`
			} `json:"items"`
		} `json:"items"`
	}
	r.JSON(t, &doc)
	var out []string
	for _, page := range doc.Items {
		if page.Items == nil {
			t.Fatalf("the %s index of %s does not inline its pages: %s", hive, id, r)
		}
		for _, it := range page.Items {
			out = append(out, it.CatalogEntry.Version)
		}
	}
	sort.Strings(out)
	return out
}

// searchVersions returns the versions of every hit of a search, keyed by the lower-case ID of the hit.
func searchVersions(t testing.TB, a *api, repo, q string) map[string][]string {
	t.Helper()
	r := a.get("/api/nuget/"+repo+"/v3/search?prerelease=true&take=50&q="+q).want(t, "search", 200)
	var doc struct {
		Data []struct {
			ID       string `json:"id"`
			Versions []struct {
				Version string `json:"version"`
			} `json:"versions"`
		} `json:"data"`
	}
	r.JSON(t, &doc)
	out := map[string][]string{}
	for _, hit := range doc.Data {
		for _, v := range hit.Versions {
			out[strings.ToLower(hit.ID)] = append(out[strings.ToLower(hit.ID)], v.Version)
		}
		sort.Strings(out[strings.ToLower(hit.ID)])
	}
	return out
}

func TestNuGetEndpoint(t *testing.T) {
	fx := nugetFx(t)
	sdk, internal := fx.pkgID("Sdk"), fx.pkgID("SdkInternal") // the Acme.Sdk of the criterion, unique per run
	lower := strings.ToLower(sdk)
	exposed := []string{"2.0.0", "2.1.0"}

	// the repository: four versions of the exposed package and one version of another package
	pub := fx.client(t, 0, "e37-pub")
	pub.config(nugetFeed(t, "localhost", nugetCustKey), true)
	for _, v := range []string{"1.9.0", "2.0.0", "2.1.0-beta.1", "2.1.0"} {
		file := pub.place(sdk, v, fx.nugetPack(t, sdk, v, "endpoint "+v))
		pub.mustPush("push "+v, "dotnet", "nuget", "push", file, "--source", nugetFeed(t, "localhost", nugetCustKey), "--api-key", "az")
	}
	file := pub.place(internal, "1.0.0", fx.nugetPack(t, internal, "1.0.0", "no rule names this package"))
	pub.mustPush("push the other package", "dotnet", "nuget", "push", file, "--source", nugetFeed(t, "localhost", nugetCustKey), "--api-key", "az")

	// the rule: the package from 2.0 on (the bare notation: 2.0 or higher), no pre-releases
	ruleName := "nuget-sdk-" + fx.run
	rule := map[string]any{
		"name": ruleName, "repository": nugetCustKey, "enabled": true,
		"packages":    map[string]any{"include": []string{sdk}, "exclude": []string{}},
		"versions":    map[string]any{"include": []string{}, "exclude": []string{}, "range": "2.0"},
		"prereleases": false,
	}
	fx.adm.do(http.MethodPost, "/api/v1/endpoints/customers/exposure-rules", rule).want(t, "create the rule", 201)
	defer func() {
		g := fx.adm.get("/api/v1/endpoints/customers/exposure-rules/" + ruleName)
		if g.Status == 200 {
			fx.adm.do(http.MethodDelete, "/api/v1/endpoints/customers/exposure-rules/"+ruleName, nil, "If-Match", g.Header.Get("ETag"))
		}
	}()
	ensureSA(t, fx.adm, "e2e-nuget-cust")
	ensurePolicy(t, fx.adm, "e2e-nuget-cust", []string{nugetCustKey}, [2]any{"sa:e2e-nuget-cust", []string{"read"}})
	bound := freshSAToken(t, fx.adm, "e2e-nuget-cust", 3600, map[string]any{"inherit": true, "endpoints": []string{"customers"}}).Token

	// the rule is in force on every replica before a client asks
	pods, stop := customersOnPods(t, bound)
	defer stop()
	for i, p := range pods {
		waitFor(t, fmt.Sprintf("replica %d to serve exactly the exposed versions", i), 15*time.Second, 200*time.Millisecond, func() bool {
			r := p.get("/api/nuget/" + nugetCustKey + "/v3/flatcontainer/" + lower + "/index.json")
			return r.Status == 200 && strings.Contains(string(r.Body), "2.0.0") && !strings.Contains(string(r.Body), "1.9.0")
		})
	}
	api := customersAPI(t, bound)

	t.Run("R-NUGET-034 E3.7 the version list, the three registration hives and the search list exactly the exposed versions", func(t *testing.T) {
		var list struct {
			Versions []string `json:"versions"`
		}
		api.get("/api/nuget/"+nugetCustKey+"/v3/flatcontainer/"+lower+"/index.json").want(t, "version list", 200).JSON(t, &list)
		if !slices.Equal(list.Versions, exposed) {
			t.Fatalf("version list = %v, want %v", list.Versions, exposed)
		}
		for _, hive := range []string{"registration", "registration-gz", "registration-semver2"} {
			if got := registrationVersions(t, api, nugetCustKey, hive, sdk); !slices.Equal(got, exposed) {
				t.Fatalf("%s lists %v, want %v", hive, got, exposed)
			}
		}
		hits := searchVersions(t, api, nugetCustKey, lower)
		if got := hits[lower]; !slices.Equal(got, exposed) {
			t.Fatalf("search lists %v for %s, want %v (all hits: %v)", got, sdk, exposed, hits)
		}
		if _, ok := hits[strings.ToLower(internal)]; ok {
			t.Fatalf("search lists %s, which no rule exposes", internal)
		}
		// a hidden version is a missing one: its .nupkg is the 404 of R-EP-013, an exposed one downloads
		api.get("/api/nuget/"+nugetCustKey+"/v3/flatcontainer/"+lower+"/1.9.0/"+lower+".1.9.0.nupkg").want(t, "the .nupkg of a hidden version", 404)
		api.get("/api/nuget/"+nugetCustKey+"/v3/flatcontainer/"+lower+"/2.1.0/"+lower+".2.1.0.nupkg").want(t, "the .nupkg of an exposed version", 200)
	})

	t.Run("R-NUGET-034 E3.7 the service index lists no publish resource and a push answers 403", func(t *testing.T) {
		idx := api.get("/api/nuget/"+nugetCustKey+"/v3/index.json").want(t, "service index on customers", 200)
		if strings.Contains(string(idx.Body), "PackagePublish") {
			t.Fatalf("the index on customers lists a publish resource:\n%s", idx)
		}
		if def := fx.adm.get("/api/nuget/"+nugetCustKey+"/v3/index.json").want(t, "service index on the default endpoint", 200); !strings.Contains(string(def.Body), "PackagePublish") {
			t.Fatalf("the index on the default endpoint lists no publish resource, so the comparison proves nothing:\n%s", def)
		}
		push := api.do(http.MethodPut, "/api/nuget/"+nugetCustKey+"/v3/package", "x", "X-NuGet-ApiKey", bound)
		if push.Status != 403 || !strings.Contains(push.String(), "endpoint_read_only") {
			t.Fatalf("push on customers answered %s, want 403 endpoint_read_only", push)
		}
		c := fx.client(t, 1, "e37-push")
		c.token = bound
		c.config(nugetFeed(t, custHost, nugetCustKey), true)
		pubFile := c.place(sdk, "3.0.0", fx.nugetPack(t, sdk, "3.0.0", "never stored"))
		if out, err := c.dotnetPush(pubFile, nugetFeed(t, custHost, nugetCustKey), bound); err == nil || pushedRE.MatchString(out) {
			t.Fatalf("dotnet nuget push to the customers endpoint succeeded:\n%s", out)
		}
	})

	custFeed := nugetFeed(t, custHost, nugetCustKey)
	exact := func(v string) string { return fmt.Sprintf("[%s]", v) }

	t.Run("R-NUGET-034 R-NUGET-027 E3.7 dotnet restore through the endpoint takes only an exposed version", func(t *testing.T) {
		c := fx.client(t, 2, "e37-dotnet")
		c.token = bound
		c.config(custFeed, true)
		c.exec("dotnet", "restore", c.consumer("floating", sdk, "*"), "--no-cache")
		if !c.hasPackage(sdk, "2.1.0") {
			t.Fatal("the floating version did not resolve to the highest exposed release 2.1.0")
		}
		for _, hidden := range []string{"1.9.0", "2.1.0-beta.1"} {
			if c.hasPackage(sdk, hidden) {
				t.Fatalf("the restore extracted the hidden version %s", hidden)
			}
		}
		c.exec("dotnet", "restore", c.consumer("pinned", sdk, exact("2.0.0")), "--no-cache")
		if !c.hasPackage(sdk, "2.0.0") {
			t.Fatal("the exposed version 2.0.0 did not restore")
		}
	})

	t.Run("R-NUGET-034 E3.7 dotnet add package of a hidden version fails with NU1102, as for a version that does not exist", func(t *testing.T) {
		c := fx.client(t, 0, "e37-add")
		c.token = bound
		c.config(custFeed, true)
		proj := c.consumer("add", sdk, "2.1.0")
		hidden := c.mustRefuse("add package of the hidden 1.9.0", "NU1102", "dotnet", "add", proj, "package", sdk, "--version", exact("1.9.0"), "--source", custFeed)
		missing := c.mustRefuse("add package of the missing 9.9.9", "NU1102", "dotnet", "add", proj, "package", sdk, "--version", exact("9.9.9"), "--source", custFeed)
		if strings.Contains(hidden, "endpoint") != strings.Contains(missing, "endpoint") {
			t.Fatalf("the hidden version fails differently from a missing one:\n%s\n---\n%s", hidden, missing)
		}
	})

	t.Run("R-NUGET-034 R-NUGET-027 E3.7 nuget restore through the endpoint takes only an exposed version", func(t *testing.T) {
		c := fx.client(t, 1, "e37-exe")
		c.token = bound
		c.config(custFeed, true)
		packages := func(version string) []byte {
			return fmt.Appendf(nil, `<packages><package id="%s" version="%s" targetFramework="net48" /></packages>`, sdk, version)
		}
		c.write("exposed/packages.config", packages("2.1.0"))
		c.write("hidden/packages.config", packages("1.9.0"))
		c.exec("nuget", "restore", "exposed/packages.config", "-PackagesDirectory", "pkgs", "-ConfigFile", "nuget.config", "-NonInteractive", "-NoCache")
		if _, err := c.try("test", "-f", c.dir()+"/pkgs/"+sdk+".2.1.0/"+sdk+".2.1.0.nupkg"); err != nil {
			t.Fatal("nuget restore did not extract the exposed version")
		}
		c.mustRefuse("nuget restore of the hidden 1.9.0", sdk, "nuget", "restore", "hidden/packages.config", "-PackagesDirectory", "pkgs-hidden", "-ConfigFile", "nuget.config", "-NonInteractive", "-NoCache")
		if _, err := c.try("test", "-e", c.dir()+"/pkgs-hidden/"+sdk+".1.9.0"); err == nil {
			t.Fatal("nuget restore extracted the hidden version")
		}
	})
}
