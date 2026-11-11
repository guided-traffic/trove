//go:build integration

package ansiblevirtual

import (
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_ANS_026_ARemoteMembersCollectionInstalledThroughAVirtualRepositoryIsListedWithItsDocumentation: the archive a client installs through
// the virtual repository is cached by its remote member, the package appears in the package API, and the version details and the three
// documentation operations answer for it, naming the remote member.
func TestR_ANS_026_ARemoteMembersCollectionInstalledThroughAVirtualRepositoryIsListedWithItsDocumentation(t *testing.T) {
	e := newEnv(t, options{})
	for _, path := range []string{"v3/collections/acme/base/versions/1.0.0/", "v3/artifacts/collections/acme-base-1.0.0.tar.gz", docsPathOf("acme", "base", "1.0.0")} {
		if a := e.get(rVirtual, path, e.reader); a.Status != http.StatusOK {
			t.Fatalf("%s: %d %s", path, a.Status, a.Body)
		}
	}
	list := e.st.Do(e.rep, http.MethodGet, "/api/v1/packages?format=ansible", e.reader, nil)
	var names []string
	for _, it := range list.JSON(t)["items"].([]any) {
		names = append(names, it.(map[string]any)["name"].(string))
	}
	if list.Status != http.StatusOK || strings.Join(names, ",") != "acme.base" {
		t.Fatalf("list: %d %v %s", list.Status, names, list.Body)
	}
	lookup := e.st.Do(e.rep, http.MethodGet, "/api/v1/packages/lookup?format=ansible&name=acme.base", e.reader, nil)
	id, _ := lookup.JSON(t)["id"].(string)
	if lookup.Status != http.StatusOK || id == "" {
		t.Fatalf("lookup: %d %s", lookup.Status, lookup.Body)
	}
	p := "/api/v1/packages/" + id + "/versions/1.0.0"
	d := e.st.Do(e.rep, http.MethodGet, p, e.reader, nil)
	detail := d.JSON(t)
	docs, _ := detail["metadata"].(map[string]any)["docs"].(map[string]any)
	if d.Status != http.StatusOK || docs["state"] != "available" || docs["source"] != "upstream" {
		t.Fatalf("details: %d %s", d.Status, d.Body)
	}
	lic, _ := detail["licenses"].([]any)
	md, _ := detail["metadata"].(map[string]any)
	if len(lic) != 1 || lic[0] != "GPL-3.0-or-later" || md["requiresAnsible"] == nil || md["requiresAnsible"] == "" || md["authors"] == nil {
		t.Errorf("the page shows %v", detail)
	}
	if first := list.JSON(t)["items"].([]any)[0].(map[string]any); first["description"] != "trove test collection acme.base" {
		t.Errorf("the list shows %v", first)
	}
	repos := ""
	for _, r := range detail["repositories"].([]any) {
		repos += r.(map[string]any)["key"].(string) + ","
	}
	if !strings.Contains(repos, rGalaxy) || !strings.Contains(repos, rVirtual) {
		t.Errorf("repositories = %s", repos)
	}
	for _, repository := range []string{"", "?repository=" + testenv.DefaultProject + "/" + rVirtual} {
		tree := e.st.Do(e.rep, http.MethodGet, p+"/docs"+repository, e.reader, nil)
		td := tree.JSON(t)
		if tree.Status != http.StatusOK || td["state"] != "available" || td["repository"] != testenv.DefaultProject+"/"+rGalaxy {
			t.Errorf("tree%s: %d %s", repository, tree.Status, tree.Body)
		}
	}
	if entry := e.st.Do(e.rep, http.MethodGet, p+"/docs/entries/module/ping_host", e.reader, nil); entry.Status != http.StatusOK || strings.Contains(string(entry.Body), "<script") {
		t.Errorf("entry: %d %s", entry.Status, entry.Body)
	}
	if file := e.st.Do(e.rep, http.MethodGet, p+"/docs/files/install.md", e.reader, nil); file.Status != http.StatusOK {
		t.Errorf("file: %d %s", file.Status, file.Body)
	}
}
