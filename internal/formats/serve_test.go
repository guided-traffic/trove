package formats

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/repository"
)

// shaped is a format whose error shape is visible in a test.
type shaped struct{ fake }

func (shaped) WriteError(w http.ResponseWriter, _ *http.Request, err error) {
	w.Header().Set("X-Shape", "native")
	status := http.StatusInternalServerError
	if e, ok := errs.As(err); ok {
		status = e.Status()
		for k, v := range e.Headers {
			w.Header().Set(k, v)
		}
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, "native error")
}

func serve(t *testing.T, kind RouteKind, h HandlerFunc, sameOrigin bool) *httptest.ResponseRecorder {
	t.Helper()
	return serveOn(t, EndpointFacts{}, kind, h, sameOrigin)
}

func serveOn(t *testing.T, ep EndpointFacts, kind RouteKind, h HandlerFunc, sameOrigin bool) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	rq := &Request{
		Request:  httptest.NewRequest(http.MethodGet, "/api/probe/key/left-pad/-/left-pad-1.0.0.tgz", nil),
		Repo:     &repository.Repository{Key: "key", Format: "probe"},
		Route:    &Route{Name: "probe.get", Kind: kind, Handler: h},
		Path:     Path{Path: "left-pad/-/left-pad-1.0.0.tgz"},
		Log:      slog.New(slog.DiscardHandler),
		Endpoint: ep,
	}
	ServeRoute(shaped{fake{d: valid("probe")}}, rec, rq, sameOrigin)
	return rec
}

func write(body string) HandlerFunc {
	return func(_ context.Context, _ *Request, w http.ResponseWriter) error {
		_, err := io.WriteString(w, body)
		return err
	}
}

// TestR_ARCH_007_ServeRouteWrapsTheContentRoutes: a route of kind Download, Metadata
// or Computed answers with the content headers, whatever the handler does to them;
// a route of another kind is not wrapped.
func TestR_ARCH_007_ServeRouteWrapsTheContentRoutes(t *testing.T) {
	const csp = "sandbox; default-src 'none'"
	for _, k := range []RouteKind{Download, Metadata, Computed} {
		t.Run("R-ARCH-007 a "+k.String()+" route carries the headers", func(t *testing.T) {
			rec := serve(t, k, write("x"), false)
			h := rec.Header()
			if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Content-Security-Policy") != csp || h.Get("Cross-Origin-Resource-Policy") != "same-site" {
				t.Fatalf("headers = %v", h)
			}
			if got := h.Get("Content-Disposition") != ""; got != (k == Download) {
				t.Fatalf("Content-Disposition = %q on a %s route", h.Get("Content-Disposition"), k)
			}
			if k == Download && !strings.Contains(h.Get("Content-Disposition"), "left-pad-1.0.0.tgz") {
				t.Fatalf("the file name is the last segment of the path: %q", h.Get("Content-Disposition"))
			}
		})
	}
	t.Run("R-ARCH-007 a handler that removes the policy before it writes still sends it", func(t *testing.T) {
		rec := serve(t, Download, func(_ context.Context, _ *Request, w http.ResponseWriter) error {
			w.Header().Del("Content-Security-Policy")
			w.Header().Del("X-Content-Type-Options")
			_, err := io.WriteString(w, "x")
			return err
		}, false)
		if rec.Header().Get("Content-Security-Policy") != csp || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("headers = %v", rec.Header())
		}
	})
	t.Run("Q-ARCH-86 a request the session cookie authenticated gets the same-origin policy", func(t *testing.T) {
		if got := serve(t, Metadata, write("x"), true).Header().Get("Cross-Origin-Resource-Policy"); got != "same-origin" {
			t.Fatalf("policy = %q", got)
		}
	})
	for _, k := range []RouteKind{Upload, Delete, Admin, Volatile} {
		t.Run("R-ARCH-007 a "+k.String()+" route is not wrapped", func(t *testing.T) {
			if got := serve(t, k, write("x"), false).Header().Get("Content-Security-Policy"); got != "" {
				t.Fatalf("a %s route got the content headers: %q", k, got)
			}
		})
	}
}

// TestR_ARCH_023_ServeRouteRendersErrorsInTheFormatsShape: an error before the status
// line is the format's own shape and no attachment; one after it aborts the
// connection; a panic is a 500 in the format's shape (R-ARCH-028).
func TestR_ARCH_023_ServeRouteRendersErrorsInTheFormatsShape(t *testing.T) {
	t.Run("an error before the status line", func(t *testing.T) {
		rec := serve(t, Download, func(context.Context, *Request, http.ResponseWriter) error {
			return errs.E(errs.NotFound, errs.CodeNotFound, "no")
		}, false)
		if rec.Code != 404 || rec.Header().Get("X-Shape") != "native" {
			t.Fatalf("answer %d %v", rec.Code, rec.Header())
		}
		if rec.Header().Get("Content-Disposition") != "" {
			t.Fatal("an error response is no attachment")
		}
	})
	t.Run("R-ARCH-028 a panic is recovered and answered as 500 in the format's shape", func(t *testing.T) {
		rec := serve(t, Metadata, func(context.Context, *Request, http.ResponseWriter) error { panic("boom") }, false)
		if rec.Code != 500 || rec.Header().Get("X-Shape") != "native" {
			t.Fatalf("answer %d %v", rec.Code, rec.Header())
		}
	})
	t.Run("an error after the status line aborts the connection", func(t *testing.T) {
		defer func() {
			if err, _ := recover().(error); !errors.Is(err, http.ErrAbortHandler) {
				t.Fatalf("recovered %v, want http.ErrAbortHandler", err)
			}
		}()
		serve(t, Download, func(_ context.Context, _ *Request, w http.ResponseWriter) error {
			_, _ = io.WriteString(w, "partial")
			return errors.New("the object store failed")
		}, false)
		t.Fatal("no abort")
	})
	t.Run("a panic after the status line aborts the connection", func(t *testing.T) {
		defer func() {
			if err, _ := recover().(error); !errors.Is(err, http.ErrAbortHandler) {
				t.Fatalf("recovered %v, want http.ErrAbortHandler", err)
			}
		}()
		serve(t, Download, func(_ context.Context, _ *Request, w http.ResponseWriter) error {
			_, _ = io.WriteString(w, "partial")
			panic("boom")
		}, false)
	})
	t.Run("http.ErrAbortHandler passes through", func(t *testing.T) {
		defer func() {
			if err, _ := recover().(error); !errors.Is(err, http.ErrAbortHandler) {
				t.Fatalf("recovered %v", err)
			}
		}()
		serve(t, Download, func(context.Context, *Request, http.ResponseWriter) error { panic(http.ErrAbortHandler) }, false)
	})
}

func TestTemplate(t *testing.T) {
	m := Template("/{package}/-/{file}")
	if p, ok := m("left-pad/-/left-pad-1.0.0.tgz"); !ok || p["package"] != "left-pad" || p["file"] != "left-pad-1.0.0.tgz" {
		t.Fatalf("match = %v %v", p, ok)
	}
	for _, no := range []string{"", "left-pad", "left-pad/x/left-pad-1.0.0.tgz", "left-pad/-/", "left-pad/-/a/b", "/-/a"} {
		if _, ok := m(no); ok {
			t.Errorf("%q matched", no)
		}
	}
	if _, ok := Template("/{package}")("%zz"); ok {
		t.Error("a malformed escape matched")
	}
	t.Run("a template that mixes text and a placeholder is a programming error", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("no panic")
			}
		}()
		Template("/{name}.json")
	})
}

// TestR_EP_018_NoCacheStatusOnAnEndpointWithRules: on an endpoint with exposure rules no
// answer of a format route carries Cache-Status, set by the handler, by an engine before
// a body or in an error's headers; on any other endpoint it is kept (R-REPO-047, Q-EP-60).
func TestR_EP_018_NoCacheStatusOnAnEndpointWithRules(t *testing.T) {
	const status = "trove; fwd=miss; stored"
	routes := map[string]HandlerFunc{
		"a document": func(_ context.Context, _ *Request, w http.ResponseWriter) error {
			w.Header().Set("Cache-Status", status)
			_, err := io.WriteString(w, "{}")
			return err
		},
		"a 304": func(_ context.Context, _ *Request, w http.ResponseWriter) error {
			w.Header().Set("Cache-Status", status)
			w.WriteHeader(http.StatusNotModified)
			return nil
		},
		"an error": func(context.Context, *Request, http.ResponseWriter) error {
			return errs.E(errs.NotFound, errs.CodeNotFound, "missing", errs.WithHeader("Cache-Status", status))
		},
	}
	for name, h := range routes {
		for _, k := range []RouteKind{Download, Metadata, Admin} {
			t.Run("R-EP-018 "+name+" of a "+k.String()+" route on an endpoint with rules", func(t *testing.T) {
				if got := serveOn(t, EndpointFacts{Name: "customers", Rules: true}, k, h, false).Header().Get("Cache-Status"); got != "" {
					t.Fatalf("Cache-Status = %q", got)
				}
			})
			t.Run("R-REPO-047 "+name+" of a "+k.String()+" route on an endpoint without rules", func(t *testing.T) {
				if got := serveOn(t, EndpointFacts{Name: "default"}, k, h, false).Header().Get("Cache-Status"); got != status {
					t.Fatalf("Cache-Status = %q", got)
				}
			})
		}
	}
}
