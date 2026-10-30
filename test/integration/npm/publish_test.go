//go:build integration

package npm

import (
	"testing"
)

// TestR_NPM_007_PublishStoresTheVersion: a published scoped and an unscoped package
// are stored as the rows, the item and the properties R-NPM-006 describes, and the
// answer is the 201 that npm ignores the body of.
func TestR_NPM_007_PublishStoresTheVersion(t *testing.T) {
	e := newEnv(t)
	a := e.replica("web-a")
	for _, name := range []string{"@acme/ui", "plain-pkg"} {
		t.Run(name, func(t *testing.T) {
			p := pkg{Name: name, Version: "1.2.0", Deps: map[string]string{"react": "^19.0.0"}}
			ans := e.mustPublish(a, "npm-local", e.tokA, p)
			var got map[string]any
			if err := jsonUnmarshal(ans.Body, &got); err != nil {
				t.Fatal(err)
			}
			if got["ok"] != true || got["id"] != name || got["rev"] == "" {
				t.Fatalf("body %s", ans.Body)
			}
			if e.versions("npm-local", name) != 1 || e.revSeq("npm-local", name) != 1 {
				t.Fatalf("versions %d rev_seq %d", e.versions("npm-local", name), e.revSeq("npm-local", name))
			}
			if tags := e.tags("npm-local", name); tags["latest"] != "1.2.0" || len(tags) != 1 {
				t.Fatalf("tags %v", tags)
			}
			integrity, shasum := digests(tarballOf(t, p))
			if got := e.st.Text(`SELECT nv.integrity FROM npm_versions nv JOIN package_versions pv ON pv.id = nv.version_id JOIN packages p ON p.id = pv.package_id WHERE p.name = $1`, name); got != integrity {
				t.Fatalf("integrity %s, want %s", got, integrity)
			}
			if got := e.st.Text(`SELECT nv.shasum FROM npm_versions nv JOIN package_versions pv ON pv.id = nv.version_id JOIN packages p ON p.id = pv.package_id WHERE p.name = $1`, name); got != shasum {
				t.Fatalf("shasum %s, want %s", got, shasum)
			}
			if got := e.st.Text(`SELECT nv.readme FROM npm_versions nv JOIN package_versions pv ON pv.id = nv.version_id JOIN packages p ON p.id = pv.package_id WHERE p.name = $1`, name); got != "# "+name+"\n" {
				t.Fatalf("readme %q", got)
			}
			// the stored manifest has the server-controlled fields of R-NPM-007
			stored := e.st.Text(`SELECT nv.manifest::text FROM npm_versions nv JOIN package_versions pv ON pv.id = nv.version_id JOIN packages p ON p.id = pv.package_id WHERE p.name = $1`, name)
			var m map[string]any
			if err := jsonUnmarshal([]byte(stored), &m); err != nil {
				t.Fatal(err)
			}
			dist, _ := m["dist"].(map[string]any)
			npmUser, _ := m["_npmUser"].(map[string]any)
			if m["_id"] != name+"@1.2.0" || dist["integrity"] != integrity || dist["shasum"] != shasum || dist["tarball"] != nil || npmUser["name"] != "alice" {
				t.Fatalf("stored manifest %s", stored)
			}
			// the item and its properties (R-NPM-006)
			base := baseName(name)
			path := name + "/-/" + base + "-1.2.0.tgz"
			if got := e.st.Number(`SELECT count(*) FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = 'npm-local' AND i.path = $1 AND i.kind = 'file'`, path); got != 1 {
				t.Fatalf("items at %s: %d", path, got)
			}
			props := e.st.Text(`SELECT string_agg(ip.key || '=' || ip.value, ',' ORDER BY ip.key) FROM item_properties ip JOIN items i ON i.id = ip.item_id WHERE i.path = $1`, path)
			want := "npm.name=" + name + ",npm.version=1.2.0"
			if name == "@acme/ui" {
				want += ",npm.scope=@acme"
			}
			if want = sortedProps(want); props != want {
				t.Fatalf("properties %q, want %q", props, want)
			}
			// the raw view serves the tarball (R-NPM-006 criterion)
			raw := e.st.Do(a, "GET", "/files/npm-local/"+path, e.tokA, nil)
			if raw.Status != 200 || len(raw.Body) == 0 {
				t.Fatalf("/files: %d", raw.Status)
			}
			if e.events("npm.publish", name, "") != 1 {
				t.Fatalf("npm.publish events: %d", e.events("npm.publish", name, ""))
			}
		})
	}
	t.Run("the lead item is the tarball", func(t *testing.T) {
		got := e.st.Text(`SELECT i.path FROM package_versions pv JOIN items i ON i.id = pv.lead_item_id JOIN packages p ON p.id = pv.package_id WHERE p.name = '@acme/ui'`)
		if got != "@acme/ui/-/ui-1.2.0.tgz" {
			t.Fatalf("lead item %q", got)
		}
	})
}

// TestR_NPM_007_PublishTagsAndTheLatestGuard: a publish applies the tag of its body; a
// lower version under latest is stored and latest stays, with the npm-notice header
// (R-NPM-016); a first publish under another tag also sets latest.
func TestR_NPM_016_PublishTagsAndTheLatestGuard(t *testing.T) {
	e := newEnv(t)
	a := e.replica("web-a")
	e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "tagged", Version: "2.0.0"})
	t.Run("R-NPM-016 a lower version under latest keeps latest and says so", func(t *testing.T) {
		ans := e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "tagged", Version: "1.1.5"})
		if n := ans.Header.Get("npm-notice"); n != "latest stays at 2.0.0; move it with npm dist-tag add" {
			t.Fatalf("npm-notice %q", n)
		}
		if tags := e.tags("npm-local", "tagged"); tags["latest"] != "2.0.0" {
			t.Fatalf("tags %v", tags)
		}
		if e.versions("npm-local", "tagged") != 2 {
			t.Fatal("the version is not stored")
		}
	})
	t.Run("R-NPM-016 another tag leaves latest", func(t *testing.T) {
		e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "tagged", Version: "3.0.0-beta.1", Tag: "next"})
		if tags := e.tags("npm-local", "tagged"); tags["latest"] != "2.0.0" || tags["next"] != "3.0.0-beta.1" {
			t.Fatalf("tags %v", tags)
		}
	})
	t.Run("R-NPM-016 a first publish under another tag also sets latest", func(t *testing.T) {
		e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "first-next", Version: "1.0.0-rc.1", Tag: "next"})
		if tags := e.tags("npm-local", "first-next"); tags["latest"] != "1.0.0-rc.1" || tags["next"] != "1.0.0-rc.1" {
			t.Fatalf("tags %v", tags)
		}
	})
	t.Run("R-NPM-016 a higher version moves latest", func(t *testing.T) {
		e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "tagged", Version: "2.1.0"})
		if tags := e.tags("npm-local", "tagged"); tags["latest"] != "2.1.0" {
			t.Fatalf("tags %v", tags)
		}
	})
}
