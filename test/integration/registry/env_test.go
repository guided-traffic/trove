//go:build integration

// Package registry tests the registry root below /v2/ (R-OCI-001 mount, R-OCI-003, R-OCI-004, R-OCI-005, R-OCI-017, R-OCI-029 mount) against the integration
// stack with a full in-process replica: the mount through the enforcement point, the dispatcher's handler, the version check,
// the catalog, a bound host, and the walk of the enforcement point through the endpoint's real handler. The handlers of the OCI
// routes are the format's skeleton (501); what is tested is the address, the match, the checks and the shapes before them.
package registry

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/registrytoken"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

// The hosts of the mirror endpoint: mirrorHost is bound to an OCI repository, plainHost to a repository of another format, which
// behaves as no binding (R-EP-006).
const (
	mirrorHost = "mirror.trove.test"
	plainHost  = "plain.trove.test"
	// filesHost belongs to an endpoint whose surface holds files only.
	filesHost = "files.trove.test"
)

var local = qname.Repo{Project: "web", Key: "docker-local"}

// endpointsJSON is server.endpoints: one further endpoint with two hosts, serving the packages surface.
var endpointsJSON = fmt.Sprintf(`{"mirror":{"listen":"127.0.0.1:0","base_url":"http://%s","hosts":[%q],"surface":["packages"],"exposure":"all",`+
	`"auth":{"token_types":["pat","sat"],"bound_tokens_only":false},"host_roots":{%q:"web/docker-local",%q:"web/files"}},`+
	`"filesonly":{"listen":"127.0.0.1:0","base_url":"http://%s","surface":["files"],"exposure":"all",`+
	`"auth":{"token_types":["pat","sat"],"bound_tokens_only":false}}}`, mirrorHost, plainHost, mirrorHost, plainHost, filesHost)

// env is one installation with the people and repositories the tests use, and one replica.
type env struct {
	t   *testing.T
	st  *testenv.Stack
	rep *testenv.Replica

	admin, reader, scoped, nobody, pusher string
	rita                                  testenv.Person
}

// newEnv seeds and starts the installation; tweak, when set, changes the options of the replica (more routes before the route
// table is checked, for instance).
func newEnv(t *testing.T, tweak func(*env, *testenv.ReplicaOptions)) *env {
	t.Helper()
	st := testenv.NewStack(t, testenv.StackOptions{Name: "registry"})
	e := &env{t: t, st: st}
	rita, reader, scoped, nobody, pusher := st.User("rita"), st.User("reader"), st.User("scoped"), st.User("nobody"), st.User("pusher")
	e.rita = rita
	st.Bind("admin", rita)
	e.pusher = st.Token(pusher, scope.Default())
	e.admin = st.Token(rita, scope.Scope{Inherit: true, Admin: true})
	e.reader = st.Token(reader, scope.Default())
	e.scoped = st.Token(scoped, scope.Scope{Repositories: []scope.Grant{{
		Repositories: []string{"web/docker-local"}, Packages: scope.Patterns{Include: []string{"team/*"}}, Actions: []string{"read"},
	}}})
	e.nobody = st.Token(nobody, scope.Default())
	st.RepositoryIn("web", "docker-local", "local", "oci", "{}")
	st.RepositoryIn("web", "team-local", "local", "oci", "{}")
	st.RepositoryIn("web", "files", "local", "generic", "{}")
	st.Policy("oci-reader", []string{"read"}, []string{"web/docker-local"}, reader)
	st.Policy("oci-push-read", []string{"read"}, []string{"web/docker-local"}, pusher)
	st.Policy("oci-push-deploy", []string{"deploy"}, []string{"web/docker-local"}, pusher)
	st.Policy("oci-scoped", []string{"read"}, []string{"web/docker-local", "web/team-local"}, scoped)
	o := testenv.ReplicaOptions{Name: "registry-a", Endpoints: endpointsJSON}
	if tweak != nil {
		tweak(e, &o)
	}
	e.rep = st.Start(t, o)
	return e
}

// handler is the whole handler of an endpoint: the chain around the router, exactly what its listener serves.
func (e *env) handler(endpoint string) http.Handler {
	e.t.Helper()
	h := e.rep.App.EndpointHandler(endpoint)
	if h == nil {
		e.t.Fatalf("the replica serves no endpoint %s", endpoint)
	}
	return h
}

// answer is one recorded response.
type answer struct {
	*httptest.ResponseRecorder
}

func (a answer) code(t *testing.T) string {
	t.Helper()
	var b struct {
		Errors []struct {
			Code string `json:"code"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(a.Body.Bytes(), &b); err != nil || len(b.Errors) != 1 {
		t.Fatalf("answer %d is no OCI error body: %v %q", a.Code, err, a.Body.String())
	}
	return b.Errors[0].Code
}

// do sends a request to an endpoint's handler; host "" is the default endpoint's host, a bearer token "" sends no credential.
func (e *env) do(endpoint, host, method, path, bearer string) answer {
	e.t.Helper()
	r := httptest.NewRequestWithContext(e.t.Context(), method, path, nil)
	r.RemoteAddr = "203.0.113.7:4711"
	r.Host = host
	if r.Host == "" {
		r.Host = testenv.DefaultHost
	}
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	e.handler(endpoint).ServeHTTP(w, r)
	return answer{w}
}

// mint asks the endpoint's token service, with the trove token, for a registry token with the grant, as a client does: the scope
// entries name the images the way the endpoint's host reads them (the full name on an unbound host, the image alone on a bound
// one).
func (e *env) mint(endpoint, host, troveToken string, g registrytoken.Grant, bound bool) string {
	e.t.Helper()
	q := url.Values{}
	for _, en := range g.Entries {
		name := en.Repo.Project + "/" + en.Repo.Key + "/" + en.Name
		if bound {
			name = en.Name
		}
		q.Add("scope", "repository:"+name+":"+en.Actions.String())
	}
	if g.Catalog {
		q.Add("scope", "registry:catalog:*")
	}
	a := e.do(endpoint, host, http.MethodGet, "/v2/_token?"+q.Encode(), troveToken)
	if a.Code != http.StatusOK {
		e.t.Fatalf("token request %s: %d %s", q.Encode(), a.Code, a.Body.String())
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(a.Body.Bytes(), &body); err != nil || body.Token == "" {
		e.t.Fatalf("token answer %q: %v", a.Body.String(), err)
	}
	return body.Token
}

var requestSeries = regexp.MustCompile(`(?m)^trove_http_requests_total\{[^}]*handler="([^"]+)"[^}]*\} (\S+)$`)

// handlers reads the requests the replica counted per handler label: a request is labelled with the route the dispatcher served.
func (e *env) handlers() map[string]float64 {
	e.t.Helper()
	out := map[string]float64{}
	for _, m := range requestSeries.FindAllStringSubmatch(e.rep.Metrics(e.t), -1) {
		if !strings.Contains(m[0], `listener="client"`) {
			continue
		}
		v, err := strconv.ParseFloat(m[2], 64)
		if err != nil {
			e.t.Fatalf("metric %q: %v", m[0], err)
		}
		out[m[1]] += v
	}
	return out
}

// served runs do and returns the handler label whose count it raised, "" when it raised none or more than one.
func (e *env) served(do func()) string {
	e.t.Helper()
	before := e.handlers()
	do()
	var hit []string
	for h, n := range e.handlers() {
		if n > before[h] {
			hit = append(hit, h)
		}
	}
	if len(hit) != 1 {
		e.t.Logf("a request raised the handlers %v", hit)
		return ""
	}
	return hit[0]
}
