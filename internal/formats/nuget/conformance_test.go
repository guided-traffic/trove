package nuget

import (
	"net/http"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/nuget/nugettest"
)

// canonicalOf maps a route example to the canonical path Classify is asked about (kit check
// 3), through the route the format itself matches it to.
func canonicalOf(f *Format) func(string) (string, bool) {
	return func(example string) (string, bool) {
		r, p, err := f.Match(http.MethodGet, example)
		if err != nil {
			return "", false
		}
		return CanonicalPath(r.Name, p)
	}
}

// conformance is the fixture of the NuGet format for the kit (R-ARCH-010). The skeleton
// serves the service index and answers 501 for every other route, so checks 4 and 5 have no
// adapter to run over (the remote and the virtual class join with their adapters, Q-NUGET-28)
// and the checks that need a pipeline or a view are skipped with their reason; the kit
// refuses a skip once the format has what the check is about.
func conformance(f *Format) formattest.Fixture {
	const notBuilt = http.StatusNotImplemented
	feed := func(p string) string { return "v3/" + p }
	return formattest.Fixture{
		Host: func() formattest.HostOptions {
			return formattest.HostOptions{
				Data:    nugettest.New(),
				Subject: authz.Subject{Kind: "user", ID: "11111111-1111-1111-1111-111111111111", Name: "alice"},
			}
		},
		Rejects: []string{
			"v3/flatcontainer/a/../index.json", "v3/flatcontainer/%2e%2e/index.json", "v3/flatcontainer/a%00b/index.json",
			"v3/flatcontainer/a%5cb/1.0.0/a.1.0.0.nupkg", "v3//flatcontainer/a/index.json", "v3/flatcontainer/a/./index.json",
			"v3/symbols/../etc/passwd", "v3/package/a/%2e%2e",
		},
		Paths: []formattest.PathCase{
			{Path: "", Want: formats.PathInfo{Kind: formats.PathComputed}},
			{Path: "acme.widgets/index.json", Want: formats.PathInfo{Kind: formats.PathMetadata, Package: "acme.widgets"}},
			{Path: "acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg", Want: formats.PathInfo{Kind: formats.Content, Package: "acme.widgets", Version: "1.2.3"}},
			{Path: "acme.widgets/1.2.3/acme.widgets.nuspec", Want: formats.PathInfo{Kind: formats.Content, Package: "acme.widgets", Version: "1.2.3"}},
			{Path: "acme.widgets/1.0.0-rc.1/acme.widgets.1.0.0-rc.1.snupkg", Want: formats.PathInfo{Kind: formats.Content, Package: "acme.widgets", Version: "1.0.0-rc.1"}},
			{Path: "acme.widgets/1.2.3/other.1.2.3.nupkg", Want: formats.PathInfo{Kind: formats.Content, Package: "acme.widgets"}},
			{Path: "acme.widgets/.registration/index.json", Want: formats.PathInfo{Kind: formats.PathMetadata, Package: "acme.widgets"}},
			{Path: ".nuget/index.json", Want: formats.PathInfo{Kind: formats.PathMetadata}},
			{Path: ".symbols/acme.widgets.pdb/0123456789abcdef0123456789abcdefFFFFFFFF/acme.widgets.pdb", Want: formats.PathInfo{Kind: formats.Content, RoutingName: "acme.widgets"}},
		},
		Canonical: canonicalOf(f),
		Protocol:  func(c string) string { return feed("flatcontainer/" + c) },
		Content: []formattest.ContentCase{
			{Name: "service index", Path: "v3/index.json"},
			{Name: "service index with a slash", Path: "v3/"},
			{Name: "service index without a slash", Path: "v3"},
			{Name: "version list", Path: feed("flatcontainer/acme.widgets/index.json"), Status: notBuilt},
			{Name: "package", Path: feed("flatcontainer/acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg"), Status: notBuilt},
			{Name: "nuspec", Path: feed("flatcontainer/acme.widgets/1.2.3/acme.widgets.nuspec"), Status: notBuilt},
			{Name: "embedded file", Path: feed("flatcontainer/acme.widgets/1.2.3/readme"), Status: notBuilt},
			{Name: "registration index", Path: feed("registration-semver2/acme.widgets/index.json"), Status: notBuilt},
			{Name: "registration page", Path: feed("registration-gz/acme.widgets/page/1.0.0/2.0.0.json"), Status: notBuilt},
			{Name: "registration leaf", Path: feed("registration/acme.widgets/1.2.3.json"), Status: notBuilt},
			{Name: "search", Path: feed("search"), Status: notBuilt},
			{Name: "autocomplete", Path: feed("autocomplete"), Status: notBuilt},
			{Name: "symbols", Path: feed("symbols/acme.widgets.pdb/0123456789abcdef0123456789abcdefFFFFFFFF/acme.widgets.pdb"), Status: notBuilt},
		},
		Names: []formattest.NameCase{
			{Spellings: []string{"Acme.Widgets", "acme.widgets", "ACME.WIDGETS"}},
			{Spellings: []string{"Newtonsoft.Json", "newtonsoft.json"}},
		},
		Skip: map[formattest.Check]string{
			formattest.CheckRewrite:  "the remote class and its adapter are WP3.2a (Q-NUGET-28)",
			formattest.CheckMerge:    "the virtual class and its adapter are WP3.2c (Q-NUGET-28)",
			formattest.CheckInspect:  "the ingestion pipeline is WP3.1e; Inspect refuses every upload until then",
			formattest.CheckSetup:    "clients, setup templates and the guide are WP3.1h",
			formattest.CheckRevision: "no route writes a package row until WP3.1e",
			formattest.CheckView:     "the package view adapter is WP3.1h",
		},
	}
}

// TestR_ARCH_010_Conformance runs the conformance kit over the NuGet format: route
// ambiguity (check 1), path traversal (2), classification (3), content headers (7) and
// package names (10) run; the others are skipped with their reason.
func TestR_ARCH_010_Conformance(t *testing.T) {
	f := New()
	formattest.Run(t, f, conformance(f))
}

// FuzzR_ARCH_028_Match: Match never resolves a path that is not clean and never panics.
func FuzzR_ARCH_028_Match(f *testing.F) { formattest.FuzzMatch(f, New()) }
