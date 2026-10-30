package npm

import (
	"net/http"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
)

// TestR_NPM_019_WhoamiAndPing: whoami names the principal as R-AUTH-033 renders it and
// both endpoints refuse a caller without an identity with the challenge of R-NPM-004.
func TestR_NPM_019_WhoamiAndPing(t *testing.T) {
	rf := newRepo(t)
	for _, c := range []struct {
		name string
		sub  authz.Subject
		want string
	}{
		{"a user", authz.Subject{Kind: "user", ID: "u1", Name: "alice"}, "alice"},
		{"a service account", authz.Subject{Kind: "service_account", ID: "s1", Name: "builder"}, "sa:builder"},
		{"a CI identity", authz.Subject{Kind: "ci", ID: "c1", Name: "gitlab/web"}, "ci:gitlab/web"},
	} {
		t.Run("whoami of "+c.name, func(t *testing.T) {
			r := rf.doAs(c.sub, http.MethodGet, "-/whoami", "")
			if r.Code != 200 || r.Header().Get("Content-Type") != "application/json" || jsonOf(t, r)["username"] != c.want {
				t.Fatalf("%d %s", r.Code, r.Body.String())
			}
		})
		t.Run("ping of "+c.name, func(t *testing.T) {
			r := rf.doAs(c.sub, http.MethodGet, "-/ping", "")
			if r.Code != 200 || r.Body.String() != "{}" {
				t.Fatalf("%d %q", r.Code, r.Body.String())
			}
		})
	}
	t.Run("the query write=true is accepted and ignored", func(t *testing.T) {
		rq, err := rf.h.NewRequest(t.Context(), http.MethodGet, "-/ping", nil)
		if err != nil {
			t.Fatal(err)
		}
		rq.URL.RawQuery = "write=true"
		rq.Subject = alice()
		if r := rf.h.ServeRequest(rq, false); r.Code != 200 || r.Body.String() != "{}" {
			t.Fatalf("%d %q", r.Code, r.Body.String())
		}
	})
	t.Run("an anonymous caller is 401 with the Basic challenge and never an otp element", func(t *testing.T) {
		for _, p := range []string{"-/whoami", "-/ping"} {
			r := rf.doAs(authz.Subject{Anonymous: true}, http.MethodGet, p, "")
			if r.Code != 401 || errorBodyOf(t, r.ResponseRecorder).Code != "unauthenticated" {
				t.Fatalf("%s: %d %s", p, r.Code, r.Body.String())
			}
			if c := r.Header().Get("WWW-Authenticate"); c != `Basic realm="trove"` {
				t.Fatalf("%s: challenge %q", p, c)
			}
		}
	})
}
