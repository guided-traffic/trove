package npm

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/npm/npmtest"
)

// stored is a document of the engine's cache.
func stored(body []byte) *formats.RemoteDocument {
	return &formats.RemoteDocument{
		Digest: sha256.Sum256(body), Size: int64(len(body)),
		Bytes:  func(context.Context) ([]byte, error) { return body, nil },
		Status: formats.CacheStatus{Hit: true, Header: "trove; hit; ttl=3000"},
	}
}

func get(t *testing.T, h *formattest.Host, path string, hdr ...string) *formattest.Response {
	t.Helper()
	resp, err := h.Serve(context.Background(), http.MethodGet, path, nil, false, func(r *http.Request) {
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestR_NPM_025_ARemoteServesTheStoredDocumentWithTarballsOnTheBaseURL(t *testing.T) {
	body := upstreamBody("@acme/ui", "2026-10-30T11:00:00.000Z", "1.0.0", "1.1.0")
	var asked []string
	eng := &scriptedRemote{metadata: func(path string) (*formats.RemoteDocument, error) {
		asked = append(asked, path)
		return stored(body), nil
	}}
	h, _, _ := remoteHost(t, eng)

	full := get(t, h, "@acme%2fui")
	if full.Code != 200 || full.Header().Get("Cache-Status") != "trove; hit; ttl=3000" || full.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("%d %v\n%s", full.Code, full.Header(), full.Body)
	}
	var d struct {
		Versions map[string]struct {
			Dist struct{ Tarball, Integrity string }
		}
	}
	if err := json.Unmarshal(full.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	for v, e := range d.Versions {
		if want := formattest.DefaultBaseURL + "/api/npm/main/@acme/ui/-/ui-" + v + ".tgz"; e.Dist.Tarball != want {
			t.Errorf("%s: tarball %q, want %q", v, e.Dist.Tarball, want)
		}
		if e.Dist.Integrity != sriOf("@acme/ui"+v) {
			t.Errorf("%s: integrity changed", v)
		}
	}
	if len(asked) != 1 || asked[0] != "@acme/ui" {
		t.Errorf("the engine was asked for %v", asked)
	}

	t.Run("the abbreviated form is derived from the one stored document", func(t *testing.T) {
		ab := get(t, h, "@acme%2fui", "Accept", "application/vnd.npm.install-v1+json")
		if ab.Code != 200 || ab.Header().Get("Content-Type") != "application/vnd.npm.install-v1+json" {
			t.Fatalf("%d %v", ab.Code, ab.Header())
		}
		if ab.Header().Get("ETag") == full.Header().Get("ETag") || !strings.Contains(ab.Body.String(), `"modified":"2026-10-30T11:00:00.000Z"`) {
			t.Errorf("etag %q / %q, body %s", ab.Header().Get("ETag"), full.Header().Get("ETag"), ab.Body)
		}
		if len(asked) != 2 {
			t.Errorf("asked %v", asked)
		}
	})
	t.Run("a validator that matches is a 304 and the document is not read", func(t *testing.T) {
		read := 0
		eng.metadata = func(string) (*formats.RemoteDocument, error) {
			d := stored(body)
			inner := d.Bytes
			d.Bytes = func(ctx context.Context) ([]byte, error) { read++; return inner(ctx) }
			return d, nil
		}
		again := get(t, h, "@acme%2fui", "If-None-Match", full.Header().Get("ETag"))
		if again.Code != 304 || read != 0 || again.Header().Get("Cache-Status") == "" || again.Body.Len() != 0 {
			t.Errorf("%d, read %d, body %q", again.Code, read, again.Body)
		}
		// The same document through the rendition cache is not read again either.
		a := get(t, h, "@acme%2fui")
		b := get(t, h, "@acme%2fui")
		if a.Code != 200 || b.Body.String() != a.Body.String() || read > 1 {
			t.Errorf("read %d times", read)
		}
	})
	t.Run("another document is another validator", func(t *testing.T) {
		eng.metadata = func(string) (*formats.RemoteDocument, error) {
			return stored(upstreamBody("@acme/ui", "2026-10-31T00:00:00.000Z", "1.0.0", "1.1.0", "1.2.0")), nil
		}
		changed := get(t, h, "@acme%2fui", "If-None-Match", full.Header().Get("ETag"))
		if changed.Code != 200 || changed.Header().Get("ETag") == full.Header().Get("ETag") {
			t.Errorf("%d %q", changed.Code, changed.Header().Get("ETag"))
		}
	})
	t.Run("the single-version document, by number and by tag, and the dist-tags", func(t *testing.T) {
		v := get(t, h, "@acme%2fui/1.2.0")
		if v.Code != 200 || !strings.Contains(v.Body.String(), `"version":"1.2.0"`) || strings.Contains(v.Body.String(), "registry.example.org") {
			t.Errorf("%d %s", v.Code, v.Body)
		}
		if tagged := get(t, h, "@acme%2fui/latest"); tagged.Code != 404 {
			t.Errorf("the document has no latest tag, got %d", tagged.Code) // upstreamBody sets none
		}
		if missing := get(t, h, "@acme%2fui/9.9.9"); missing.Code != 404 {
			t.Errorf("an unknown version: %d", missing.Code)
		}
		tags := get(t, h, "-/package/@acme%2fui/dist-tags")
		if tags.Code != 200 || strings.TrimSpace(tags.Body.String()) != "{}" || tags.Header().Get("Cache-Control") != "private, no-cache" {
			t.Errorf("%d %q %v", tags.Code, tags.Body, tags.Header())
		}
	})
	t.Run("a negative entry is a 404 that says why", func(t *testing.T) {
		eng.metadata = func(string) (*formats.RemoteDocument, error) {
			return &formats.RemoteDocument{Status: formats.CacheStatus{Hit: true, Negative: true, Header: "trove; hit; detail=negative"}}, nil
		}
		r := get(t, h, "nope")
		if r.Code != 404 || r.Header().Get("Cache-Status") != "trove; hit; detail=negative" {
			t.Errorf("%d %v", r.Code, r.Header())
		}
	})
	t.Run("an error of the engine is the answer", func(t *testing.T) {
		eng.metadata = func(string) (*formats.RemoteDocument, error) {
			return nil, errs.E(errs.UpstreamFailed, errs.CodeNPMInvalidUpstreamDocument, "bad", errs.WithHeader("Cache-Status", "trove; fwd=uri-miss"))
		}
		r := get(t, h, "bad")
		if r.Code != 502 || r.Header().Get("Cache-Status") != "trove; fwd=uri-miss" || !strings.Contains(r.Body.String(), "npm_invalid_upstream_document") {
			t.Errorf("%d %v %s", r.Code, r.Header(), r.Body)
		}
	})
}

func TestR_REPO_032_TheCacheViewNamesItselfInTheTarballAddresses(t *testing.T) {
	body := upstreamBody("lodash", "2026-10-30T11:00:00.000Z", "4.17.21")
	h, _, f := remoteHost(t, &scriptedRemote{metadata: func(string) (*formats.RemoteDocument, error) { return stored(body), nil }})
	rq := requestFor(t, h, "lodash")
	rq.CacheView = true
	w := httpRecorder()
	formats.ServeRoute(f, w, rq, false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "/api/npm/main~cache/lodash/-/lodash-4.17.21.tgz") {
		t.Errorf("%d %s", w.Code, w.Body)
	}
	normal := get(t, h, "lodash")
	if strings.Contains(normal.Body.String(), "~cache") || normal.Header().Get("ETag") == w.Header().Get("ETag") {
		t.Errorf("the cache view and the repository share a validator or an address")
	}
}

func TestR_NPM_025_ARemoteTarballIsFetchedThroughTheEngineWithTheMarksApplied(t *testing.T) {
	var got struct {
		path     string
		expected formats.Digests
	}
	eng := &scriptedRemote{artifact: func(p formats.Path, expected formats.Digests, w http.ResponseWriter) error {
		got.path, got.expected = p.Path, expected
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("tarball"))
		return nil
	}}
	h, _, _ := remoteHost(t, eng)
	r := get(t, h, "@acme%2fui/-/ui-1.0.0.tgz")
	if r.Code != 200 || r.Body.String() != "tarball" || got.path != "@acme/ui/-/ui-1.0.0.tgz" {
		t.Fatalf("%d %q %+v", r.Code, r.Body, got)
	}
	if r.Header().Get("Cache-Control") != immutableCacheControl {
		t.Errorf("Cache-Control = %q", r.Header().Get("Cache-Control"))
	}
	if bad := get(t, h, "@acme%2fui/-/ui-1.0.tgz"); bad.Code != 404 {
		t.Errorf("a file name that is no version: %d", bad.Code)
	}
}

func TestR_NPM_029_WritesToARemoteRepositoryAreNotServedByTheRoutes(t *testing.T) {
	f := New()
	for _, r := range f.Routes() {
		write := r.Action != formats.ActionRead
		serves := r.ServesClass(formats.Remote)
		isDeferred := strings.HasPrefix(r.Name, "npm.deferred.")
		if write && !isDeferred && serves {
			t.Errorf("route %s writes and serves remote repositories", r.Name)
		}
		if write && !isDeferred && !r.ServesClass(formats.Local) {
			t.Errorf("route %s no longer serves local repositories", r.Name)
		}
		if !write && !serves {
			t.Errorf("read route %s does not serve remote repositories", r.Name)
		}
	}
}

// resolverFake is virtual resolution with scripted answers.
type resolverFake struct {
	formats.Resolver
	merged   func() (*formats.Merged, error)
	artifact func(p formats.Path, w http.ResponseWriter) error
	paths    []string
}

func (r *resolverFake) Metadata(_ context.Context, _ *formats.Request, p formats.Path, info formats.PathInfo, variant string) (*formats.Merged, error) {
	r.paths = append(r.paths, p.Path+" "+variant+" "+info.Package)
	return r.merged()
}

func (r *resolverFake) Artifact(_ context.Context, _ *formats.Request, p formats.Path, _ formats.PathInfo, w http.ResponseWriter) error {
	return r.artifact(p, w)
}

func TestR_NPM_030_AVirtualDocumentIsRewrittenAfterTheMergeForTheVirtualRepository(t *testing.T) {
	merged := []byte(`{"name":"lodash","dist-tags":{"latest":"1.1.0"},"versions":{` +
		`"1.0.0":{"name":"lodash","version":"1.0.0","dist":{"tarball":"urn:trove:npm-tarball:lodash-1.0.0.tgz","integrity":"sha512-x"}},` +
		`"1.1.0":{"name":"lodash","version":"1.1.0","dist":{"tarball":"https://registry.example.org/lodash/-/lodash-1.1.0.tgz"}}}}`)
	fp := [32]byte{7}
	res := &resolverFake{merged: func() (*formats.Merged, error) {
		return &formats.Merged{Bytes: merged, MergeFingerprint: fp, CacheStatus: "trove; fwd=miss"}, nil
	}}
	f := New()
	h := formattest.NewHost(f, formattest.HostOptions{Class: formats.Virtual, RepoKey: "npm-all", Data: npmtest.New(), Resolver: res})
	r := get(t, h, "lodash")
	if r.Code != 200 || r.Header().Get("Cache-Status") != "trove; fwd=miss" {
		t.Fatalf("%d %v\n%s", r.Code, r.Header(), r.Body)
	}
	for _, v := range []string{"1.0.0", "1.1.0"} {
		if !strings.Contains(r.Body.String(), formattest.DefaultBaseURL+"/api/npm/npm-all/lodash/-/lodash-"+v+".tgz") {
			t.Errorf("%s is not under the virtual repository's URL: %s", v, r.Body)
		}
	}
	if strings.Contains(r.Body.String(), "registry.example.org") || strings.Contains(r.Body.String(), "urn:trove") {
		t.Errorf("a member's address leaked: %s", r.Body)
	}
	if !strings.Contains(r.Body.String(), `"_rev":"1-07`) {
		t.Errorf("no synthetic _rev from the merge fingerprint: %s", r.Body)
	}
	if len(res.paths) != 1 || res.paths[0] != "lodash full lodash" {
		t.Errorf("the resolver was asked for %v", res.paths)
	}
	// The validator is a function of the merge: the same fingerprint, the same validator; another,
	// another.
	if again := get(t, h, "lodash", "If-None-Match", r.Header().Get("ETag")); again.Code != 304 {
		t.Errorf("the same merge: %d", again.Code)
	}
	fp = [32]byte{8}
	if other := get(t, h, "lodash", "If-None-Match", r.Header().Get("ETag")); other.Code != 200 {
		t.Errorf("another merge: %d", other.Code)
	}

	t.Run("the abbreviated form, the version and the merged tags", func(t *testing.T) {
		ab := get(t, h, "lodash", "Accept", "application/vnd.npm.install-v1+json")
		if ab.Code != 200 || !strings.Contains(ab.Body.String(), "/api/npm/npm-all/lodash/-/lodash-1.1.0.tgz") {
			t.Errorf("%d %s", ab.Code, ab.Body)
		}
		v := get(t, h, "lodash/latest")
		if v.Code != 200 || !strings.Contains(v.Body.String(), `"version":"1.1.0"`) {
			t.Errorf("%d %s", v.Code, v.Body)
		}
		tags := get(t, h, "-/package/lodash/dist-tags")
		if tags.Code != 200 || strings.TrimSpace(tags.Body.String()) != `{"latest":"1.1.0"}` {
			t.Errorf("%d %s", tags.Code, tags.Body)
		}
	})
	t.Run("a package no member knows is 404", func(t *testing.T) {
		res.merged = func() (*formats.Merged, error) { return nil, errs.E(errs.NotFound, errs.CodeNotFound, "no") }
		if r := get(t, h, "nobody"); r.Code != 404 {
			t.Errorf("%d", r.Code)
		}
	})
	t.Run("a tarball is resolved through the tiers", func(t *testing.T) {
		res.artifact = func(p formats.Path, w http.ResponseWriter) error {
			if p.Path != "lodash/-/lodash-1.1.0.tgz" {
				t.Errorf("path = %q", p.Path)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("bytes"))
			return nil
		}
		if r := get(t, h, "lodash/-/lodash-1.1.0.tgz"); r.Code != 200 || r.Body.String() != "bytes" || r.Header().Get("Cache-Control") != immutableCacheControl {
			t.Errorf("%d %q %v", r.Code, r.Body, r.Header())
		}
	})
}

func TestR_NPM_030_TheMergeAppliesTheStrategiesOfTheVirtualRepositoryAndCountsConflicts(t *testing.T) {
	f := New()
	h := formattest.NewHost(f, formattest.HostOptions{Class: formats.Virtual, RepoKey: "npm-all", Data: npmtest.New()})
	rq := requestFor(t, h, "lodash")
	rq.Repo.Config = []byte(`{"npm":{"distTagStrategy":"highest-semver","latestStrategy":"newest-published"}}`)
	doc := func(member, v, tag string) formats.MemberDocument {
		return formats.MemberDocument{
			Member: formats.RepoRef{Key: member}, Tier: formats.TierLocal, Doc: []byte(`{"name":"lodash","dist-tags":{"beta":"` + tag + `"},"versions":{"` + v + `":{"name":"lodash","version":"` + v + `","dist":{"integrity":"sha512-` + member + `"}},"` + tag + `":{"name":"lodash","version":"` + tag + `"}}}`),
		}
	}
	a, b := doc("a", "1.0.0", "1.0.0"), doc("b", "1.0.0", "2.0.0-beta.1")
	b.Position = 1
	out, err := f.Virtual().Merge(context.Background(), rq, formats.Path{Path: "lodash"}, []formats.MemberDocument{a, b})
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Tags map[string]string `json:"dist-tags"`
	}
	_ = json.Unmarshal(out, &m)
	if m.Tags["beta"] != "2.0.0-beta.1" {
		t.Errorf("beta = %q: the strategy of the repository was not applied", m.Tags["beta"])
	}
}

func TestR_AUTHZ_008_AMergeListsOnlyTheCachedVersionsOfAMemberTheCallerMayNotFill(t *testing.T) {
	// H-283: the conformance kit does not check the restriction yet, so the npm adapter is
	// checked here: a caller who may read a remote member and not fill its cache sees only the
	// versions whose tarball is cached, so that every version a document lists can be installed.
	f := New()
	h := formattest.NewHost(f, formattest.HostOptions{Class: formats.Virtual, RepoKey: "npm-all", Data: npmtest.New()})
	rq := requestFor(t, h, "lodash")
	remoteDoc := upstreamBody("lodash", "2026-10-30T11:00:00.000Z", "1.0.0", "1.1.0", "1.2.0")
	merge := func(restricted bool) map[string]json.RawMessage {
		t.Helper()
		out, err := f.Virtual().Merge(context.Background(), rq, formats.Path{Path: "lodash"}, []formats.MemberDocument{{
			Member: formats.RepoRef{Key: "npmjs"}, Tier: formats.TierRemote, Doc: remoteDoc, Restricted: restricted,
			CachedVersions: map[string]bool{"1.1.0": true},
		}})
		if err != nil {
			t.Fatal(err)
		}
		var d struct {
			Versions map[string]json.RawMessage `json:"versions"`
		}
		_ = json.Unmarshal(out, &d)
		return d.Versions
	}
	if v := merge(true); len(v) != 1 || v["1.1.0"] == nil {
		t.Errorf("a caller who may not fill sees %d versions", len(v))
	}
	if v := merge(false); len(v) != 3 {
		t.Errorf("a caller who may fill sees %d versions", len(v))
	}
}
