package ansible

import (
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
)

// unbuiltStore is the data port of a test that reaches no handler which reads it: a call would panic on the nil interface. The handlers
// of the skeleton answer 501 before they reach the port.
type unbuiltStore struct{ Store }

// conformance is the fixture of the kit over the skeleton (ansible-work-packages.md, WP0): the routes answer 501, so the checks that
// need a built protocol are skipped or listed as not built, each with its reason, and the work package that builds the operation removes
// the entry (the kit refuses a skip the format contradicts).
func conformance() formattest.Fixture {
	const notBuilt = "the handler is built by the local class (ansible-work-packages.md, B1); the skeleton answers 501 ansible_not_implemented"
	return formattest.Fixture{
		Rejects: []string{"v3/collections/%2e%2e/x/", "../v3/collections/acme/tools/", "v3/collections/acme/tools%00/"},
		Paths: []formattest.PathCase{
			{Path: "collections/acme/tools/acme-tools-1.0.0.tar.gz", Want: formats.PathInfo{Kind: formats.Content, Package: "acme.tools", Version: "1.0.0"}},
			{Path: "collections/acme/tools/collection.json", Want: formats.PathInfo{Kind: formats.PathMetadata, Package: "acme.tools"}},
			{Path: "collections/acme/tools/versions.json", Want: formats.PathInfo{Kind: formats.PathMetadata, Package: "acme.tools"}},
			{Path: "collections/acme/tools/1.0.0.json", Want: formats.PathInfo{Kind: formats.PathMetadata, Package: "acme.tools", Version: "1.0.0"}},
			{Path: "api-root.json", Want: formats.PathInfo{Kind: formats.PathMetadata}},
		},
		Canonical: func(example string) (string, bool) { return canonicalOfExample(example) },
		Protocol: func(canonical string) string {
			ns, name, _ := strings.Cut(strings.TrimSuffix(strings.TrimPrefix(canonical, "collections/"), "/"+CollectionDocument), "/")
			return "v3/collections/" + ns + "/" + name + "/"
		},
		Host: func() formattest.HostOptions {
			return formattest.HostOptions{Data: unbuiltStore{}}
		},
		Names: []formattest.NameCase{
			{Spellings: []string{"acme.tools"}}, {Spellings: []string{"community.general", "Community.General", "COMMUNITY.general"}},
		},
		Inspect: formattest.InspectTarGz("collections/acme/tools/acme-tools-1.0.0.tar.gz", "MANIFEST.json"),
		Content: []formattest.ContentCase{
			{Name: "root", Path: "", Status: http.StatusNotImplemented},
			{Name: "collection", Path: "v3/collections/acme/tools/", Status: http.StatusNotImplemented},
			{Name: "versions", Path: "v3/collections/acme/tools/versions/", Status: http.StatusNotImplemented},
			{Name: "version", Path: "v3/collections/acme/tools/versions/1.0.0/", Status: http.StatusNotImplemented},
			{Name: "artifact", Path: "v3/artifacts/collections/acme-tools-1.0.0.tar.gz", Status: http.StatusNotImplemented},
			{Name: "import task", Path: "v3/imports/collections/6f1c0000-0000-4000-8000-000000000002/", Status: http.StatusNotImplemented},
		},
		NoContent: map[string]string{
			routeRoot: notBuilt, routeCollection: notBuilt, routeVersions: notBuilt, routeVersion: notBuilt, routeArtifact: notBuilt,
			routeImport: notBuilt,
		},
		NoRevision: map[string]string{routePublish: notBuilt, "OnCommit": notBuilt, "OnItemsRemoved": notBuilt},
		Skip: map[formattest.Check]string{
			formattest.CheckRewrite: "the remote adapter joins with the remote class (R-ANS-017)",
			formattest.CheckMerge:   "the virtual adapter joins with the virtual class (R-ANS-022)",
			formattest.CheckSetup:   "the format declares no client yet: the setup templates and the guide come with the client surface (R-ANS-025)",
			formattest.CheckView:    "the package screens come with the local class (R-ANS-026)",
		},
	}
}

// canonicalOfExample maps an escaped route example to the canonical path of the thing it addresses (kit check 3).
func canonicalOfExample(example string) (string, bool) {
	f := New()
	r, params, err := f.Match(http.MethodGet, example)
	if err != nil {
		r, params, err = f.Match(http.MethodPost, example)
	}
	if err != nil {
		return "", false
	}
	for _, k := range []string{paramNamespace, paramName} {
		if v, ok := params[k]; ok {
			params[k] = strings.ToLower(v)
		}
	}
	return CanonicalPath(r.Name, params)
}

// TestR_ARCH_010_Conformance runs the conformance kit over the skeleton.
func TestR_ARCH_010_Conformance(t *testing.T) {
	formattest.Run(t, New(), conformance())
}

// FuzzR_ARCH_028_Match: Match never resolves a path that is not clean and never panics.
func FuzzR_ARCH_028_Match(f *testing.F) { formattest.FuzzMatch(f, New()) }
