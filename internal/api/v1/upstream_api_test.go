package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
)

// nopRunner runs no transaction: the operations under test never write.
type nopRunner struct{}

func (nopRunner) InTx(context.Context, func(meta.Tx) error) error { return nil }

// recordingProber answers every probe with a fixed result and keeps what it was asked.
type recordingProber struct {
	got repository.UnsavedProbe
}

func (p *recordingProber) TestSaved(context.Context, repository.Repository, repository.RemoteSettings) repository.UpstreamTestResult {
	return repository.UpstreamTestResult{}
}

func (p *recordingProber) TestUnsaved(_ context.Context, u repository.UnsavedProbe) repository.UpstreamTestResult {
	p.got = u
	status := 200
	yes := true
	return repository.UpstreamTestResult{Reachable: true, HTTPStatus: &status, Authenticated: &yes, DurationMs: 3}
}

func upstreamServer(t *testing.T, prober repository.UpstreamProber) *Server {
	t.Helper()
	cat := catalog.NewRegistry()
	cat.Register(catalog.Entry{Facts: catalog.Facts{
		ID: "npmlike", Classes: []catalog.Class{catalog.Local, catalog.Remote}, Layout: "npmlike", ProtocolBasePath: "/api/npmlike/{key}/",
		PatternSubject: catalog.SubjectPath, DefaultUpstreamURL: "https://registry.example.org/", ProbePath: "-/ping",
	}})
	svc, err := repository.NewService(repository.Options{
		Runner: nopRunner{}, Notifier: noNotifier{}, Catalog: cat, Remote: repository.RemoteDeps{Prober: prober},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &Server{o: Options{
		Repositories: RepositoryServices{Service: svc},
		API:          Services{Config: config.API{MaxConfigurationBodyBytes: 1 << 20}},
	}}
}

func post(s *Server, h func(http.ResponseWriter, *http.Request), body, contentType string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/upstream-tests", strings.NewReader(body))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

func TestUpstreamTestOperation(t *testing.T) {
	t.Run("R-REPO-024 an unsaved test validates the section like a create and probes it", func(t *testing.T) {
		p := &recordingProber{}
		s := upstreamServer(t, p)
		w := post(s, s.TestUpstream, `{"format":"npmlike","remote":{"authMode":"bearer","password":{"value":"tok"},"sourceNetwork":"10.99.0.0/24"}}`, "application/json")
		if w.Code != http.StatusOK {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		var res map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		if res["reachable"] != true || res["httpStatus"] != float64(200) || res["error"] != nil {
			t.Fatalf("result = %v", res)
		}
		if p.got.Format != "npmlike" || p.got.Settings.URL != "https://registry.example.org/" || p.got.Settings.AuthMode != "bearer" ||
			p.got.Password == nil || p.got.Password.Reveal() != "tok" || p.got.Settings.SourceNetwork.String() != "10.99.0.0/24" {
			t.Fatalf("probe = %+v", p.got)
		}
		if strings.Contains(w.Body.String(), "tok\"") {
			t.Fatalf("the answer carries the secret: %s", w.Body)
		}
	})

	t.Run("R-REPO-008 R-REPO-024 an unsaved test reports every violation in one 422", func(t *testing.T) {
		s := upstreamServer(t, &recordingProber{})
		w := post(s, s.TestUpstream, `{"format":"npmlike","remote":{"url":"https://u:p@x.example/","connectTimeoutMs":50,"sourceNetwork":"10.99.0.11"}}`, "application/json")
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		var problem struct {
			Errors []struct{ Pointer, Code string } `json:"errors"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, e := range problem.Errors {
			got[e.Pointer] = e.Code
		}
		if got["/remote/url"] != "invalid_upstream_url" || got["/remote/connectTimeoutMs"] != "out_of_range" || got["/remote/sourceNetwork"] != "invalid_source_network" {
			t.Fatalf("errors = %v", got)
		}
		if strings.Contains(w.Body.String(), "u:p@") {
			t.Fatalf("the problem repeats the credential: %s", w.Body)
		}
	})

	t.Run("R-REPO-033 an unsaved test refuses the marker of a stored value", func(t *testing.T) {
		s := upstreamServer(t, &recordingProber{})
		w := post(s, s.TestUpstream, `{"format":"npmlike","remote":{"authMode":"bearer","password":{"set":true}}}`, "application/json")
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "invalid_secret") {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	})

	t.Run("R-REPO-008 an unknown format, a format without remote and a misspelt member are refused", func(t *testing.T) {
		s := upstreamServer(t, &recordingProber{})
		for body, want := range map[string]int{
			`{"format":"nope","remote":{}}`:                      http.StatusUnprocessableEntity,
			`{"format":"npmlike","remote":{"connectTimeout":5}}`: http.StatusBadRequest,
			`{"format":"npmlike","remote":{},"extra":1}`:         http.StatusBadRequest,
			`{"remote":{}}`: http.StatusUnprocessableEntity,
			`not json`:      http.StatusBadRequest,
			`{"format":"npmlike","remote":{"password":{"value":"a","secretRef":{"file":"t"}}}}`: http.StatusUnprocessableEntity,
		} {
			if w := post(s, s.TestUpstream, body, "application/json"); w.Code != want {
				t.Errorf("%s: %d %s", body, w.Code, w.Body)
			}
		}
	})

	t.Run("R-API-003 the body must be JSON", func(t *testing.T) {
		s := upstreamServer(t, &recordingProber{})
		if w := post(s, s.TestUpstream, `{}`, "text/plain"); w.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("%d", w.Code)
		}
	})

	t.Run("R-REPO-024 a replica without the upstream test answers 503", func(t *testing.T) {
		s := upstreamServer(t, nil)
		w := post(s, s.TestUpstream, `{"format":"npmlike","remote":{}}`, "application/json")
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		w = post(s, func(w http.ResponseWriter, r *http.Request) { s.TestRepositoryUpstream(w, r, "npm-remote") }, ``, "")
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	})

	t.Run("R-REPO-024 without a repository service both operations answer 503", func(t *testing.T) {
		s := &Server{o: Options{}}
		if w := post(s, s.TestUpstream, `{}`, "application/json"); w.Code != http.StatusServiceUnavailable {
			t.Fatalf("%d", w.Code)
		}
		if w := post(s, func(w http.ResponseWriter, r *http.Request) { s.TestRepositoryUpstream(w, r, "npm-remote") }, ``, ""); w.Code != http.StatusServiceUnavailable {
			t.Fatalf("%d", w.Code)
		}
	})
}

func TestUpstreamTestDeclaration(t *testing.T) {
	doc, err := LoadDocument(EmbeddedYAML())
	if err != nil {
		t.Fatal(err)
	}
	t.Run("R-REPO-024 both operations need repositories:write-config", func(t *testing.T) {
		found := 0
		for pattern, op := range doc.Operations() {
			if op.ID != "testRepositoryUpstream" && op.ID != "testUpstream" {
				continue
			}
			found++
			if op.Requirement.Permission != "repositories:write-config" {
				t.Errorf("%s (%s): %+v", op.ID, pattern, op.Requirement)
			}
		}
		if found != 2 {
			t.Fatalf("found %d of the 2 operations", found)
		}
	})
}
