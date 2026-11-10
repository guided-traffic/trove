//go:build integration

package importer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/config"
	imp "github.com/guided-traffic/trove/internal/importer"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/npmstub"
	"github.com/guided-traffic/trove/test/nugetstub"
)

// The import tooling (R-IMP-001 to R-IMP-010) against the integration stack: the sources are the counting test upstreams of
// test/npmstub and test/nugetstub behind TLS, the targets are local repositories of a replica that runs in the test
// process. The importer reaches the replica by the name its base URL carries (trove.test), connected to the replica's
// listener.

const (
	targetHost = "trove.test"
	rNPM       = "npm-imp"
	rNPM2      = "npm-imp-b"
	rNuGet     = "nuget-imp"
	rNuGetHard = "nuget-imp-hard"
)

type env struct {
	t   *testing.T
	st  *testenv.Stack
	rep *testenv.Replica

	npm      *npmstub.Server
	npmURL   string
	nuget    *nugetstub.Server
	nugetURL string
	ca       string
	npmTLS   *npmstub.SelfSigned
	nugetTLS *nugetstub.SelfSigned

	factory *outbound.Factory
	// writer may read and deploy, reader may only read, admin reads the configuration too (it is bound to the admin role).
	writer, reader, admin string
	writerID              string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ss, err := npmstub.NewSelfSigned("localhost", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	ns, err := nugetstub.NewSelfSigned("localhost", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, npmTLS: ss, nugetTLS: ns}
	// One CA file serves both stubs: the trust store takes the concatenation.
	e.ca = filepath.Join(t.TempDir(), "stubs-ca.pem")
	if err := os.WriteFile(e.ca, append(append([]byte{}, ss.CAPEM...), ns.CAPEM...), 0o600); err != nil {
		t.Fatal(err)
	}
	e.npm, err = npmstub.New(npmstub.Config{})
	if err != nil {
		t.Fatal(err)
	}
	e.npmURL = e.serveNPM(e.npm)
	e.nuget, err = nugetstub.New(nugetstub.Config{})
	if err != nil {
		t.Fatal(err)
	}
	e.nugetURL = e.serveNuGet(e.nuget)

	st := testenv.NewStack(t, testenv.StackOptions{Name: "import", Env: []string{"TROVE_VULNERABILITIES_SOURCES_OSV_MODE=off"}})
	e.st = st
	writer, reader, rita := st.User("writer"), st.User("reader"), st.User("rita")
	st.Bind("admin", rita)
	e.writerID = writer.ID
	e.writer = st.Token(writer, scope.Default())
	e.reader = st.Token(reader, scope.Default())
	e.admin = st.Token(rita, scope.Scope{Inherit: true, Admin: true})
	st.Repository(rNPM, "local", "npm", `{}`)
	st.Repository(rNPM2, "local", "npm", `{}`)
	st.Repository(rNuGet, "local", "nuget", `{}`)
	st.Repository(rNuGetHard, "local", "nuget", `{"nuget":{"deleteMode":"delete"}}`)
	repos := []string{rNPM, rNPM2, rNuGet, rNuGetHard}
	st.Policy("import-writers", []string{"read", "deploy"}, repos, writer)
	st.Policy("import-readers", []string{"read"}, repos, reader)
	e.rep = st.Start(t, testenv.ReplicaOptions{Name: "imp1"})

	e.factory = outbound.New(config.Outbound{
		TLS:           config.OutboundTLS{UseSystemRoots: false, CAFiles: []string{e.ca}, MinVersion: "1.2"},
		AddressPolicy: config.OutboundAddressPolicy{AllowCIDRs: []string{"127.0.0.1/32"}},
	}, outbound.WithDialOverride(map[string]string{targetHost + ":80": e.rep.Addr("default")}))
	if err := e.factory.Err(); err != nil {
		t.Fatal(err)
	}
	return e
}

// serveNPM serves a stub behind the TLS certificate the importer trusts and returns its registry address.
func (e *env) serveNPM(stub *npmstub.Server) string {
	e.t.Helper()
	srv := httptest.NewUnstartedServer(stub)
	srv.TLS = e.npmTLS.ServerConfig()
	srv.StartTLS()
	e.t.Cleanup(srv.Close)
	return srv.URL + "/"
}

// serveNuGet serves a stub behind the TLS certificate the importer trusts and returns its service index address.
func (e *env) serveNuGet(stub *nugetstub.Server) string {
	e.t.Helper()
	srv := httptest.NewUnstartedServer(stub)
	srv.TLS = e.nugetTLS.ServerConfig()
	srv.StartTLS()
	e.t.Cleanup(srv.Close)
	return srv.URL + "/v3/index.json"
}

// npmTarget and nugetTarget are the addresses of the local repositories as a package manager is given them.
func npmTarget(repo string) string { return "http://" + targetHost + "/api/npm/it/" + repo + "/" }

func nugetTarget(repo string) string {
	return "http://" + targetHost + "/api/nuget/it/" + repo + "/v3/index.json"
}

// fast makes a retry wait nothing, so a test that provokes a refusal does not sleep.
var fast = imp.Retry{Attempts: 2, Sleep: func(context.Context, time.Duration) error { return nil }}

// build assembles an import.
func (e *env) build(format, from, to, token string) (imp.Format, *imp.SourceClient, *imp.TargetClient) {
	e.t.Helper()
	f, src, dst, err := imp.Build(e.factory, imp.Params{Format: format, From: from, To: to, Token: token, ExtraHosts: []string{"localhost"}})
	if err != nil {
		e.t.Fatal(err)
	}
	src.Retry, dst.Retry = fast, fast
	return f, src, dst
}

func (e *env) options(format, from, to, token string, names ...string) imp.Options {
	e.t.Helper()
	f, src, dst := e.build(format, from, to, token)
	return imp.Options{Format: f, Source: src, Target: dst, Names: names, Concurrency: 2, Dir: e.t.TempDir()}
}

func (e *env) run(format, from, to, token string, names ...string) (*imp.Report, error) {
	e.t.Helper()
	return imp.Run(context.Background(), e.options(format, from, to, token, names...))
}

// versions counts the live versions of a package in a repository.
func (e *env) versions(repo, name string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT count(*) FROM package_versions pv JOIN packages p ON p.id = pv.package_id JOIN repositories r ON r.id = p.repo_id
		WHERE r.key = $1 AND lower(p.name) = lower($2) AND pv.trashed_at IS NULL`, repo, name)
}

func (e *env) published() int64 {
	e.t.Helper()
	return e.st.Number(`SELECT count(*) FROM audit_event WHERE action IN ('npm.publish', 'package.published')`)
}

func pkgOf(t *testing.T, rep *imp.Report, name string) imp.PackageResult {
	t.Helper()
	for _, p := range rep.Packages {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("the report has no package %s: %+v", name, rep.Packages)
	return imp.PackageResult{}
}

// outcome returns the result of one version in a report.
func outcome(t *testing.T, rep *imp.Report, name, version string) imp.VersionResult {
	t.Helper()
	for _, v := range pkgOf(t, rep, name).Versions {
		if v.Version == version {
			return v
		}
	}
	t.Fatalf("the report has no %s@%s: %+v", name, version, pkgOf(t, rep, name))
	return imp.VersionResult{}
}

// cutAfter wraps the target's transport so that the context is cancelled after n successful publishes: the run is cut short
// as a killed process is, between two requests.
func cutAfter(dst *imp.TargetClient, n int64, cancel context.CancelFunc) {
	var seen atomic.Int64
	base := dst.HTTP.Transport
	dst.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		resp, err := base.RoundTrip(r)
		if err == nil && r.Method == http.MethodPut && resp.StatusCode == http.StatusCreated && !strings.Contains(r.URL.Path, "/dist-tags/") {
			if seen.Add(1) == n {
				cancel()
			}
		}
		return resp, err
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// sourceDoc reads a package document of the npm stub through the importer's own client.
func (e *env) sourceDoc(name string) map[string]any {
	e.t.Helper()
	_, src, _ := e.build("npm", e.npmURL, npmTarget(rNPM), e.writer)
	var doc map[string]any
	if found, err := src.GetJSON(context.Background(), e.npmURL+name, "application/json", 1<<24, &doc); err != nil || !found {
		e.t.Fatalf("GET %s: %v %v", name, found, err)
	}
	return doc
}
