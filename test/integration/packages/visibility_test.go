//go:build integration

package packages

import (
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_API_029_ListCountsOnlyWhatTheCallerMayRead: a caller without read on a repository
// never sees its versions, in a list, in a count, in a latest version, in a search or in
// a repository list.
func TestR_API_029_ListCountsOnlyWhatTheCallerMayRead(t *testing.T) {
	e := newEnv(t)
	e.seed()

	t.Run("a reader of npm-local sees the older latest version and only the versions of npm-local", func(t *testing.T) {
		items := e.all(e.tokB, "/api/v1/packages")
		if got, want := names(items), []string{"@team-a/ui", "left-pad"}; !slices.Equal(got, want) {
			t.Fatalf("names %v, want %v", got, want)
		}
		ui := pkgByName(items, "@team-a/ui")
		if num(ui["versionCount"]) != 3 || ui["latestVersion"] != "1.4.0" || !slices.Equal(toStrings(ui["repositories"]), []string{"npm-local"}) {
			t.Fatalf("a hidden version leaks into %v", ui)
		}
	})

	t.Run("the acceptance case of the spec: after three publishes the list shows the latest, and a reader of 1.3.0 only sees 1.3.0", func(t *testing.T) {
		// bob reads npm-old, which holds only 1.3.0 of @only/lib; npm-new holds 1.3.0 and 1.4.0.
		e.publish("npm-old", spec{Name: "@only/lib", Version: "1.3.0"})
		for _, v := range []string{"1.3.0", "1.4.0"} {
			e.publish("npm-new", spec{Name: "@only/lib", Version: v})
		}
		whole := pkgByName(e.all(e.tokA, "/api/v1/packages?q=only"), "@only/lib")
		if whole == nil || whole["latestVersion"] != "1.4.0" {
			t.Fatalf("the writer sees %v", whole)
		}
		part := pkgByName(e.all(e.tokB, "/api/v1/packages?q=only"), "@only/lib")
		if part["latestVersion"] != "1.3.0" || num(part["versionCount"]) != 1 {
			t.Fatalf("the reader of npm-old sees %v", part)
		}
	})

	t.Run("a search does not find a hidden package by its description or keyword", func(t *testing.T) {
		for _, q := range []string{"q=needle-word", "keyword=needle-kw", "q=secret", "namePrefix=secret", "namePrefix=%40team-b"} {
			if got := names(e.all(e.tokB, "/api/v1/packages?"+q)); len(got) != 0 {
				t.Errorf("%s finds %v for a reader of npm-local", q, got)
			}
		}
		if got := names(e.all(e.tokE, "/api/v1/packages?q=needle-word")); !slices.Equal(got, []string{"@team-b/core"}) {
			t.Errorf("a reader of both does not find it: %v", got)
		}
	})

	t.Run("a repository filter on a repository the caller may not read lists nothing", func(t *testing.T) {
		if got := names(e.all(e.tokB, "/api/v1/packages?repository=npm-team")); len(got) != 0 {
			t.Fatalf("%v", got)
		}
	})

	t.Run("a grant narrowed to the packages @team-a/* shows those packages and no others", func(t *testing.T) {
		items := e.all(e.tokC, "/api/v1/packages")
		if got := names(items); !slices.Equal(got, []string{"@team-a/ui"}) {
			t.Fatalf("names %v", got)
		}
		ui := items[0]
		if num(ui["versionCount"]) != 2 || ui["latestVersion"] != "1.5.0" || !slices.Equal(toStrings(ui["repositories"]), []string{"npm-team"}) {
			t.Fatalf("%v", ui)
		}
		if got := names(e.all(e.tokC, "/api/v1/packages?q=needle-word")); len(got) != 0 {
			t.Fatalf("a package outside the pattern is found: %v", got)
		}
	})

	t.Run("a caller without a grant sees an empty list and an anonymous caller is refused", func(t *testing.T) {
		if got := e.all(e.tokD, "/api/v1/packages"); len(got) != 0 {
			t.Fatalf("%v", got)
		}
		if a := e.get("", "/api/v1/packages"); a.Status != 401 {
			t.Fatalf("anonymous: %d %s", a.Status, a.Body)
		}
	})

	t.Run("a package or a version the caller may not read answers the 404 of a missing one", func(t *testing.T) {
		missing := e.get(e.tokB, "/api/v1/packages/lookup?format=npm&name="+url.QueryEscape("@nobody/nothing"))
		hidden := e.get(e.tokB, "/api/v1/packages/lookup?format=npm&name="+url.QueryEscape("@team-b/core"))
		if missing.Status != 404 || hidden.Status != 404 || missing.Problem(t) != hidden.Problem(t) {
			t.Fatalf("missing %d %s, hidden %d %s", missing.Status, missing.Body, hidden.Status, hidden.Body)
		}
		id := e.idOf(e.tokE, "@team-b/core")
		byID := e.get(e.tokB, "/api/v1/packages/"+id)
		if byID.Status != 404 || byID.Problem(t) != missing.Problem(t) {
			t.Fatalf("by id %d %s", byID.Status, byID.Body)
		}
		uiID := e.idOf(e.tokB, "@team-a/ui")
		hiddenVersion := e.get(e.tokB, "/api/v1/packages/"+uiID+"/versions/1.5.0")
		absentVersion := e.get(e.tokB, "/api/v1/packages/"+uiID+"/versions/9.9.9")
		if hiddenVersion.Status != 404 || hiddenVersion.Problem(t) != absentVersion.Problem(t) {
			t.Fatalf("hidden %d %s, absent %d %s", hiddenVersion.Status, hiddenVersion.Body, absentVersion.Status, absentVersion.Body)
		}
		for _, p := range []string{"/readme", ""} {
			if a := e.get(e.tokB, "/api/v1/packages/"+uiID+"/versions/1.5.0"+p); a.Status != 404 {
				t.Errorf("1.5.0%s: %d", p, a.Status)
			}
		}
		if a := e.get(e.tokB, "/api/v1/packages/"+e.idOf(e.tokE, "secret-tool")+"/versions"); a.Status != 404 {
			t.Errorf("versions of a hidden package: %d", a.Status)
		}
		if a := e.get(e.tokB, "/api/v1/packages/pkg_not-an-id"); a.Status != 404 {
			t.Errorf("a malformed id: %d", a.Status)
		}
	})

	t.Run("downloads of a hidden repository are not part of the count", func(t *testing.T) {
		tgz := func(repo, name, file string) {
			if a := e.st.Do(e.r, "GET", "/api/npm/"+repo+"/"+name+"/-/"+file, e.tokA, nil); a.Status != 200 {
				t.Fatalf("download %s: %d", file, a.Status)
			}
		}
		tgz("npm-local", "left-pad", "left-pad-1.0.0.tgz")
		for range 3 {
			tgz("npm-team", "secret-tool", "secret-tool-9.9.9.tgz")
		}
		for range 5 {
			tgz("npm-team", "@team-a/ui", "ui-1.5.0.tgz")
		}
		testenv.WaitFor(t, "the download statistics", 30*time.Second, func() bool {
			return num(pkgByName(e.all(e.tokE, "/api/v1/packages"), "@team-a/ui")["downloadCount"]) == 5
		})
		wide := pkgByName(e.all(e.tokE, "/api/v1/packages"), "@team-a/ui")
		narrow := pkgByName(e.all(e.tokB, "/api/v1/packages"), "@team-a/ui")
		if num(wide["downloadCount"]) != 5 || num(narrow["downloadCount"]) != 0 {
			t.Fatalf("wide %v narrow %v", wide["downloadCount"], narrow["downloadCount"])
		}
		if left := pkgByName(e.all(e.tokB, "/api/v1/packages"), "left-pad"); num(left["downloadCount"]) != 1 {
			t.Fatalf("left-pad %v", left["downloadCount"])
		}
	})
}

// TestR_EP_014_PackagesOfAnEndpointWithRules: on an endpoint with exposure rules the
// package list, the versions and the version details cover the exposed packages and
// versions only, the counts are computed after the filter, and the dist-tags name no
// version that is hidden (R-EP-014, R-API-029).
func TestR_EP_014_PackagesOfAnEndpointWithRules(t *testing.T) {
	e := newEnv(t)
	e.seed()
	e.st.Exec(`INSERT INTO endpoint_exposure_rule (id, endpoint, name, repo_id, packages, versions)
		SELECT gen_random_uuid(), 'partners', 'ui', id, '{"include":["@team-a/*"],"exclude":[]}', '{"include":[],"exclude":[],"range":">=1.4.0"}'
		  FROM repositories WHERE key = 'npm-local'`)
	endpoints := `{"partners":{"listen":"127.0.0.1:0","base_url":"https://partners.example.test","surface":["api"]}}`
	r := e.st.Start(t, testenv.ReplicaOptions{Name: "web2", Endpoints: endpoints})
	tok := e.tokBot
	get := func(path string) testenv.Answer {
		return e.st.DoAt(r.Addr("partners"), "partners.example.test", "GET", path, tok, nil)
	}
	var list page
	a := get("/api/v1/packages")
	if a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	mustJSON(t, a.Body, &list)
	// A format without an exposure adapter exposes nothing on an endpoint with rules
	// (R-EP-018, fail closed); the package list then is empty, and the detailed
	// expectations below apply once the format can be exposed. The authz unit tests
	// (TestR_API_029_ReadScopeExposureDecidesVersions) decide versions with a format
	// that has one.
	if en, _ := catalog.Default().Lookup("npm"); !en.Exposure.Exposable {
		if len(list.Items) != 0 {
			t.Fatalf("npm has no exposure adapter and %v is listed", names(list.Items))
		}
		return
	}
	if got := names(list.Items); !slices.Equal(got, []string{"@team-a/ui"}) {
		t.Fatalf("names %v", got)
	}
	ui := list.Items[0]
	if num(ui["versionCount"]) != 1 || ui["latestVersion"] != "1.4.0" || !slices.Equal(toStrings(ui["repositories"]), []string{"npm-local"}) {
		t.Fatalf("the counts are not computed after the filter: %v", ui)
	}
	id := ui["id"].(string)
	var vs page
	mustJSON(t, get("/api/v1/packages/"+id+"/versions").Body, &vs)
	if len(vs.Items) != 1 || vs.Items[0]["version"] != "1.4.0" {
		t.Fatalf("versions %v", vs.Items)
	}
	if a := get("/api/v1/packages/" + id + "/versions/1.3.0"); a.Status != 404 {
		t.Fatalf("an unexposed version: %d %s", a.Status, a.Body)
	}
	if a := get("/api/v1/packages/lookup?format=npm&name=left-pad"); a.Status != 404 {
		t.Fatalf("an unexposed package: %d", a.Status)
	}
	d := get("/api/v1/packages/" + id + "/versions/1.4.0").JSON(t)
	md, _ := d["metadata"].(map[string]any)
	tags, _ := md["distTags"].(map[string]any)
	if len(tags) != 1 || tags["latest"] != "1.4.0" {
		t.Fatalf("a dist-tag names a hidden version: %v", tags)
	}

	// The same caller on the default endpoint sees everything and the tag of the
	// pre-release.
	full := e.get(e.tokE, "/api/v1/packages/"+id+"/versions/1.4.0").JSON(t)
	fullTags, _ := full["metadata"].(map[string]any)["distTags"].(map[string]any)
	if fullTags["next"] != "2.0.0-beta.1" {
		t.Fatalf("the default endpoint hides a tag: %v", fullTags)
	}
}

// TestR_REPO_005_PackagesOfARepositoryWithPatterns: the include and exclude patterns of a
// repository are routing rules that bind every caller; a version they do not admit is not
// served, so the package screens neither list nor count it, whatever the caller may read
// (R-API-029).
func TestR_REPO_005_PackagesOfARepositoryWithPatterns(t *testing.T) {
	e := newEnv(t)
	e.seed()
	// The patterns arrive after the data: a replica started now builds its snapshot with them.
	e.st.Exec(`UPDATE repositories SET config = '{"local":{"packageOwnership":"off"},"excludePatterns":["secret-tool","@team-b/*"]}'::jsonb WHERE key = 'npm-team'`)
	r := e.st.Start(t, testenv.ReplicaOptions{Name: "web2"})
	list := func(token, path string) []map[string]any {
		var p page
		a := e.st.Do(r, "GET", path, token, nil)
		if a.Status != 200 {
			t.Fatalf("%s: %d %s", path, a.Status, a.Body)
		}
		mustJSON(t, a.Body, &p)
		return p.Items
	}
	for name, tok := range map[string]string{"a reader of both": e.tokE, "the writer": e.tokA} {
		got := names(list(tok, "/api/v1/packages"))
		if !slices.Equal(got, []string{"@team-a/ui", "left-pad"}) {
			t.Errorf("%s sees %v: a package the repository does not admit is listed", name, got)
		}
	}
	if a := e.st.Do(r, "GET", "/api/v1/packages/lookup?format=npm&name="+url.QueryEscape("@team-b/core"), e.tokA, nil); a.Status != 404 {
		t.Errorf("lookup of an excluded package: %d", a.Status)
	}
	// The admitted package keeps its versions in the repository.
	ui := pkgByName(list(e.tokE, "/api/v1/packages"), "@team-a/ui")
	if num(ui["versionCount"]) != 4 {
		t.Errorf("versionCount %v", ui["versionCount"])
	}
}
