package document

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/formats/npm/rows"
)

var epoch = time.Date(2026, 10, 28, 9, 12, 44, 512_000_000, time.UTC)

func manifestOf(name, version string, extra map[string]any) []byte {
	m := map[string]any{
		"name": name, "version": version, "_id": name + "@" + version, "_npmUser": map[string]string{"name": "alice"},
		"_hasShrinkwrap": false,
		"dist":           map[string]any{"integrity": "sha512-AAAA", "shasum": strings.Repeat("a", 40), "fileCount": 3, "unpackedSize": 1200},
	}
	for k, v := range extra {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return b
}

func samplePackage() *rows.LocalPackage {
	return &rows.LocalPackage{
		PackageID: 9, RevSeq: 7, CreatedAt: epoch, ModifiedAt: epoch.Add(time.Hour),
		Tags: map[string]string{"latest": "1.10.0", "next": "2.0.0-rc.1"},
		Versions: []rows.LocalVersion{
			{Version: "1.10.0", Manifest: manifestOf("@acme/ui", "1.10.0", map[string]any{"description": "ten", "license": "MIT", "readme": "inline", "keywords": []string{"ui"}}), PublishedAt: epoch.Add(2 * time.Minute)},
			{Version: "2.0.0-rc.1", Manifest: manifestOf("@acme/ui", "2.0.0-rc.1", nil), PublishedAt: epoch.Add(3 * time.Minute)},
			{Version: "1.2.0", Manifest: manifestOf("@acme/ui", "1.2.0", map[string]any{"deprecated": "from the manifest", "dependencies": map[string]string{"react": "^19"}}), Deprecated: "use 1.10", PublishedAt: epoch.Add(time.Minute)},
		},
		Readme: "# readme of ten", ReadmeFilename: "README.md",
	}
}

func options() Options {
	return Options{
		Name: "@acme/ui", Basename: "ui", TarballPrefix: "https://trove.example.com/api/npm/npm-local/@acme/ui/-/",
		Rev: "7-0123456789abcdef0123456789abcdef", AbbreviatedTime: true,
	}
}

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	return m
}

// TestR_NPM_010_FullDocument: the document is a function of the rows: the versions in
// SemVer order, the links on the prefix, the deprecation of the column, the
// top-level fields of the version latest points to.
func TestR_NPM_010_FullDocument(t *testing.T) {
	b, err := FullDocument(samplePackage(), options())
	if err != nil {
		t.Fatal(err)
	}
	doc := decode(t, b)
	if doc["_id"] != "@acme/ui" || doc["_rev"] != "7-0123456789abcdef0123456789abcdef" || doc["name"] != "@acme/ui" {
		t.Fatalf("identity %v", doc)
	}
	t.Run("the versions are in SemVer order whatever order the rows came in", func(t *testing.T) {
		raw := string(b)
		from := strings.Index(raw, `"versions"`)
		last := -1
		for _, v := range []string{`"1.2.0":`, `"1.10.0":`, `"2.0.0-rc.1":`} {
			i := strings.Index(raw[from:], v)
			if i <= last {
				t.Fatalf("1.2.0 < 1.10.0 < 2.0.0-rc.1 expected in the bytes; %s is at %d after %d", v, i, last)
			}
			last = i
		}
	})
	t.Run("a second rendering of the same rows is the same bytes", func(t *testing.T) {
		p := samplePackage()
		p.Versions[0], p.Versions[2] = p.Versions[2], p.Versions[0]
		again, err := FullDocument(p, options())
		if err != nil || string(again) != string(b) {
			t.Fatalf("the rendering depends on the order of the rows: %v", err)
		}
	})
	t.Run("dist.tarball is on the prefix with the unencoded scope separator", func(t *testing.T) {
		v := doc["versions"].(map[string]any)["1.2.0"].(map[string]any)
		dist := v["dist"].(map[string]any)
		if dist["tarball"] != "https://trove.example.com/api/npm/npm-local/@acme/ui/-/ui-1.2.0.tgz" || dist["integrity"] != "sha512-AAAA" {
			t.Fatalf("dist %v", dist)
		}
	})
	t.Run("deprecated is the column, never the manifest", func(t *testing.T) {
		vs := doc["versions"].(map[string]any)
		if vs["1.2.0"].(map[string]any)["deprecated"] != "use 1.10" {
			t.Fatalf("%v", vs["1.2.0"])
		}
		if _, ok := vs["1.10.0"].(map[string]any)["deprecated"]; ok {
			t.Fatal("a version with no deprecation is deprecated")
		}
	})
	t.Run("the top level comes from latest, and the README from the column unless the manifest has one", func(t *testing.T) {
		if doc["description"] != "ten" || doc["license"] != "MIT" || doc["keywords"] == nil || doc["readmeFilename"] != "README.md" {
			t.Fatalf("%v", doc)
		}
		if doc["readme"] != "inline" {
			t.Fatalf("readme %v", doc["readme"])
		}
		for v, m := range doc["versions"].(map[string]any) {
			if _, ok := m.(map[string]any)["readme"]; ok {
				t.Fatalf("version %s carries a readme", v)
			}
		}
		p := samplePackage()
		p.Versions[0].Manifest = manifestOf("@acme/ui", "1.10.0", nil)
		alt := decode(t, mustFull(t, p))
		if alt["readme"] != "# readme of ten" {
			t.Fatalf("readme %v", alt["readme"])
		}
	})
	t.Run("time has created, modified and each version, to the millisecond in UTC", func(t *testing.T) {
		tm := doc["time"].(map[string]any)
		if tm["created"] != "2026-10-28T09:12:44.512Z" || tm["modified"] != "2026-10-28T10:12:44.512Z" || tm["1.2.0"] != "2026-10-28T09:13:44.512Z" {
			t.Fatalf("%v", tm)
		}
		if len(tm) != 5 {
			t.Fatalf("%v", tm)
		}
	})
	t.Run("maintainers are the distinct publishers", func(t *testing.T) {
		p := samplePackage()
		p.Versions[1].Manifest = manifestOf("@acme/ui", "2.0.0-rc.1", map[string]any{"_npmUser": map[string]string{"name": "sa:builder"}})
		ms := decode(t, mustFull(t, p))["maintainers"].([]any)
		if fmt.Sprint(ms) != "[map[name:alice] map[name:sa:builder]]" {
			t.Fatalf("%v", ms)
		}
	})
	t.Run("a manifest that is not JSON is an error, not a document", func(t *testing.T) {
		p := samplePackage()
		p.Versions[0].Manifest = []byte("{")
		if _, err := FullDocument(p, options()); err == nil {
			t.Fatal("no error")
		}
	})
}

func mustFull(t *testing.T, p *rows.LocalPackage) []byte {
	t.Helper()
	b, err := FullDocument(p, options())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestR_NPM_011_AbbreviatedDocument: name, modified, tags, the installer's fields.
func TestR_NPM_011_AbbreviatedDocument(t *testing.T) {
	p := samplePackage()
	p.Versions[2].Manifest = manifestOf("@acme/ui", "1.2.0", map[string]any{
		"dependencies": map[string]string{"react": "^19"}, "peerDependencies": map[string]string{"x": "1"}, "bin": map[string]string{"ui": "bin.js"},
		"engines": map[string]string{"node": ">=20"}, "os": []string{"linux"}, "cpu": []string{"x64"}, "libc": []string{"glibc"},
		"scripts": map[string]string{"build": "x"}, "main": "i.js", "funding": "https://x", "hasInstallScript": true,
	})
	b, err := AbbreviatedDocument(p, options())
	if err != nil {
		t.Fatal(err)
	}
	doc := decode(t, b)
	if len(doc) != 5 || doc["name"] != "@acme/ui" || doc["modified"] != "2026-10-28T10:12:44.512Z" || doc["time"] == nil || doc["dist-tags"] == nil {
		t.Fatalf("top level %v", doc)
	}
	v := doc["versions"].(map[string]any)["1.2.0"].(map[string]any)
	for _, k := range []string{"name", "version", "dist", "deprecated", "dependencies", "peerDependencies", "bin", "engines", "os", "cpu", "libc", "funding", "_hasShrinkwrap", "hasInstallScript"} {
		if _, ok := v[k]; !ok {
			t.Errorf("the abbreviated version lacks %q: %v", k, v)
		}
	}
	for _, k := range []string{"scripts", "main", "_id", "_npmUser", "description"} {
		if _, ok := v[k]; ok {
			t.Errorf("the abbreviated version holds %q", k)
		}
	}
	if v["dist"].(map[string]any)["tarball"] == nil || v["dist"].(map[string]any)["fileCount"] == nil {
		t.Fatalf("dist %v", v["dist"])
	}
	t.Run("the time map is the setting's", func(t *testing.T) {
		o := options()
		o.AbbreviatedTime = false
		b, err := AbbreviatedDocument(p, o)
		if err != nil || decode(t, b)["time"] != nil {
			t.Fatalf("%v %s", err, b)
		}
	})
	t.Run("it is smaller than the full document", func(t *testing.T) {
		if full := mustFull(t, p); len(b) >= len(full) {
			t.Fatalf("abbreviated %d bytes, full %d", len(b), len(full))
		}
	})
}

// TestR_NPM_014_VersionDocumentIsTheEntryOfTheFullDocument.
func TestR_NPM_014_VersionDocumentIsTheEntryOfTheFullDocument(t *testing.T) {
	p := samplePackage()
	full := decode(t, mustFull(t, p))["versions"].(map[string]any)
	for _, v := range p.Versions {
		one := &rows.LocalPackage{PackageID: p.PackageID, RevSeq: p.RevSeq, Versions: []rows.LocalVersion{v}}
		b, err := VersionDocument(one, options())
		if err != nil {
			t.Fatal(err)
		}
		want, _ := json.Marshal(full[v.Version])
		got, _ := json.Marshal(decode(t, b))
		if string(got) != string(want) {
			t.Errorf("%s:\n got %s\nwant %s", v.Version, got, want)
		}
	}
	if _, err := VersionDocument(p, options()); err == nil {
		t.Fatal("a document of three versions rendered as one")
	}
}

// TestR_NPM_011_Negotiate.
func TestR_NPM_011_Negotiate(t *testing.T) {
	for _, c := range []struct {
		accept string
		want   Variant
	}{
		{"application/vnd.npm.install-v1+json; q=1.0, application/json; q=0.8, */*", Abbreviated},
		{"application/vnd.npm.install-v1+json", Abbreviated},
		{"application/vnd.npm.install-v1+json, application/json", Full},
		{"application/vnd.npm.install-v1+json; q=0.9, application/json; q=1", Full},
		{"application/json; q=0.1, application/vnd.npm.install-v1+json; q=0.2", Abbreviated},
		{"application/vnd.npm.install-v1+json; q=1, */*; q=0.5", Abbreviated},
		{"application/vnd.npm.install-v1+json; q=0.5, */*", Full},
		{"application/vnd.npm.install-v1+json; q=0, */*", Full},
		{"APPLICATION/VND.NPM.INSTALL-V1+JSON;Q=1, application/json;q=0.8", Abbreviated},
		{"", Full},
		{"*/*", Full},
		{"application/json", Full},
		{"text/html", Full},
		{"application/*", Full},
		{"application/vnd.npm.install-v1+json; q=oops, application/json; q=0.5", Abbreviated},
	} {
		if got := Negotiate(c.accept); got != c.want {
			t.Errorf("Accept %q: %s, want %s", c.accept, got, c.want)
		}
	}
	if Abbreviated.ContentType() != "application/vnd.npm.install-v1+json" || Full.ContentType() != "application/json" {
		t.Fatal("the media type of a variant")
	}
}

// TestR_NPM_013_ETagInputs: every input changes the validator, none of them is per
// replica, and the weak comparison of RFC 9110 decides a match.
func TestR_NPM_013_ETagInputs(t *testing.T) {
	base := Inputs{Repo: 1, Package: 2, Rev: 3, Variant: "full", BaseURL: "https://t.example", Endpoint: "public", Exposure: "fp", Settings: "4/true"}
	again := base
	if base.ETag() != again.ETag() || !strings.HasPrefix(base.ETag(), `W/"`) {
		t.Fatalf("%q", base.ETag())
	}
	seen := map[string]string{base.ETag(): "base"}
	for name, mod := range map[string]func(*Inputs){
		"repo": func(i *Inputs) { i.Repo++ }, "package": func(i *Inputs) { i.Package++ }, "rev": func(i *Inputs) { i.Rev++ },
		"variant": func(i *Inputs) { i.Variant = "abbreviated" }, "base URL": func(i *Inputs) { i.BaseURL = "https://u.example" },
		"endpoint": func(i *Inputs) { i.Endpoint = "internal" }, "exposure": func(i *Inputs) { i.Exposure = "fq" },
		"settings": func(i *Inputs) { i.Settings = "5/true" },
		// fields that would run together without length prefixes
		"boundary": func(i *Inputs) { i.Endpoint, i.Exposure = "publicf", "p" },
	} {
		in := base
		mod(&in)
		if prev, dup := seen[in.ETag()]; dup {
			t.Errorf("changing %s gives the validator of %s", name, prev)
		}
		seen[in.ETag()] = name
	}
	etag := base.ETag()
	for header, want := range map[string]bool{
		etag: true, strings.TrimPrefix(etag, "W/"): true, `"x", ` + etag: true, etag + `, "x"`: true,
		`W/"x"`: false, "": false, "*": false, `W/"` + etag: false,
	} {
		if got := IfNoneMatch(header, etag); got != want {
			t.Errorf("If-None-Match %q: %v, want %v", header, got, want)
		}
	}
}

func TestAcceptsGzip(t *testing.T) {
	for h, want := range map[string]bool{
		"gzip": true, "gzip, deflate, br": true, "deflate, gzip;q=0.5": true, "x-gzip": true,
		"gzip;q=0": false, "identity": false, "": false, "br": false,
	} {
		if got := AcceptsGzip(h); got != want {
			t.Errorf("Accept-Encoding %q: %v", h, got)
		}
	}
}

// bigPackage is a package of n versions with manifests of a few kilobytes each.
func bigPackage(n int) *rows.LocalPackage {
	p := &rows.LocalPackage{PackageID: 1, RevSeq: 9, CreatedAt: epoch, ModifiedAt: epoch, Tags: map[string]string{"latest": fmt.Sprintf("1.%d.0", n-1)}}
	deps := map[string]string{}
	for i := range 40 {
		deps[fmt.Sprintf("dependency-%d", i)] = "^1.0.0"
	}
	for i := range n {
		v := fmt.Sprintf("1.%d.0", i)
		p.Versions = append(p.Versions, rows.LocalVersion{
			Version: v, PublishedAt: epoch.Add(time.Duration(i) * time.Minute),
			Manifest: manifestOf("big", v, map[string]any{
				"description": strings.Repeat("d", 200), "dependencies": deps, "devDependencies": deps, "scripts": map[string]string{"build": "tsc", "test": "jest"},
				"repository": map[string]string{"type": "git", "url": "git+https://example.org/big.git"}, "keywords": []string{"a", "b", "c"},
				"engines": map[string]string{"node": ">=20"}, "exports": map[string]any{".": map[string]string{"import": "./i.mjs", "require": "./i.cjs"}},
			}),
		})
	}
	return p
}

// BenchmarkR_NPM_010_RenderThousandVersions: the full document of a package with 1,000
// versions renders on a miss in well under the 200 ms target of R-NPM-010 (the target
// is checked by the integration test over the real stack as well).
func BenchmarkR_NPM_010_RenderThousandVersions(b *testing.B) {
	p := bigPackage(1000)
	o := options()
	o.Name, o.Basename = "big", "big"
	for _, c := range []struct {
		name   string
		render func(*rows.LocalPackage, Options) ([]byte, error)
	}{{"full", FullDocument}, {"abbreviated", AbbreviatedDocument}} {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			var size int
			for b.Loop() {
				doc, err := c.render(p, o)
				if err != nil {
					b.Fatal(err)
				}
				size = len(doc)
			}
			b.ReportMetric(float64(size)/1024, "KiB/doc")
		})
	}
}

// TestR_NPM_010_ThousandVersionsRenderInMemoryAndOnce renders the 1,000 versions once
// and checks that the document is complete; the time is the benchmark's.
func TestR_NPM_010_ThousandVersionsRender(t *testing.T) {
	o := options()
	o.Name, o.Basename = "big", "big"
	doc, err := FullDocument(bigPackage(1000), o)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(decode(t, doc)["versions"].(map[string]any)); got != 1000 {
		t.Fatalf("%d versions", got)
	}
}
