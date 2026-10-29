package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

type allowAll struct{}

func (allowAll) Require(context.Context, string) error { return nil }

type denyAnonymous struct{}

func (denyAnonymous) Require(ctx context.Context, _ string) error {
	if httpserver.PrincipalFrom(ctx).Anonymous {
		return errs.E(errs.Unauthenticated, errs.CodeUnauthenticated, "credentials required")
	}
	return nil
}

type fakeReplicas []gen.Replica

func (f fakeReplicas) Replicas(context.Context) ([]gen.Replica, error) { return f, nil }

type fakeFacts struct{ f Facts }

func (f fakeFacts) Facts(context.Context) (Facts, error) { return f.f, nil }

func ptr[T any](v T) *T { return &v }

func replica(name, role, node string, state gen.ReplicaState, lost bool, degraded ...string) gen.Replica {
	return gen.Replica{
		Name: name, InstanceUid: "uid-" + name, Version: "0.1.0", Revision: "abc", Roles: []gen.ReplicaRoles{gen.ReplicaRoles(role)},
		State: state, Lost: lost, StartedAt: time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC), HeartbeatAt: time.Date(2026, 10, 29, 8, 1, 0, 0, time.UTC),
		Leases: []struct {
			Name  string    `json:"name"`
			Since time.Time `json:"since"`
		}{}, Node: ptr(node), Checks: map[string]gen.CheckResult{"database": {Status: gen.CheckResultStatusPass}}, Degraded: degraded,
	}
}

type setup struct {
	handler http.Handler
	router  *httpserver.Router
	doc     *Document
	ready   *bool
}

func newSetup(t *testing.T, opts func(*Options), authn func(*http.Request) (httpserver.Principal, error)) setup {
	t.Helper()
	doc, err := LoadDocument(EmbeddedYAML())
	if err != nil {
		t.Fatal(err)
	}
	ready := true
	o := Options{
		Document: doc, Identity: Identity{Replica: "trove-web-1", Role: "web"}, Ready: func() bool { return ready },
		Access: allowAll{}, Replicas: fakeReplicas{replica("trove-web-1", "web", "n1", gen.Ready, false)}, Facts: fakeFacts{},
	}
	if opts != nil {
		opts(&o)
	}
	router := httpserver.NewClientRouter()
	New(o).Register(router)
	res, err := httpserver.NewResolver(nil, "https://trove.example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	chain := httpserver.Chain{Router: router, Resolver: res, Authenticate: authn, Replica: "trove-web-1", ReplicaHeader: httpserver.ReplicaAuthenticated}
	return setup{handler: chain.Handler(), router: router, doc: doc, ready: &ready}
}

func (s setup) get(path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w
}

func authed(*http.Request) (httpserver.Principal, error) {
	return httpserver.Principal{Name: "alice"}, nil
}

func TestPing(t *testing.T) {
	s := newSetup(t, nil, nil)
	t.Run("R-OPS-002 ping answers 200 text/plain OK while ready", func(t *testing.T) {
		w := s.get("/api/v1/system/ping")
		if w.Code != 200 || w.Body.String() != "OK" || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
			t.Fatalf("%d %q %q", w.Code, w.Body.String(), w.Header().Get("Content-Type"))
		}
		if w.Header().Get("X-Request-Id") == "" {
			t.Fatal("no X-Request-Id")
		}
	})
	t.Run("R-OPS-002 ping answers 503 UNAVAILABLE when the replica is not ready", func(t *testing.T) {
		*s.ready = false
		defer func() { *s.ready = true }()
		w := s.get("/api/v1/system/ping")
		if w.Code != 503 || w.Body.String() != "UNAVAILABLE" {
			t.Fatalf("%d %q", w.Code, w.Body.String())
		}
	})
	t.Run("R-OPS-002 ping accepts anonymous callers", func(t *testing.T) {
		if w := s.get("/api/v1/system/ping"); w.Code != 200 {
			t.Fatalf("%d", w.Code)
		}
	})
}

func TestVersion(t *testing.T) {
	t.Run("R-OPS-002 an anonymous call to version gets 401", func(t *testing.T) {
		s := newSetup(t, nil, nil)
		w := s.get("/api/v1/system/version")
		if w.Code != 401 || w.Header().Get("WWW-Authenticate") != "Bearer" || w.Header().Get("X-Trove-Replica") != "" {
			t.Fatalf("%d challenge %q replica %q", w.Code, w.Header().Get("WWW-Authenticate"), w.Header().Get("X-Trove-Replica"))
		}
	})
	t.Run("R-OPS-002 an authenticated call gets the build facts and the replica header", func(t *testing.T) {
		s := newSetup(t, nil, authed)
		w := s.get("/api/v1/system/version")
		if w.Code != 200 || w.Header().Get("X-Trove-Replica") != "trove-web-1" {
			t.Fatalf("%d replica %q", w.Code, w.Header().Get("X-Trove-Replica"))
		}
		var v map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"version", "revision", "buildTime", "goVersion", "formats"} {
			if _, ok := v[k]; !ok {
				t.Errorf("missing %s in %v", k, v)
			}
		}
		if f, _ := v["formats"].([]any); f == nil {
			t.Errorf("formats = %v, want an empty array", v["formats"])
		}
	})
	t.Run("R-OPS-002 system.public_version admits anonymous callers", func(t *testing.T) {
		s := newSetup(t, func(o *Options) { o.PublicVersion = true }, nil)
		if w := s.get("/api/v1/system/version"); w.Code != 200 {
			t.Fatalf("%d", w.Code)
		}
	})
}

func TestSystemInfo(t *testing.T) {
	facts := Facts{
		InstallationID: ptr("0b7c3f5e"), Incarnation: ptr("5d10e2a4"),
		Valkey:      gen.ValkeyFacts{Mode: gen.Sentinel, PrimaryName: "trove", Sentinels: 3, Tls: "verify-full", KeyPrefix: "trove:", Keyspace: ptr("5f2a9c0b13de")},
		ObjectStore: gen.ObjectStoreFacts{Endpoint: "https://s3.example.com", Bucket: "trove", Prefix: "blobs", CredentialSource: "static"},
	}
	reps := fakeReplicas{
		replica("trove-web-1", "web", "n1", gen.Ready, false, "valkey"),
		replica("trove-web-2", "web", "n1", gen.Ready, false),
		replica("trove-web-3", "web", "n2", gen.Starting, false),
		replica("trove-worker-1", "worker", "n3", gen.Ready, false),
		replica("trove-worker-gone", "worker", "n4", gen.Ready, true),
	}
	reps[0].Valkey = &gen.ValkeyView{State: gen.ValkeyViewStateDegraded, Circuit: "open"}
	s := newSetup(t, func(o *Options) { o.Facts = fakeFacts{facts}; o.Replicas = reps }, authed)

	t.Run("R-OPS-004 info reports answeredBy, replica summary per role and the installation facts", func(t *testing.T) {
		w := s.get("/api/v1/system/info")
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		var info gen.SystemInfo
		if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
			t.Fatal(err)
		}
		if info.AnsweredBy.Replica != "trove-web-1" || info.AnsweredBy.Role != "web" {
			t.Errorf("answeredBy = %+v", info.AnsweredBy)
		}
		if info.Replicas.Live != 4 || info.Replicas.Ready != 3 {
			t.Errorf("replicas = %+v (a lost replica is neither live nor ready)", info.Replicas)
		}
		web := info.Replicas.Roles["web"]
		if web.Live != 3 || web.Ready != 2 || web.Degraded != 1 || web.Nodes != 2 {
			t.Errorf("web = %+v", web)
		}
		if info.Replicas.Roles["worker"].Live != 1 {
			t.Errorf("worker = %+v", info.Replicas.Roles["worker"])
		}
		if info.BaseUrl != "https://trove.example.com" || info.BaseUrlSource != gen.Config {
			t.Errorf("baseUrl %q %q", info.BaseUrl, info.BaseUrlSource)
		}
		if info.Valkey.DegradedReplicas == nil || *info.Valkey.DegradedReplicas != 1 {
			t.Errorf("valkey.degradedReplicas = %v", info.Valkey.DegradedReplicas)
		}
		if info.Formats == nil || info.Runtime.Goos == "" || info.Runtime.Gomaxprocs < 1 {
			t.Errorf("formats/runtime: %+v %+v", info.Formats, info.Runtime)
		}
	})

	t.Run("R-OPS-004 the response holds every documented block, null where the source is not wired", func(t *testing.T) {
		var m map[string]json.RawMessage
		_ = json.Unmarshal(s.get("/api/v1/system/info").Body.Bytes(), &m)
		for _, k := range []string{
			"installationId", "incarnation", "answeredBy", "version", "baseUrl", "baseUrlSource", "maintenanceMode",
			"replicas", "database", "valkey", "objectStore", "backup", "features", "formats", "runtime",
		} {
			if _, ok := m[k]; !ok {
				t.Errorf("missing %q", k)
			}
		}
		if string(m["database"]) != "null" || string(m["backup"]) != "null" {
			t.Errorf("database %s backup %s", m["database"], m["backup"])
		}
	})

	t.Run("R-OPS-004 info carries no secret and no Sentinel address", func(t *testing.T) {
		body := strings.ToLower(s.get("/api/v1/system/info").Body.String())
		for _, bad := range []string{"password", "secret", "trv_", "26379", "serverversion"} {
			if strings.Contains(body, bad) {
				t.Errorf("response contains %q", bad)
			}
		}
	})

	t.Run("R-API-038 the document and the responses use the nested Valkey view and replicas.roles", func(t *testing.T) {
		raw := string(EmbeddedYAML())
		for _, old := range []string{"valkeyPrimary", "valkeyPrimarySince", "valkeySentinelsReachable", "byRole"} {
			if strings.Contains(raw, old) {
				t.Errorf("openapi.yaml contains %q", old)
			}
		}
		var list map[string][]map[string]any
		_ = json.Unmarshal(s.get("/api/v1/system/replicas").Body.Bytes(), &list)
		if len(list["items"]) != 5 {
			t.Fatalf("items = %d", len(list["items"]))
		}
		for _, it := range list["items"] {
			if _, ok := it["valkey"]; !ok {
				t.Errorf("replica %v lacks the valkey view", it["name"])
			}
		}
	})

	t.Run("R-OPS-004 info requires system.read", func(t *testing.T) {
		s := newSetup(t, func(o *Options) { o.Access = denyAnonymous{} }, nil)
		for _, path := range []string{"/api/v1/system/info", "/api/v1/system/replicas"} {
			if w := s.get(path); w.Code != 401 {
				t.Errorf("%s: %d", path, w.Code)
			}
		}
	})
}

func TestReplicas(t *testing.T) {
	t.Run("R-OPS-003 replicas lists name, roles, state, lost, node and checks; times end in Z", func(t *testing.T) {
		s := newSetup(t, nil, authed)
		w := s.get("/api/v1/system/replicas")
		if w.Code != 200 {
			t.Fatalf("%d", w.Code)
		}
		var list struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Items) != 1 {
			t.Fatalf("%v %s", err, w.Body.String())
		}
		it := list.Items[0]
		for _, k := range []string{"name", "instanceUid", "version", "revision", "roles", "state", "lost", "startedAt", "heartbeatAt", "leases", "node", "checks", "degraded", "valkey"} {
			if _, ok := it[k]; !ok {
				t.Errorf("missing %q", k)
			}
		}
		if ts, _ := it["startedAt"].(string); !strings.HasSuffix(ts, "Z") {
			t.Errorf("startedAt = %v", it["startedAt"])
		}
	})
	t.Run("R-OPS-003 an empty registry is an empty array", func(t *testing.T) {
		s := newSetup(t, func(o *Options) { o.Replicas = fakeReplicas(nil) }, authed)
		if body := s.get("/api/v1/system/replicas").Body.String(); !strings.Contains(body, `"items":[]`) {
			t.Fatalf("%s", body)
		}
	})
}

func TestDocument(t *testing.T) {
	s := newSetup(t, nil, nil)

	t.Run("R-API-002 the served openapi.json equals the embedded document", func(t *testing.T) {
		w := s.get("/api/v1/openapi.json")
		if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("%d %s", w.Code, w.Header().Get("Content-Type"))
		}
		var served, embedded any
		if err := json.Unmarshal(w.Body.Bytes(), &served); err != nil {
			t.Fatal(err)
		}
		if err := yaml.Unmarshal(EmbeddedYAML(), &embedded); err != nil {
			t.Fatal(err)
		}
		// servers[0].url is the one instance-specific value: the public base URL.
		servers := served.(map[string]any)["servers"].([]any)[0].(map[string]any)
		if servers["url"] != "https://trove.example.com" {
			t.Fatalf("servers[0].url = %v", servers["url"])
		}
		embedded.(map[string]any)["servers"].([]any)[0].(map[string]any)["url"] = "https://trove.example.com"
		if !reflect.DeepEqual(roundTrip(t, served), roundTrip(t, embedded)) {
			t.Fatal("served document differs from the embedded one")
		}
	})

	t.Run("R-API-002 openapi.json declares OpenAPI 3.1 and needs no authentication", func(t *testing.T) {
		var doc map[string]any
		_ = json.Unmarshal(s.get("/api/v1/openapi.json").Body.Bytes(), &doc)
		if v, _ := doc["openapi"].(string); !strings.HasPrefix(v, "3.1.") {
			t.Fatalf("openapi = %q", v)
		}
	})

	t.Run("R-API-002 openapi.yaml serves the embedded document with the base URL", func(t *testing.T) {
		w := s.get("/api/v1/openapi.yaml")
		if w.Code != 200 || !strings.Contains(w.Body.String(), "url: https://trove.example.com") {
			t.Fatalf("%d %s", w.Code, w.Body.String()[:200])
		}
		if !strings.Contains(w.Body.String(), "operationId: getPing") {
			t.Fatal("not the embedded document")
		}
	})

	t.Run("R-API-002 the embedded copy equals api/openapi.yaml", func(t *testing.T) {
		src, err := os.ReadFile("../../../api/openapi.yaml")
		if err != nil {
			t.Fatal(err)
		}
		if string(src) != string(EmbeddedYAML()) {
			t.Fatal("internal/api/v1/openapi.yaml is stale: run go generate ./internal/api/...")
		}
	})

	t.Run("R-API-002 every operation has an operationId, a permission declaration and a problem response", func(t *testing.T) {
		var tree map[string]any
		_ = yaml.Unmarshal(EmbeddedYAML(), &tree)
		for path, item := range tree["paths"].(map[string]any) {
			for method, opv := range item.(map[string]any) {
				op, ok := opv.(map[string]any)
				if !ok {
					continue // path-level parameters
				}
				name := method + " " + path
				if op["operationId"] == "" || op["operationId"] == nil {
					t.Errorf("%s: no operationId", name)
				}
				if p, _ := op["x-trove-permission"].(map[string]any); len(p) == 0 {
					t.Errorf("%s: no x-trove-permission", name)
				}
				raw, _ := json.Marshal(op["responses"])
				if !strings.Contains(string(raw), "responses/Unauthenticated") && !strings.Contains(string(raw), "schemas/Problem") && !strings.Contains(string(raw), "responses/Internal") {
					t.Errorf("%s: no problem response", name)
				}
			}
		}
	})

	t.Run("R-API-002 a route registered but missing from the document stops start-up and names the route", func(t *testing.T) {
		s := newSetup(t, nil, nil)
		s.router.HandleFunc("GET /api/v1/undocumented", func(http.ResponseWriter, *http.Request) {})
		err := s.doc.CheckRoutes(s.router.Patterns())
		if err == nil || !strings.Contains(err.Error(), "GET /api/v1/undocumented") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-API-002 a documented operation without a route stops start-up", func(t *testing.T) {
		doc, _ := LoadDocument(EmbeddedYAML())
		r := httpserver.NewClientRouter() // nothing registered
		err := doc.CheckRoutes(r.Patterns())
		if err == nil || !strings.Contains(err.Error(), "GET /api/v1/system/ping") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-API-002 the registered routes and the document agree", func(t *testing.T) {
		if err := s.doc.CheckRoutes(s.router.Patterns()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("R-API-002 an operation without x-trove-permission is refused at load", func(t *testing.T) {
		raw := strings.Replace(string(EmbeddedYAML()), "      x-trove-permission:\n        permission: system.read\n      responses:\n        \"200\":\n          description: The installation facts.", "      responses:\n        \"200\":\n          description: The installation facts.", 1)
		if raw == string(EmbeddedYAML()) {
			t.Fatal("test setup: replacement did not apply")
		}
		_, err := LoadDocument([]byte(raw))
		if err == nil || !strings.Contains(err.Error(), "getSystemInfo") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-API-002 the authorization table is keyed by route pattern", func(t *testing.T) {
		op, ok := s.doc.Operations()["GET /api/v1/system/info"]
		if !ok || op.ID != "getSystemInfo" || op.Permission["permission"] != "system.read" {
			t.Fatalf("%+v", op)
		}
	})
}

func roundTrip(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestProblemCatalog(t *testing.T) {
	s := newSetup(t, nil, nil)
	t.Run("R-API-009 the type URI of a problem resolves to a description", func(t *testing.T) {
		w := s.get(errs.ProblemTypePrefix + errs.CodeValidation)
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		var p gen.ProblemType
		_ = json.Unmarshal(w.Body.Bytes(), &p)
		if p.Code != "validation_failed" || p.Status != 400 || p.Description == "" || p.Title == "" {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("R-API-009 an unknown code is a 404 problem", func(t *testing.T) {
		w := s.get(errs.ProblemTypePrefix + "no_such_code")
		if w.Code != 404 || w.Header().Get("Content-Type") != errs.ProblemContentType {
			t.Fatalf("%d %s", w.Code, w.Header().Get("Content-Type"))
		}
	})
	t.Run("R-API-009 every code in the registry resolves", func(t *testing.T) {
		for _, c := range errs.Codes() {
			if w := s.get(errs.ProblemTypePrefix + c.Code); w.Code != 200 {
				t.Errorf("%s: %d", c.Code, w.Code)
			}
		}
	})
}

func TestVersionRouting(t *testing.T) {
	s := newSetup(t, nil, nil)
	t.Run("R-API-001 other API versions answer 404 with a problem body", func(t *testing.T) {
		for _, p := range []string{"/api/v0/repositories", "/api/v3/repositories", "/api/v1/nonexistent"} {
			w := s.get(p)
			if w.Code != 404 || w.Header().Get("Content-Type") != errs.ProblemContentType {
				t.Errorf("%s: %d %s", p, w.Code, w.Header().Get("Content-Type"))
			}
		}
	})
	t.Run("R-API-004 a wrong method answers 405 with Allow", func(t *testing.T) {
		w := httptest.NewRecorder()
		s.handler.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/system/ping", nil))
		if w.Code != 405 || !strings.Contains(w.Header().Get("Allow"), "GET") {
			t.Fatalf("%d Allow %q", w.Code, w.Header().Get("Allow"))
		}
	})
}

func TestRouteTable(t *testing.T) {
	t.Run("R-HA-041 the API routes pass the route-table rules", func(t *testing.T) {
		s := newSetup(t, nil, nil)
		if v := httpserver.CheckRouteTable(s.router.Patterns(), nil); len(v) != 0 {
			t.Fatalf("violations: %v", v)
		}
	})
}

// recordingFacts and recordingReplicas fail the test when a handler behind a
// denied check runs.
type recordingFacts struct{ t *testing.T }

func (f recordingFacts) Facts(context.Context) (Facts, error) {
	f.t.Error("a handler ran although its permission check denied the caller")
	return Facts{}, nil
}

type recordingReplicas struct{ t *testing.T }

func (f recordingReplicas) Replicas(context.Context) ([]gen.Replica, error) {
	f.t.Error("a handler ran although its permission check denied the caller")
	return nil, nil
}

// pathOf turns a route pattern into a request: "GET /api/v1/problems/{code}" →
// GET /api/v1/problems/x.
func pathOf(pattern string) (method, path string) {
	method, path, _ = strings.Cut(pattern, " ")
	for {
		i, j := strings.Index(path, "{"), strings.Index(path, "}")
		if i < 0 || j < i {
			return method, path
		}
		path = path[:i] + "x" + path[j+1:]
	}
}

func TestAuthorizationStep(t *testing.T) {
	doc, err := LoadDocument(EmbeddedYAML())
	if err != nil {
		t.Fatal(err)
	}

	t.Run("R-API-008 an anonymous caller gets 401 on every operation that is not public, before the handler runs", func(t *testing.T) {
		// bootstrapLike answers as the M0 access of the composition root does
		// with system.bootstrap_anonymous_read off.
		s := newSetup(t, func(o *Options) {
			o.Access, o.Facts, o.Replicas, o.PublicVersion = bootstrapLike{}, recordingFacts{t}, recordingReplicas{t}, false
		}, nil)
		ops := doc.Operations()
		if len(ops) == 0 {
			t.Fatal("no operations")
		}
		for pattern, op := range ops {
			method, path := pathOf(pattern)
			w := httptest.NewRecorder()
			s.handler.ServeHTTP(w, httptest.NewRequest(method, path, nil))
			if op.Requirement.Public {
				if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
					t.Errorf("%s (%s) is public but answered %d", pattern, op.ID, w.Code)
				}
				continue
			}
			if w.Code != http.StatusUnauthorized || w.Header().Get("Content-Type") != errs.ProblemContentType || w.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Errorf("%s (%s, %s): %d %s challenge %q", pattern, op.ID, op.Requirement, w.Code, w.Header().Get("Content-Type"), w.Header().Get("WWW-Authenticate"))
			}
		}
	})

	t.Run("R-API-008 every declaration of the document is in an enforced form", func(t *testing.T) {
		for pattern, op := range doc.Operations() {
			q := op.Requirement
			n := 0
			for _, set := range []bool{q.Public, q.Authenticated, q.Permission != ""} {
				if set {
					n++
				}
			}
			if n != 1 {
				t.Errorf("%s: requirement %+v", pattern, q)
			}
		}
	})

	t.Run("R-API-008 an operation missing from the table is denied to everyone", func(t *testing.T) {
		d, _ := LoadDocument(EmbeddedYAML())
		delete(d.ops, "GET /api/v1/system/ping")
		router := httpserver.NewClientRouter()
		New(Options{Document: d, Access: allowAll{}, Ready: func() bool { return true }}).Register(router)
		res, _ := httpserver.NewResolver(nil, "https://trove.example.com", nil)
		h := httpserver.Chain{Router: router, Resolver: res, Authenticate: authed, Replica: "r"}.Handler()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/system/ping", nil))
		if w.Code != http.StatusForbidden || w.Body.String() == "OK" {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		if err := d.CheckRoutes(router.Patterns()); err == nil {
			t.Error("CheckRoutes accepted the undeclared route")
		}
	})

	t.Run("R-API-008 the permission check runs with the declared permission name", func(t *testing.T) {
		var asked []string
		s := newSetup(t, func(o *Options) {
			o.Access = accessFunc(func(_ context.Context, p string) error { asked = append(asked, p); return nil })
		}, authed)
		s.get("/api/v1/system/info")
		s.get("/api/v1/system/replicas")
		s.get("/api/v1/system/ping")
		if len(asked) != 2 || asked[0] != PermissionSystemRead || asked[1] != PermissionSystemRead {
			t.Fatalf("Access.Require calls = %v", asked)
		}
	})

	t.Run("R-API-002 a declaration in a form the step does not enforce is refused at load", func(t *testing.T) {
		for _, form := range []string{"role: admin", "public: false", "permission: \"\"", "authenticated: true\n        publicSwitch: system.anything", "public: true\n        permission: system.read"} {
			raw := strings.Replace(string(EmbeddedYAML()), "      x-trove-permission:\n        permission: system.read\n      responses:\n        \"200\":\n          description: The installation facts.",
				"      x-trove-permission:\n        "+form+"\n      responses:\n        \"200\":\n          description: The installation facts.", 1)
			if raw == string(EmbeddedYAML()) {
				t.Fatal("test setup: replacement did not apply")
			}
			if _, err := LoadDocument([]byte(raw)); err == nil || !strings.Contains(err.Error(), "getSystemInfo") {
				t.Errorf("%q: err = %v", form, err)
			}
		}
	})
}

type bootstrapLike struct{}

func (bootstrapLike) Require(ctx context.Context, _ string) error {
	if httpserver.PrincipalFrom(ctx).Anonymous {
		return errs.E(errs.Unauthenticated, errs.CodeUnauthenticated, "This endpoint requires an authenticated caller.", errs.WithChallenge("Bearer"))
	}
	return nil
}

type accessFunc func(ctx context.Context, permission string) error

func (f accessFunc) Require(ctx context.Context, permission string) error { return f(ctx, permission) }
