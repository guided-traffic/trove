//go:build integration

// Package apikey tests the API-key credential of the package protocols
// (R-AUTH-001 item 1, R-AUTH-033, R-NUGET-012, E3.5) on replicas of the production
// composition root: the real chain, the real authentication stage with real tokens,
// the real dispatcher and decision point, the audit trail in PostgreSQL, the request
// log and the metrics. The routes are those of a test format whose push, delete and
// relist declare the API-key header like NuGet's publish resource (formats.Route.APIKey,
// Q-ARCH-266); the stage learns the declaration from the dispatcher's match
// (Q-AUTH-186). The NuGet format's own routes run the same path once they are
// registered. Every test creates a database of its own.
package apikey

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

// keyedFormat is the test format: a service document, the push, delete and relist of a
// publish resource that declare the API-key header, and a read on the version path that
// does not. Every handler answers with the principal the request reached it as. It is
// registered for the whole test binary, so the repository model accepts the format and
// the dispatcher serves its prefix.
type keyedFormat struct{ formats.Base }

func init() { formats.Register(keyedFormat{}) }

type foldedNames struct{}

func (foldedNames) Normalize(name string) (string, bool) { return strings.ToLower(name), name != "" }

func (keyedFormat) Descriptor() formats.Descriptor {
	return formats.Descriptor{
		Facts: catalog.Facts{
			ID: "keyed", DisplayName: "Keyed", Classes: []formats.Class{formats.Local}, Layout: "keyed",
			ProtocolBasePath: "/api/keyed/{project}/{key}/", PatternSubject: formats.SubjectName, PackageNames: true,
		},
		ContractVersion: formats.ContractVersion, Names: foldedNames{}, PropertyPrefix: "keyed.",
	}
}

func (f keyedFormat) Routes() []formats.Route {
	return []formats.Route{
		{
			Name: "keyed.index.get", Method: http.MethodGet, Template: "/index.json", Examples: []string{"index.json"},
			Action: formats.ActionRead, Kind: formats.Computed, Handler: f.who(http.StatusOK),
		},
		{
			Name: "keyed.package.put", Method: http.MethodPut, Template: "/package", Examples: []string{"package"},
			Action: formats.ActionDeploy, Kind: formats.Upload, Handler: f.who(http.StatusCreated), APIKey: true,
		},
		{
			Name: "keyed.version.get", Method: http.MethodGet, Template: "/package/{id}/{version}", Examples: []string{"package/acme.widgets/1.2.3"},
			Action: formats.ActionRead, Kind: formats.Computed, Handler: f.who(http.StatusOK),
		},
		{
			Name: "keyed.version.delete", Method: http.MethodDelete, Template: "/package/{id}/{version}", Examples: []string{"package/acme.widgets/1.2.3"},
			Action: formats.ActionDelete, Kind: formats.Delete, Handler: f.who(http.StatusOK), APIKey: true,
		},
		{
			Name: "keyed.version.relist", Method: http.MethodPost, Template: "/package/{id}/{version}", Examples: []string{"package/acme.widgets/1.2.3"},
			Action: formats.ActionDeploy, Kind: formats.Admin, Handler: f.who(http.StatusOK), APIKey: true,
		},
	}
}

// who answers with the subject the request reached the handler as.
func (keyedFormat) who(status int) formats.HandlerFunc {
	return func(_ context.Context, rq *formats.Request, w http.ResponseWriter) error {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		return json.NewEncoder(w).Encode(map[string]any{
			"name": rq.Subject.Name, "method": rq.Subject.AuthMethod, "tokenId": rq.Subject.TokenID, "anonymous": rq.Subject.Anonymous,
		})
	}
}

func (f keyedFormat) Match(m, p string) (*formats.Route, formats.Params, error) {
	return formats.MatchRoutes(f.Routes(), m, p)
}

// Classify: the service document is computed; the push path takes an upload.
func (keyedFormat) Classify(p formats.Path) formats.PathInfo {
	if p.Path == "index.json" {
		return formats.PathInfo{Kind: formats.PathComputed}
	}
	return formats.PathInfo{Kind: formats.Content}
}

func (keyedFormat) PackageDocumentPath(string) (formats.Path, bool) { return formats.Path{}, false }

// WriteError renders a problem with the challenge of an Unauthenticated error, as a
// package format does for the refusals of the chain (Q-ARCH-214).
func (keyedFormat) WriteError(w http.ResponseWriter, r *http.Request, err error) {
	errs.Write(w, r, err, httpserver.RequestID(r.Context()), httpserver.Logger(r.Context()))
}

const (
	repoKey   = "keyed-local"
	feed      = "/api/keyed/" + repoKey
	push      = feed + "/package"
	version   = feed + "/package/acme.widgets/1.2.3"
	apiKey    = "X-NuGet-ApiKey"
	origin    = "http://trove.test"
	breakPass = "correct horse battery staple 1"
	// guard bounds a wait for something that happens on its own (a revocation reaching
	// the replica, an asynchronous audit event); it is generous by the rule of
	// docs/developer/build-and-test.md, the budgets are other tests'.
	guard = 30 * time.Second
)

// env is one installation: alice may push, delete and relist; ci-bot may push; bob
// may only read.
type env struct {
	t     *testing.T
	stack *testenv.Stack

	alice, ciBot, bob      testenv.Person
	alicePAT, alicePAT2    string
	ciSAT, bobPAT, doomPAT string
	pwFile                 string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	s := testenv.NewStack(t, testenv.StackOptions{Valkey: true, Name: "apikey"})
	e := &env{t: t, stack: s}
	s.Repository(repoKey, "local", "keyed", "{}")
	e.alice, e.ciBot, e.bob = s.User("alice"), s.ServiceAccount("ci-bot"), s.User("bob")
	s.Policy("alice-publishes", []string{"read", "deploy", "delete"}, []string{repoKey}, e.alice)
	s.Policy("ci-pushes", []string{"read", "deploy"}, []string{repoKey}, e.ciBot)
	s.Policy("bob-reads", []string{"read"}, []string{repoKey}, e.bob)
	e.alicePAT, e.alicePAT2, e.doomPAT = s.Token(e.alice, scope.Default()), s.Token(e.alice, scope.Default()), s.Token(e.alice, scope.Default())
	e.ciSAT, e.bobPAT = s.Token(e.ciBot, scope.Default()), s.Token(e.bob, scope.Default())
	e.pwFile = testenv.WriteFile(t, t.TempDir(), "break-glass", breakPass)
	return e
}

// start brings up a web replica with the break-glass account, whose session the
// cookie test uses, and the extra environment.
func (e *env) start(name string, env ...string) *testenv.Replica {
	e.t.Helper()
	return e.stack.Start(e.t, testenv.ReplicaOptions{Name: name, Env: append([]string{
		"TROVE_AUTH_BREAK_GLASS_ENABLED=true", "TROVE_AUTH_BREAK_GLASS_PASSWORD_FILE=" + e.pwFile,
	}, env...)})
}

// reply is the subject a handler was reached as.
type reply struct {
	Name, Method, TokenID string
	Anonymous             bool
}

// send sends one request without a bearer token, with the headers given, and decodes
// the subject of an answer below 300.
func (e *env) send(r *testenv.Replica, method, path string, hdr ...string) (testenv.Answer, reply) {
	e.t.Helper()
	a := e.stack.Do(r, method, path, "", nil, hdr...)
	var who reply
	if a.Status < 300 {
		if err := json.Unmarshal(a.Body, &who); err != nil {
			e.t.Fatalf("%s %s: %d %s", method, path, a.Status, a.Body)
		}
	}
	return a, who
}

func basic(user, tok string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+tok))
}
