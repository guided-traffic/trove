package npm

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/formats/npm/document"
	"github.com/guided-traffic/trove/internal/formats/npm/remote"
	"github.com/guided-traffic/trove/internal/formats/npm/rows"
)

// TestR_NPM_011_RemoteFormsAreTheLocalFormsOfTheSameDocument pins the two renderings of the
// install document and of the single-version document to each other: the local one renders them
// from rows, the remote one derives them from the full document. A member added to one list and
// not to the other makes a client see different documents from a local and from a remote
// repository of the same package.
func TestR_NPM_011_RemoteFormsAreTheLocalFormsOfTheSameDocument(t *testing.T) {
	at := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	manifest := func(v string) []byte {
		b, _ := json.Marshal(map[string]any{
			"name": "@acme/ui", "version": v, "description": "d", "main": "i.js", "scripts": map[string]string{"test": "x"},
			"dependencies": map[string]string{"a": "^1"}, "optionalDependencies": map[string]string{"b": "^1"},
			"devDependencies": map[string]string{"c": "^1"}, "peerDependencies": map[string]string{"d": "^1"},
			"peerDependenciesMeta": map[string]any{"d": map[string]bool{"optional": true}}, "bin": map[string]string{"ui": "bin.js"},
			"engines": map[string]string{"node": ">=18"}, "cpu": []string{"x64"}, "os": []string{"linux"}, "libc": []string{"glibc"},
			"funding": "https://example.org/f", "hasInstallScript": true, "_hasShrinkwrap": false, "directories": map[string]string{"bin": "b"},
			"bundleDependencies": []string{"a"}, "acceptDependencies": map[string]string{"e": "^1"},
			"dist":     map[string]any{"integrity": "sha512-x", "shasum": "abc", "fileCount": 3, "unpackedSize": 10},
			"_npmUser": map[string]string{"name": "alice"}, "readme": "# x", "readmeFilename": "README.md", "keywords": []string{"k"},
		})
		return b
	}
	p := &rows.LocalPackage{
		PackageID: 7, RevSeq: 4, CreatedAt: at, ModifiedAt: at.Add(time.Hour), Tags: map[string]string{"latest": "1.1.0", "next": "1.0.0"},
		Versions: []rows.LocalVersion{
			{Version: "1.0.0", Manifest: manifest("1.0.0"), PublishedAt: at},
			{Version: "1.1.0", Manifest: manifest("1.1.0"), Deprecated: "use 2", PublishedAt: at.Add(time.Hour)},
		},
	}
	prefix := "https://t.example/api/npm/r/@acme/ui/-/"
	o := document.Options{Name: "@acme/ui", Basename: "ui", TarballPrefix: prefix, Rev: "4-x", AbbreviatedTime: true}
	full, err := document.FullDocument(p, o)
	if err != nil {
		t.Fatal(err)
	}
	local, err := document.AbbreviatedDocument(p, o)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := remote.Parse(full)
	if err != nil {
		t.Fatal(err)
	}
	derived, err := parsed.Abbreviated("@acme/ui", prefix, true)
	if err != nil {
		t.Fatal(err)
	}
	if !sameJSON(t, local, derived) {
		t.Errorf("the install documents differ:\nlocal   %s\nderived %s", local, derived)
	}
	single, err := document.VersionDocument(&rows.LocalPackage{PackageID: 7, RevSeq: 4, Tags: p.Tags, Versions: p.Versions[1:]}, o)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := parsed.Version("@acme/ui", prefix, "latest")
	if err != nil || !ok || !sameJSON(t, single, got) {
		t.Errorf("the version documents differ: %v %v\nlocal   %s\nderived %s", ok, err, single, got)
	}
	again, err := parsed.Full("@acme/ui", prefix, "")
	if err != nil || !sameJSON(t, full, again) {
		t.Errorf("rewriting the local full document changed it: %v", err)
	}
}

func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatalf("%v\n%s", err, a)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	return reflect.DeepEqual(x, y)
}
