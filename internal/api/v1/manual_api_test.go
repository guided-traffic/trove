package v1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	docs "github.com/guided-traffic/trove/docs/manual"
	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/manual"
)

// manualServer serves the manual operations to the caller authn produces (nil is
// anonymous), with anonymous access on or off.
func manualServer(t *testing.T, m *manual.Manual, authn func(*http.Request) (httpserver.Principal, error), anonymous bool) http.Handler {
	t.Helper()
	doc, err := LoadDocument(EmbeddedYAML())
	if err != nil {
		t.Fatal(err)
	}
	router := httpserver.NewClientRouter()
	New(Options{
		Document: doc, Identity: Identity{Replica: "r", Role: "web"}, Ready: func() bool { return true },
		Access: allowAll{}, Replicas: fakeReplicas{}, Facts: fakeFacts{},
		Repositories: RepositoryServices{AnonymousAccess: func(context.Context) (bool, error) { return anonymous, nil }},
		Manual:       m,
	}).Register(router)
	res, err := httpserver.NewResolver(nil, "https://trove.example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	return httpserver.Chain{Router: router, Resolver: res, Authenticate: authn, Replica: "r", ReplicaHeader: httpserver.ReplicaAuthenticated}.Handler()
}

// TestR_API_054_TheManualOperations: the contents list both guides in reading order,
// a page carries its text, headings and neighbours, both answer with the manual's
// ETag and 304 for it, and an unknown page is 404.
func TestR_API_054_TheManualOperations(t *testing.T) {
	m, err := manual.Load(docs.Files)
	if err != nil {
		t.Fatal(err)
	}
	h := manualServer(t, m, authed, false)

	w := get(h, "/api/v1/manual")
	var contents gen.ManualContents
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &contents) != nil {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("ETag") != m.ETag() || len(contents.Sections) != 2 || contents.Sections[0].Id != "user" || contents.Sections[1].Id != "admin" {
		t.Fatalf("%s %+v", w.Header().Get("ETag"), contents.Sections)
	}
	first := contents.Sections[0].Pages[0]

	w = get(h, "/api/v1/manual/user/"+first.Id)
	var p gen.ManualPage
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &p) != nil {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if p.Title != first.Title || p.Summary != first.Summary || p.Html == "" || len(p.Headings) == 0 || p.Previous != nil || p.Next == nil {
		t.Fatalf("%+v", p)
	}

	r := httptest.NewRequest("GET", "/api/v1/manual/user/"+first.Id, nil)
	r.Header.Set("If-None-Match", m.ETag())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: %d", rec.Code)
	}
	for _, path := range []string{"/api/v1/manual/user/nothing", "/api/v1/manual/nothing/" + first.Id} {
		if w := get(h, path); w.Code != 404 {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
}

// TestR_API_054_AnonymousReadersFollowTheAnonymousAccessSetting: an anonymous caller
// reads the manual where anonymous access is on and gets 401 where it is off; a
// replica without a manual answers 503.
func TestR_API_054_AnonymousReadersFollowTheAnonymousAccessSetting(t *testing.T) {
	m, err := manual.Load(docs.Files)
	if err != nil {
		t.Fatal(err)
	}
	if w := get(manualServer(t, m, nil, true), "/api/v1/manual"); w.Code != 200 {
		t.Errorf("anonymous access on: %d", w.Code)
	}
	if w := get(manualServer(t, m, nil, false), "/api/v1/manual"); w.Code != 401 {
		t.Errorf("anonymous access off: %d", w.Code)
	}
	if w := get(manualServer(t, nil, authed, false), "/api/v1/manual"); w.Code != 503 {
		t.Errorf("no manual: %d", w.Code)
	}
}

// TestR_API_054_TheManualIsLoadedByTheFirstRequestThatNeedsIt: the replica does not
// render the manual before it is ready (Q-OPS-157); the first operation does, once,
// and a loader that fails answers 503.
func TestR_API_054_TheManualIsLoadedByTheFirstRequestThatNeedsIt(t *testing.T) {
	build := func(load func() (*manual.Manual, error)) http.Handler {
		doc, err := LoadDocument(EmbeddedYAML())
		if err != nil {
			t.Fatal(err)
		}
		router := httpserver.NewClientRouter()
		New(Options{
			Document: doc, Identity: Identity{Replica: "r", Role: "web"}, Ready: func() bool { return true },
			Access: allowAll{}, Replicas: fakeReplicas{}, Facts: fakeFacts{},
			Repositories: RepositoryServices{AnonymousAccess: func(context.Context) (bool, error) { return false, nil }},
			LoadManual:   load,
		}).Register(router)
		res, err := httpserver.NewResolver(nil, "https://trove.example.com", nil)
		if err != nil {
			t.Fatal(err)
		}
		return httpserver.Chain{Router: router, Resolver: res, Authenticate: authed, Replica: "r", ReplicaHeader: httpserver.ReplicaAuthenticated}.Handler()
	}
	loads := 0
	m, err := manual.Load(docs.Files)
	if err != nil {
		t.Fatal(err)
	}
	h := build(sync.OnceValues(func() (*manual.Manual, error) { loads++; return m, nil }))
	if loads != 0 {
		t.Fatalf("the manual was loaded when the server was built (%d)", loads)
	}
	for i := 0; i < 3; i++ {
		if w := get(h, "/api/v1/manual"); w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
	if loads != 1 {
		t.Fatalf("the manual was loaded %d times, want once", loads)
	}
	failing := build(func() (*manual.Manual, error) { return nil, errors.New("broken") })
	if w := get(failing, "/api/v1/manual"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("a manual that cannot be loaded: %d, want 503", w.Code)
	}
}
