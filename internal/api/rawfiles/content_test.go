package rawfiles

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/files"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/items"
)

const contentDownload = "/api/v1/repositories/generic-local/content/a.bin"

// as returns r carrying the credential method of a principal; the authentication stage sets it in a server.
func as(r *http.Request, method httpserver.AuthMethod) *http.Request {
	p := httpserver.Principal{Kind: httpserver.KindUser, ID: "u1", Name: "alice", Credential: httpserver.Credential{Method: method}}
	return r.WithContext(httpserver.WithPrincipal(r.Context(), p))
}

// denier refuses every decision with the outcome it holds.
type denier struct {
	decider
	outcome authz.Outcome
}

func (d *denier) DecideOn(_ context.Context, idx *authz.Index, _ authz.Subject, _ authz.Coordinate) authz.Decision {
	d.ask(idx)
	return authz.Decision{Outcome: d.outcome}
}

// countingStore counts the bodies a deploy spooled.
type countingStore struct {
	store
	spooled atomic.Int32
}

func (s *countingStore) Spool(ctx context.Context, body io.Reader, o items.IngestOptions) (*items.Spooled, error) {
	s.spooled.Add(1)
	if _, err := io.Copy(io.Discard, body); err != nil {
		return nil, err
	}
	return &blob.Spooled{}, nil
}

func handlerOver(t *testing.T, d authz.Decider, st files.Items) *Handler {
	t.Helper()
	svc, err := files.New(files.Options{Items: st, Repos: repos{}})
	if err != nil {
		t.Fatal(err)
	}
	return New(Options{Service: svc, Decider: d})
}

func notFoundRoute() error {
	return errs.E(errs.NotFound, errs.CodeNotFound, "No route matches the request path.")
}

func jsonDecode(b []byte, v any) error { return json.Unmarshal(b, v) }

func serveContent(t *testing.T, h *Handler, maxUpload int64, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	router := httpserver.NewClientRouter()
	h.Content(maxUpload).Register(router)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, r)
	return rec
}

func TestContentRoutes(t *testing.T) {
	var sum blob.Digest
	sum[0] = 1
	file := items.Item{ID: 3, RepoID: 5, Repository: "generic-local", Path: "a.bin", Kind: items.KindFile, Size: 3, Digest: sum, SHA512: make([]byte, 64)}

	t.Run("R-API-025 Q-UI-106 a download for the session answers what /files/ answers", func(t *testing.T) {
		d := &decider{}
		h := handler(t, d, &store{file: file, body: []byte("abc")})
		viaFiles := httptest.NewRecorder()
		h.ServeHTTP(viaFiles, httptest.NewRequest(http.MethodGet, "/files/generic-local/a.bin", nil))
		viaContent := serveContent(t, h, 0, as(httptest.NewRequest(http.MethodGet, contentDownload, nil), httpserver.MethodSession))
		if viaContent.Code != http.StatusOK || viaContent.Body.String() != "abc" {
			t.Fatalf("status %d, body %q", viaContent.Code, viaContent.Body.String())
		}
		for _, k := range []string{"Etag", "Content-Length", "Accept-Ranges", "Content-Disposition", "X-Content-Type-Options", "Repr-Digest"} {
			if viaContent.Header().Get(k) != viaFiles.Header().Get(k) {
				t.Errorf("%s: %q on the content route, %q on /files/", k, viaContent.Header().Get(k), viaFiles.Header().Get(k))
			}
		}
		if got := viaContent.Header().Get("Cross-Origin-Resource-Policy"); got != "same-origin" {
			t.Errorf("a session download has the resource policy %q, want same-origin (Q-ARCH-86)", got)
		}
	})

	t.Run("R-API-025 Q-UI-106 a download decides on one index with the repository key, like /files/", func(t *testing.T) {
		d := &decider{}
		serveContent(t, handler(t, d, &store{file: file, body: []byte("abc")}), 0,
			as(httptest.NewRequest(http.MethodGet, contentDownload, nil), httpserver.MethodSession))
		if d.begins != 1 || len(d.keys[0]) != 1 || d.keys[0][0] != "generic-local" || len(d.asked) == 0 {
			t.Fatalf("begins %d, keys %v, %d asked", d.begins, d.keys, len(d.asked))
		}
		for _, idx := range d.asked {
			if idx != d.issued[0] {
				t.Error("a decision was asked on another index than the request began with")
			}
		}
	})

	t.Run("R-API-025 Q-UI-106 a range and HEAD work as on /files/", func(t *testing.T) {
		h := handler(t, &decider{}, &store{file: file, body: []byte("abc")})
		req := as(httptest.NewRequest(http.MethodGet, contentDownload, nil), httpserver.MethodSession)
		req.Header.Set("Range", "bytes=1-2")
		if rec := serveContent(t, h, 0, req); rec.Code != http.StatusPartialContent || rec.Header().Get("Content-Range") != "bytes 1-2/3" {
			t.Errorf("range: %d %q", rec.Code, rec.Header())
		}
		head := serveContent(t, h, 0, as(httptest.NewRequest(http.MethodHead, contentDownload, nil), httpserver.MethodSession))
		if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Etag") == "" {
			t.Errorf("head: %d %q %v", head.Code, head.Body.String(), head.Header())
		}
	})

	t.Run("R-API-025 Q-UI-106 a path that is not stored answers 404", func(t *testing.T) {
		h := handler(t, &decider{}, &store{})
		rec := serveContent(t, h, 0, as(httptest.NewRequest(http.MethodGet, "/api/v1/repositories/generic-local/content/missing.bin", nil), httpserver.MethodSession))
		want := httptest.NewRecorder()
		h.ServeHTTP(want, httptest.NewRequest(http.MethodGet, "/files/generic-local/missing.bin", nil))
		if rec.Code != http.StatusNotFound || rec.Code != want.Code {
			t.Errorf("content %d, files %d", rec.Code, want.Code)
		}
	})

	t.Run("R-API-025 Q-UI-106 every request the session did not authenticate is the 404 of an unknown path, before any decision", func(t *testing.T) {
		unknown := httptest.NewRecorder()
		httpserver.Problem(unknown, httptest.NewRequest(http.MethodGet, contentDownload, nil), notFoundRoute())
		for name, method := range map[string]httpserver.AuthMethod{
			"a bearer token": httpserver.MethodBearer, "a basic credential": httpserver.MethodBasic, "an API key": httpserver.MethodAPIKey, "no credential": httpserver.MethodNone,
		} {
			d := &decider{}
			h := handler(t, d, &store{file: file, body: []byte("abc")})
			for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodPut} {
				rec := serveContent(t, h, 0, as(httptest.NewRequest(m, contentDownload, strings.NewReader("x")), method))
				if rec.Code != http.StatusNotFound {
					t.Errorf("%s with %s: %d %s", m, name, rec.Code, rec.Body.String())
				}
				if m == http.MethodGet && !strings.Contains(rec.Body.String(), `"not_found"`) {
					t.Errorf("%s: body %s", name, rec.Body.String())
				}
			}
			if d.begins != 0 {
				t.Errorf("%s: a request that is not the session's began %d indexes", name, d.begins)
			}
		}
		anonymous := httptest.NewRequest(http.MethodGet, contentDownload, nil)
		if rec := serveContent(t, handler(t, &decider{}, &store{file: file}), 0, anonymous); rec.Code != http.StatusNotFound {
			t.Errorf("a request without a principal: %d", rec.Code)
		}
	})

	t.Run("R-API-025 R-AUTHZ-016 a caller without read gets the answer /files/ gives", func(t *testing.T) {
		for _, outcome := range []authz.Outcome{authz.DenyHidden, authz.DenyVisible} {
			d := &denier{outcome: outcome}
			h := handlerOver(t, d, &store{file: file, body: []byte("abc")})
			viaFiles := httptest.NewRecorder()
			h.ServeHTTP(viaFiles, httptest.NewRequest(http.MethodGet, "/files/generic-local/a.bin", nil))
			viaContent := serveContent(t, h, 0, as(httptest.NewRequest(http.MethodGet, contentDownload, nil), httpserver.MethodSession))
			if viaContent.Code != viaFiles.Code || viaContent.Code < 400 || strings.Contains(viaContent.Body.String(), "abc") {
				t.Errorf("outcome %v: content %d %q, files %d", outcome, viaContent.Code, viaContent.Body.String(), viaFiles.Code)
			}
			if viaContent.Code == http.StatusNotFound && !strings.Contains(viaContent.Body.String(), `"not_found"`) && !strings.Contains(viaContent.Body.String(), "repository") {
				t.Logf("hidden outcome body: %s", viaContent.Body.String())
			}
		}
	})

	t.Run("R-API-025 Q-UI-106 an upload within the limit is stored", func(t *testing.T) {
		h := handlerOver(t, &decider{}, &countingStore{})
		rec := serveContent(t, h, 4, as(httptest.NewRequest(http.MethodPut, "/api/v1/repositories/generic-local/content/new/b.bin", strings.NewReader("xyz")), httpserver.MethodSession))
		if rec.Code != http.StatusCreated {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		var d struct {
			ContentURL string `json:"contentUrl"`
		}
		if err := jsonDecode(rec.Body.Bytes(), &d); err != nil || d.ContentURL != "/api/v1/repositories/generic-local/content/new/b.bin" {
			t.Errorf("the answer's contentUrl = %q (%v)", d.ContentURL, err)
		}
	})

	t.Run("R-API-025 Q-UI-106 an upload above ui.deploy.max_upload_bytes answers 413, a declared length before anything is spooled", func(t *testing.T) {
		st := &countingStore{}
		h := handlerOver(t, &decider{}, st)
		rec := serveContent(t, h, 4, as(httptest.NewRequest(http.MethodPut, "/api/v1/repositories/generic-local/content/big.bin", strings.NewReader("12345")), httpserver.MethodSession))
		if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "payload_too_large") {
			t.Fatalf("declared: %d %s", rec.Code, rec.Body.String())
		}
		// A body of unknown length is cut when it crosses the limit.
		req := httptest.NewRequest(http.MethodPut, "/api/v1/repositories/generic-local/content/big.bin", io.NopCloser(strings.NewReader("12345")))
		req.ContentLength = -1
		if rec := serveContent(t, h, 4, as(req, httpserver.MethodSession)); rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("chunked: %d %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("R-API-025 Q-UI-106 a limit of 0 is no limit, and /files/ has none of its own", func(t *testing.T) {
		h := handlerOver(t, &decider{}, &countingStore{})
		body := strings.Repeat("x", 1<<16)
		if rec := serveContent(t, h, 0, as(httptest.NewRequest(http.MethodPut, "/api/v1/repositories/generic-local/content/big.bin", strings.NewReader(body)), httpserver.MethodSession)); rec.Code != http.StatusCreated {
			t.Errorf("content: %d %s", rec.Code, rec.Body.String())
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/files/generic-local/big.bin", strings.NewReader(body)))
		if rec.Code != http.StatusCreated {
			t.Errorf("files: %d %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("R-API-025 Q-UI-106 a caller who may not deploy is refused before the body is read, not told 413", func(t *testing.T) {
		st := &countingStore{}
		h := handlerOver(t, &denier{outcome: authz.DenyVisible}, st)
		rec := serveContent(t, h, 4, as(httptest.NewRequest(http.MethodPut, "/api/v1/repositories/generic-local/content/big.bin", strings.NewReader("123456789")), httpserver.MethodSession))
		if rec.Code != http.StatusForbidden || st.spooled.Load() != 0 {
			t.Errorf("status %d, %d bodies spooled: %s", rec.Code, st.spooled.Load(), rec.Body.String())
		}
	})

	t.Run("R-API-025 Q-API-195 a path ServeMux would clean is redirected, not served", func(t *testing.T) {
		h := handler(t, &decider{}, &store{file: file, body: []byte("abc")})
		rec := serveContent(t, h, 0, as(httptest.NewRequest(http.MethodGet, "/api/v1/repositories/generic-local/content/x/../a.bin", nil), httpserver.MethodSession))
		if rec.Code/100 != 3 || rec.Header().Get("Location") != "/api/v1/repositories/generic-local/content/a.bin" || rec.Body.String() == "abc" {
			t.Errorf("status %d, location %q", rec.Code, rec.Header().Get("Location"))
		}
	})

	t.Run("R-API-025 Q-UI-106 DELETE is not a content route", func(t *testing.T) {
		rec := serveContent(t, handler(t, &decider{}, &store{file: file}), 0, as(httptest.NewRequest(http.MethodDelete, contentDownload, nil), httpserver.MethodSession))
		if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
			t.Errorf("status %d", rec.Code)
		}
	})
}

func TestSplitContentPath(t *testing.T) {
	for _, tc := range []struct {
		in        string
		key, rest string
		ok        bool
	}{
		{"/api/v1/repositories/r/content/a/b.bin", "r", "a/b.bin", true},
		{"/api/v1/repositories/r/content/", "r", "", true},
		{"/api/v1/repositories/my%2Drepo/content/a%2Fb/x", "my-repo", "a%2Fb/x", true},
		{"/api/v1/repositories/r/content/content/x", "r", "content/x", true},
		{"/api/v1/repositories//content/x", "", "", false},
		{"/api/v1/repositories/r/items", "", "", false},
		{"/api/v1/repositories/a/b/content/x", "", "", false},
		{"/files/r/x", "", "", false},
	} {
		key, rest, ok := splitContentPath(tc.in)
		if key != tc.key || rest != tc.rest || ok != tc.ok {
			t.Errorf("splitContentPath(%q) = %q, %q, %v", tc.in, key, rest, ok)
		}
	}
}

func TestContentRoutesAgreeWithTheDocument(t *testing.T) {
	doc, err := v1.LoadDocument(v1.EmbeddedYAML())
	if err != nil {
		t.Fatal(err)
	}
	t.Run("R-API-025 Q-UI-106 the routes of the content family and the document agree in both directions", func(t *testing.T) {
		router := httpserver.NewClientRouter()
		New(Options{}).Content(0).Register(router)
		patterns := router.Patterns()
		for p := range doc.Operations() { // the management API is registered elsewhere
			patterns = append(patterns, p)
		}
		for p := range doc.FileOperations() {
			patterns = append(patterns, strings.Replace(p, "{path}", "{path...}", 1))
		}
		if err := doc.CheckRoutes(patterns); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-API-025 the content operations declare the repository action of the /files/ operation with the same method", func(t *testing.T) {
		want := map[string]string{"GET": "read", "HEAD": "read", "PUT": "deploy"}
		got := doc.ContentOperations()
		if len(got) != len(want) {
			t.Fatalf("operations = %v", got)
		}
		for pattern, op := range got {
			method, path, _ := strings.Cut(pattern, " ")
			if path != "/api/v1/repositories/{repoKey}/content/{path}" || string(op.Requirement.Action) != want[method] ||
				op.Requirement.Repository != "path.repoKey" || op.Requirement.Path != "path.path" {
				t.Errorf("%s: %+v", pattern, op.Requirement)
			}
			files := doc.FileOperations()[method+" /files/{repoKey}/{path}"]
			if files.Requirement != op.Requirement {
				t.Errorf("%s declares %+v, /files/ declares %+v", pattern, op.Requirement, files.Requirement)
			}
		}
	})
	t.Run("R-API-002 a content route that the document lacks is named", func(t *testing.T) {
		router := httpserver.NewClientRouter()
		router.HandleFunc("DELETE /api/v1/repositories/{repoKey}/content/{path...}", func(http.ResponseWriter, *http.Request) {})
		patterns := router.Patterns()
		for p := range doc.Operations() {
			patterns = append(patterns, p)
		}
		if err := doc.CheckRoutes(patterns); err == nil || !strings.Contains(err.Error(), "DELETE /api/v1/repositories/{repoKey}/content/{path...}") {
			t.Fatalf("err = %v", err)
		}
	})
}
