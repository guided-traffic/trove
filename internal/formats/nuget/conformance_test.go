package nuget

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
)

// nugetChanges are the writes of check 9: every hook and every route that changes package
// rows raises packages.rev_seq of the affected package.
func nugetChanges(f *Format) []formattest.ChangeCase {
	const id = "acme.widgets"
	push := func(version string) func(h *formattest.Host) error {
		return func(h *formattest.Host) error {
			pkg, err := makeNupkg("Acme.Widgets", version)
			if err != nil {
				return err
			}
			body, ct, err := makeMultipart(pkg)
			if err != nil {
				return err
			}
			r, err := h.Serve(context.Background(), http.MethodPut, "v3/package", body, false, func(r *http.Request) { r.Header.Set("Content-Type", ct) })
			if err == nil && r.Code != http.StatusCreated {
				err = fmt.Errorf("push %s: status %d: %s", version, r.Code, r.Body.String())
			}
			return err
		}
	}
	serve := func(method, path string, want int) func(h *formattest.Host) error {
		return func(h *formattest.Host) error {
			r, err := h.Serve(context.Background(), method, path, nil, false)
			if err == nil && r.Code != want {
				err = fmt.Errorf("%s %s: status %d: %s", method, path, r.Code, r.Body.String())
			}
			return err
		}
	}
	seed := func(steps ...func(h *formattest.Host) error) func(h *formattest.Host) error {
		return func(h *formattest.Host) error {
			for _, s := range steps {
				if err := s(h); err != nil {
					return err
				}
			}
			return nil
		}
	}
	deleteMode := func(h *formattest.Host) error {
		h.Repo().Config = []byte(`{"nuget":{"deleteMode":"delete"}}`)
		return nil
	}
	do := func(fn func(h *formattest.Host) error) func(context.Context, *formattest.Host) error {
		return func(_ context.Context, h *formattest.Host) error { return fn(h) }
	}
	remove := func(paths ...string) func(context.Context, *formattest.Host) error {
		return func(ctx context.Context, h *formattest.Host) error {
			rq, err := h.NewRequest(ctx, http.MethodDelete, "v3/package/"+id+"/1.0.0", nil)
			if err != nil {
				return err
			}
			return h.Remove(ctx, rq, "delete", paths...)
		}
	}
	symbols := func(version string) func(h *formattest.Host) error {
		return func(h *formattest.Host) error {
			pkg, err := newSnupkg("Acme.Widgets", version, snupkgSpec{entries: map[string]string{"lib/net8.0/Acme.Widgets.pdb": portablePDB}})
			if err != nil {
				return err
			}
			body, ct, err := makeMultipart(pkg)
			if err != nil {
				return err
			}
			r, err := h.Serve(context.Background(), http.MethodPut, "v3/symbolpackage", body, false, func(r *http.Request) { r.Header.Set("Content-Type", ct) })
			if err == nil && r.Code != http.StatusCreated {
				err = fmt.Errorf("symbol push %s: status %d: %s", version, r.Code, r.Body.String())
			}
			return err
		}
	}
	return []formattest.ChangeCase{
		{Name: "a first push (OnCommit)", Package: id, Do: do(push("1.0.0"))},
		{Name: "a later version (OnCommit)", Package: id, Seed: push("1.0.0"), Do: do(push("1.1.0"))},
		{Name: "an unlist (DELETE in unlist mode)", Package: id, Seed: push("1.0.0"), Do: do(serve(http.MethodDelete, "v3/package/"+id+"/1.0.0", http.StatusNoContent))},
		{Name: "a symbol push (OnCommit)", Package: id, Seed: push("1.0.0"), Do: do(symbols("1.0.0"))},
		{
			Name: "a relist (POST)", Package: id, Seed: seed(push("1.0.0"), serve(http.MethodDelete, "v3/package/"+id+"/1.0.0", http.StatusNoContent)),
			Do: do(serve(http.MethodPost, "v3/package/"+id+"/1.0.0", http.StatusOK)),
		},
		{
			Name: "a hard delete (DELETE in delete mode, OnItemsRemoved)", Package: id, Seed: seed(deleteMode, push("1.0.0"), push("1.1.0")),
			Do: do(serve(http.MethodDelete, "v3/package/"+id+"/1.0.0", http.StatusNoContent)),
		},
		{
			Name: "the removal of a package through the items (OnItemsRemoved)", Package: id, Seed: seed(push("1.0.0"), push("1.1.0")),
			Do: remove(NupkgPath(id, "1.0.0"), NuspecPath(id, "1.0.0")),
		},
	}
}

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

// seedVersions stores a package at the versions, in this order, the way a push stores them: the
// package row and the version rows of the core through the writer of the Host, the NuGet row
// through the data port (the ingestion pipeline of R-NUGET-005 is WP3.1e; its Store calls are
// these two). The last version is the newest arrival.
func seedVersions(lowerID string, versions ...string) func(h *formattest.Host) error {
	return func(h *formattest.Host) error {
		store := h.FormatData(ID).(interface {
			InsertVersion(context.Context, formats.Tx, rows.VersionInput) error
		})
		ctx := context.Background()
		return h.Write(ctx, nil, func(tx formats.Tx, w formats.PackageWriter) error {
			pkg, _, err := w.EnsureLocked(ctx, h.Ref(), lowerID, lowerID)
			if err != nil {
				return err
			}
			for i, v := range versions {
				id, err := w.InsertVersion(ctx, pkg, formats.NewVersion{Version: v, DisplayVersion: v})
				if err != nil {
					return err
				}
				if err := store.InsertVersion(ctx, tx, rows.VersionInput{
					RepoID: h.Ref().ID, VersionID: int64(id), LowerID: lowerID, DeclaredID: lowerID, OriginalVersion: v, LowerVersion: v,
					PublishedAt: time.Date(2026, 1, 1, 0, i, 0, 0, time.UTC), PackageTypes: []string{"dependency"}, Metadata: []byte(`{}`),
				}); err != nil {
					return err
				}
			}
			return nil
		})
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
				Data: newDocStore(),
				// The push reads the shadowing verdict and the search the scope of the caller (R-AUTHZ-016).
				Resolver: conformanceResolver{shadowResolver: shadowResolver{out: formats.ShadowOutcome{Proceed: true}}, scope: scopeFake{shape: formats.ReadAll}},
				Subject:  authz.Subject{Kind: "user", ID: "11111111-1111-1111-1111-111111111111", Name: "alice"},
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
		Inspect:   formattest.InspectZip("acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg", "acme.widgets.nuspec"),
		Canonical: canonicalOf(f),
		Protocol:  func(c string) string { return feed("flatcontainer/" + c) },
		Content: []formattest.ContentCase{
			{Name: "service index", Path: "v3/index.json"},
			{Name: "service index with a slash", Path: "v3/"},
			{Name: "service index without a slash", Path: "v3"},
			{Name: "version list", Path: feed("flatcontainer/acme.widgets/index.json"), Seed: seedWidgets},
			{Name: "package", Path: feed("flatcontainer/acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg"), Seed: seedWidgets},
			{Name: "nuspec", Path: feed("flatcontainer/acme.widgets/1.2.3/acme.widgets.nuspec"), Seed: seedWidgets},
			{Name: "embedded file", Path: feed("flatcontainer/acme.widgets/1.2.3/readme"), Status: notBuilt},
			{Name: "registration index", Path: feed("registration-semver2/acme.widgets/index.json"), Seed: seedWidgets},
			{Name: "registration page", Path: feed("registration-gz/acme.widgets/page/1.0.0/2.0.0.json"), Seed: seedWidgets},
			{Name: "registration leaf", Path: feed("registration/acme.widgets/1.2.3.json"), Seed: seedWidgets},
			{Name: "search", Path: feed("search")},
			{Name: "autocomplete", Path: feed("autocomplete"), Status: notBuilt},
			{Name: "symbols", Path: feed("symbols/acme.widgets.pdb/0123456789abcdef0123456789abcdefFFFFFFFF/acme.widgets.pdb"), Status: notBuilt},
		},
		Changes: nugetChanges(f),
		Names: []formattest.NameCase{
			{Spellings: []string{"Acme.Widgets", "acme.widgets", "ACME.WIDGETS"}},
			{Spellings: []string{"Newtonsoft.Json", "newtonsoft.json"}},
		},
		// The arrival order is not the version order, so a Stored that sorts by version fails.
		View: &formattest.ViewFixture{
			Versions: []string{"1.0.0", "1.10.0", "1.2.0", "2.0.0-beta.1", "2.0.0"},
			Package:  "view.sample",
			Seed:     seedVersions("view.sample", "1.0.0", "1.10.0", "1.2.0", "2.0.0-beta.1", "2.0.0"),
		},
		OptionNames: []string{"--password", "--store-password-in-clear-text", "-Password", "ClearTextPassword", "TROVE_TOKEN"},
		Remote:      remoteFixture(),
		Skip: map[formattest.Check]string{
			formattest.CheckMerge: "the virtual class and its adapter are WP3.2c (Q-NUGET-28)",
		},
	}
}

// upstreamHost is the origin of the remote repository of the kit's check 4.
const upstreamHost = "https://api.upstream.test"

// remoteFixture is the fixture of check 4: the stored documents of a remote repository as the
// upstream serves them. Their package content addresses are on the upstream's host, and none
// may be left in the rendered document.
func remoteFixture() *formattest.RemoteFixture {
	const (
		content = upstreamHost + "/v3-flatcontainer/acme.widgets/"
		reg     = upstreamHost + "/v3/registration/acme.widgets/"
	)
	leaf := func(version string) string {
		return `{"@id":"` + reg + version + `.json","@type":"Package","catalogEntry":{"@id":"` + reg + version + `.json",` +
			`"id":"Acme.Widgets","version":"` + version + `","listed":true,"published":"2026-10-01T10:00:00Z","authors":"Acme",` +
			`"description":"Widgets","packageContent":"` + content + version + `/acme.widgets.` + version + `.nupkg",` +
			`"dependencyGroups":[{"targetFramework":"net8.0","dependencies":[{"id":"Acme.Core","range":"[1.0.0, )","registration":"` + upstreamHost + `/v3/registration/acme.core/index.json"}]}]},` +
			`"packageContent":"` + content + version + `/acme.widgets.` + version + `.nupkg","registration":"` + reg + `index.json"}`
	}
	index := `{"@id":"` + reg + `index.json","count":1,"items":[{"@id":"` + reg + `index.json#page/1.0.0/1.1.0","count":2,"lower":"1.0.0","upper":"1.1.0",` +
		`"items":[` + leaf("1.0.0") + `,` + leaf("1.1.0-beta.1") + `]}]}`
	page := `{"@id":"` + reg + `page/1.0.0/1.1.0.json","count":1,"lower":"1.0.0","upper":"1.0.0","items":[` + leaf("1.0.0") + `]}`
	return &formattest.RemoteFixture{
		Upstream: upstreamHost,
		Docs: []formattest.RemoteDoc{
			{Name: "registration index", Path: "acme.widgets/.registration/index.json", Variant: documentVariant, Body: []byte(index)},
			{Name: "registration page", Path: "acme.widgets/.registration/page/1.0.0/1.0.0.json", Variant: documentVariant, Body: []byte(page)},
			{Name: "version list", Path: "acme.widgets/index.json", Variant: documentVariant, Body: []byte(`{"versions":["1.0.0","1.1.0-beta.1"]}`)},
		},
		DownloadLinks: func(doc []byte) []string {
			var out []string
			var walk func(v any)
			walk = func(v any) {
				switch x := v.(type) {
				case map[string]any:
					for k, e := range x {
						if s, ok := e.(string); ok && (k == "packageContent" || k == "registration" || k == "@id") {
							out = append(out, s)
							continue
						}
						walk(e)
					}
				case []any:
					for _, e := range x {
						walk(e)
					}
				}
			}
			var v any
			if json.Unmarshal(doc, &v) == nil {
				walk(v)
			}
			return out
		},
	}
}

// conformanceResolver answers the shadowing check of the push and the read scope of the search.
type conformanceResolver struct {
	shadowResolver
	scope formats.ReadScope
}

func (r conformanceResolver) ReadScope(context.Context, *formats.Request, []string) (formats.ReadScope, error) {
	return r.scope, nil
}

// seedWidgets stores version 1.2.3 of acme.widgets with its files, the state the content cases
// of the documents and the files read.
func seedWidgets(h *formattest.Host) error {
	st := h.FormatData(ID).(*docStore)
	st.bind(h)
	if err := st.commit(h, "acme.widgets", registrationVersion("Acme.Widgets", "1.2.3", true, `{"description":"Widget primitives."}`)); err != nil {
		return err
	}
	putFiles(h)
	return nil
}

// TestR_ARCH_010_Conformance runs the conformance kit over the NuGet format: route
// ambiguity (check 1), path traversal (2), classification (3), the setup templates and the
// guide (8), content headers (7), package names (10) and the view adapter (11) run; the others
// are skipped with their reason.
func TestR_ARCH_010_Conformance(t *testing.T) {
	f := New()
	formattest.Run(t, f, conformance(f))
}

// FuzzR_ARCH_028_Match: Match never resolves a path that is not clean and never panics.
func FuzzR_ARCH_028_Match(f *testing.F) { formattest.FuzzMatch(f, New()) }
