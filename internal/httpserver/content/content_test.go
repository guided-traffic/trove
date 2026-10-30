package content

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWrap(t *testing.T) {
	sandbox := "sandbox; default-src 'none'"
	cases := []struct {
		name       string
		kind       Kind
		sameOrigin bool
		csp        string
		corp       string
		disp       string
	}{
		{"R-ARCH-007 a file carries the four headers", File, false, sandbox, "same-site", "attachment; filename*=UTF-8''x%20y.tgz"},
		{"R-ARCH-007 Q-ARCH-86 a session request gets the same-origin policy", File, true, sandbox, "same-origin", "attachment; filename*=UTF-8''x%20y.tgz"},
		{"R-ARCH-007 R-STOR-017 the directory index has no sandbox and no disposition", Index, false, "default-src 'none'", "same-site", ""},
		{"R-ARCH-007 a package document has the sandbox and no disposition", Document, false, sandbox, "same-site", ""},
		{"R-ARCH-007 a package document on a session request", Document, true, sandbox, "same-origin", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			w := Wrap(rec, c.kind, "x y.tgz", c.sameOrigin)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
			h := rec.Header()
			if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Content-Security-Policy") != c.csp ||
				h.Get("Cross-Origin-Resource-Policy") != c.corp || h.Get("Content-Disposition") != c.disp {
				t.Fatalf("headers = %v", h)
			}
			if h.Get("Content-Type") != "application/json" {
				t.Fatal("the handler may set the content type")
			}
		})
	}
}

// TestWrapHoldsAgainstTheHandler is the acceptance criterion of R-ARCH-007: a
// handler that deletes or weakens a header before it writes still sends it.
func TestWrapHoldsAgainstTheHandler(t *testing.T) {
	for _, kind := range []Kind{File, Document, Index} {
		want := Headers(kind, "a.bin", false)
		t.Run("R-ARCH-007 the handler cannot remove a header", func(t *testing.T) {
			rec := httptest.NewRecorder()
			w := Wrap(rec, kind, "a.bin", false)
			for k := range want {
				w.Header().Del(k)
			}
			w.Header().Set("Content-Security-Policy", "default-src *")
			_, _ = w.Write([]byte("x"))
			for k := range want {
				if rec.Header().Get(k) != want.Get(k) {
					t.Errorf("%s = %q, want %q", k, rec.Header().Get(k), want.Get(k))
				}
			}
		})
	}
	t.Run("R-ARCH-007 a 1xx status is not the response", func(t *testing.T) {
		rec := httptest.NewRecorder()
		w := Wrap(rec, File, "a", false)
		w.Header().Del("X-Content-Type-Options")
		w.WriteHeader(http.StatusContinue)
		if w.(*writer).wrote {
			t.Fatal("a 100 counted as the response")
		}
	})
	t.Run("Flush enforces and passes through", func(t *testing.T) {
		rec := httptest.NewRecorder()
		w := Wrap(rec, File, "a", false)
		w.Header().Del("Content-Security-Policy")
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Fatal(err)
		}
		if rec.Header().Get("Content-Security-Policy") == "" {
			t.Fatal("Flush sent the response without the policy")
		}
	})
}

func TestEncodeFileName(t *testing.T) {
	for in, want := range map[string]string{
		"a.bin":      "a.bin",
		"a b":        "a%20b",
		"é":          "%C3%A9",
		"x'y\"z;":    "x%27y%22z%3B",
		"100%":       "100%25",
		"a/b":        "a%2Fb",
		"ok!#$&+-.^": "ok!#$&+-.^",
	} {
		if got := EncodeFileName(in); got != want {
			t.Errorf("EncodeFileName(%q) = %q, want %q", in, got, want)
		}
	}
	if strings.Contains(Headers(File, "a\r\nX: y", false).Get("Content-Disposition"), "\n") {
		t.Fatal("a file name injected a header line")
	}
}
