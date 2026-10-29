package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestR_ARCH_007_SessionContentIsSameOrigin(t *testing.T) {
	corp := func(p *Principal, kind ContentKind) string {
		r := httptest.NewRequest(http.MethodGet, "/files/generic-local/a.png", nil)
		if p != nil {
			r = r.WithContext(WithPrincipal(r.Context(), *p))
		}
		rec := httptest.NewRecorder()
		w := ContentWriterFor(rec, r, kind, "a.png")
		w.Header().Del("Cross-Origin-Resource-Policy")
		w.WriteHeader(http.StatusOK)
		return rec.Header().Get("Cross-Origin-Resource-Policy")
	}
	session := Principal{ID: "u", Name: "alice", Kind: KindUser, Credential: Credential{Method: MethodSession}}
	bearer := Principal{ID: "u", Name: "alice", Kind: KindUser, Credential: Credential{Method: MethodBearer}}
	for _, c := range []struct {
		name string
		p    *Principal
		kind ContentKind
		want string
	}{
		{"Q-ARCH-86 a file read with the session cookie is same-origin", &session, ContentFile, "same-origin"},
		{"Q-ARCH-86 a directory index read with the session cookie is same-origin", &session, ContentIndex, "same-origin"},
		{"R-ARCH-007 a file read with a token stays same-site", &bearer, ContentFile, "same-site"},
		{"R-ARCH-007 an anonymous read stays same-site", nil, ContentFile, "same-site"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := corp(c.p, c.kind); got != c.want {
				t.Fatalf("Cross-Origin-Resource-Policy %q, want %q", got, c.want)
			}
		})
	}
	t.Run("R-ARCH-007 the form without a request keeps same-site", func(t *testing.T) {
		rec := httptest.NewRecorder()
		ContentWriter(rec, ContentFile, "a").WriteHeader(http.StatusOK)
		if got := rec.Header().Get("Cross-Origin-Resource-Policy"); got != "same-site" {
			t.Fatalf("%q", got)
		}
	})
}
