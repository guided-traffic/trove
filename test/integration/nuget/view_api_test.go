//go:build integration

package nuget

import (
	"slices"
	"testing"
	"time"
)

// TestR_API_029_NuGetPackagesAreListedAndDescribed runs the package screens of the management API over NuGet
// rows: the list shows the package under its original ID casing and its description, the versions are ordered
// by NuGet's precedence, and the details carry the licenses, the README flag and the metadata of Q-NUGET-48.
func TestR_API_029_NuGetPackagesAreListedAndDescribed(t *testing.T) {
	e := newEnv(t)
	repoID := e.st.Number(`SELECT id FROM repositories WHERE key = 'nuget-local'`)
	pkgID := e.st.Number(`INSERT INTO packages (repo_id, name, display_name) VALUES ($1, 'acme.widgets', 'Acme.Widgets') RETURNING id`, repoID)
	meta := `{"description": "Acme widgets for Contoso", "tags": ["widgets", "contoso"], "authors": "Ada Lovelace, Grace Hopper",
	          "projectUrl": "https://acme.example/widgets", "license": {"type": "expression", "value": "MIT"}, "readme": "README.md",
	          "dependencyGroups": [{"targetFramework": "net8.0", "dependencies": [{"id": "Newtonsoft.Json", "range": "[13.0.1, )"}]}]}`
	for i, v := range []string{"1.0.0", "1.10.0", "1.2.0", "2.0.0-rc.1"} {
		id := e.st.Number(`INSERT INTO package_versions (package_id, version, display_version) VALUES ($1, $2, $2) RETURNING id`, pkgID, v)
		e.st.Exec(`INSERT INTO nuget_version (version_id, repository_id, lower_id, declared_id, original_version, sort_key, is_prerelease, is_semver2, listed, published_at, metadata, search_doc, origin)
		           VALUES ($1, $2, 'acme.widgets', 'Acme.Widgets', $3, '\x01', $4, false, $5, $6, $7::jsonb, to_tsvector('simple', 'acme widgets'), 'push')`,
			id, repoID, v, v == "2.0.0-rc.1", v != "1.2.0", time.Date(2026, 5, 1, i, 0, 0, 0, time.UTC), meta)
	}
	r := e.replica("web-a")

	list := e.st.Do(r, "GET", "/api/v1/packages?format=nuget", e.tokReader, nil)
	if list.Status != 200 {
		t.Fatalf("%d %s", list.Status, list.Body)
	}
	items, _ := list.JSON(t)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("%s", list.Body)
	}
	first, _ := items[0].(map[string]any)
	if first["name"] != "Acme.Widgets" || !slices.Equal(anyStrings(first["keywords"]), []string{"widgets", "contoso"}) || first["description"] != "Acme widgets for Contoso" || first["latestVersion"] != "1.10.0" {
		t.Errorf("the list shows %v", first)
	}
	if found := e.st.Do(r, "GET", "/api/v1/packages?format=nuget&q=contoso", e.tokReader, nil); found.Status != 200 || len(found.JSON(t)["items"].([]any)) != 1 {
		t.Errorf("search by description: %d %s", found.Status, found.Body)
	}
	if found := e.st.Do(r, "GET", "/api/v1/packages?format=nuget&keyword=contoso", e.tokReader, nil); found.Status != 200 || len(found.JSON(t)["items"].([]any)) != 1 {
		t.Errorf("search by tag: %d %s", found.Status, found.Body)
	}

	lookup := e.st.Do(r, "GET", "/api/v1/packages/lookup?format=nuget&name=ACME.Widgets", e.tokReader, nil)
	if lookup.Status != 200 {
		t.Fatalf("lookup: %d %s", lookup.Status, lookup.Body)
	}
	id, _ := lookup.JSON(t)["id"].(string)
	vers := e.st.Do(r, "GET", "/api/v1/packages/"+id+"/versions?sort=-version", e.tokReader, nil)
	var got []string
	vitems, _ := vers.JSON(t)["items"].([]any)
	for _, it := range vitems {
		got = append(got, it.(map[string]any)["version"].(string))
	}
	if !slices.Equal(got, []string{"2.0.0-rc.1", "1.10.0", "1.2.0", "1.0.0"}) {
		t.Errorf("versions by precedence: %v\n%s", got, vers.Body)
	}

	d := e.st.Do(r, "GET", "/api/v1/packages/"+id+"/versions/1.10.0", e.tokReader, nil)
	if d.Status != 200 {
		t.Fatalf("details: %d %s", d.Status, d.Body)
	}
	detail := d.JSON(t)
	md, _ := detail["metadata"].(map[string]any)
	if !slices.Equal(anyStrings(detail["licenses"]), []string{"MIT"}) || detail["hasReadme"] != true || md["id"] != "Acme.Widgets" || md["homepage"] != "https://acme.example/widgets" ||
		!slices.Equal(anyStrings(md["authors"]), []string{"Ada Lovelace", "Grace Hopper"}) || md["listed"] != true {
		t.Errorf("details %v", detail)
	}
	groups, _ := md["dependencyGroups"].([]any)
	if len(groups) != 1 || groups[0].(map[string]any)["targetFramework"] != "net8.0" {
		t.Errorf("dependency groups %v", md["dependencyGroups"])
	}
	if un := e.st.Do(r, "GET", "/api/v1/packages/"+id+"/versions/1.2.0", e.tokReader, nil).JSON(t); un["metadata"].(map[string]any)["listed"] != false {
		t.Errorf("an unlisted version: %v", un["metadata"])
	}
	if hidden := e.st.Do(r, "GET", "/api/v1/packages?format=nuget", e.tokNobody, nil); hidden.Status != 200 || len(hidden.JSON(t)["items"].([]any)) != 0 {
		t.Errorf("a person without a grant sees %s", hidden.Body)
	}
}

func anyStrings(v any) []string {
	var out []string
	l, _ := v.([]any)
	for _, x := range l {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}
