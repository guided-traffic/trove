package nuget

import (
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
)

// otherNuspec is the .nuspec of the package B an upstream serves under the path of package A.
func otherNuspec(extra string) []byte {
	return []byte(`<?xml version="1.0" encoding="utf-8"?>` + extra + `<package xmlns="http://schemas.microsoft.com/packaging/2013/05/nuspec.xsd"><metadata>` +
		`<id>Other.Pkg</id><version>1.2.3</version><authors>Mallory</authors><description>not the package asked for</description></metadata></package>`)
}

// TestR_NUGET_015_APackageWhoseIdentityCannotBeCheckedIs502AndStoresNothing (Q-NUGET-84): an
// upstream that serves package B under the path of package A, in an archive trove's reader
// refuses for any reason but "no package at all", is answered 502 upstream_package_mismatch and
// nothing is stored. Before, every refusal of the reader meant "serve as it came without the
// identity check", so B was cached under A for good, and with it the archives the push road
// refuses to protect clients.
func TestR_NUGET_015_APackageWhoseIdentityCannotBeCheckedIs502AndStoresNothing(t *testing.T) {
	const path = "acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg"
	whitespace := otherNuspec("<!--" + strings.Repeat(" ", 600<<10) + "-->")
	trailing := append(buildNupkg(t, "Other.Pkg", "1.2.3"), []byte("trailing bytes after the end record")...)
	for name, pkg := range map[string][]byte{
		"a .nuspec that compresses better than 100:1": buildNupkg(t, "Other.Pkg", "1.2.3", withoutNuspec(), withEntry("Other.Pkg.nuspec", whitespace)),
		"two .nuspec files at the root":               buildNupkg(t, "Other.Pkg", "1.2.3", withEntry("Second.nuspec", otherNuspec(""))),
		"two entries of one name":                     buildNupkg(t, "Other.Pkg", "1.2.3", withEntry("LIB/net8.0/other.pkg.dll", []byte("MZ"))),
		"a name that leaves the archive":              buildNupkg(t, "Other.Pkg", "1.2.3", withEntry("../evil.dll", []byte("MZ"))),
		"a document type declaration":                 buildNupkg(t, "Other.Pkg", "1.2.3", withoutNuspec(), withEntry("Other.Pkg.nuspec", otherNuspec("<!DOCTYPE package>"))),
		"bytes after the end record":                  trailing,
		"an ID the upstream rule refuses":             buildNupkg(t, "Acme..Widgets", "1.2.3"),
		"a version that does not parse":               buildNupkg(t, "Acme.Widgets", "one.two"),
	} {
		got, err := inspectFill(t, path, pkg)
		e, ok := errs.As(err)
		if got != nil || !ok || e.Kind != errs.UpstreamFailed || e.Code != errs.CodeUpstreamPackageMismatch {
			t.Errorf("%s: %+v %v, want 502 upstream_package_mismatch and no inspection", name, got, err)
		}
	}
}

// TestR_NUGET_015_ContentThatIsNoPackageForAnyReaderIsServedAsItCame: what no client reads as a
// package either - no ZIP end record, an archive without a .nuspec, a .nuspec that is not XML -
// stays the pass-through of R-NUGET-015, without NuGet rows.
func TestR_NUGET_015_ContentThatIsNoPackageForAnyReaderIsServedAsItCame(t *testing.T) {
	const path = "acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg"
	for name, pkg := range map[string][]byte{
		"a page of HTML":      []byte("<html><body>maintenance</body></html>"),
		"a .nuspec of no XML": buildNupkg(t, "Acme.Widgets", "1.2.3", withoutNuspec(), withEntry("Acme.Widgets.nuspec", []byte("<package><metadata>"))),
		"no .nuspec, a .nuspec below the root": buildNupkg(t, "Acme.Widgets", "1.2.3", withoutNuspec(),
			withEntry("content/Acme.Widgets.nuspec", otherNuspec(""))),
	} {
		got, err := inspectFill(t, path, pkg)
		if err != nil || got != nil {
			t.Errorf("%s: %+v %v, want neither an inspection nor an error", name, got, err)
		}
	}
}

// TestR_NUGET_003_TheRowsOfACachedPackageAreKeyedByThePathsID (Q-NUGET-84): the declared ID is
// compared with the path's as a client lower-cases an ID into its path, and the rows take the
// path's ID, which names the item. Before, the comparison folded case (U+017F LONG S matched
// "s") and the rows took the declared spelling lower-cased by ASCII alone, so an item at
// "kelvin/" got rows under "Kelvin" and its eviction left an orphan package row.
func TestR_NUGET_003_TheRowsOfACachedPackageAreKeyedByThePathsID(t *testing.T) {
	got, err := inspectFill(t, "kelvin/1.0.0/kelvin.1.0.0.nupkg", buildNupkg(t, "Kelvin", "1.0.0"))
	if err != nil || got == nil {
		t.Fatalf("a declared ID a client lower-cases to the path's: %+v %v", got, err)
	}
	in := got.Payload.(*incoming)
	if in.lowerID != "kelvin" || in.declaredID != "Kelvin" || in.nupkgPath() != "kelvin/1.0.0/kelvin.1.0.0.nupkg" {
		t.Errorf("rows under %q (declared %q, path %q), want the path's ID", in.lowerID, in.declaredID, in.nupkgPath())
	}
	_, err = inspectFill(t, "secret/1.0.0/secret.1.0.0.nupkg", buildNupkg(t, "ſecret", "1.0.0"))
	if e, ok := errs.As(err); !ok || e.Code != errs.CodeUpstreamPackageMismatch {
		t.Errorf("an ID that only folds to the path's: %v, want 502 upstream_package_mismatch", err)
	}
	// A legacy ID with a capital letter outside ASCII, asked for in the client's lower case.
	if got, err := inspectFill(t, "ünïcode.pkg/1.0.0/ünïcode.pkg.1.0.0.nupkg", buildNupkg(t, "Ünïcode.Pkg", "1.0.0")); err != nil || got == nil ||
		got.Payload.(*incoming).lowerID != "ünïcode.pkg" {
		t.Errorf("a legacy ID in the client's lower case: %+v %v", got, err)
	}
}
