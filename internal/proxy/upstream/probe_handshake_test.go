package upstream

import (
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/guided-traffic/trove/internal/observability"
	"github.com/guided-traffic/trove/internal/repository"
)

// tokenService is a test registry's token service: it answers a login of the right Basic credential with a token, a wrong one
// with 401, and keeps every request it got.
type tokenService struct {
	srv    *httptest.Server
	mu     sync.Mutex
	got    []*http.Request
	status int // when not 0, every answer
}

func (s *tokenService) requests() []*http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*http.Request(nil), s.got...)
}

// handshakeRig is a registry on 127.0.0.1 whose challenge names a token service reached as example.com, a second host (the
// test certificate is valid for that name), with the right credential ci / right.
type handshakeRig struct {
	f      *fixture
	ts     *tokenService
	reg    *httptest.Server
	realmH string
}

func newHandshakeRig(t *testing.T) *handshakeRig {
	t.Helper()
	ts := &tokenService{}
	ts.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts.mu.Lock()
		ts.got = append(ts.got, r.Clone(r.Context()))
		status := ts.status
		ts.mu.Unlock()
		user, pass, ok := r.BasicAuth()
		switch {
		case status != 0:
			w.WriteHeader(status)
		case !ok || user != "ci" || pass != "right":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"tkn","expires_in":300}`))
		}
	}))
	t.Cleanup(ts.srv.Close)
	_, port, _ := net.SplitHostPort(ts.srv.Listener.Addr().String())
	realmHost := "example.com:" + port
	reg := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Www-Authenticate", `Bearer realm="https://`+realmHost+`/jwt/auth",service="registry.test",scope="repository:x:pull"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(reg.Close)
	f := newFixture(t, fixtureOpts{
		servers: []*httptest.Server{reg, ts.srv},
		factory: factoryWith(t, map[string]string{realmHost: ts.srv.Listener.Addr().String()}, reg, ts.srv),
	})
	return &handshakeRig{f: f, ts: ts, reg: reg, realmH: realmHost}
}

// unsaved runs the unsaved test of a remote of the handshake format with the given hosts and credential.
func (h *handshakeRig) unsaved(t *testing.T, mode, user, password string, hosts ...repository.ExtraHost) repository.UpstreamTestResult {
	t.Helper()
	s := settings(h.reg.URL+"/", mode)
	s.Password = nil
	s.Username = user
	if hosts != nil {
		s.ExtraHosts = hosts
	}
	p := repository.UnsavedProbe{Format: "handshakefmt", Settings: s}
	if password != "" {
		pw := observability.Secret(password)
		p.Password = &pw
	}
	return h.f.clients.TestUnsaved(t.Context(), p)
}

// TestR_REPO_064_TheUpstreamTestRunsTheRegistrysLogin: a Bearer challenge's token service on a host the repository does not list is
// foreign_host_denied and is not asked; an allowed one that receives the credential is asked once without a scope, as a client's
// login does, and its answer is the verdict; without a credential, or on a host that receives none, it is not asked; authMode
// bearer runs no handshake; and the saved test answers the same with the stored credential.
func TestR_REPO_064_TheUpstreamTestRunsTheRegistrysLogin(t *testing.T) {
	h := newHandshakeRig(t)
	allowed := repository.ExtraHost{Pattern: "example.com", SendCredentials: true}

	t.Run("R-REPO-064 a token service on a foreign host is named and not asked", func(t *testing.T) {
		res := h.unsaved(t, repository.AuthBasic, "ci", "right")
		if !res.Reachable || res.Authenticated != nil || res.Error == nil || res.Error.Code != CodeForeignHostDenied ||
			!strings.Contains(res.Error.Message, "example.com") {
			t.Fatalf("result = %+v, error %+v", res, res.Error)
		}
		if res.TokenService == nil || *res.TokenService != (repository.UpstreamTokenService{Host: "example.com"}) {
			t.Fatalf("token service = %+v", res.TokenService)
		}
		if n := len(h.ts.requests()); n != 0 {
			t.Fatalf("the token service got %d requests", n)
		}
	})
	t.Run("R-REPO-064 the right password is accepted by one login without a scope", func(t *testing.T) {
		res := h.unsaved(t, repository.AuthBasic, "ci", "right", allowed)
		if res.Authenticated == nil || !*res.Authenticated || res.Error != nil ||
			*res.TokenService != (repository.UpstreamTokenService{Host: "example.com", Allowed: true, ReceivesCredential: true}) {
			t.Fatalf("result = %+v, error %+v, token service %+v", res, res.Error, res.TokenService)
		}
		got := h.ts.requests()
		if len(got) != 1 {
			t.Fatalf("the token service got %d requests", len(got))
		}
		r := got[0]
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte("ci:right"))
		if r.URL.Path != "/jwt/auth" || r.URL.Query().Get("service") != "registry.test" || r.URL.Query().Has("scope") ||
			r.Header.Get("Authorization") != want {
			t.Fatalf("the login was %s %s, Authorization %q", r.Method, r.URL, r.Header.Get("Authorization"))
		}
	})
	t.Run("R-REPO-064 a wrong password is refused", func(t *testing.T) {
		res := h.unsaved(t, repository.AuthBasic, "ci", "wrong", allowed)
		if res.Authenticated == nil || *res.Authenticated || res.Error != nil {
			t.Fatalf("result = %+v, error %+v", res, res.Error)
		}
	})
	t.Run("R-REPO-064 a host that receives no credential is not asked", func(t *testing.T) {
		before := len(h.ts.requests())
		res := h.unsaved(t, repository.AuthBasic, "ci", "right", repository.ExtraHost{Pattern: "example.com"})
		if res.Authenticated != nil || res.Error != nil ||
			*res.TokenService != (repository.UpstreamTokenService{Host: "example.com", Allowed: true}) {
			t.Fatalf("result = %+v, error %+v, token service %+v", res, res.Error, res.TokenService)
		}
		if len(h.ts.requests()) != before {
			t.Fatal("the token service was asked")
		}
	})
	t.Run("R-REPO-064 without a credential the token service is not asked", func(t *testing.T) {
		before := len(h.ts.requests())
		res := h.unsaved(t, repository.AuthNone, "", "", allowed)
		if res.Authenticated != nil || res.Error != nil || !res.TokenService.Allowed {
			t.Fatalf("result = %+v, error %+v", res, res.Error)
		}
		if len(h.ts.requests()) != before {
			t.Fatal("the token service was asked")
		}
	})
	t.Run("R-REPO-064 authMode bearer runs no handshake", func(t *testing.T) {
		before := len(h.ts.requests())
		res := h.unsaved(t, repository.AuthBearer, "", "static", allowed)
		if res.Authenticated == nil || *res.Authenticated || res.TokenService != nil {
			t.Fatalf("result = %+v", res)
		}
		if len(h.ts.requests()) != before {
			t.Fatal("the token service was asked")
		}
	})
	t.Run("R-REPO-064 another answer of the token service is no verdict", func(t *testing.T) {
		h.ts.mu.Lock()
		h.ts.status = http.StatusInternalServerError
		h.ts.mu.Unlock()
		defer func() { h.ts.mu.Lock(); h.ts.status = 0; h.ts.mu.Unlock() }()
		res := h.unsaved(t, repository.AuthBasic, "ci", "right", allowed)
		if res.Authenticated != nil || res.Error == nil || res.Error.Code != CodeRequestFailed || !strings.Contains(res.Error.Message, "example.com") {
			t.Fatalf("result = %+v, error %+v", res, res.Error)
		}
	})
	t.Run("R-REPO-064 the saved test answers the same with the stored credential", func(t *testing.T) {
		r := repo(140, "reg-saved", 1)
		r.Format = "handshakefmt"
		h.f.seal(140, "right")
		s := settings(h.reg.URL+"/", repository.AuthBasic)
		s.Username, s.ExtraHosts = "ci", []repository.ExtraHost{allowed}
		res := h.f.clients.TestSaved(t.Context(), r, s)
		if res.Authenticated == nil || !*res.Authenticated || res.Error != nil {
			t.Fatalf("result = %+v, error %+v", res, res.Error)
		}
	})
}

func TestBearerParamsReadsTheRealmAndTheService(t *testing.T) {
	for v, want := range map[string][2]string{
		`Bearer realm="https://a.example/token",service="reg",scope="repository:x:pull"`: {"https://a.example/token", "reg"},
		`bearer service=reg, realm=https://a.example/t`:                                  {"https://a.example/t", "reg"},
		`Bearer realm="https://a.example/q\"x",service=""`:                               {`https://a.example/q"x`, ""},
	} {
		h := http.Header{}
		h.Set("Www-Authenticate", v)
		realm, service, ok := bearerParams(h)
		if !ok || realm != want[0] || service != want[1] {
			t.Errorf("%s: %q %q %v", v, realm, service, ok)
		}
	}
	for _, v := range []string{`Basic realm="x"`, `Bearer`, `Bearer service="reg"`, "Bearer realm=" + strings.Repeat("x", maxChallengeBytes)} {
		h := http.Header{}
		h.Set("Www-Authenticate", v)
		if _, _, ok := bearerParams(h); ok {
			t.Errorf("%.40s reads as a Bearer challenge", v)
		}
	}
}
