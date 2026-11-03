package engine

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/bastion/guard"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastion/mainclient"
	"github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/repository"
)

// TestR_BAS_003_OnlyThePackagePathFamiliesReachTheReadFlow sends every row of INV-6's path table: only
// /api/<format>/… and /files/… reach the read flow, the others answer 404 bastion_not_served from the Bastion and
// the counting double of the main installation records no request.
func TestR_BAS_003_OnlyThePackagePathFamiliesReachTheReadFlow(t *testing.T) {
	rows := []struct {
		path   string
		served bool
	}{
		{"/api/v1/system/ping", false},
		{"/api/v1/repositories", false},
		{"/api/v1/bastion/handshake", false},
		{"/api/v1", false},
		{"/api/", false},
		{"/api/npm", false},
		{"/api/npm/", false},
		{"/api/npm/npm-all/lodash", true},
		{"/api/nuget/nuget-all/v3/index.json", true},
		{"/files/raw-all/a/b.txt", true},
		{"/files/", false},
		{"/files", false},
		{"/auth/oidc/corp/start", false},
		{"/ui/", false},
		{"/ui/repositories", false},
		{"/-/npm/v1/oidc/token/exchange/package/x", false},
		{"/v2/", false},
		{"/v2/library/alpine/manifests/latest", false},
		{"/.well-known/terraform.json", false},
		{"/", false},
		{"/healthz", false},
		{"/metrics", false},
	}
	for _, row := range rows {
		t.Run(row.path, func(t *testing.T) {
			s := newSiteHarness(t, options{})
			w := s.get(row.path, "Authorization", bearer(s.full))
			if row.served {
				if problemCode(w) == "bastion_not_served" || s.main.count() == 0 {
					t.Fatalf("%s did not reach the read flow: %d %s, %d requests", row.path, w.Code, w.Body, s.main.count())
				}
				return
			}
			if w.Code != http.StatusNotFound || problemCode(w) != "bastion_not_served" {
				t.Fatalf("%s: %d %s", row.path, w.Code, w.Body)
			}
			if n := s.main.count(); n != 0 {
				t.Fatalf("%s: the main installation saw %d requests", row.path, n)
			}
		})
	}
}

func TestR_BAS_003_ACookieIsIgnoredAndNeverForwarded(t *testing.T) {
	s := newSiteHarness(t, options{})
	s.site.artifacts["/api/npm/npm-all/lodash"] = []byte("doc")
	// anonymous access is on, so a cookie-only request is an anonymous request
	w := s.get("/api/npm/npm-all/lodash", "Cookie", "__Host-trv_session=abc")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	for _, r := range s.main.requests() {
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Fatalf("the forwarded request carries %v", r.Header)
		}
		if r.Class != "anonymous" {
			t.Fatalf("class %q, want the anonymous budget class", r.Class)
		}
	}
	// with anonymous access off the cookie authenticates nobody
	s.setAnonymous(false)
	w = s.get("/api/npm/npm-all/other", "Cookie", "__Host-trv_session=abc")
	if w.Code != http.StatusUnauthorized || problemCode(w) != "credentials_required" {
		t.Fatalf("cookie only with anonymous off: %d %s", w.Code, w.Body)
	}
}

// ---- R-BAS-018 --------------------------------------------------------------------------------------------

func TestR_BAS_018_TheHostIsTheHostOfTheBaseURL(t *testing.T) {
	s := newSiteHarness(t, options{})
	cases := []struct{ host string }{{"trove.example.com"}, {"203.0.113.9"}, {"[2001:db8::1]"}, {"[2001:db8::1]:8443"}, {""}, {"packages.example.com.evil.net"}}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/api/npm/npm-all/lodash", nil)
		r.RemoteAddr = "203.0.113.7:1"
		r.Host = c.host
		w := httptest.NewRecorder()
		s.e.ServeHTTP(w, r)
		if w.Code != http.StatusMisdirectedRequest || problemCode(w) != "misdirected_request" {
			t.Errorf("host %q: %d %s", c.host, w.Code, w.Body)
		}
	}
	if s.main.count() != 0 {
		t.Fatal("a misdirected request reached the main installation")
	}
	// the host of the base URL, with a port and in capitals, is the Bastion's
	for _, host := range []string{"packages.example.com", "Packages.Example.COM:8443"} {
		r := httptest.NewRequest(http.MethodGet, "/api/npm/npm-all/lodash", nil)
		r.RemoteAddr = "203.0.113.7:1"
		r.Host = host
		w := httptest.NewRecorder()
		s.e.ServeHTTP(w, r)
		if w.Code == http.StatusMisdirectedRequest {
			t.Errorf("host %q refused", host)
		}
	}
}

func TestR_BAS_018_AForwardedHostCountsOnlyFromATrustedProxy(t *testing.T) {
	s := newSiteHarness(t, options{cfg: []func(*configT){func(c *configT) { c.Server.TrustedProxies = []string{"10.244.0.0/16"} }}})
	do := func(remote string) int {
		r := httptest.NewRequest(http.MethodGet, "/api/npm/npm-all/lodash", nil)
		r.Host = "ingress.internal"
		r.RemoteAddr = remote
		r.Header.Set("X-Forwarded-Host", "packages.example.com")
		w := httptest.NewRecorder()
		s.e.ServeHTTP(w, r)
		return w.Code
	}
	if code := do("10.244.1.5:1234"); code == http.StatusMisdirectedRequest {
		t.Fatal("the forwarded host of a trusted proxy was ignored")
	}
	if code := do("198.51.100.5:1234"); code != http.StatusMisdirectedRequest {
		t.Fatalf("an untrusted peer's forwarded host counted: %d", code)
	}
}

func TestR_BAS_018_TheBlockListAnswersBeforeThePathIsRead(t *testing.T) {
	g := newGuard()
	g.blocked = guard.Verdict{Status: 429, Code: iface.CodeAddressBlocked, RetryAfter: 15 * time.Minute, Reason: metrics.RefusedBlocked}
	s := newSiteHarness(t, options{guard: g})
	w := s.get("/ui/", "Authorization", "garbage")
	if w.Code != http.StatusTooManyRequests || problemCode(w) != "address_blocked" || w.Header().Get("Retry-After") != "900" {
		t.Fatalf("%d %s %v", w.Code, w.Body, w.Header())
	}
	if g.reqs != 0 || s.main.count() != 0 {
		t.Fatal("the flow went on after a block")
	}
	if counterValue(t, s.harness, "refused", metrics.RefusedBlocked) != 1 {
		t.Fatal("the block is not counted")
	}
}

func TestR_BAS_018_TheKeyRuleIsTheOneOfINV5(t *testing.T) {
	if keyPattern.String() != repository.KeyPattern {
		t.Fatalf("keyPattern %q differs from repository.KeyPattern %q", keyPattern, repository.KeyPattern)
	}
	s := newSiteHarness(t, options{})
	for _, p := range []string{
		"/api/npm/NPM-ALL/lodash", "/api/npm/-npm/lodash", "/api/npm/n/lodash", "/api/npm/" + strings.Repeat("a", 65) + "/x",
		"/api/npm/npm%2Dall/lodash", "/files/Raw/x", "/api/npm/npm%20all/x",
	} {
		w := s.get(p, "Authorization", bearer(s.full))
		if w.Code != http.StatusNotFound || problemCode(w) != "bastion_not_served" {
			t.Errorf("%s: %d %s", p, w.Code, w.Body)
		}
	}
	if s.main.count() != 0 {
		t.Fatalf("a malformed key reached the main installation %d times", s.main.count())
	}
}

func TestR_BAS_018_TheCacheKeyHoldsThePathQueryAndTheVaryHeaders(t *testing.T) {
	s := newSiteHarness(t, options{})
	s.site.docs["/api/npm/npm-all/lodash"] = "doc"
	get := func(accept string) {
		hdr := []string{"Authorization", bearer(s.full)}
		if accept != "" {
			hdr = append(hdr, "Accept", accept)
		}
		if w := s.get("/api/npm/npm-all/lodash", hdr...); w.Code != http.StatusOK {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	}
	get("")                                    // learns Vary: Accept
	get("application/vnd.npm.install-v1+json") // a second variant
	get("Application/vnd.npm.install-v1+json") // the same variant, spelled differently
	get("application/vnd.npm.install-v1+json") // hit
	s.clock.advance(0)
	// one entry per Accept variant, and the entry at the plain key that tells another replica the Vary (saveEntry)
	if n := s.cache.entryCount(); n != 3 {
		t.Fatalf("%d cache entries, want one per Accept variant (2) and the plain-key entry", n)
	}
	// the path and query exactly as received: two spellings are two keys
	a := s.e.keyFor("/api/npm/npm-all/x?a=%2F", nil)
	b := s.e.keyFor("/api/npm/npm-all/x?a=/", nil)
	if a == b {
		t.Fatal("percent-encoding was normalized away from the cache key")
	}
}

func TestR_BAS_018_AMethodOtherThanGetAndHeadIsNotTheReadFlow(t *testing.T) {
	s := newSiteHarness(t, options{})
	w := s.do(http.MethodOptions, "/api/npm/npm-all/lodash", nil, "Authorization", bearer(s.full))
	if w.Code != http.StatusMethodNotAllowed || problemCode(w) != "method_not_allowed" || w.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("OPTIONS: %d %s %v", w.Code, w.Body, w.Header())
	}
	if s.main.count() != 0 {
		t.Fatal("OPTIONS reached the main installation")
	}
}

func TestR_BAS_018_TheLimitsComeBeforeTheCredentials(t *testing.T) {
	g := newGuard()
	g.request = guard.Verdict{Status: 429, Code: iface.CodeRateLimited, RetryAfter: 3 * time.Second, Reason: metrics.RefusedRateLimited}
	s := newSiteHarness(t, options{guard: g})
	w := s.get("/api/npm/npm-all/lodash", "Authorization", "Bearer not-a-token")
	if w.Code != http.StatusTooManyRequests || problemCode(w) != "rate_limited" || w.Header().Get("Retry-After") != "3" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Header().Get("RateLimit-Policy"), "bastion:requests") || !strings.Contains(w.Header().Get("RateLimit"), "r=0") {
		t.Fatalf("the RateLimit fields are missing: %v", w.Header())
	}
	if g.failedCount() != 0 {
		t.Fatal("a rate-limited request counted as a failed authentication")
	}
}

// ---- R-BAS-019 --------------------------------------------------------------------------------------------

func TestR_BAS_019_AMalformedTokenAnswers401WithoutARequest(t *testing.T) {
	s := newSiteHarness(t, options{})
	flipped := []byte(s.full)
	flipped[20] ^= 1
	for name, auth := range map[string]string{
		"a flipped character": bearer(string(flipped)),
		"a Bastion token":     bearer(newToken(t, token.TypeBST)),
		"an unknown prefix":   bearer("trv_xyz_" + strings.Repeat("a", 49)),
		"not a token":         bearer("hello"),
		"an unknown scheme":   "Digest abc",
		"a broken Basic":      "Basic !!!",
		"Basic without colon": "Basic " + base64.StdEncoding.EncodeToString([]byte("justaname")),
	} {
		w := s.get("/api/npm/npm-all/lodash", "Authorization", auth)
		if w.Code != http.StatusUnauthorized || problemCode(w) != "invalid_token" || w.Header().Get("WWW-Authenticate") != `Basic realm="trove"` {
			t.Errorf("%s: %d %s %v", name, w.Code, w.Body, w.Header())
		}
		if w.Header().Get("Cache-Control") != "no-store" || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json") {
			t.Errorf("%s: %v", name, w.Header())
		}
	}
	if s.main.count() != 0 {
		t.Fatalf("the main installation saw %d requests", s.main.count())
	}
	if n := s.guard.failedCount(); n != 7 {
		t.Fatalf("%d failed authentications counted, want 7", n)
	}
	if counterValue(t, s.harness, "refused", metrics.RefusedInvalid) != 7 {
		t.Fatal("the refusals are not counted")
	}
}

func TestR_BAS_019_ABrowserGetsTheBearerChallenge(t *testing.T) {
	s := newSiteHarness(t, options{})
	w := s.get("/api/npm/npm-all/lodash", "Authorization", "Bearer x", "Sec-Fetch-Mode", "navigate")
	if w.Header().Get("WWW-Authenticate") != `Bearer realm="trove"` {
		t.Fatalf("%v", w.Header())
	}
}

func TestR_BAS_019_PatSatAndCitAreAcceptedBstIsNot(t *testing.T) {
	for _, typ := range []token.Type{token.TypePAT, token.TypeSAT, token.TypeCIT} {
		s := newSiteHarness(t, options{})
		tok := newToken(t, typ)
		s.site.addReader(tok, &reader{id: string(typ), read: iface.ReadFull})
		s.site.artifacts["/api/npm/npm-all/x"] = []byte("x")
		if w := s.get("/api/npm/npm-all/x", "Authorization", bearer(tok)); w.Code != http.StatusOK {
			t.Errorf("%s: %d %s", typ, w.Code, w.Body)
		}
	}
	s := newSiteHarness(t, options{})
	if w := s.get("/api/npm/npm-all/x", "Authorization", bearer(newToken(t, token.TypeBST))); w.Code != http.StatusUnauthorized {
		t.Errorf("bst: %d", w.Code)
	}
}

func TestR_BAS_019_TheSyntaxCheckAgreesWithTheMainInstallationsFormat(t *testing.T) {
	for _, typ := range []token.Type{token.TypePAT, token.TypeSAT, token.TypeCIT, token.TypeBST} {
		for i := 0; i < 50; i++ {
			raw := newToken(t, typ)
			got, ok := mainclient.TokenType(raw)
			want, err := token.Check(raw)
			if !ok || err != nil || got != string(want) {
				t.Fatalf("%s: engine (%q, %v), main (%q, %v)", raw, got, ok, want, err)
			}
			for _, pos := range []int{0, 5, 8, 30, 50, 56} {
				bad := []byte(raw)
				bad[pos] = 'Z'
				if string(bad) == raw {
					bad[pos] = 'Y'
				}
				_, ok := mainclient.TokenType(string(bad))
				_, err := token.Check(string(bad))
				if ok != (err == nil) {
					t.Fatalf("%s: engine %v, main %v", bad, ok, err)
				}
			}
		}
	}
}

func TestR_BAS_019_BasicWithTheTokenAsPasswordIsAcceptedAndForwardedUnchanged(t *testing.T) {
	s := newSiteHarness(t, options{})
	s.site.artifacts["/api/npm/npm-all/x"] = []byte("x")
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte("anyone:"+s.full))
	w := s.get("/api/npm/npm-all/x", "Authorization", auth)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if got := s.main.requests()[0].Header.Get("Authorization"); got != auth {
		t.Fatalf("the forwarded Authorization is %q, want the caller's unchanged", got)
	}
}

func TestR_BAS_019_TheNuGetAPIKeyIsNeverAReadCredential(t *testing.T) {
	s := newSiteHarness(t, options{})
	s.site.artifacts["/api/nuget/npm-all/x"] = []byte("x")
	s.setAnonymous(false)
	w := s.get("/api/nuget/npm-all/x", "X-NuGet-ApiKey", s.full)
	if w.Code != http.StatusUnauthorized || problemCode(w) != "credentials_required" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	s.setAnonymous(true)
	if w = s.get("/api/nuget/npm-all/x", "X-NuGet-ApiKey", s.full); w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	for _, r := range s.main.requests() {
		if r.Header.Get("X-Nuget-Apikey") != "" || r.Header.Get("Authorization") != "" {
			t.Fatalf("the API key was forwarded as a credential: %v", r.Header)
		}
	}
}

func TestR_BAS_019_AnonymousExactlyWhenTheMainInstallationAdmitsIt(t *testing.T) {
	s := newSiteHarness(t, options{})
	s.site.artifacts["/api/npm/npm-all/x"] = []byte("x")
	s.setAnonymous(false)
	for i := 0; i < 1000; i++ {
		if w := s.get("/api/npm/npm-all/x"); w.Code != http.StatusUnauthorized || problemCode(w) != "credentials_required" || w.Header().Get("WWW-Authenticate") != `Basic realm="trove"` {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	}
	if s.main.count() != 0 {
		t.Fatalf("%d anonymous requests reached the main installation", s.main.count())
	}
	if s.guard.failedCount() != 0 {
		t.Fatal("an anonymous request counted as a failed authentication")
	}
	// switched on: the next anonymous request is served, under the key anonymous, shared by every anonymous caller
	s.setAnonymous(true)
	if w := s.get("/api/npm/npm-all/x"); w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	before := s.main.count()
	r := httptest.NewRequest(http.MethodGet, "/api/npm/npm-all/x", nil)
	r.RemoteAddr = "198.51.100.99:1"
	r.Host = "packages.example.com"
	w := httptest.NewRecorder()
	s.e.ServeHTTP(w, r)
	if w.Code != http.StatusOK || s.main.count() != before {
		t.Fatalf("a second anonymous caller: %d, %d new requests", w.Code, s.main.count()-before)
	}
}

func TestR_BAS_019_AnIPv6AddressCountsAsItsSlash64AndTheMainInstallationGetsTheAddress(t *testing.T) {
	g := newGuard()
	s := newSiteHarness(t, options{guard: g})
	s.site.artifacts["/api/npm/npm-all/x"] = []byte("x")
	r := httptest.NewRequest(http.MethodGet, "/api/npm/npm-all/x", nil)
	r.RemoteAddr = "[2001:db8:1:2:aaaa:bbbb:cccc:dddd]:1"
	r.Host = "packages.example.com"
	w := httptest.NewRecorder()
	s.e.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("%d", w.Code)
	}
	if len(g.addrs) == 0 || g.addrs[0] != "2001:db8:1:2::/64" {
		t.Fatalf("the guard was asked about %v, want the /64", g.addrs)
	}
	if got := s.main.requests()[0].Addr; got != "2001:db8:1:2:aaaa:bbbb:cccc:dddd" {
		t.Fatalf("the forwarded client address is %q, want the address itself", got)
	}
}

func TestR_BAS_019_TheCredentialKeyIsTheSHA256AndNoTokenIsKept(t *testing.T) {
	s := newSiteHarness(t, options{})
	s.site.artifacts["/api/npm/npm-all/x"] = []byte("x")
	s.get("/api/npm/npm-all/x", "Authorization", bearer(s.full))
	if s.e.answers.credential(t.Context(), credentialKey(s.full)) == nil {
		t.Fatal("no credential answer under the SHA-256 of the token")
	}
	for k := range s.e.answers.mem.items {
		if strings.Contains(k, s.full) {
			t.Fatalf("the token string is part of the key %q", k)
		}
	}
}

func TestR_BAS_019_TheFailedAuthenticationTimeoutRefusesUnverifiedCredentialsOnly(t *testing.T) {
	g := newGuard()
	s := newSiteHarness(t, options{guard: g})
	s.site.artifacts["/api/npm/npm-all/x"] = []byte("x")
	// $FULL is known
	if w := s.get("/api/npm/npm-all/x", "Authorization", bearer(s.full)); w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
	g.timeout = guard.Verdict{Status: 429, Code: iface.CodeAuthTimeout, RetryAfter: 15 * time.Minute, Reason: metrics.RefusedAuthTimeout}
	before := s.main.count()
	if w := s.get("/api/npm/npm-all/x", "Authorization", bearer(s.full)); w.Code != http.StatusOK {
		t.Fatalf("a credential with a usable answer was cut off: %d", w.Code)
	}
	w := s.get("/api/npm/npm-all/x", "Authorization", bearer(s.part))
	if w.Code != http.StatusTooManyRequests || problemCode(w) != "auth_timeout" || w.Header().Get("Retry-After") != "900" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if s.main.count() != before {
		t.Fatal("a request in the timeout reached the main installation")
	}
	// anonymous requests stay anonymous requests
	if w := s.get("/api/npm/npm-all/x"); w.Code != http.StatusOK {
		t.Fatalf("anonymous during the timeout: %d", w.Code)
	}
}
