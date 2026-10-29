package v1

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/httpserver"
)

func TestOpenAPIDocument(t *testing.T) {
	t.Run("R-API-002 E1.6 the document validates as OpenAPI 3.1", func(t *testing.T) {
		if _, err := apitest.LoadDocument(EmbeddedYAML()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-API-002 the validator fails a document that is not OpenAPI 3.1", func(t *testing.T) {
		old := strings.Replace(string(EmbeddedYAML()), "openapi: 3.1.0", "openapi: 3.0.3", 1)
		if _, err := apitest.LoadDocument([]byte(old)); err == nil {
			t.Fatal("a 3.0 document was accepted")
		}
	})
	t.Run("R-API-004 every literal sibling of a {key} segment is a reserved name of that key", func(t *testing.T) {
		problems, err := apitest.ReservedNameProblems(EmbeddedYAML())
		if err != nil {
			t.Fatal(err)
		}
		if len(problems) > 0 {
			t.Fatalf("%s", strings.Join(problems, "\n"))
		}
	})
	t.Run("R-API-004 the check finds a literal sibling that the key schema does not list", func(t *testing.T) {
		spec := `openapi: 3.1.0
paths:
  /api/v1/things/{key}:
    parameters:
      - name: key
        in: path
        required: true
        schema: {type: string}
  /api/v1/things/me:
    get: {operationId: x}
`
		problems, _ := apitest.ReservedNameProblems([]byte(spec))
		if len(problems) != 1 || !strings.Contains(problems[0], `"me"`) {
			t.Fatalf("%v", problems)
		}
		listed := strings.Replace(spec, "schema: {type: string}", "schema: {type: string, x-trove-reserved-names: [me]}", 1)
		if problems, _ := apitest.ReservedNameProblems([]byte(listed)); len(problems) != 0 {
			t.Fatalf("%v", problems)
		}
	})
	t.Run("R-API-002 every non-public operation declares the problem responses it can produce", func(t *testing.T) {
		doc, err := apitest.LoadDocument(EmbeddedYAML())
		if err != nil {
			t.Fatal(err)
		}
		for path, item := range doc.Paths.Map() {
			for method, op := range item.Operations() {
				has4xx5xx := false
				for status := range op.Responses.Map() {
					if strings.HasPrefix(status, "4") || strings.HasPrefix(status, "5") {
						has4xx5xx = true
					}
				}
				if !has4xx5xx {
					t.Errorf("%s %s (%s) has no problem response", method, path, op.OperationID)
				}
			}
		}
	})
}

// sampleValue fills a path parameter of an operation for a smoke request.
func samplePath(template string) string {
	return regexp.MustCompile(`\{([a-zA-Z]+)\}`).ReplaceAllStringFunc(template, func(p string) string {
		switch p {
		case "{id}":
			return "01933b2a-7c41-7d1e-9a52-3f0e4b8c2d11"
		case "{name}":
			return "gc"
		}
		return "not_found"
	})
}

func TestResponsesMatchTheDocument(t *testing.T) {
	val := apitest.New(t, EmbeddedYAML())
	for _, tc := range []struct {
		name  string
		authn func(*http.Request) (httpserver.Principal, error)
	}{{"anonymous", nil}, {"authenticated", authed}} {
		facts := Facts{
			Valkey:      gen.ValkeyFacts{Mode: gen.Sentinel, PrimaryName: "trove", Sentinels: 3, Tls: "verify-full", KeyPrefix: "trove:"},
			ObjectStore: gen.ObjectStoreFacts{Endpoint: "https://s3.example.com", Bucket: "trove", Prefix: "blobs", CredentialSource: "static"},
		}
		s := newSetup(t, func(o *Options) { o.Facts = fakeFacts{facts} }, tc.authn)
		h := val.Handler(t, s.handler)
		doc, _ := LoadDocument(EmbeddedYAML())
		ops := doc.Operations()
		patterns := make([]string, 0, len(ops))
		for p := range ops {
			patterns = append(patterns, p)
		}
		sort.Strings(patterns)
		for _, pattern := range patterns {
			method, path, _ := strings.Cut(pattern, " ")
			if method != http.MethodGet {
				continue // writes need a database; the integration tests cover them
			}
			t.Run("R-API-002 E1.6 "+tc.name+" "+pattern, func(t *testing.T) {
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest(method, samplePath(path), nil))
				if rec.Code >= 500 && rec.Code != 503 {
					t.Fatalf("%d %s", rec.Code, rec.Body.String())
				}
			})
		}
	}
	t.Run("R-API-002 the M0 operations were reached", func(t *testing.T) {
		if missing := val.Uncovered("getPing", "getVersion", "getSystemInfo", "listReplicas", "getOpenApiJson", "getOpenApiYaml", "getProblemType"); len(missing) > 0 {
			t.Fatalf("not reached: %v", missing)
		}
	})
	t.Run("R-API-002 the validator fails an answer that does not match the document", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v1/system/replicas", nil)
		hdr := http.Header{"Content-Type": {"application/json; charset=utf-8"}}
		if _, err := val.Check(req, 200, hdr, []byte(`{"items":[{"name":1}]}`)); err == nil {
			t.Fatal("a replica without its required members was accepted")
		}
		if _, err := val.Check(req, 418, hdr, []byte(`{}`)); err == nil {
			t.Fatal("an undeclared status was accepted")
		}
		if _, err := val.Check(httptest.NewRequest("GET", "/api/v1/nothing", nil), 200, hdr, []byte(`{}`)); err == nil {
			t.Fatal("a path outside the document was accepted")
		}
		if _, err := val.Check(req, 200, hdr, []byte(`{"items":[]}`)); err != nil {
			t.Fatalf("a valid answer was refused: %v", err)
		}
	})
	t.Run("R-API-002 the 3.1 constructs of the document are validated: a null branch and a type array", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v1/system/info", nil)
		hdr := http.Header{"Content-Type": {"application/json"}}
		bad := `{"installationId":5}`
		if _, err := val.Check(req, 200, hdr, []byte(bad)); err == nil {
			t.Fatal("a number where the type array allows string or null was accepted")
		}
	})
}
