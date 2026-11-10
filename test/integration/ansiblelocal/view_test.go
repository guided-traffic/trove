//go:build integration

package ansiblelocal

import (
	"slices"
	"strings"
	"testing"
)

// TestR_ANS_026_ThePackageScreensShowACollection: the package list shows the collection with its description and tags, the versions are
// ordered by semantic-version precedence, and the version details carry the licenses, the metadata of the manifest and the README
// read from the archive (R-ANS-026, R-API-029).
func TestR_ANS_026_ThePackageScreensShowACollection(t *testing.T) {
	e := newEnv(t)
	for _, v := range []string{"1.0.0", "1.10.0", "1.2.0", "2.0.0-rc.1"} {
		s := spec(v)
		s.Info = map[string]any{"tags": []string{"tools", "acme"}, "authors": []string{"Ada <ada@example.com>"}, "repository": "https://git.example/acme/tools"}
		if a := e.publish(e.tokA, specBytes(s), true); a.Status != 202 {
			t.Fatalf("publish %s: %d %s", v, a.Status, a.Body)
		}
	}
	list := e.st.Do(e.r, "GET", "/api/v1/packages?format=ansible", e.tokC, nil)
	items, _ := list.JSON(t)["items"].([]any)
	if list.Status != 200 || len(items) != 1 {
		t.Fatalf("list: %d %s", list.Status, list.Body)
	}
	first := items[0].(map[string]any)
	if first["name"] != "acme.tools" || first["description"] != "A test collection." || first["latestVersion"] != "2.0.0-rc.1" {
		t.Errorf("the list shows %v", first)
	}
	if found := e.st.Do(e.r, "GET", "/api/v1/packages?format=ansible&q=test+collection", e.tokC, nil); found.Status != 200 || len(found.JSON(t)["items"].([]any)) != 1 {
		t.Errorf("search by description: %d %s", found.Status, found.Body)
	}
	if found := e.st.Do(e.r, "GET", "/api/v1/packages?format=ansible&keyword=acme", e.tokC, nil); found.Status != 200 || len(found.JSON(t)["items"].([]any)) != 1 {
		t.Errorf("search by tag: %d %s", found.Status, found.Body)
	}
	lookup := e.st.Do(e.r, "GET", "/api/v1/packages/lookup?format=ansible&name=ACME.Tools", e.tokC, nil)
	if lookup.Status != 200 {
		t.Fatalf("lookup: %d %s", lookup.Status, lookup.Body)
	}
	id, _ := lookup.JSON(t)["id"].(string)
	vers := e.st.Do(e.r, "GET", "/api/v1/packages/"+id+"/versions?sort=-version", e.tokC, nil)
	var got []string
	for _, it := range vers.JSON(t)["items"].([]any) {
		got = append(got, it.(map[string]any)["version"].(string))
	}
	if !slices.Equal(got, []string{"2.0.0-rc.1", "1.10.0", "1.2.0", "1.0.0"}) {
		t.Errorf("versions by precedence: %v\n%s", got, vers.Body)
	}
	d := e.st.Do(e.r, "GET", "/api/v1/packages/"+id+"/versions/1.10.0", e.tokC, nil)
	if d.Status != 200 {
		t.Fatalf("details: %d %s", d.Status, d.Body)
	}
	detail := d.JSON(t)
	md, _ := detail["metadata"].(map[string]any)
	if detail["hasReadme"] != true || md["requiresAnsible"] != ">=2.14.0" || md["repositoryUrl"] != "https://git.example/acme/tools" {
		t.Errorf("details %v", detail)
	}
	if licenses, _ := detail["licenses"].([]any); len(licenses) != 1 || licenses[0] != "MIT" {
		t.Errorf("licenses %v", detail["licenses"])
	}
	readme := e.st.Do(e.r, "GET", "/api/v1/packages/"+id+"/versions/1.10.0/readme", e.tokC, nil)
	if readme.Status != 200 || !strings.Contains(string(readme.Body), "Tools for the tests") {
		t.Errorf("README: %d %s", readme.Status, readme.Body)
	}
}
