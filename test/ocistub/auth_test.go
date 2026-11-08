// Self-tests of the counting stub of R-OCI-035; they check the test tool, not trove, and carry no requirement ID (Q-ROAD-5).

package ocistub

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// parseChallenge reads the parameters of a Bearer challenge.
func parseChallenge(t *testing.T, header string) map[string]string {
	t.Helper()
	rest, ok := strings.CutPrefix(header, "Bearer ")
	if !ok {
		t.Fatalf("challenge %q is no Bearer challenge", header)
	}
	out := map[string]string{}
	for _, part := range strings.Split(rest, `",`) {
		k, v, ok := strings.Cut(part, `="`)
		if !ok {
			t.Fatalf("challenge part %q", part)
		}
		out[k] = strings.TrimSuffix(v, `"`)
	}
	return out
}

func basic(user, pass string) map[string]string {
	req, _ := http.NewRequest(http.MethodGet, "http://x", nil)
	req.SetBasicAuth(user, pass)
	return map[string]string{"Authorization": req.Header.Get("Authorization")}
}

// The registry challenges, the realm answers on its own host, and the retry with the token succeeds (R-OCI-021).
func TestTokenHandshake(t *testing.T) {
	f := newFixture(t, Config{})
	r := f.get(f.reg+"/v2/library/alpine/manifests/3", map[string]string{"Accept": acceptAll})
	if r.status != http.StatusUnauthorized || r.errorCode(t) != "UNAUTHORIZED" {
		t.Fatalf("unauthenticated manifest: %d %s", r.status, r.body)
	}
	ch := parseChallenge(t, r.header.Get("WWW-Authenticate"))
	if ch["realm"] != f.realm+"/token" || ch["service"] != DefaultService || ch["scope"] != "repository:library/alpine:pull" {
		t.Fatalf("challenge = %v, want realm %s/token, service %s, scope repository:library/alpine:pull", ch, f.realm, DefaultService)
	}
	if r.header.Get("Docker-Distribution-API-Version") != "registry/2.0" {
		t.Errorf("API version header = %q", r.header.Get("Docker-Distribution-API-Version"))
	}

	tokenURL := ch["realm"] + "?" + url.Values{"service": {ch["service"]}, "scope": {ch["scope"]}}.Encode()
	tr := f.get(tokenURL, nil)
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		IssuedAt    string `json:"issued_at"`
	}
	tr.json(t, &tok)
	if tr.status != 200 || tok.Token == "" || tok.Token != tok.AccessToken || tok.ExpiresIn != DefaultTokenTTL || tok.IssuedAt == "" {
		t.Fatalf("token answer = %d %+v", tr.status, tok)
	}
	if got := f.get(f.reg+"/v2/library/alpine/manifests/3", map[string]string{"Authorization": "Bearer " + tok.Token, "Accept": acceptAll}); got.status != 200 {
		t.Fatalf("retry with the token: %d %s", got.status, got.body)
	}

	c := f.stub.Counts()
	if n := c.TokenRequests(""); n != 1 {
		t.Errorf("realm requests = %d, want 1", n)
	}
	if got := c.AuthSchemes(hostPort(t, f.reg)); strings.Join(got, ",") != "bearer,none" {
		t.Errorf("registry host saw credential schemes %v, want none and bearer", got)
	}
	if got := c.AuthSchemes(hostPort(t, f.realm)); strings.Join(got, ",") != "none" {
		t.Errorf("realm host saw %v", got)
	}
}

func TestBaseChallengeHasNoScope(t *testing.T) {
	f := newFixture(t, Config{})
	r := f.get(f.reg+"/v2/", nil)
	ch := parseChallenge(t, r.header.Get("WWW-Authenticate"))
	if r.status != 401 || ch["scope"] != "" || ch["service"] == "" {
		t.Fatalf("base challenge = %d %v", r.status, ch)
	}
	if got := f.get(f.reg+"/v2/", f.bearer()); got.status != 200 {
		t.Fatalf("base with a token = %d", got.status)
	}
}

func TestAuthModes(t *testing.T) {
	f := newFixture(t, Config{})
	scope := "repository:library/alpine:pull"
	tokenURL := f.realm + "/token?service=" + DefaultService + "&scope=" + url.QueryEscape(scope)

	// anonymous (the default): a token with or without the right credentials; wrong credentials never.
	if r := f.get(tokenURL, nil); r.status != 200 {
		t.Errorf("anonymous request in mode anonymous = %d", r.status)
	}
	if r := f.get(tokenURL, basic(DefaultUsername, DefaultPassword)); r.status != 200 {
		t.Errorf("credentialed request in mode anonymous = %d", r.status)
	}
	if r := f.get(tokenURL, basic(DefaultUsername, "wrong")); r.status != 401 || r.header.Get("WWW-Authenticate") == "" {
		t.Errorf("wrong credentials = %d", r.status)
	}

	// basic: credentials are required.
	if err := f.stub.SetMode(Mode{Auth: AuthBasic}); err != nil {
		t.Fatal(err)
	}
	if r := f.get(tokenURL, nil); r.status != 401 || !strings.HasPrefix(r.header.Get("WWW-Authenticate"), "Basic ") {
		t.Errorf("anonymous request in mode basic = %d %q", r.status, r.header.Get("WWW-Authenticate"))
	}
	if r := f.get(tokenURL, basic(DefaultUsername, DefaultPassword)); r.status != 200 {
		t.Errorf("credentialed request in mode basic = %d", r.status)
	}

	// none: the registry does not challenge.
	if err := f.stub.SetMode(Mode{Auth: AuthNone}); err != nil {
		t.Fatal(err)
	}
	if r := f.manifestOf("library/alpine", "3"); r.status != 200 {
		t.Errorf("mode none = %d", r.status)
	}
}

func TestCustomCredentialsAndService(t *testing.T) {
	f := newFixture(t, Config{Username: "u", Password: "p", Service: "registry.example"})
	r := f.get(f.reg+"/v2/", nil)
	if ch := parseChallenge(t, r.header.Get("WWW-Authenticate")); ch["service"] != "registry.example" {
		t.Fatalf("service = %q", ch["service"])
	}
	if got := f.get(f.realm+"/token?service=registry.example", basic("u", "p")); got.status != 200 {
		t.Errorf("custom credentials = %d", got.status)
	}
	if got := f.get(f.realm+"/token?service="+DefaultService, nil); got.status != 400 {
		t.Errorf("a token request for another service = %d, want 400", got.status)
	}
}

// A token covers the scopes the realm granted and nothing else; the realm grants pull only (R-OCI-021).
func TestTokenScopes(t *testing.T) {
	f := newFixture(t, Config{})
	hdr := f.bearer("repository:library/alpine:pull,push")
	var c claims
	var err error
	if c, err = verify(strings.TrimPrefix(hdr["Authorization"], "Bearer ")); err != nil {
		t.Fatal(err)
	}
	if len(c.Access) != 1 || strings.Join(c.Access[0].Actions, ",") != "pull" {
		t.Fatalf("granted %+v, want pull only", c.Access)
	}

	hdr["Accept"] = acceptAll
	if r := f.get(f.reg+"/v2/library/busybox/manifests/latest", hdr); r.status != 401 || parseChallenge(t, r.header.Get("WWW-Authenticate"))["error"] != "insufficient_scope" {
		t.Errorf("a token for another repository = %d %q", r.status, r.header.Get("WWW-Authenticate"))
	}
	if r := f.get(f.reg+"/v2/library/alpine/manifests/3", hdr); r.status != 200 {
		t.Errorf("a token for the repository = %d", r.status)
	}

	// Several scope parameters, and several scopes in one parameter, are all granted.
	multi := f.bearer("repository:library/alpine:pull repository:library/busybox:pull", "repository:acme/a:pull")
	multi["Accept"] = acceptAll
	for _, name := range []string{"library/alpine", "library/busybox", "acme/a"} {
		if r := f.get(f.reg+"/v2/"+name+"/tags/list", multi); r.status != 200 {
			t.Errorf("tags of %s with a multi-scope token = %d", name, r.status)
		}
	}
}

func TestPrivateRepositories(t *testing.T) {
	f := newFixture(t, Config{})
	if err := f.stub.SetMode(Mode{PrivateRepos: []string{"acme/a"}}); err != nil {
		t.Fatal(err)
	}
	scope := "repository:acme/a:pull"
	anon := f.bearer(scope)
	if r := f.get(f.reg+"/v2/acme/a/tags/list", anon); r.status != 401 {
		t.Errorf("anonymous token for a private repository = %d", r.status)
	}
	q := url.Values{"service": {DefaultService}, "scope": {scope}}
	tr := f.get(f.realm+"/token?"+q.Encode(), basic(DefaultUsername, DefaultPassword))
	var tok struct {
		Token string `json:"token"`
	}
	tr.json(t, &tok)
	if r := f.get(f.reg+"/v2/acme/a/tags/list", map[string]string{"Authorization": "Bearer " + tok.Token}); r.status != 200 {
		t.Errorf("credentialed token for a private repository = %d", r.status)
	}
	// A public repository is still open to the anonymous token.
	if r := f.get(f.reg+"/v2/other/c/tags/list", f.bearer("repository:other/c:pull")); r.status != 200 {
		t.Errorf("public repository = %d", r.status)
	}
}

func TestTokenExpiryAndRevocation(t *testing.T) {
	clk := newClock()
	f := newFixture(t, Config{Now: clk.now})
	if err := f.stub.SetMode(Mode{TokenTTLSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	scope := "repository:library/alpine:pull"
	hdr := f.bearer(scope)
	target := f.reg + "/v2/library/alpine/tags/list"
	if r := f.get(target, hdr); r.status != 200 {
		t.Fatalf("fresh token = %d", r.status)
	}
	clk.advance(59 * time.Second)
	if r := f.get(target, hdr); r.status != 200 {
		t.Fatalf("token 59 s old with a 60 s lifetime = %d", r.status)
	}
	clk.advance(2 * time.Second)
	if r := f.get(target, hdr); r.status != 401 || parseChallenge(t, r.header.Get("WWW-Authenticate"))["error"] != "invalid_token" {
		t.Fatalf("expired token = %d %q", r.status, r.header.Get("WWW-Authenticate"))
	}

	hdr = f.bearer(scope)
	if r := f.get(target, hdr); r.status != 200 {
		t.Fatalf("new token = %d", r.status)
	}
	f.stub.RevokeTokens()
	if r := f.get(target, hdr); r.status != 401 {
		t.Fatalf("revoked token = %d", r.status)
	}
	if r := f.get(target, f.bearer(scope)); r.status != 200 {
		t.Fatalf("token issued after the revocation = %d", r.status)
	}
	// Tokens differ per issue even for one scope at one instant.
	if a, b := f.bearer(scope), f.bearer(scope); a["Authorization"] == b["Authorization"] {
		t.Error("two tokens for one scope are identical")
	}
}

func TestTokenOmitExpiryAndRefusal(t *testing.T) {
	f := newFixture(t, Config{})
	tokenURL := f.realm + "/token?service=" + DefaultService
	if err := f.stub.SetMode(Mode{TokenOmitExpiry: true}); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	f.get(tokenURL, nil).json(t, &doc)
	if _, has := doc["expires_in"]; has || doc["token"] == "" {
		t.Errorf("answer without expiry = %v", doc)
	}
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusInternalServerError} {
		if err := f.stub.SetMode(Mode{RefuseToken: status}); err != nil {
			t.Fatal(err)
		}
		if r := f.get(tokenURL, nil); r.status != status {
			t.Errorf("RefuseToken %d answered %d", status, r.status)
		}
	}
}

func TestStaticBearerToken(t *testing.T) {
	f := newFixture(t, Config{})
	if err := f.stub.SetMode(Mode{StaticToken: "s3cret"}); err != nil {
		t.Fatal(err)
	}
	if r := f.get(f.reg+"/v2/library/alpine/tags/list", map[string]string{"Authorization": "Bearer s3cret"}); r.status != 200 {
		t.Errorf("static token = %d", r.status)
	}
	if r := f.get(f.reg+"/v2/library/alpine/tags/list", map[string]string{"Authorization": "Bearer other"}); r.status != 401 {
		t.Errorf("wrong static token = %d", r.status)
	}
	if n := f.stub.Counts().TokenRequests(""); n != 0 {
		t.Errorf("a static token runs no handshake, the realm saw %d requests", n)
	}
}

// The three hosts are three servers: each serves its own paths and nothing else.
func TestHostsAreSeparate(t *testing.T) {
	f := newFixture(t, Config{})
	if f.reg != "https://"+LoopbackRegistryHost+":"+portOf(t, f.reg) || hostPort(t, f.realm) != LoopbackRealmHost+":"+portOf(t, f.reg) || hostPort(t, f.cdn) != LoopbackCDNHost+":"+portOf(t, f.reg) {
		t.Errorf("hosts = %s %s %s", f.reg, f.realm, f.cdn)
	}
	hosts := map[string]bool{hostPort(t, f.reg): true, hostPort(t, f.realm): true, hostPort(t, f.cdn): true}
	if len(hosts) != 3 {
		t.Fatalf("hosts %v are not three distinct names", hosts)
	}
	f.open()
	if r := f.get(f.realm+"/v2/", nil); r.status != 404 {
		t.Errorf("registry API on the realm host = %d", r.status)
	}
	if r := f.get(f.cdn+"/v2/", nil); r.status != 404 {
		t.Errorf("registry API on the CDN host = %d", r.status)
	}
	if r := f.get(f.reg+"/token?service="+DefaultService, nil); r.status != 404 {
		t.Errorf("realm on the registry host = %d", r.status)
	}
	if r := f.get(f.reg+"/cdn/sha256:"+strings.Repeat("0", 64), nil); r.status != 404 {
		t.Errorf("CDN path on the registry host = %d", r.status)
	}
	if r := f.get(f.reg+"/v2/", nil); r.status != 200 {
		t.Errorf("registry API on the registry host = %d", r.status)
	}
}

// Fixed hosts of the Config are matched by the Host header, with or without the default port, and appear in challenges and redirects.
func TestConfiguredHosts(t *testing.T) {
	f := newFixture(t, Config{PublicURL: "https://registry.test", RealmURL: "https://auth.test", CDNURL: "https://cdn.test"})
	if f.reg != "https://registry.test" || f.realm != "https://auth.test" || f.cdn != "https://cdn.test" {
		t.Fatalf("running hosts = %s %s %s", f.reg, f.realm, f.cdn)
	}
	addr := f.run.Addr
	asHost := func(host, path string) reply {
		return f.get("https://"+addr+path, map[string]string{"Host": host})
	}
	r := asHost("registry.test", "/v2/")
	if ch := parseChallenge(t, r.header.Get("WWW-Authenticate")); r.status != 401 || ch["realm"] != "https://auth.test/token" {
		t.Fatalf("challenge on a configured host = %d %v", r.status, ch)
	}
	if r := asHost("registry.test:443", "/v2/"); r.status != 401 {
		t.Errorf("Host with the default port = %d", r.status)
	}
	if r := asHost("auth.test", "/v2/"); r.status != 404 {
		t.Errorf("API on the realm host = %d", r.status)
	}
	if r := asHost("auth.test", "/token?service="+DefaultService); r.status != 200 {
		t.Errorf("realm on its host = %d", r.status)
	}
	f.open()
	b := f.get("https://"+addr+"/v2/library/alpine/blobs/"+firstLayer(t, f.stub, "library/alpine", "3.19"), map[string]string{"Host": "registry.test"})
	if loc := b.header.Get("Location"); b.status != 307 || !strings.HasPrefix(loc, "https://cdn.test/cdn/sha256:") {
		t.Errorf("blob redirect = %d %q", b.status, loc)
	}
}

// The three hosts are names nothing resolves: the dial override maps each host:port to the listener.
func TestDialOverrideMapsTheHosts(t *testing.T) {
	f := newFixture(t, Config{})
	port := portOf(t, f.reg)
	want := map[string]string{
		LoopbackRegistryHost + ":" + port: f.run.Addr,
		LoopbackRealmHost + ":" + port:    f.run.Addr,
		LoopbackCDNHost + ":" + port:      f.run.Addr,
	}
	got := f.run.DialOverride()
	if len(got) != 3 {
		t.Fatalf("override = %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("override[%s] = %q, want %q", k, got[k], v)
		}
	}
	fixed := newFixture(t, Config{PublicURL: "https://registry.test", RealmURL: "https://auth.test:8443", CDNURL: "https://cdn.test"})
	o := fixed.run.DialOverride()
	if o["registry.test:443"] != fixed.run.Addr || o["auth.test:8443"] != fixed.run.Addr || o["cdn.test:443"] != fixed.run.Addr {
		t.Errorf("override of configured hosts = %v", o)
	}
}
