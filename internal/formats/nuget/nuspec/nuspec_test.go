package nuspec

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/guided-traffic/trove/internal/errs"
)

const ns2013 = "http://schemas.microsoft.com/packaging/2013/05/nuspec.xsd"

func doc(ns, body string) string {
	attr := ""
	if ns != "" {
		attr = ` xmlns="` + ns + `"`
	}
	return `<?xml version="1.0" encoding="utf-8"?><package` + attr + `><metadata>` + body + `</metadata></package>`
}

const minimal = `<id>Acme.Widgets</id><version>1.2.3</version>`

func utf16Bytes(s string, bigEndian bool, bom bool) []byte {
	var out []byte
	put := func(u uint16) {
		if bigEndian {
			out = append(out, byte(u>>8), byte(u))
		} else {
			out = append(out, byte(u), byte(u>>8))
		}
	}
	if bom {
		put(0xFEFF)
	}
	for _, u := range utf16.Encode([]rune(s)) {
		put(u)
	}
	return out
}

func TestR_NUGET_005_NuspecMetadata(t *testing.T) {
	full := doc(ns2013, `
  <id> Acme.Widgets </id><version>1.2.3-beta.1+build</version>
  <title>Widgets</title><authors>Ann, Bob</authors><description>Parts &amp; pieces &#x41;</description>
  <summary>s</summary><releaseNotes>rn</releaseNotes><copyright>c</copyright><language>en-US</language>
  <tags>one  two
three</tags>
  <projectUrl>https://example.org/p</projectUrl><iconUrl>https://example.org/i.png</iconUrl>
  <licenseUrl>https://example.org/l</licenseUrl>
  <license type="expression" version="1.0">MIT OR Apache-2.0</license>
  <icon>images\icon.png</icon><readme>docs/README.md</readme>
  <requireLicenseAcceptance>true</requireLicenseAcceptance>
  <developmentDependency>TRUE</developmentDependency><serviceable>false</serviceable>
  <repository type="git" url="https://example.org/r.git" branch="main" commit="abc"/>
  <packageTypes><packageType name="Template"/><packageType name="DotnetTool" version="1.0"/></packageTypes>
  <dependencies>
    <group targetFramework="net8.0"><dependency id="Acme.Core" version="[1.0,2.0)" include="Compile" exclude="Build"/></group>
    <group targetFramework=".NETStandard2.0"/>
    <group><dependency id="Acme.Any"/></group>
  </dependencies>`)
	full = strings.Replace(full, "<metadata>", `<metadata minClientVersion="4.1.0">`, 1)
	m, err := Parse([]byte(full), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	eq := func(name, got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	eq("id", m.ID, "Acme.Widgets")
	eq("version", m.Version, "1.2.3-beta.1+build")
	eq("title", m.Title, "Widgets")
	eq("authors", m.Authors, "Ann, Bob")
	eq("description", m.Description, "Parts & pieces A")
	eq("summary", m.Summary, "s")
	eq("releaseNotes", m.ReleaseNotes, "rn")
	eq("copyright", m.Copyright, "c")
	eq("language", m.Language, "en-US")
	eq("projectUrl", m.ProjectURL, "https://example.org/p")
	eq("iconUrl", m.IconURL, "https://example.org/i.png")
	eq("licenseUrl", m.LicenseURL, "https://example.org/l")
	eq("icon", m.Icon, `images\icon.png`)
	eq("readme", m.Readme, "docs/README.md")
	eq("minClientVersion", m.MinClientVersion, "4.1.0")
	eq("namespace", m.Namespace, ns2013)
	if got := strings.Join(m.Tags, "|"); got != "one|two|three" {
		t.Errorf("tags = %q", got)
	}
	if m.License == nil || *m.License != (License{Type: "expression", Version: "1.0", Value: "MIT OR Apache-2.0"}) {
		t.Errorf("license = %+v", m.License)
	}
	if !m.RequireLicenseAcceptance || !m.DevelopmentDependency || m.Serviceable {
		t.Errorf("flags = %v %v %v", m.RequireLicenseAcceptance, m.DevelopmentDependency, m.Serviceable)
	}
	if m.Repository == nil || *m.Repository != (Repository{Type: "git", URL: "https://example.org/r.git", Branch: "main", Commit: "abc"}) {
		t.Errorf("repository = %+v", m.Repository)
	}
	want := []DependencyGroup{
		{TargetFramework: "net8.0", Dependencies: []Dependency{{ID: "Acme.Core", Range: "[1.0,2.0)", Include: "Compile", Exclude: "Build"}}},
		{TargetFramework: ".NETStandard2.0"},
		{Dependencies: []Dependency{{ID: "Acme.Any"}}},
	}
	if len(m.DependencyGroups) != len(want) {
		t.Fatalf("groups = %+v", m.DependencyGroups)
	}
	for i, g := range want {
		got := m.DependencyGroups[i]
		if got.TargetFramework != g.TargetFramework || len(got.Dependencies) != len(g.Dependencies) {
			t.Errorf("group %d = %+v, want %+v", i, got, g)
			continue
		}
		for j := range g.Dependencies {
			if got.Dependencies[j] != g.Dependencies[j] {
				t.Errorf("group %d dependency %d = %+v, want %+v", i, j, got.Dependencies[j], g.Dependencies[j])
			}
		}
	}
	if len(m.PackageTypes) != 2 || m.PackageTypes[1] != (PackageType{Name: "DotnetTool", Version: "1.0"}) || m.IsSymbols() {
		t.Errorf("package types = %+v", m.PackageTypes)
	}
}

func TestR_NUGET_005_NuspecDefaults(t *testing.T) {
	m, err := Parse([]byte(doc("", minimal)), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Types(); len(got) != 1 || got[0].Name != "Dependency" || len(m.PackageTypes) != 0 {
		t.Errorf("no declared type reads as Dependency, got %+v / %+v", got, m.PackageTypes)
	}
	if m.License != nil || m.Repository != nil || m.DependencyGroups != nil || m.Tags != nil {
		t.Errorf("absent elements stay empty: %+v", m)
	}
	flat, err := Parse([]byte(doc("", minimal+`<dependencies><dependency id="A" version="1.0"/><dependency id="B"/></dependencies>`)), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(flat.DependencyGroups) != 1 || flat.DependencyGroups[0].TargetFramework != "" || len(flat.DependencyGroups[0].Dependencies) != 2 {
		t.Errorf("a flat list is one group without a target framework: %+v", flat.DependencyGroups)
	}
	lic, err := Parse([]byte(doc("", minimal+`<license type="file">LICENSE.txt</license>`)), Limits{})
	if err != nil || lic.License.Type != "file" || lic.License.Value != "LICENSE.txt" {
		t.Errorf("file license: %+v, %v", lic.License, err)
	}
	sym, err := Parse([]byte(doc("", minimal+`<packageTypes><packageType name="symbolspackage"/></packageTypes>`)), Limits{})
	if err != nil || !sym.IsSymbols() {
		t.Errorf("SymbolsPackage is found case-insensitively: %v", err)
	}
}

func TestR_NUGET_005_NuspecNamespaces(t *testing.T) {
	for _, ns := range []string{
		"", "http://schemas.microsoft.com/packaging/2010/07/nuspec.xsd",
		"http://schemas.microsoft.com/packaging/2011/08/nuspec.xsd", ns2013,
		"http://schemas.microsoft.com/packaging/2012/06/nuspec.xsd",
	} {
		m, err := Parse([]byte(doc(ns, minimal)), Limits{})
		if err != nil || m.ID != "Acme.Widgets" || m.Namespace != ns {
			t.Errorf("namespace %q: %+v, %v", ns, m, err)
		}
	}
}

func TestR_NUGET_005_NuspecEncodings(t *testing.T) {
	body := doc(ns2013, minimal+`<description>Grüße 😀</description>`)
	for name, data := range map[string][]byte{
		"UTF-8":                  []byte(body),
		"UTF-8 with BOM":         append([]byte{0xEF, 0xBB, 0xBF}, body...),
		"UTF-16 LE":              utf16Bytes(strings.Replace(body, "utf-8", "utf-16", 1), false, true),
		"UTF-16 BE":              utf16Bytes(strings.Replace(body, "utf-8", "UTF-16", 1), true, true),
		"UTF-16 without decl":    utf16Bytes(strings.Replace(body, `<?xml version="1.0" encoding="utf-8"?>`, "", 1), false, true),
		"UTF-16LE label":         utf16Bytes(strings.Replace(body, "utf-8", "utf-16le", 1), false, true),
		"UTF-8 without any decl": []byte(strings.Replace(body, `<?xml version="1.0" encoding="utf-8"?>`, "", 1)),
	} {
		m, err := Parse(data, Limits{})
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if m.ID != "Acme.Widgets" || m.Description != "Grüße 😀" {
			t.Errorf("%s: id %q description %q", name, m.ID, m.Description)
		}
	}
}

func TestR_NUGET_005_NuspecRefusals(t *testing.T) {
	deep := minimal + strings.Repeat("<x>", 70) + strings.Repeat("</x>", 70)
	tests := []struct {
		name   string
		data   []byte
		reason error
	}{
		{"document type declaration", []byte(`<!DOCTYPE package>` + doc("", minimal)), ErrDoctype},
		{"document type declaration after the declaration", []byte(`<?xml version="1.0"?><!DOCTYPE package [<!ENTITY x "y">]><package><metadata>` + minimal + `</metadata></package>`), ErrDoctype},
		{"billion laughs", []byte(`<?xml version="1.0"?><!DOCTYPE l [<!ENTITY a "aaaaaaaaaa"><!ENTITY b "&a;&a;&a;&a;&a;&a;&a;&a;&a;&a;">]><package><metadata><id>&b;</id><version>1</version></metadata></package>`), ErrDoctype},
		{"external entity", []byte(`<?xml version="1.0"?><!DOCTYPE l [<!ENTITY x SYSTEM "file:///etc/passwd">]><package><metadata><id>&x;</id><version>1</version></metadata></package>`), ErrDoctype},
		{"lowercase document type declaration", []byte(`<!doctype package>` + doc("", minimal)), ErrDoctype},
		{"entity that nothing defines", []byte(doc("", `<id>&x;</id><version>1</version>`)), ErrNotXML},
		{"unterminated", []byte(`<package><metadata><id>a</id>`), ErrNotXML},
		{"not XML", []byte(`PK\x03\x04`), ErrNotXML},
		{"empty", nil, ErrNotXML},
		{"two roots", []byte(doc("", minimal) + `<package/>`), ErrNotXML},
		{"text after the root", []byte(doc("", minimal) + `junk`), ErrNotXML},
		{"NUL character", []byte(doc("", `<id>a`+"\x00"+`</id><version>1</version>`)), ErrNotXML},
		{"UTF-16 without a byte order mark", utf16Bytes(doc("", minimal), false, false), ErrEncoding},
		{"invalid UTF-8", []byte(doc("", `<id>Acme</id><version>1</version><title>`+"\xff\xfe"+`</title>`)), ErrEncoding},
		{"odd UTF-16 length", append(utf16Bytes(doc("", minimal), false, true), 0x41), ErrEncoding},
		{"lone surrogate", append(append([]byte{0xFF, 0xFE}, utf16Bytes("<package>", false, false)...), 0x00, 0xD8, 0x3E, 0x00), ErrEncoding},
		{"declared encoding that is not the one used", []byte(strings.Replace(doc("", minimal), "utf-8", "utf-16", 1)), ErrEncoding},
		{"UTF-16 declared as UTF-8", utf16Bytes(doc("", minimal), false, true), ErrEncoding},
		{"other declared encoding", []byte(strings.Replace(doc("", minimal), "utf-8", "iso-8859-1", 1)), ErrEncoding},
		{"too deep", []byte(doc("", deep)), ErrTooLarge},
		{"too large", []byte(doc("", minimal+"<description>"+strings.Repeat("a", DefaultMaxBytes)+"</description>")), ErrTooLarge},
		{"wrong root", []byte(`<project><metadata>` + minimal + `</metadata></project>`), ErrShape},
		{"foreign namespace", []byte(doc("http://example.org/ns", minimal)), ErrShape},
		{"no metadata", []byte(`<package/>`), ErrMissingFields},
		{"two metadata", []byte(`<package><metadata>` + minimal + `</metadata><metadata>` + minimal + `</metadata></package>`), ErrDuplicate},
		{"no id", []byte(doc("", `<version>1.0.0</version>`)), ErrMissingFields},
		{"blank id", []byte(doc("", `<id>  </id><version>1.0.0</version>`)), ErrMissingFields},
		{"no version", []byte(doc("", `<id>A</id>`)), ErrMissingFields},
		{"two ids", []byte(doc("", `<id>A</id><id>B</id><version>1</version>`)), ErrDuplicate},
		{"two versions", []byte(doc("", `<id>A</id><version>1</version><version>2</version>`)), ErrDuplicate},
		{"element inside the id", []byte(doc("", `<id>A<b>x</b></id><version>1</version>`)), ErrShape},
		{"id in another namespace than the root", []byte(doc(ns2013, `<id xmlns="http://example.org/x">A</id><version>1</version>`)), ErrMissingFields},
		{"mixed dependency forms", []byte(doc("", minimal+`<dependencies><group targetFramework="net8.0"/><dependency id="A"/></dependencies>`)), ErrMixedDeps},
		{"two dependencies elements", []byte(doc("", minimal+`<dependencies/><dependencies/>`)), ErrDuplicate},
		{"dependency without an id", []byte(doc("", minimal+`<dependencies><dependency version="1.0"/></dependencies>`)), ErrShape},
		{"package type without a name", []byte(doc("", minimal+`<packageTypes><packageType/></packageTypes>`)), ErrShape},
		{"two license elements", []byte(doc("", minimal+`<license>MIT</license><license>X</license>`)), ErrDuplicate},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, err := Parse(tc.data, Limits{})
			if err == nil {
				t.Fatalf("accepted: %+v", m)
			}
			var e *errs.Error
			if !errors.As(err, &e) || e.Kind != errs.Invalid || e.Code != CodeInvalidPackage {
				t.Fatalf("error = %#v, want a 400 %s", err, CodeInvalidPackage)
			}
			if !errors.Is(err, tc.reason) {
				t.Errorf("reason = %v, want %v", err, tc.reason)
			}
		})
	}
}

// The identity trove authorizes is the identity a NuGet client reads: the text of
// an element is its text in document order, CDATA and character references
// included.
func TestR_NUGET_005_NuspecIdentityReadsLikeAClient(t *testing.T) {
	m, err := Parse([]byte(doc("", `<id><![CDATA[Acme]]>.Wi&#100;gets</id><version> 1.0.0 </version>`)), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "Acme.Widgets" || m.Version != "1.0.0" {
		t.Errorf("id %q version %q", m.ID, m.Version)
	}
}

func TestR_NUGET_005_NuspecLimitsOnlyTighten(t *testing.T) {
	data := []byte(doc("", minimal+"<description>"+strings.Repeat("a", 2000)+"</description>"))
	if _, err := Parse(data, Limits{MaxBytes: 1000}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("a tighter bound is applied: %v", err)
	}
	big := append(bytes.Clone(data), bytes.Repeat([]byte(" "), DefaultMaxBytes)...)
	if _, err := Parse(big, Limits{MaxBytes: 1 << 30}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("a looser bound is not honoured: %v", err)
	}
	if _, err := Parse([]byte(doc("", minimal+"<a><b><c/></b></a>")), Limits{MaxDepth: 3}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("a tighter depth is applied: %v", err)
	}
}

func FuzzR_NUGET_005_Nuspec(f *testing.F) {
	f.Add([]byte(doc(ns2013, minimal+`<dependencies><group targetFramework="net8.0"><dependency id="A" version="1.0"/></group></dependencies>`)))
	f.Add(utf16Bytes(doc("", minimal), false, true))
	f.Add([]byte(`<!DOCTYPE a [<!ENTITY b "c">]><package/>`))
	f.Add([]byte("\xEF\xBB\xBF<package><metadata><id>a</id><version>1</version></metadata></package>"))
	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := Parse(data, Limits{})
		if err != nil {
			var e *errs.Error
			if !errors.As(err, &e) || e.Kind != errs.Invalid || e.Code != CodeInvalidPackage {
				t.Fatalf("an unexpected error type: %#v", err)
			}
			return
		}
		if m.ID == "" || m.Version == "" || m.ID != strings.TrimSpace(m.ID) {
			t.Fatalf("an accepted document without an identity: %+v", m)
		}
	})
}
