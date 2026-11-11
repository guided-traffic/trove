package ansible

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
)

// conformance is the fixture of the kit over the local class: the routes serve over the in-memory twin of the data port, a publish is
// driven through the route, and the checks of the remote and the virtual class and of the client surface are skipped with the reason
// until their classes join (the kit refuses a skip the format contradicts).
func conformance() formattest.Fixture {
	const noTask = "a task id is random: the import task is read by the ansiblelocal integration test after a publish returned it"
	versions := []string{"1.0.0", "1.1.0", "2.0.0"}
	seed := func(vs ...string) func(h *formattest.Host) error {
		return func(h *formattest.Host) error {
			for _, v := range vs {
				if err := publishVersion(v)(h); err != nil {
					return err
				}
			}
			return nil
		}
	}
	do := func(fn func(h *formattest.Host) error) func(context.Context, *formattest.Host) error {
		return func(_ context.Context, h *formattest.Host) error { return fn(h) }
	}
	const noDocs = "the route answers 501 until the handlers of the documentation land (R-ANS-035); its documents are proven over HTTP in test/integration/ansiblelocal"
	const notLocal = "the role routes are served by remote and virtual repositories (R-ANS-014) and the kit's host is a local repository; the remote handlers are proven over HTTP in test/integration/ansibleremote"
	return formattest.Fixture{
		Rejects: []string{"v3/collections/%2e%2e/x/", "../v3/collections/acme/tools/", "v3/collections/acme/tools%00/"},
		Paths: []formattest.PathCase{
			{Path: "collections/acme/tools/acme-tools-1.0.0.tar.gz", Want: formats.PathInfo{Kind: formats.Content, Package: "acme.tools", Version: "1.0.0"}},
			{Path: "collections/acme/tools/collection.json", Want: formats.PathInfo{Kind: formats.PathMetadata, Package: "acme.tools"}},
			{Path: "collections/acme/tools/versions.json", Want: formats.PathInfo{Kind: formats.PathMetadata, Package: "acme.tools"}},
			{Path: "collections/acme/tools/1.0.0.json", Want: formats.PathInfo{Kind: formats.PathMetadata, Package: "acme.tools", Version: "1.0.0"}},
			{Path: "api-root.json", Want: formats.PathInfo{Kind: formats.PathMetadata}},
		},
		Canonical: canonicalOfExample,
		Protocol: func(canonical string) string {
			if id, ok := strings.CutSuffix(strings.TrimPrefix(canonical, "roles/"), "/versions.json"); ok && strings.HasPrefix(canonical, "roles/") {
				return "v1/roles/" + id + "/versions/"
			}
			ns, name, _ := strings.Cut(strings.TrimSuffix(strings.TrimPrefix(canonical, "collections/"), "/"+CollectionDocument), "/")
			return "v3/collections/" + ns + "/" + name + "/"
		},
		Host:      hostOptions,
		Remote:    remoteFixture(),
		Merge:     mergeFixture(),
		Discovery: discoveryFixture(),
		Names: []formattest.NameCase{
			{Spellings: []string{"acme.tools"}},
			{Spellings: []string{"community.general", "Community.General", "COMMUNITY.general"}},
			// The package of a role of a remote's upstream (R-ANS-020, Q-ANS-36).
			{Spellings: []string{"role/16"}},
		},
		OptionNames: optionNames(),
		Inspect:     formattest.InspectTarGz("collections/acme/tools/acme-tools-1.0.0.tar.gz", "MANIFEST.json"),
		Content: []formattest.ContentCase{
			{Name: "root", Path: ""},
			{Name: "collection", Path: "v3/collections/acme/tools/", Seed: seed("1.0.0")},
			{Name: "versions", Path: "v3/collections/acme/tools/versions/", Seed: seed("1.0.0", "1.1.0")},
			{Name: "version", Path: "v3/collections/acme/tools/versions/1.0.0/", Seed: seed("1.0.0")},
			{Name: "artifact", Path: "v3/artifacts/collections/acme-tools-1.0.0.tar.gz", Seed: seed("1.0.0")},
			// The route is declared and answers 501 until the handlers of the documentation land (R-ANS-035).
			{Name: "documentation of a version", Path: "v3/collections/acme/tools/versions/1.0.0/docs-blob/", Seed: seed("1.0.0"), Status: http.StatusNotImplemented},
			{Name: "an unknown collection", Path: "v3/collections/acme/nope/", Status: http.StatusNotFound},
			{Name: "an unknown task", Path: "v3/imports/collections/6f1c0000-0000-4000-8000-000000000002/", Status: http.StatusNotFound},
			// The kit's host is a local repository, which has no role API: the remote and virtual classes serve the three role
			// routes, and the remote handlers are proven over HTTP in test/integration/ansibleremote.
			{Name: "role lookup", Path: "v1/roles/", Status: http.StatusNotImplemented},
			{Name: "role versions", Path: "v1/roles/16/versions/", Status: http.StatusNotImplemented},
			{Name: "role archive", Path: "v1/roles/archives/16/v1.0.0.tar.gz", Status: http.StatusNotImplemented},
		},
		NoContent: map[string]string{routeDocs: noDocs, routeImport: noTask, routeRolesFind: notLocal, routeRoleVersions: notLocal, routeRoleArchive: notLocal},
		Changes: []formattest.ChangeCase{
			{Name: "a first publish (OnCommit)", Package: "acme.tools", Do: do(publishVersion("1.0.0"))},
			{Name: "a later version (OnCommit)", Package: "acme.tools", Seed: seed("1.0.0"), Do: do(publishVersion("1.1.0"))},
			{
				Name: "the removal of a version through the items (OnItemsRemoved)", Package: "acme.tools", Seed: seed("1.0.0", "1.1.0"),
				Do: func(ctx context.Context, h *formattest.Host) error {
					rq, err := h.NewRequest(ctx, http.MethodGet, "v3/collections/acme/tools/", nil)
					if err != nil {
						return err
					}
					return h.Remove(ctx, rq, "delete", ArtifactPath("acme", "tools", "1.0.0"))
				},
			},
		},
		NoRevision: map[string]string{},
		View: &formattest.ViewFixture{
			Versions: versions, Package: "acme.tools",
			Seed: func(h *formattest.Host) error { return seed(versions...)(h) },
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

// TestR_ARCH_010_Conformance runs the conformance kit over the local class.
func TestR_ARCH_010_Conformance(t *testing.T) {
	formattest.Run(t, New(), conformance())
}

// FuzzR_ARCH_028_Match: Match never resolves a path that is not clean and never panics.
func FuzzR_ARCH_028_Match(f *testing.F) { formattest.FuzzMatch(f, New()) }
