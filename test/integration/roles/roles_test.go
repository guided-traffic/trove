//go:build integration

// Package roles tests the binary roles api and packages (R-ARCH-001, phase 6a, E6a.1 and E6a.2) on the integration stack: one
// installation with an api replica, a packages replica and, for the contrast, a web replica (the bridge), all in-process
// through the production composition root. It covers what the unit tests of internal/app cannot: the areas a replica really
// builds, the 404 of the other role's families on real routes, a session cookie that packages ignores, and the explanation
// of a virtual repository on api over the read-only view of its remote member, which sends nothing to the upstream.
package roles

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/app"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/auth/session"
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
func (in *installation) probe(r *testenv.Replica, method, path, token string) testenv.Answer {
	in.t.Helper()
	return in.st.DoAt(r.Addr("default"), "probe."+testenv.DefaultHost, method, path, token, nil)
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

// TestR_ARCH_001_AFamilyOfTheOtherRoleIsAnUnknownPath: on the real routes, a request of a family the role does not serve
// answers the problem of GET /does-not-exist, apart from the request ID (R-ARCH-005, R-EP-005): the 404 for a caller with a
// token and the 401 of the chain for one without, with anonymous access off.
func TestR_ARCH_001_AFamilyOfTheOtherRoleIsAnUnknownPath(t *testing.T) {
	in := newInstallation(t)
	type probe struct{ method, path string }
	cases := map[string][]probe{
		"packages": {
			{"GET", "/ui/"},
			{"GET", "/"},
			{"GET", "/auth/oidc/corp/start"},
			{"GET", "/api/v1/system/ping"},
			{"GET", "/api/v1/repositories"},
			{"POST", "/api/v1/auth/oidc/token"},
		},
		"api": {
			{"GET", "/api/npm/npm-all/" + "left-pad"},
			{"GET", "/api/npm/npm-local/@acme%2fsdk"},
			{"GET", "/files/gen-local/x"},
			{"PUT", "/files/gen-local/x"},
			{"GET", "/v2/"},
		},
	}
	for role, probes := range cases {
		r := in.start(role)
		for _, token := range []string{"", in.admin} {
			unknown := in.probe(r, http.MethodGet, "/does-not-exist", token)
			// With anonymous access off a request without a credential is refused before the router (401), for an unknown
			// path as for any other: the other role's families must not tell themselves apart from it by that either.
			if want := map[bool]int{false: http.StatusUnauthorized, true: http.StatusNotFound}[token != ""]; unknown.Status != want {
				t.Fatalf("%s: an unknown path answers %d %s, want %d", role, unknown.Status, unknown.Body, want)
			}
			for _, p := range probes {
				got := in.probe(r, p.method, p.path, token)
				if got.Status != unknown.Status || got.Problem(t) != unknown.Problem(t) || got.Header.Get("Content-Type") != unknown.Header.Get("Content-Type") {
					t.Errorf("%s %s on %s (token %v): %d %s\nwant %d %s", p.method, p.path, role, token != "", got.Status, got.Body, unknown.Status, unknown.Body)
				}
			}
		}
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
	const file = "/files/gen-local/x"
	if got := in.get(web, file, "", "Cookie", cookie); got.Status != http.StatusNotFound {
		t.Errorf("the session on web: %d %s, want the 404 of a missing file", got.Status, got.Body)
	}
	got := in.get(packages, file, "", "Cookie", cookie)
	if got.Status != http.StatusUnauthorized || got.Code(t) != "credentials_required" {
		t.Errorf("the session on packages: %d %s, want 401 credentials_required", got.Status, got.Body)
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
		a := in.get(api, "/api/v1/repositories/npm-all/resolution?path="+in.pkg.Name, in.admin)
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

	fill := in.get(packages, "/api/npm/npm-all/"+in.pkg.Name, in.admin)
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
	if a := in.get(api, "/api/v1/repositories/npm-all", in.admin); a.Status != http.StatusOK {
		t.Errorf("reading the repository on api: %d %s", a.Status, a.Body)
	}
}
