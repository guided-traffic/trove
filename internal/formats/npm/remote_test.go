package npm

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/npm/npmtest"
)

// scriptedRemote is a remote engine whose answers a test sets.
type scriptedRemote struct {
	formats.RemoteFetcher
	ensure   func(path string) (*formats.RemoteDocument, formats.Currency, error)
	ensured  []string
	metadata func(path string) (*formats.RemoteDocument, error)
	artifact func(p formats.Path, expected formats.Digests, w http.ResponseWriter) error
}

func (s *scriptedRemote) EnsureCurrent(_ context.Context, _ *formats.Request, p formats.Path, variant string) (*formats.RemoteDocument, formats.Currency, error) {
	s.ensured = append(s.ensured, p.Path+" "+variant)
	return s.ensure(p.Path)
}

func (s *scriptedRemote) Metadata(_ context.Context, _ *formats.Request, p formats.Path, _ string) (*formats.RemoteDocument, error) {
	return s.metadata(p.Path)
}

func (s *scriptedRemote) Artifact(_ context.Context, _ *formats.Request, p formats.Path, expected formats.Digests, w http.ResponseWriter) error {
	return s.artifact(p, expected, w)
}

func remoteHost(t *testing.T, eng formats.RemoteFetcher) (*formattest.Host, *npmtest.Store, *Format) {
	t.Helper()
	f := New()
	twin := npmtest.New()
	h := formattest.NewHost(f, formattest.HostOptions{Class: formats.Remote, Data: twin, Remote: eng})
	return h, twin, f
}

func sriOf(s string) string {
	sum := sha512.Sum512([]byte(s))
	return "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
}

func TestR_NPM_024_TheRemoteClassIsRegisteredWithItsAdapter(t *testing.T) {
	f := New()
	d := f.Descriptor()
	for _, c := range []formats.Class{formats.Local, formats.Remote, formats.Virtual} {
		if !d.Supports(c) {
			t.Errorf("the format does not list class %s", c)
		}
	}
	if err := formats.Validate(f); err != nil {
		t.Fatal(err)
	}
	if !f.Remote().TracksVersionRemoval() {
		t.Error("npm tracks the removal of versions itself (R-REPO-029)")
	}
}

func TestR_NPM_024_UpstreamRequests(t *testing.T) {
	a := New().Remote()
	rc := formats.RemoteContext{Upstream: mustURL(t, "https://registry.example.org/")}
	for _, c := range []struct{ path, want string }{
		{"lodash", "https://registry.example.org/lodash"},
		{"@acme/ui", "https://registry.example.org/@acme%2Fui"},
		{"@acme/ui/-/ui-1.0.0.tgz", "https://registry.example.org/@acme/ui/-/ui-1.0.0.tgz"},
	} {
		req, err := a.UpstreamRequest(context.Background(), rc, formats.Path{Path: c.path}, "full")
		if err != nil || req.URL.String() != c.want {
			t.Errorf("%s: %v %v, want %s", c.path, req, err, c.want)
		}
	}
	if _, err := a.UpstreamRequest(context.Background(), rc, formats.Path{Path: "-/ping"}, ""); err == nil {
		t.Error("a protocol operation was mapped to an upstream request")
	}
	x := a.(formats.TypeExpecter)
	if !x.ExpectsType(formats.Path{Path: "lodash"}, "application/json") || x.ExpectsType(formats.Path{Path: "lodash"}, "text/html") ||
		!x.ExpectsType(formats.Path{Path: "lodash/-/lodash-1.0.0.tgz"}, "application/octet-stream") {
		t.Error("the expected media types")
	}
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func upstreamBody(name string, modified string, versions ...string) []byte {
	vs := map[string]any{}
	times := map[string]string{"created": "2020-01-01T00:00:00.000Z"}
	if modified != "" {
		times["modified"] = modified
	}
	for _, v := range versions {
		vs[v] = map[string]any{"name": name, "version": v, "dist": map[string]any{"tarball": "https://registry.example.org/" + name + "/-/" + name + "-" + v + ".tgz", "integrity": sriOf(name + v)}}
	}
	b, _ := json.Marshal(map[string]any{"name": name, "versions": vs, "time": times})
	return b
}

func TestR_NPM_024_PrepareFillChecksTheDocumentAndRecordsOnlyForeignTarballURLs(t *testing.T) {
	a := New().Remote()
	rc := formats.RemoteContext{Upstream: mustURL(t, "https://registry.example.org/")}
	body := upstreamBody("lodash", "2024-01-01T00:00:00.000Z", "1.0.0", "2.0.0")
	prep, err := a.PrepareFill(context.Background(), rc, formats.Path{Path: "lodash"}, "full", bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	if len(prep.Mappings) != 0 {
		t.Errorf("documents that name the default URLs record %d mappings", len(prep.Mappings))
	}
	foreign := bytes.ReplaceAll(body, []byte("https://registry.example.org/lodash/-/lodash-2.0.0.tgz"), []byte("https://cdn.example.net/blobs/2.tgz"))
	prep, err = a.PrepareFill(context.Background(), rc, formats.Path{Path: "lodash"}, "full", bytes.NewReader(foreign), int64(len(foreign)))
	if err != nil || len(prep.Mappings) != 1 || prep.Mappings[0].Path.Path != "lodash/-/lodash-2.0.0.tgz" || prep.Mappings[0].Upstream != "https://cdn.example.net/blobs/2.tgz" {
		t.Errorf("mappings = %+v, %v", prep.Mappings, err)
	}
	bad := upstreamBody("impostor", "", "1.0.0")
	_, err = a.PrepareFill(context.Background(), rc, formats.Path{Path: "lodash"}, "full", bytes.NewReader(bad), int64(len(bad)))
	if e, ok := errs.As(err); !ok || e.Code != errs.CodeNPMInvalidUpstreamDocument {
		t.Errorf("err = %v", err)
	}
}

// fill runs OnFill for a document the way the engine does.
func fill(t *testing.T, f *Format, twin *npmtest.Store, body []byte, ev formats.FillEvent) (bool, formats.Prepared) {
	t.Helper()
	rc := formats.RemoteContext{Upstream: mustURL(t, "https://registry.example.org/")}
	prep, err := f.Remote().PrepareFill(context.Background(), rc, formats.Path{Path: "react"}, "full", bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	ev.Repo = formats.RepoRef{ID: 9, Key: "npm-remote", Class: formats.Remote, Format: ID}
	ev.Path, ev.Variant, ev.Prepared, ev.Data = formats.Path{Path: "react"}, "full", prep, twin
	if ev.Packages == nil {
		ev.Packages = &fakeWriter{}
	}
	keep, err := f.Remote().OnFill(context.Background(), nil, ev)
	if err != nil {
		t.Fatal(err)
	}
	return keep, prep
}

func TestR_NPM_024_OnFillRecordsAFillAndKeepsTheNewerDocument(t *testing.T) {
	f := New()
	twin := npmtest.New()
	t0 := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	newer := upstreamBody("react", "2026-10-30T11:00:00.000Z", "18.0.0", "18.1.0", "19.0.0")
	older := upstreamBody("react", "2026-10-30T10:00:00.000Z", "18.0.0", "18.1.0")

	// A first fill records every version; it asks the core to store the document.
	w := &fakeWriter{}
	if keep, _ := fill(t, f, twin, older, formats.FillEvent{Digest: [32]byte{1}, FetchedAt: t0, Packages: w}); keep {
		t.Fatal("a first fill keeps nothing")
	}
	if len(twin.Fills) != 1 || len(twin.Fills[0].Versions) != 2 {
		t.Fatalf("fills = %+v", twin.Fills)
	}
	if len(w.notes) != 1 {
		t.Errorf("the refresh with new content is signalled %d times, want once", len(w.notes))
	}

	// The newer document replaces it, and the version the older one lacked is recorded.
	fill(t, f, twin, newer, formats.FillEvent{Digest: [32]byte{2}, FetchedAt: t0.Add(time.Second)})
	if len(twin.Fills) != 2 {
		t.Fatalf("the newer document was not recorded: %d fills", len(twin.Fills))
	}

	// A fill that carries the older document and was fetched later: the stale-copy rule
	// (R-NPM-024) keeps the stored document, renews its freshness and changes no row.
	keep, _ := fill(t, f, twin, older, formats.FillEvent{Digest: [32]byte{1}, FetchedAt: t0.Add(2 * time.Second)})
	if !keep || len(twin.Fills) != 2 {
		t.Errorf("a stale copy was stored: keep %v, %d fills", keep, len(twin.Fills))
	}
	facts, _ := twin.TarballFacts(context.Background(), 9, "react", "19.0.0")
	if !facts.Known || !facts.Listed {
		t.Errorf("19.0.0 = %+v: the stale copy marked a version removed", facts)
	}

	// The core keeps the entry that was fetched later (R-REPO-026): the format records
	// nothing for the fill that loses that comparison, whichever document it carries.
	keep, _ = fill(t, f, twin, upstreamBody("react", "2026-10-30T13:00:00.000Z", "18.0.0"), formats.FillEvent{
		Digest: [32]byte{3}, FetchedAt: t0.Add(500 * time.Millisecond),
	})
	if !keep || len(twin.Fills) != 2 {
		t.Errorf("a fill the core discards was recorded: keep %v, %d fills", keep, len(twin.Fills))
	}

	// The upstream drops 18.1.0: it is marked, once.
	fill(t, f, twin, upstreamBody("react", "2026-10-30T14:00:00.000Z", "18.0.0", "19.0.0"), formats.FillEvent{
		Digest: [32]byte{4}, FetchedAt: t0.Add(20 * time.Second),
	})
	if facts, _ := twin.TarballFacts(context.Background(), 9, "react", "18.1.0"); !facts.Known || facts.Listed {
		t.Errorf("18.1.0 = %+v", facts)
	}
}

// requestFor builds a request for a path of the remote repository the host serves.
func requestFor(t *testing.T, h *formattest.Host, path string) *formats.Request {
	t.Helper()
	rq, err := h.NewRequest(context.Background(), http.MethodGet, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	return rq
}

func TestR_NPM_025_TheGateRevalidatesAStaleDocumentAndAppliesTheMarks(t *testing.T) {
	ctx := context.Background()
	eng := &scriptedRemote{}
	h, twin, f := remoteHost(t, eng)
	gate := f.Remote().(formats.ArtifactGate)
	path := formats.Path{Path: "evil/-/evil-1.0.1.tgz"}
	rq := requestFor(t, h, "evil/-/evil-1.0.1.tgz")

	record := func(listed bool) {
		vs := []string{"1.0.0"}
		if listed {
			vs = append(vs, "1.0.1")
		}
		body := upstreamBody("evil", "2026-10-30T11:00:00.000Z", vs...)
		rc := formats.RemoteContext{Upstream: mustURL(t, "https://registry.example.org/")}
		prep, err := f.Remote().PrepareFill(ctx, rc, formats.Path{Path: "evil"}, "full", bytes.NewReader(body), int64(len(body)))
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.Remote().OnFill(ctx, nil, formats.FillEvent{
			Repo: h.Ref(), Path: formats.Path{Path: "evil"}, Variant: "full", Prepared: prep, Data: twin, Packages: &fakeWriter{},
			FetchedAt: time.Now(),
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	t.Run("a version the repository has no record of asks the upstream for the document first", func(t *testing.T) {
		eng.ensure = func(string) (*formats.RemoteDocument, formats.Currency, error) {
			record(true)
			twin.SetRemoteState(npmtest.RemoteState{DocStored: true, DocCurrent: true})
			return &formats.RemoteDocument{}, formats.Revalidated, nil
		}
		d, err := gate.BeforeArtifact(ctx, h, rq, path)
		if err != nil {
			t.Fatal(err)
		}
		if len(eng.ensured) != 1 || eng.ensured[0] != "evil full" {
			t.Errorf("ensured = %v", eng.ensured)
		}
		if !bytes.Equal(d.SHA512[:8], mustDigest(sriOf("evil1.0.1"))[:8]) {
			t.Errorf("the announced digest is not the document's: %x", d.SHA512)
		}
	})
	t.Run("a current document is not asked again", func(t *testing.T) {
		eng.ensured = nil
		if _, err := gate.BeforeArtifact(ctx, h, rq, path); err != nil || len(eng.ensured) != 0 {
			t.Errorf("err %v, ensured %v", err, eng.ensured)
		}
	})
	t.Run("an expired document is revalidated, and a version it stopped listing is 404", func(t *testing.T) {
		twin.SetRemoteState(npmtest.RemoteState{DocStored: true, DocCurrent: false})
		eng.ensure = func(string) (*formats.RemoteDocument, formats.Currency, error) {
			record(false)
			twin.SetRemoteState(npmtest.RemoteState{DocStored: true, DocCurrent: true})
			return &formats.RemoteDocument{}, formats.Revalidated, nil
		}
		_, err := gate.BeforeArtifact(ctx, h, rq, path)
		if e, ok := errs.As(err); !ok || e.Kind != errs.NotFound {
			t.Errorf("err = %v, want 404", err)
		}
	})
	t.Run("the removed version stays 404 without another upstream request while the document is current", func(t *testing.T) {
		eng.ensured = nil
		_, err := gate.BeforeArtifact(ctx, h, rq, path)
		if e, ok := errs.As(err); !ok || e.Kind != errs.NotFound || len(eng.ensured) != 0 {
			t.Errorf("err = %v, ensured %v", err, eng.ensured)
		}
	})
	t.Run("serveRemovedVersions keeps it served", func(t *testing.T) {
		twin.SetRemoteState(npmtest.RemoteState{DocStored: true, DocCurrent: true, ServeRemoved: true})
		if _, err := gate.BeforeArtifact(ctx, h, rq, path); err != nil {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a gone package is 404 unless removed versions are served", func(t *testing.T) {
		twin.SetRemoteState(npmtest.RemoteState{DocStored: true, DocCurrent: true, DocGone: true})
		_, err := gate.BeforeArtifact(ctx, h, requestFor(t, h, "evil/-/evil-1.0.0.tgz"), formats.Path{Path: "evil/-/evil-1.0.0.tgz"})
		if e, ok := errs.As(err); !ok || e.Kind != errs.NotFound {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("when the upstream cannot be asked the stored marks decide", func(t *testing.T) {
		twin.SetRemoteState(npmtest.RemoteState{DocStored: true, DocCurrent: false})
		eng.ensure = func(string) (*formats.RemoteDocument, formats.Currency, error) {
			return nil, 0, errs.E(errs.Unavailable, errs.CodeUpstreamAssumedOffline, "offline")
		}
		if _, err := gate.BeforeArtifact(ctx, h, requestFor(t, h, "evil/-/evil-1.0.0.tgz"), formats.Path{Path: "evil/-/evil-1.0.0.tgz"}); err != nil {
			t.Errorf("a recorded version was refused because the upstream is down: %v", err)
		}
		// A version nobody recorded cannot be decided: the error of the engine stands.
		_, err := gate.BeforeArtifact(ctx, h, requestFor(t, h, "evil/-/evil-7.7.7.tgz"), formats.Path{Path: "evil/-/evil-7.7.7.tgz"})
		if e, ok := errs.As(err); !ok || e.Kind != errs.Unavailable {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("the cache view never contacts the upstream", func(t *testing.T) {
		eng.ensured = nil
		twin.SetRemoteState(npmtest.RemoteState{DocStored: true, DocCurrent: false})
		cv := requestFor(t, h, "evil/-/evil-1.0.0.tgz")
		cv.CacheView = true
		if _, err := gate.BeforeArtifact(ctx, h, cv, formats.Path{Path: "evil/-/evil-1.0.0.tgz"}); err != nil || len(eng.ensured) != 0 {
			t.Errorf("err %v, ensured %v", err, eng.ensured)
		}
	})
	t.Run("a file that is no tarball is 404", func(t *testing.T) {
		_, err := gate.BeforeArtifact(ctx, h, rq, formats.Path{Path: "evil/-/evil-1.0.0.sigstore"})
		if e, ok := errs.As(err); !ok || e.Kind != errs.NotFound {
			t.Errorf("err = %v", err)
		}
	})
}

func mustDigest(integrity string) []byte {
	raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(integrity, "sha512-"))
	return raw
}

func TestR_NPM_026_ServeCheckOfARemoteRepositoryAppliesTheStoredMarks(t *testing.T) {
	h, twin, f := remoteHost(t, &scriptedRemote{})
	body := upstreamBody("evil", "2026-10-30T11:00:00.000Z", "1.0.0")
	rc := formats.RemoteContext{Upstream: mustURL(t, "https://registry.example.org/")}
	prep, err := f.Remote().PrepareFill(context.Background(), rc, formats.Path{Path: "evil"}, "full", bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Remote().OnFill(context.Background(), nil, formats.FillEvent{Repo: h.Ref(), Path: formats.Path{Path: "evil"}, Variant: "full", Prepared: prep, Data: twin, Packages: &fakeWriter{}, FetchedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	item := func(p string) formats.ItemRef { return formats.ItemRef{Repo: h.Ref(), Path: p} }
	check := func(p string) error {
		return f.ServeCheck(context.Background(), h, h.Ref(), item(p), formats.ServeStored)
	}
	if err := check("evil/-/evil-1.0.0.tgz"); err != nil {
		t.Errorf("a listed version: %v", err)
	}
	if err := check("evil/-/evil-9.9.9.tgz"); err != nil {
		t.Errorf("a version nobody recorded is not withdrawn: %v", err)
	}
	// 1.0.0 disappears from the document.
	body = upstreamBody("evil", "2026-10-30T12:00:00.000Z", "1.1.0")
	prep, _ = f.Remote().PrepareFill(context.Background(), rc, formats.Path{Path: "evil"}, "full", bytes.NewReader(body), int64(len(body)))
	if _, err := f.Remote().OnFill(context.Background(), nil, formats.FillEvent{Repo: h.Ref(), Path: formats.Path{Path: "evil"}, Variant: "full", Prepared: prep, Data: twin, Packages: &fakeWriter{}, FetchedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := check("evil/-/evil-1.0.0.tgz"); err == nil {
		t.Error("a removed version is served")
	}
	twin.SetRemoteState(npmtest.RemoteState{ServeRemoved: true})
	if err := check("evil/-/evil-1.0.0.tgz"); err != nil {
		t.Errorf("serveRemovedVersions: %v", err)
	}
	if err := check("evil"); err != nil {
		t.Errorf("a document is no item to check: %v", err)
	}
}

func TestR_NPM_001_AWriteToARemoteRepositoryAnswersClassNotWritable(t *testing.T) {
	f := New()
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "/api/npm/npm-remote/lodash", nil)
		f.WriteError(w, r, errs.E(errs.MethodNotAllowed, errs.CodeMethodNotAllowed, "A remote repository does not serve this request.",
			errs.WithAllow("GET", "HEAD"), errs.WithExtension("repositoryClass", "remote")))
		var b map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
			t.Fatal(err)
		}
		if w.Code != 405 || b["code"] != "npm_class_not_writable" || w.Header().Get("Allow") != "GET, HEAD" || !strings.Contains(b["error"], "remote repository") {
			t.Errorf("%s: %d %v %q", method, w.Code, b, w.Header().Get("Allow"))
		}
	}
	// A virtual repository's 405 keeps its own code.
	w := httptest.NewRecorder()
	f.WriteError(w, httptest.NewRequest(http.MethodPut, "/x", nil), errs.E(errs.MethodNotAllowed, errs.CodeNoDeployTarget, "no target", errs.WithExtension("repositoryClass", "virtual")))
	if !strings.Contains(w.Body.String(), "no_deploy_target") {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestR_NPM_025_AnErrorOfTheEngineKeepsItsCacheStatus(t *testing.T) {
	f := New()
	w := httptest.NewRecorder()
	f.WriteError(w, httptest.NewRequest(http.MethodGet, "/x", nil),
		errs.E(errs.NotFound, errs.CodeNotFound, "gone", errs.WithHeader("Cache-Status", "trove; hit; detail=negative")))
	if w.Header().Get("Cache-Status") != "trove; hit; detail=negative" {
		t.Errorf("headers = %v", w.Header())
	}
}
