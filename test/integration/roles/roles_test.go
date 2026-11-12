//go:build integration

// Package roles tests the binary roles api and packages (R-ARCH-001, phase 6a, E6a.1 and E6a.2) on the integration stack: one
// installation with an api replica, a packages replica and, for the contrast, a web replica (the bridge), all in-process
// through the production composition root. It covers what the unit tests of internal/app cannot: the areas a replica really
// builds, the 404 of the other role's families on real routes, a session cookie that packages ignores, and the explanation
// of a virtual repository on api over the read-only view of its remote member, which sends nothing to the upstream.
package roles

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/app"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/npmstub"
	"github.com/guided-traffic/trove/test/npmstub/corpus"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

const (
	password = "correct horse battery staple 1"
	origin   = "http://trove.test"
)

// installation is one database with a local, a remote and a virtual npm repository, a generic repository, the upstream the
// remote points to and an administrator.
type installation struct {
	t     *testing.T
	st    *testenv.Stack
	stub  *npmstub.Server
	admin string // the PAT of a user with the admin role
	pkg   *corpus.Package
	pw    string // the break-glass password file
}

func newInstallation(t *testing.T) *installation {
	t.Helper()
	stub, err := npmstub.New(npmstub.Config{})
	if err != nil {
		t.Fatal(err)
	}
	ss, err := npmstub.NewSelfSigned("localhost", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	run, err := stub.Start("127.0.0.1:0", ss.ServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.Close() })
	dir := t.TempDir()
	ca := filepath.Join(dir, "stub-ca.pem")
	if err := os.WriteFile(ca, ss.CAPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	st := testenv.NewStack(t, testenv.StackOptions{Name: "roles", Valkey: true, Env: []string{
		"TROVE_PROXY_OUTBOUND_TLS_CA_FILES=[" + strconv.Quote(ca) + "]",
		`TROVE_PROXY_OUTBOUND_ADDRESS_POLICY_ALLOW_CIDRS=["127.0.0.1/32"]`,
		"TROVE_VULNERABILITIES_SOURCES_OSV_MODE=off",
	}})
	in := &installation{t: t, st: st, stub: stub, pw: testenv.WriteFile(t, dir, "break-glass", password)}
	for _, p := range corpus.Default().Packages {
		if !strings.HasPrefix(p.Name, "@") && len(p.Versions) >= 2 && len(p.Versions) < 30 {
			in.pkg = p
			break
		}
	}
	if in.pkg == nil {
		t.Fatal("the corpus has no package")
	}
	remote := map[string]any{"remote": map[string]any{
		"url": run.URL + "/", "extraHosts": []any{}, "authMode": "none", "username": "", "password": nil,
		"clientCertificate": nil, "headers": []any{}, "forwardAuthOnRedirect": false, "outboundProxy": map[string]any{"mode": "direct"},
		"connectTimeoutMs": 5000, "readTimeoutMs": 15000, "sourceNetwork": nil, "cacheFillAction": "read",
		"cache": map[string]any{
			"storeArtifacts": true, "metadataTtlSeconds": 3600, "metadataRefreshTimeoutSeconds": 10, "notFoundTtlSeconds": 60,
			"maxStaleSeconds": 0, "unusedRetentionHours": 0,
		},
		"integrity":    map[string]any{"checksumPolicy": "verify-if-available", "blockUnexpectedContentTypes": true, "allowedContentTypes": []any{}},
		"availability": map[string]any{"mode": "online", "failureThreshold": 3, "circuitOpenSeconds": 30},
		"limits":       map[string]any{"maxArtifactBytes": nil, "maxMetadataBytes": nil, "maxConcurrentFetches": 16},
	}}
	raw, err := json.Marshal(remote)
	if err != nil {
		t.Fatal(err)
	}
	st.Repository("gen-local", "local", "generic", `{}`)
	st.Repository("npm-local", "local", "npm", `{"local":{"autoClaim":false,"packageOwnership":"enforce"}}`)
	st.Repository("npm-remote", "remote", "npm", string(raw))
	st.Repository("npm-all", "virtual", "npm", `{}`)
	for i, m := range []string{"npm-local", "npm-remote"} {
		st.Exec(`INSERT INTO virtual_members (virtual_id, position, member_id)
			SELECT v.id, $2, m.id FROM repositories v, repositories m WHERE v.key = $1 AND m.key = $3`, "npm-all", i, m)
	}
	ops := st.User("ops")
	st.Bind("admin", ops)
	in.admin = st.Token(ops, scope.Scope{Inherit: true, Admin: true})
	return in
}

// start brings up a replica of a role; the replicas share the database. Only an api or web replica gets the break-glass
// secret, as the chart mounts it (R-HA-030).
func (in *installation) start(role string) *testenv.Replica {
	in.t.Helper()
	opts := testenv.ReplicaOptions{Name: role + "-" + testenv.RandHex(2), Role: role}
	if role != "packages" {
		opts.Env = []string{"TROVE_AUTH_BREAK_GLASS_ENABLED=true", "TROVE_AUTH_BREAK_GLASS_PASSWORD_FILE=" + in.pw}
	}
	return in.st.Start(in.t, opts)
}

func (in *installation) get(r *testenv.Replica, path, token string, hdr ...string) testenv.Answer {
	in.t.Helper()
	return in.st.Do(r, http.MethodGet, path, token, nil, hdr...)
}

// probe is a request to the default endpoint that is not validated against the management API's document: a route of the
// other role answers a 404 that the document does not list for it.
func (in *installation) probe(r *testenv.Replica, method, path, token string, hdr ...string) testenv.Answer {
	in.t.Helper()
	return in.st.DoAt(r.Addr("default"), "probe."+testenv.DefaultHost, method, path, token, nil, hdr...)
}

// TestR_ARCH_001_ReplicasBuildTheAreasOfTheirRole: a replica of each role starts on the real infrastructure and has exactly
// the areas of the role table, in step order (E6a.1).
func TestR_ARCH_001_ReplicasBuildTheAreasOfTheirRole(t *testing.T) {
	in := newInstallation(t)
	for _, role := range []string{"api", "packages", "web"} {
		r := in.start(role)
		sub, err := app.SubsystemsFor(role)
		if err != nil {
			t.Fatal(err)
		}
		want := make([]string, 0, len(sub.Areas))
		for _, a := range sub.Areas {
			want = append(want, string(a))
		}
		if got := r.App.Areas(); !slices.Equal(got, want) {
			t.Errorf("%s built %v, want %v", role, got, want)
		}
		if got := r.App.EndpointNames(); !slices.Equal(got, []string{"default"}) {
			t.Errorf("%s serves the endpoints %v", role, got)
		}
		built := map[string]bool{}
		for _, a := range r.App.Areas() {
			built[a] = true
		}
		switch role {
		case "packages":
			for _, a := range []string{"sessions", "login", "ci_exchange", "packages-api", "vuln-api"} {
				if built[a] {
					t.Errorf("packages built %s", a)
				}
			}
			if !built["proxy-engine"] || !built["formathost"] || !built["auth"] {
				t.Errorf("packages lacks an area of its families: %v", r.App.Areas())
			}
		case "api":
			for _, a := range []string{"proxy-engine", "formathost"} {
				if built[a] {
					t.Errorf("api built %s", a)
				}
			}
			if !built["sessions"] || !built["login"] || !built["ci_exchange"] || !built["packages-api"] || !built["vuln-api"] || !built["virtual-engine"] {
				t.Errorf("api lacks an area of its families: %v", r.App.Areas())
			}
		}
	}
}

// allowedOrigin is the origin the replicas of the family test admit for CORS (api.cors.allowed_origins, R-API-018).
const allowedOrigin = "https://portal.example.com"

// seen is what a client sees of an answer, without what names the request: the status, the header without X-Request-Id
// and Date (and Content-Length, which follows the problem's instance member, the request path), and the body without its
// per-request members.
func seen(t *testing.T, a testenv.Answer) string {
	t.Helper()
	h := a.Header.Clone()
	for _, k := range []string{"X-Request-Id", "Date", "Content-Length"} {
		h.Del(k)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d\n", a.Status)
	for _, k := range slices.Sorted(maps.Keys(h)) {
		fmt.Fprintf(&b, "%s: %q\n", k, h[k])
	}
	if json.Valid(a.Body) {
		b.WriteString(a.Problem(t))
	} else {
		b.Write(a.Body)
	}
	return b.String()
}

// TestR_ARCH_001_AFamilyOfTheOtherRoleIsAnUnknownPath: on the real routes, a request of a family the role does not serve
// is the request to /does-not-exist with the same method and headers, status, header and body alike apart from the request
// ID and the date (R-ARCH-005, R-EP-005, Q-ARCH-302, Q-ARCH-306): before authentication (no credential, a preflight of an
// allowed origin, a browser) and after it (a token, a rejected token, a live session with and without its CSRF token, an
// ended session), with anonymous access off and on. No path rule of the chain - the headers of the management API, CORS,
// the rate-limit classes, the session cookie's path rule, the challenge, the routes that are always open, the hard limit
// on unsafe anonymous requests to /api/v1 - tells the other role's families apart from an unknown path.
func TestR_ARCH_001_AFamilyOfTheOtherRoleIsAnUnknownPath(t *testing.T) {
	in := newInstallation(t)
	// CORS and the general rate limits are on: their headers are what an unserved family must not carry.
	edge := []string{`TROVE_API_CORS_ALLOWED_ORIGINS=["` + allowedOrigin + `"]`, "TROVE_API_RATE_LIMITS_ENABLED=true"}
	replicas := map[string]*testenv.Replica{}
	for _, role := range []string{"api", "packages"} {
		opts := testenv.ReplicaOptions{Name: role + "-" + testenv.RandHex(2), Role: role, Env: slices.Clone(edge)}
		if role == "api" {
			opts.Env = append(opts.Env, "TROVE_AUTH_BREAK_GLASS_ENABLED=true", "TROVE_AUTH_BREAK_GLASS_PASSWORD_FILE="+in.pw)
		}
		replicas[role] = in.st.Start(t, opts)
	}
	live, csrf := in.signIn(replicas["api"])
	ended, endedCSRF := in.signIn(replicas["api"])
	if out := in.st.Do(replicas["api"], http.MethodPost, "/auth/logout", "", nil, "Cookie", ended, "Origin", origin, session.HeaderCSRF, endedCSRF); out.Status != http.StatusOK && out.Status != http.StatusNoContent {
		t.Fatalf("logout: %d %s", out.Status, out.Body)
	}
	if got := in.get(replicas["api"], "/api/v1/users/me/preferences", "", "Cookie", ended); got.Status != http.StatusUnauthorized || got.Code(t) != "session_expired" {
		t.Fatalf("the ended session on api: %d %s", got.Status, got.Body)
	}
	rejected, err := token.Generate(token.TypePAT)
	if err != nil {
		t.Fatal(err)
	}
	callers := map[string][]string{
		"no credential":                         nil,
		"a token":                               {"Authorization", "Bearer " + in.admin},
		"a rejected token":                      {"Authorization", "Bearer " + rejected},
		"a live session":                        {"Cookie", live},
		"a live session with its CSRF token":    {"Cookie", live, "Origin", origin, session.HeaderCSRF, csrf},
		"an ended session":                      {"Cookie", ended},
		"a browser":                             {"Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "document"},
		"an allowed origin with a token":        {"Origin", allowedOrigin, "Authorization", "Bearer " + in.admin},
		"the preflight of an allowed origin":    {"Origin", allowedOrigin, "Access-Control-Request-Method", "GET"},
		"a preflight with a token in the clear": {"Origin", allowedOrigin, "Access-Control-Request-Method", "GET", "Authorization", "Bearer " + in.admin},
	}
	type probe struct{ method, path string }
	cases := map[string][]probe{
		"packages": {
			{"GET", "/ui/"},
			{"GET", "/"},
			{"GET", "/auth/oidc/corp/start"},
			{"GET", "/api/v1/system/ping"},
			{"GET", "/api/v1/repositories"},
			{"POST", "/api/v1/projects/it/repositories"},
			{"POST", "/api/v1/auth/oidc/token"},
			{"GET", "/api/v1/auth/login-options"},
			{"GET", "/api/v1/projects/it/repositories/gen-local/content/x"},
			{"PUT", "/api/v1/projects/it/repositories/gen-local/content/x"},
			{"OPTIONS", "/api/v1/system/ping"},
			{"OPTIONS", "/api/v1/repositories"},
		},
		"api": {
			{"GET", "/api/npm/it/npm-all/" + "left-pad"},
			{"GET", "/api/npm/it/npm-local/@acme%2fsdk"},
			{"GET", "/files/it/gen-local/x"},
			{"PUT", "/files/it/gen-local/x"},
			{"DELETE", "/files/it/gen-local/x"},
			{"GET", "/v2/"},
			{"GET", "/-/v1/login"},
			{"OPTIONS", "/files/it/gen-local/x"},
		},
	}
	compare := func(anonymous string) {
		t.Helper()
		for role, probes := range cases {
			r := replicas[role]
			for name, hdr := range callers {
				for _, p := range probes {
					want := seen(t, in.probe(r, p.method, "/does-not-exist", "", hdr...))
					if got := seen(t, in.probe(r, p.method, p.path, "", hdr...)); got != want {
						t.Errorf("anonymous requests %s, %s on %s, %s: %s %s\n got %s\nwant %s", anonymous, name, role, p.method, p.path, p.method, got, want)
					}
				}
			}
		}
	}
	// A request without a credential is anonymous on a default endpoint, and an unknown path is a 404 for it as for a token:
	// the other role's families must not tell themselves apart from it by their own answer.
	for role, r := range replicas {
		if got := in.probe(r, http.MethodGet, "/does-not-exist", ""); got.Status != http.StatusNotFound {
			t.Fatalf("%s: an unknown path without a credential answers %d %s", role, got.Status, got.Body)
		}
		if got := in.probe(r, http.MethodGet, "/does-not-exist", in.admin); got.Status != http.StatusNotFound {
			t.Fatalf("%s: an unknown path with a token answers %d %s", role, got.Status, got.Body)
		}
	}
	compare("admitted")

	// The controls: the rules the comparison is about are on, where the family is served.
	if got := in.probe(replicas["api"], http.MethodOptions, "/api/v1/system/ping", "", callers["the preflight of an allowed origin"]...); got.Status != http.StatusNoContent || got.Header.Get("Access-Control-Allow-Origin") != allowedOrigin {
		t.Errorf("the preflight on api: %d %v", got.Status, got.Header)
	}
	if got := in.probe(replicas["api"], http.MethodGet, "/api/v1/system/ping", in.admin); got.Header.Get("RateLimit-Policy") == "" || got.Header.Get("Content-Security-Policy") == "" {
		t.Errorf("the management API on api carries no rate limit or API header: %v", got.Header)
	}
	if got := in.probe(replicas["packages"], http.MethodGet, "/files/it/gen-local/x", ""); got.Header.Get("Www-Authenticate") != `Basic realm="trove"` {
		t.Errorf("/files/ on packages without a credential: %d %v", got.Status, got.Header)
	}
	if got := in.probe(replicas["api"], http.MethodGet, "/api/v1/users/me/preferences", "", "Cookie", ended); got.Code(t) != "session_expired" {
		t.Errorf("the ended session on the management API of api: %d %s", got.Status, got.Body)
	}
}

// TestR_EP_010_PackagesIgnoresASessionCookie: the session of a signed-in user is valid on api and on web and counts for
// nothing on packages, which has no session store: the request is one without a credential (E6a.2, R-ARCH-001).
func TestR_EP_010_PackagesIgnoresASessionCookie(t *testing.T) {
	in := newInstallation(t)
	api, packages, web := in.start("api"), in.start("packages"), in.start("web")

	login := in.st.Do(api, http.MethodPost, "/auth/local/login", "", map[string]string{"username": "admin", "password": password}, "Origin", origin)
	if login.Status != http.StatusNoContent {
		t.Fatalf("local login on api: %d %s", login.Status, login.Body)
	}
	var cookie string
	for _, c := range (&http.Response{Header: login.Header}).Cookies() {
		if c.Name == session.CookieSession {
			cookie = c.Name + "=" + c.Value
		}
	}
	if cookie == "" {
		t.Fatalf("the login set no session cookie: %v", login.Header.Values("Set-Cookie"))
	}

	if got := in.get(api, "/api/v1/users/me/preferences", "", "Cookie", cookie); got.Status != http.StatusOK {
		t.Fatalf("the session on api: %d %s", got.Status, got.Body)
	}
	// A file that does not exist, asked by a caller who is known: 404. Asked by none: 401.
	const file = "/files/it/gen-local/x"
	if got := in.get(web, file, "", "Cookie", cookie); got.Status != http.StatusNotFound {
		t.Errorf("the session on web: %d %s, want the 404 of a missing file", got.Status, got.Body)
	}
	got := in.get(packages, file, "", "Cookie", cookie)
	// The default endpoint admits a request without a credential (R-EP-010), so the cookie that packages ignores leaves one that the
	// decision point refuses for a file that nobody makes public.
	if got.Status != http.StatusUnauthorized || got.Code(t) != "unauthenticated" {
		t.Errorf("the session on packages: %d %s, want 401 unauthenticated", got.Status, got.Body)
	}
	if without := in.get(packages, file, ""); without.Status != got.Status || without.Problem(t) != got.Problem(t) {
		t.Errorf("a request with the cookie differs from one without: %s against %s", got.Body, without.Body)
	}
	// A cookie is no credential that could be refused or that could shadow a token: the token wins on packages.
	if tok := in.get(packages, file, in.admin, "Cookie", cookie); tok.Status != http.StatusNotFound {
		t.Errorf("a token and a cookie on packages: %d %s, want the 404 of a missing file", tok.Status, tok.Body)
	}
	if bogus := in.get(packages, file, in.admin, "Cookie", session.CookieSession+"=garbage"); bogus.Status != http.StatusNotFound {
		t.Errorf("a token and a garbage cookie on packages: %d %s", bogus.Status, bogus.Body)
	}
}

// TestQ_ARCH_294_ApiExplainsAVirtualRepositoryWithoutContactingTheUpstream: the explanation (R-REPO-019) on api reads the
// remote member's state through the read-only view: a miss before packages filled the cache, a hit after, and the upstream
// sees only the one request packages made.
func TestQ_ARCH_294_ApiExplainsAVirtualRepositoryWithoutContactingTheUpstream(t *testing.T) {
	in := newInstallation(t)
	api, packages := in.start("api"), in.start("packages")
	explain := func() map[string]string {
		t.Helper()
		a := in.get(api, "/api/v1/projects/it/repositories/npm-all/resolution?path="+in.pkg.Name, in.admin)
		if a.Status != http.StatusOK {
			t.Fatalf("explanation: %d %s", a.Status, a.Body)
		}
		var body struct {
			Members []struct{ Key, Decision string } `json:"members"`
		}
		if err := json.Unmarshal(a.Body, &body); err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, m := range body.Members {
			out[m.Key] = m.Decision
		}
		return out
	}

	before := explain()
	if before["npm-local"] != "miss" || before["npm-remote"] != "miss" {
		t.Fatalf("before the fill: %v", before)
	}
	if n := in.stub.Counts().Total(); n != 0 {
		t.Fatalf("the explanation made the upstream see %d requests", n)
	}

	fill := in.get(packages, "/api/npm/it/npm-all/"+in.pkg.Name, in.admin)
	if fill.Status != http.StatusOK {
		t.Fatalf("the fill through packages: %d %.300s", fill.Status, fill.Body)
	}
	filled := in.stub.Counts().Total()
	if filled == 0 {
		t.Fatal("packages did not fill from the upstream")
	}

	after := explain()
	if after["npm-remote"] != "hit" {
		t.Errorf("after the fill: %v, want a hit on npm-remote", after)
	}
	if n := in.stub.Counts().Total(); n != filled {
		t.Errorf("the explanation on api made the upstream see %d requests more", n-filled)
	}
	// The same read-only view is what the remote's state shows to the other operations of api: the API of the virtual
	// repository's members works without any engine.
	if a := in.get(api, "/api/v1/projects/it/repositories/npm-all", in.admin); a.Status != http.StatusOK {
		t.Errorf("reading the repository on api: %d %s", a.Status, a.Body)
	}
}

// signIn logs the break-glass administrator in on a replica of the api family and returns the cookies and the CSRF token.
func (in *installation) signIn(r *testenv.Replica) (cookies, csrf string) {
	in.t.Helper()
	login := in.st.Do(r, http.MethodPost, "/auth/local/login", "", map[string]string{"username": "admin", "password": password}, "Origin", origin)
	if login.Status != http.StatusNoContent {
		in.t.Fatalf("local login on %s: %d %s", r.Role, login.Status, login.Body)
	}
	var sess, token string
	for _, c := range (&http.Response{Header: login.Header}).Cookies() {
		switch c.Name {
		case session.CookieSession:
			sess = c.Name + "=" + c.Value
		case session.CookieCSRF:
			token = c.Value
		}
	}
	if sess == "" || token == "" {
		in.t.Fatalf("the login set no session or CSRF cookie: %v", login.Header.Values("Set-Cookie"))
	}
	return sess + "; " + session.CookieCSRF + "=" + token, token
}

// TestQ_UI_106_ContentRoutesAreOfTheApiFamily: the UI's content routes (R-API-025) are served by the roles of the api family
// from the files service over the serve-check host of Q-ARCH-301, for the session only, and are an unknown path on packages,
// which serves /files/ for tokens (R-ARCH-005).
func TestQ_UI_106_ContentRoutesAreOfTheApiFamily(t *testing.T) {
	in := newInstallation(t)
	api, packages, web := in.start("api"), in.start("packages"), in.start("web")
	cookie, csrf := in.signIn(api)
	const path = "/api/v1/projects/it/repositories/gen-local/content/ui/a%20b.txt"

	put := in.st.Do(api, http.MethodPut, path, "", "hello", "Cookie", cookie, "Origin", origin, session.HeaderCSRF, csrf, "Content-Type", "text/plain")
	if put.Status != http.StatusCreated {
		t.Fatalf("upload through the content route on api: %d %s", put.Status, put.Body)
	}
	for _, r := range []*testenv.Replica{api, web} {
		if got := in.get(r, path, "", "Cookie", cookie); got.Status != http.StatusOK || string(got.Body) != "hello" {
			t.Errorf("download through the content route on %s: %d %q", r.Role, got.Status, got.Body)
		}
	}
	// A token is answered like an unknown path on api, because the content routes are for the session.
	tokenGet := in.probe(api, http.MethodGet, path, in.admin)
	if tokenGet.Status != http.StatusNotFound {
		t.Errorf("a token on the content route of api: %d %s", tokenGet.Status, tokenGet.Body)
	}
	// So is a method the routes do not offer: no 405 with Allow tells a token caller that the path exists (Q-ARCH-307). The
	// session's DELETE is the 405, since the UI deletes through the items operation.
	for _, m := range []string{http.MethodDelete, http.MethodPost, http.MethodPatch} {
		if got := in.probe(api, m, path, in.admin); got.Status != http.StatusNotFound || got.Header.Get("Allow") != "" || got.Problem(t) != tokenGet.Problem(t) {
			t.Errorf("%s with a token on the content route of api: %d Allow %q %s", m, got.Status, got.Header.Get("Allow"), got.Body)
		}
	}
	if got := in.probe(api, http.MethodDelete, path, "", "Cookie", cookie, "Origin", origin, session.HeaderCSRF, csrf); got.Status != http.StatusMethodNotAllowed || got.Header.Get("Allow") != "GET, HEAD, PUT" {
		t.Errorf("DELETE of the session on the content route of api: %d Allow %q %s", got.Status, got.Header.Get("Allow"), got.Body)
	}
	// What the content route stored is the file /files/ serves on packages, which has no content route.
	if got := in.get(packages, "/files/it/gen-local/ui/a%20b.txt", in.admin); got.Status != http.StatusOK || string(got.Body) != "hello" {
		t.Errorf("/files/ on packages: %d %q", got.Status, got.Body)
	}
	unknown := in.probe(packages, http.MethodGet, "/does-not-exist", in.admin)
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPut} {
		got := in.probe(packages, method, path, in.admin)
		if got.Status != http.StatusNotFound || (method != http.MethodHead && got.Problem(t) != unknown.Problem(t)) {
			t.Errorf("%s on the content route of packages: %d %s, want the unknown path %d %s", method, got.Status, got.Body, unknown.Status, unknown.Body)
		}
	}
}

// TestQ_OPS_165_FetchShareCountsTheReplicasThatFill: the proxy's fetch share divides by the live replicas that fill client
// misses, whatever their role and pool (R-REPO-040, Q-ARCH-297): a second packages replica and a web replica raise it, an api
// replica does not.
func TestQ_OPS_165_FetchShareCountsTheReplicasThatFill(t *testing.T) {
	in := newInstallation(t)
	quick := []string{"TROVE_SYSTEM_HEARTBEAT_INTERVAL=1s", "TROVE_SYSTEM_HEARTBEAT_STALE_AFTER=5s"}
	start := func(role, pool string) *testenv.Replica {
		opts := testenv.ReplicaOptions{Name: role + "-" + testenv.RandHex(2), Role: role, Env: append([]string{}, quick...)}
		if pool != "" {
			opts.Env = append(opts.Env, "TROVE_POOL_NAME="+pool)
		}
		if role != "packages" {
			opts.Env = append(opts.Env, "TROVE_AUTH_BREAK_GLASS_ENABLED=true", "TROVE_AUTH_BREAK_GLASS_PASSWORD_FILE="+in.pw)
		}
		return in.st.Start(t, opts)
	}
	waitFor := func(r *testenv.Replica, want int) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for {
			got := r.App.FetchShareReplicas()
			if got == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s counts %d replicas that fill, want %d", r.Name, got, want)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	p1 := start("packages", "")
	waitFor(p1, 1)
	if api := start("api", ""); api.App.FetchShareReplicas() != 0 {
		t.Errorf("api has a fetch share: %d", api.App.FetchShareReplicas())
	}
	waitFor(p1, 1) // an api replica does not fill
	start("packages", "bulk")
	waitFor(p1, 2) // a pool counts
	start("web", "")
	waitFor(p1, 3)
}
