package npm

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/repository"
)

// fakeHost is a Host that implements what the handlers under test reach and panics
// on anything else (the nil embedded interface).
type fakeHost struct {
	formats.Host
	authorize func(*formats.Request, formats.Target) error
	data      any
}

func (h *fakeHost) Authorize(_ context.Context, rq *formats.Request, t formats.Target) error {
	if h.authorize != nil {
		return h.authorize(rq, t)
	}
	return nil
}

func (h *fakeHost) FormatData(formats.ID) any { return h.data }

// request routes a request through the format's Match, as the dispatcher does, and
// returns the formats.Request a handler gets.
func request(t *testing.T, f *Format, host formats.Host, method, escaped string, body string, hdr ...string) *formats.Request {
	t.Helper()
	route, params, err := f.Match(method, escaped)
	if err != nil {
		t.Fatalf("Match(%s %q): %v", method, escaped, err)
	}
	r := httptest.NewRequest(method, "http://trove.test/api/npm/npm-local/"+escaped, strings.NewReader(body))
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	return &formats.Request{
		Request: r, Repo: &repository.Repository{ID: "7", Key: "npm-local", Class: formats.Local, Format: "npm"},
		Route: route, Params: params, Escaped: escaped, Host: host,
	}
}

// serve runs the route as the dispatcher does (formats.ServeRoute: content headers,
// recovery, WriteError) and returns the recorded response.
func serve(t *testing.T, f *Format, rq *formats.Request) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	formats.ServeRoute(f, w, rq, false)
	return w
}

// errorBodyOf decodes the native error body and fails unless all three members are
// present and non-empty (R-NPM-005).
func errorBodyOf(t *testing.T, w *httptest.ResponseRecorder) errorBody {
	t.Helper()
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type %q, want application/json (status %d, body %q)", ct, w.Code, w.Body.String())
	}
	var raw map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("error body %q: %v", w.Body.String(), err)
	}
	for _, k := range []string{"error", "message", "code"} {
		if raw[k] == "" {
			t.Fatalf("error body %q lacks %q", w.Body.String(), k)
		}
	}
	return errorBody{Error: raw["error"], Message: raw["message"], Code: raw["code"]}
}

func httpRecorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }

func defaultConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{NPM: defaultReplica().cfg}
}
