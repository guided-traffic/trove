package nuget

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/nuget/nugettest"
)

func newHost(t *testing.T, o formattest.HostOptions) *formattest.Host {
	t.Helper()
	if o.Data == nil {
		o.Data = nugettest.New()
	}
	return formattest.NewHost(New(), o)
}

func get(t *testing.T, h *formattest.Host, method, path string, mods ...func(*http.Request)) *formattest.Response {
	t.Helper()
	resp, err := h.Serve(context.Background(), method, path, nil, false, mods...)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

const feedOfMain = "https://trove.test/api/nuget/main/v3/"

// TestR_NUGET_001_TheServiceIndexIsIdenticalAtThreeSpellings: "<feed>/index.json", "<feed>/"
// and "<feed>" answer with byte-identical bodies and headers, without a redirect.
func TestR_NUGET_001_TheServiceIndexIsIdenticalAtThreeSpellings(t *testing.T) {
	h := newHost(t, formattest.HostOptions{})
	first := get(t, h, http.MethodGet, "v3/index.json")
	if first.Code != http.StatusOK {
		t.Fatalf("status %d: %s", first.Code, first.Body.String())
	}
	for _, p := range []string{"v3/", "v3"} {
		r := get(t, h, http.MethodGet, p)
		if r.Code != http.StatusOK {
			t.Errorf("GET %q: status %d", p, r.Code)
		}
		if r.Body.String() != first.Body.String() {
			t.Errorf("GET %q: body differs from v3/index.json", p)
		}
		for _, k := range []string{"Content-Type", "ETag", "Content-Length"} {
			if r.Header().Get(k) != first.Header().Get(k) {
				t.Errorf("GET %q: %s = %q, want %q", p, k, r.Header().Get(k), first.Header().Get(k))
			}
		}
	}
	if ct := first.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
}

// TestR_NUGET_002_TheServiceIndexListsEveryResourceUnderEveryAlias holds the document to the
// table of R-NUGET-002: version 3.0.0, one entry per alias, every @id below the repository's
// own feed root.
func TestR_NUGET_002_TheServiceIndexListsEveryResourceUnderEveryAlias(t *testing.T) {
	h := newHost(t, formattest.HostOptions{})
	var doc struct {
		Version   string `json:"version"`
		Resources []struct {
			ID   string `json:"@id"`
			Type string `json:"@type"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(get(t, h, http.MethodGet, "v3/index.json").Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version != "3.0.0" {
		t.Errorf("version = %q", doc.Version)
	}
	want := map[string]string{
		"PackageBaseAddress/3.0.0":        "flatcontainer/",
		"PackagePublish/2.0.0":            "package",
		"SymbolPackagePublish/4.9.0":      "symbolpackage",
		"RegistrationsBaseUrl":            "registration/",
		"RegistrationsBaseUrl/3.0.0-beta": "registration/",
		"RegistrationsBaseUrl/3.0.0-rc":   "registration/",
		"RegistrationsBaseUrl/3.4.0":      "registration-gz/",
		"RegistrationsBaseUrl/3.6.0":      "registration-semver2/",
		"SearchQueryService":              "search",
		"SearchQueryService/3.0.0-beta":   "search",
		"SearchQueryService/3.0.0-rc":     "search",
		"SearchQueryService/3.5.0":        "search",
	}
	got := map[string]string{}
	for _, r := range doc.Resources {
		if !strings.HasPrefix(r.ID, feedOfMain) {
			t.Errorf("@id %q is not below %s", r.ID, feedOfMain)
		}
		if _, dup := got[r.Type]; dup {
			t.Errorf("type %s is listed twice", r.Type)
		}
		got[r.Type] = strings.TrimPrefix(r.ID, feedOfMain)
	}
	for typ, path := range want {
		if got[typ] != path {
			t.Errorf("%s = %q, want %q", typ, got[typ], path)
		}
	}
	if len(got) != len(want) {
		t.Errorf("%d types listed, want %d: %v", len(got), len(want), got)
	}
}

// TestR_NUGET_002_ThePublishResourcesAreListedForARepositoryThatTakesPushes: a repository that
// takes no push - a remote one, a virtual one without a deployment target - lists neither
// publish resource, and nothing else changes.
func TestR_NUGET_002_ThePublishResourcesAreListedForARepositoryThatTakesPushes(t *testing.T) {
	with, without := string(ServiceIndex(feedOfMain, true)), string(ServiceIndex(feedOfMain, false))
	for _, typ := range []string{"PackagePublish/2.0.0", "SymbolPackagePublish/4.9.0"} {
		if !strings.Contains(with, typ) || strings.Contains(without, typ) {
			t.Errorf("%s: with pushes %v, without %v", typ, strings.Contains(with, typ), strings.Contains(without, typ))
		}
	}
	if strings.Count(with, "@id")-strings.Count(without, "@id") != 2 {
		t.Errorf("the two documents differ by more than the two publish entries")
	}
}

// TestR_NUGET_002_TheServiceIndexCarriesAWeakETag: the validator is weak, answers 304 for a
// match by the weak comparison, and HEAD returns the GET headers without a body.
func TestR_NUGET_002_TheServiceIndexCarriesAWeakETag(t *testing.T) {
	h := newHost(t, formattest.HostOptions{})
	resp := get(t, h, http.MethodGet, "v3/index.json")
	etag := resp.Header().Get("ETag")
	if !strings.HasPrefix(etag, `W/"`) {
		t.Fatalf("ETag = %q, want a weak validator", etag)
	}
	ifNone := func(v string) func(*http.Request) { return func(r *http.Request) { r.Header.Set("If-None-Match", v) } }
	for _, v := range []string{etag, strings.TrimPrefix(etag, "W/"), `"other", ` + etag} {
		r := get(t, h, http.MethodGet, "v3/index.json", ifNone(v))
		if r.Code != http.StatusNotModified || r.Body.Len() != 0 || r.Header().Get("ETag") != etag {
			t.Errorf("If-None-Match %q: status %d, body %d bytes, ETag %q", v, r.Code, r.Body.Len(), r.Header().Get("ETag"))
		}
	}
	for _, v := range []string{`W/"other"`, "*"} {
		if r := get(t, h, http.MethodGet, "v3/index.json", ifNone(v)); r.Code != http.StatusOK {
			t.Errorf("If-None-Match %q: status %d, want 200", v, r.Code)
		}
	}
	head := get(t, h, http.MethodHead, "v3/index.json")
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Errorf("HEAD: status %d with %d body bytes", head.Code, head.Body.Len())
	}
	for _, k := range []string{"Content-Type", "ETag", "Content-Length"} {
		if head.Header().Get(k) != resp.Header().Get(k) {
			t.Errorf("HEAD %s = %q, GET has %q", k, head.Header().Get(k), resp.Header().Get(k))
		}
	}
	if n, err := strconv.Atoi(resp.Header().Get("Content-Length")); err != nil || n != resp.Body.Len() {
		t.Errorf("Content-Length = %q for a body of %d bytes", resp.Header().Get("Content-Length"), resp.Body.Len())
	}
}

// TestR_NUGET_002_TheETagFollowsItsInputs: the validator is a function of the inputs of the
// document - base URL, repository, configuration version, endpoint, exposure - and of nothing
// else, so two replicas send one value and a change of an input sends another.
func TestR_NUGET_002_TheETagFollowsItsInputs(t *testing.T) {
	etagOf := func(o formattest.HostOptions) string {
		return get(t, newHost(t, o), http.MethodGet, "v3/index.json").Header().Get("ETag")
	}
	base := etagOf(formattest.HostOptions{})
	if again := etagOf(formattest.HostOptions{}); again != base {
		t.Errorf("two hosts with the same inputs send %q and %q", base, again)
	}
	for name, o := range map[string]formattest.HostOptions{
		"base URL":   {BaseURL: "https://other.test"},
		"repository": {RepoKey: "nuget-local"},
	} {
		if got := etagOf(o); got == base {
			t.Errorf("a changed %s keeps the ETag %q", name, base)
		}
	}
	// the inputs the kit's host does not vary are checked on the function itself
	h := newHost(t, formattest.HostOptions{})
	rq, err := h.NewRequest(context.Background(), http.MethodGet, "v3/index.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	const base2 = "https://trove.test"
	orig := indexETag(rq, base2, true)
	if indexETag(rq, base2, false) == orig {
		t.Error("the publish resources do not change the ETag")
	}
	rq.Repo.Version++
	if indexETag(rq, base2, true) == orig {
		t.Error("a new configuration version keeps the ETag")
	}
	rq.Repo.Version--
	rq.Endpoint.ExposureFingerprint = "rules-1"
	if indexETag(rq, base2, true) == orig {
		t.Error("an exposure fingerprint keeps the ETag")
	}
	rq.Endpoint.ExposureFingerprint = ""
	rq.Endpoint.Name = "customers"
	if indexETag(rq, base2, true) == orig {
		t.Error("another endpoint keeps the ETag")
	}
}

// TestR_NUGET_001_AnOperationThatIsNotBuiltAnswers501: every route of the table that has no
// handler yet answers 501 nuget_not_implemented in the native shape, never a 404 that a client
// would read as "no such package".
func TestR_NUGET_001_AnOperationThatIsNotBuiltAnswers501(t *testing.T) {
	h := newHost(t, formattest.HostOptions{})
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "v3/flatcontainer/acme.widgets/1.2.3/readme"},
	} {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			r := get(t, h, c.method, c.path)
			if r.Code != http.StatusNotImplemented {
				t.Fatalf("status %d, want 501", r.Code)
			}
			if ct := r.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
				t.Errorf("Content-Type = %q", ct)
			}
			if !strings.HasPrefix(r.Body.String(), "nuget_not_implemented: ") {
				t.Errorf("body = %q", r.Body.String())
			}
		})
	}
}
