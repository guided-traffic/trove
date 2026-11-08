package nuget

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
	"github.com/guided-traffic/trove/internal/qname"
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
		{
			Name: "the restore of a package from the trash (OnItemsRestored)", Package: id,
			Seed: seed(push("1.0.0"), push("1.1.0"), func(h *formattest.Host) error {
				return h.Remove(context.Background(), nil, "trash", NupkgPath(id, "1.0.0"), NuspecPath(id, "1.0.0"))
			}),
			Do: func(ctx context.Context, h *formattest.Host) error {
				return h.Restore(ctx, nil, NupkgPath(id, "1.0.0"), NuspecPath(id, "1.0.0"))
			},
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

// conformance is the fixture of the NuGet format for the kit (R-ARCH-010). Every check runs
// and none is skipped: the stored remote documents of check 4, the member documents of check 5
// (merge, R-NUGET-017), the crafted packages of check 6 (which the fill hook of the remote
// adapter reads as well, R-NUGET-015) and the view of check 11 are below. What the kit does not
// check is listed with its reason: the content routes of P1 operations, which answer 501 until
// they are built (NoContent).
func conformance(f *Format) formattest.Fixture {
	const notBuilt = http.StatusNotImplemented
	feed := func(p string) string { return "v3/" + p }
	return formattest.Fixture{
		Host: func() formattest.HostOptions {
			return formattest.HostOptions{
				Data: newDocStore(),
				// The vulnerability information is served while trove's NuGet data is complete (R-VULN-018).
				Vulnerabilities: newScanning(),
				// The push reads the shadowing verdict, and the search and the documents the scope of the caller, which is
				// the kit's: its Allow (R-AUTHZ-016, Q-AUTHZ-255).
				Resolver: conformanceResolver{shadowResolver: shadowResolver{out: formats.ShadowOutcome{Proceed: true}}},
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
			// Two versions, so kit check 13 hides one of them and the documents keep the other (Q-AUTHZ-255).
			{Name: "version list of two versions", Path: feed("flatcontainer/acme.widgets/index.json"), Seed: seedTwoWidgets},
			{Name: "registration index of two versions", Path: feed("registration-semver2/acme.widgets/index.json"), Seed: seedTwoWidgets},
			{Name: "search", Path: feed("search")},
			{Name: "vulnerability index", Path: feed("vulnerabilities/index.json")},
			{Name: "vulnerability page", Path: feed("vulnerabilities/trove.json")},
			{Name: "autocomplete", Path: feed("autocomplete"), Status: notBuilt},
			{Name: "symbols", Path: feed("symbols/acme.widgets.pdb/0123456789abcdef0123456789abcdefFFFFFFFF/acme.widgets.pdb"), Status: notBuilt},
		},
		// The content routes of P1 operations, which answer 501 nuget_not_implemented until they are
		// built (M6); check 7 checks the headers of what a route serves, and refuses an entry once a
		// case serves the route.
		NoContent: map[string]string{
			"nuget.flatcontainer.embedded": "the embedded icon, license and readme are P1 (R-NUGET-024) and answer 501 until built",
			"nuget.autocomplete":           "autocomplete is P1 (R-NUGET-011) and answers 501 until built",
			"nuget.symbols":                "the symbol server is P1 (R-NUGET-021) and answers 501 until built; the symbol push is served (R-NUGET-020)",
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
		Merge:       mergeFixture(),
		Discovery:   discoveryFixture(),
	}
}

// discoveryFixture are the service indexes of check 6 (R-ARCH-025): one in the shape nuget.org serves,
// and crafted ones that must be refused - an address with user info, a scheme that is no web
// scheme, a relative address, an index that lacks the package content resource among 20,000
// others, a resource whose type nests 100,000 deep.
func discoveryFixture() []formattest.DiscoveryCase {
	const index = `{"version":"3.0.0","resources":[
	 {"@id":"https://api.nuget.test/v3-flatcontainer/","@type":"PackageBaseAddress/3.0.0"},
	 {"@id":"https://api.nuget.test/v3/registration5-gz-semver2/","@type":"RegistrationsBaseUrl/3.6.0"},
	 {"@id":"https://search.nuget.test/query","@type":"SearchQueryService/3.5.0"}]}`
	with := func(content string) []byte {
		return []byte(`{"resources":[{"@id":"` + content + `","@type":"PackageBaseAddress/3.0.0"},{"@id":"https://h.test/r/","@type":"RegistrationsBaseUrl/3.6.0"}]}`)
	}
	many := make([]byte, 0, 20_000*60)
	many = append(many, `{"resources":[`...)
	for i := range 20_000 {
		if i > 0 {
			many = append(many, ',')
		}
		many = append(many, fmt.Sprintf(`{"@id":"https://h.test/%d/","@type":"Other/%d"}`, i, i)...)
	}
	many = append(many, `]}`...)
	deep := `{"resources":[{"@id":"https://h.test/c/","@type":` + strings.Repeat("[", 100_000) + strings.Repeat("]", 100_000) + `}]}`
	return []formattest.DiscoveryCase{
		{Name: "a service index in the shape nuget.org serves", Doc: []byte(index), Valid: true},
		{Name: "an address with user info", Doc: with("https://user:secret@h.test/c/")},
		{Name: "a scheme that is no web scheme", Doc: with("file:///etc/")},
		{Name: "a relative address", Doc: with("/c/")},
		{Name: "20,000 resources without the package content", Doc: many},
		{Name: "a type nested 100,000 deep", Doc: []byte(deep)},
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
			{Name: "registration index", Path: "acme.widgets/.registration/index.json", Request: "v3/registration-semver2/acme.widgets/index.json", Variant: documentVariant, Body: []byte(index)},
			{Name: "registration page", Path: "acme.widgets/.registration/page/1.0.0/1.0.0.json", Request: "v3/registration-semver2/acme.widgets/page/1.0.0/1.0.0.json", Variant: documentVariant, Body: []byte(page)},
			{Name: "version list", Path: "acme.widgets/index.json", Request: "v3/flatcontainer/acme.widgets/index.json", Variant: documentVariant, Body: []byte(`{"versions":["1.0.0","1.1.0-beta.1"]}`)},
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
}

// ReadScope is the kit's scope of the request's Host: every row while its Allow is nil, row by row and path by path once a
// check refuses targets with it.
func (r conformanceResolver) ReadScope(ctx context.Context, rq *formats.Request, keys []qname.Repo) (formats.ReadScope, error) {
	return formattest.Resolver{}.ReadScope(ctx, rq, keys)
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

// seedTwoWidgets stores versions 1.2.3 and 1.3.0 of acme.widgets, for the documents that list more than one version.
func seedTwoWidgets(h *formattest.Host) error {
	if err := seedWidgets(h); err != nil {
		return err
	}
	return h.FormatData(ID).(*docStore).commit(h, "acme.widgets", registrationVersion("Acme.Widgets", "1.3.0", true, `{"description":"Widget primitives, the next one."}`))
}

// TestR_ARCH_010_NoCheckIsSkipped: every check of the kit runs over NuGet (E3.3); a skip added to
// the fixture later fails here, since the kit reports a skipped check as skipped, not as failed.
func TestR_ARCH_010_NoCheckIsSkipped(t *testing.T) {
	if s := conformance(New()).Skip; len(s) != 0 {
		t.Fatalf("the NuGet fixture skips %v", s)
	}
}

// TestR_ARCH_010_Conformance runs the conformance kit over the NuGet format: all eleven
// checks run, none is skipped (E3.3).
func TestR_ARCH_010_Conformance(t *testing.T) {
	f := New()
	formattest.Run(t, f, conformance(f))
}

// FuzzR_ARCH_028_Match: Match never resolves a path that is not clean and never panics.
func FuzzR_ARCH_028_Match(f *testing.F) { formattest.FuzzMatch(f, New()) }
