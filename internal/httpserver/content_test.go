package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestContentWriter(t *testing.T) {
	file := map[string]string{
		"X-Content-Type-Options":       "nosniff",
		"Content-Security-Policy":      "sandbox; default-src 'none'",
		"Cross-Origin-Resource-Policy": "same-site",
	}
	check := func(t *testing.T, h http.Header, want map[string]string) {
		t.Helper()
		for k, v := range want {
			if got := h.Get(k); got != v {
				t.Errorf("%s = %q, want %q", k, got, v)
			}
		}
	}

	t.Run("R-ARCH-007 R-STOR-012 stored content carries the four headers, the file name encoded", func(t *testing.T) {
		rec := httptest.NewRecorder()
		w := ContentWriter(rec, ContentFile, "evil β.svg")
		w.Header().Set("Content-Type", "image/svg+xml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<svg onload=alert(1)/>"))
		check(t, rec.Header(), file)
		if got := rec.Header().Get("Content-Disposition"); got != "attachment; filename*=UTF-8''evil%20%CE%B2.svg" {
			t.Errorf("Content-Disposition = %q", got)
		}
		if got := rec.Header().Get("Content-Type"); got != "image/svg+xml" {
			t.Errorf("a handler can set Content-Type: %q", got)
		}
	})

	t.Run("R-ARCH-007 a handler that deletes or weakens the headers still sends them", func(t *testing.T) {
		for _, name := range []string{"WriteHeader", "Write"} {
			rec := httptest.NewRecorder()
			w := ContentWriter(rec, ContentFile, "a.bin")
			w.Header().Del("Content-Security-Policy")
			w.Header().Del("Content-Disposition")
			w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
			w.Header().Set("X-Content-Type-Options", "")
			if name == "WriteHeader" {
				w.WriteHeader(http.StatusPartialContent)
			} else {
				_, _ = w.Write([]byte("x"))
			}
			check(t, rec.Header(), file)
			if rec.Header().Get("Content-Disposition") == "" {
				t.Errorf("%s: Content-Disposition was removed", name)
			}
		}
	})

	t.Run("R-STOR-017 the directory index has the header set without sandbox and without a disposition", func(t *testing.T) {
		rec := httptest.NewRecorder()
		w := ContentWriter(rec, ContentIndex, "")
		w.Header().Set("Content-Disposition", "attachment")
		w.WriteHeader(http.StatusOK)
		check(t, rec.Header(), map[string]string{
			"X-Content-Type-Options":       "nosniff",
			"Content-Security-Policy":      "default-src 'none'",
			"Cross-Origin-Resource-Policy": "same-site",
		})
		if got := rec.Header().Get("Content-Disposition"); got != "" {
			t.Errorf("Content-Disposition = %q on a rendered page", got)
		}
	})

	t.Run("R-ARCH-007 a 1xx status does not count as the response", func(t *testing.T) {
		rec := httptest.NewRecorder()
		w := ContentWriter(rec, ContentFile, "a")
		w.Header().Del("Content-Security-Policy")
		w.WriteHeader(http.StatusEarlyHints)
		w.WriteHeader(http.StatusOK)
		check(t, rec.Header(), file)
	})

	t.Run("R-ARCH-005 the wrapper keeps http.ResponseController working", func(t *testing.T) {
		rec := httptest.NewRecorder()
		w := ContentWriter(rec, ContentFile, "a")
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestHandleUncleaned(t *testing.T) {
	newRouter := func() (*Router, *[]string) {
		var seen []string
		r := NewClientRouter()
		h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			seen = append(seen, req.URL.EscapedPath())
			w.WriteHeader(http.StatusTeapot)
		})
		r.Handle("GET /files/{repoKey}/{path...}", h)
		r.HandleUncleaned("/files/", h)
		return r, &seen
	}
	serve := func(r *Router, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.URL.Path, req.URL.RawPath = path, ""
		r.ServeHTTP(rec, req)
		return rec
	}

	t.Run("R-GENERIC-002 a path with an empty or dot segment reaches the handler instead of a redirect", func(t *testing.T) {
		for _, p := range []string{"/files/r/a//b", "/files/r/a/./b", "/files/r/a/../b", "/files//a"} {
			r, seen := newRouter()
			if rec := serve(r, p); rec.Code != http.StatusTeapot || len(*seen) != 1 {
				t.Errorf("%s: %d, %d calls", p, rec.Code, len(*seen))
			}
		}
	})

	t.Run("R-ARCH-005 a clean path, with or without a trailing slash, takes the pattern", func(t *testing.T) {
		for _, p := range []string{"/files/r/a/b", "/files/r/a/b/", "/files/r/", "/files/r/%2e%2e/x"} {
			r, seen := newRouter()
			if rec := serve(r, p); rec.Code != http.StatusTeapot || len(*seen) != 1 {
				t.Errorf("%s: %d, %d calls", p, rec.Code, len(*seen))
			}
		}
	})

	t.Run("R-ARCH-005 a path below another prefix is still redirected by ServeMux", func(t *testing.T) {
		r, _ := newRouter()
		r.Handle("GET /api/v1/x/{id}", http.NotFoundHandler())
		if rec := serve(r, "/api/v1/x//y"); rec.Code/100 != 3 {
			t.Errorf("status %d", rec.Code)
		}
	})

	t.Run("R-EP-005 the prefix is dropped on an endpoint whose surface leaves the family out", func(t *testing.T) {
		r := NewEndpointRouter(endpointFixture(t, twoEndpoints(), "customers"))
		teapot := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
		r.Handle("GET /files/{repoKey}/{path...}", teapot)
		r.HandleUncleaned("/files/", teapot)
		if len(r.Dropped()) != 1 {
			t.Fatalf("dropped = %v", r.Dropped())
		}
		if rec := serve(r, "/files/r/a//b"); rec.Code == http.StatusTeapot {
			t.Error("the files handler answered on an endpoint without the files surface")
		}
	})

	t.Run("R-ARCH-005 the label of an uncleaned request is the prefix, never the path", func(t *testing.T) {
		r, _ := newRouter()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.URL.Path = "/files/r/a//b"
		if got := r.Pattern(req); got != "/files/" || strings.Contains(got, "a//b") {
			t.Errorf("Pattern = %q", got)
		}
	})
}
