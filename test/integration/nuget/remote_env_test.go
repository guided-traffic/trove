//go:build integration

package nuget

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/nugetstub"
)

// The remote class (R-NUGET-014, R-NUGET-015, R-NUGET-031) against the integration stack, with
// the counting test upstream of test/nugetstub in the test process: a TLS server the replicas
// trust and may reach, whose counters say what the replicas asked of it. The upstream spreads its
// resources over two names of one listener, as nuget.org spreads them over two hosts: the
// service index and the registrations on 127.0.0.1, the flat container and the search on
// localhost, which the remote admits as an extra host.

// The repositories of the installation.
const (
	rRemote  = "nuget-org"
	rFast    = "nuget-fast"
	rExclude = "nuget-excl"
	rNoHost  = "nuget-nohost"
	rLocal   = "nuget-local"
)

// renv is one installation with the repositories above, the upstream and three people.
type renv struct {
	t    *testing.T
	st   *testenv.Stack
	stub *nugetstub.Server
	srv  *httptest.Server
	// url is the service index of the upstream.
	url string

	tokAdmin, tokReader, tokNobody string
}

// remoteOptions are the settings a test changes.
type remoteOptions struct {
	// Valkey: the replicas use the stack's Valkey; false is "Valkey stopped".
	Valkey bool
	// Wrap puts a handler in front of the upstream.
	Wrap func(http.Handler) http.Handler
	// Env is further environment of every replica.
	Env []string
}

func newRemoteEnv(t *testing.T, o remoteOptions) *renv {
	t.Helper()
	stub, err := nugetstub.New(nugetstub.Config{})
	if err != nil {
		t.Fatal(err)
	}
	ss, err := nugetstub.NewSelfSigned("localhost", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	var h http.Handler = stub
	if o.Wrap != nil {
		h = o.Wrap(stub)
	}
	srv := httptest.NewUnstartedServer(h)
	srv.TLS = ss.ServerConfig()
	srv.StartTLS()
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	ca := filepath.Join(dir, "stub-ca.pem")
	if err := os.WriteFile(ca, ss.CAPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	st := testenv.NewStack(t, testenv.StackOptions{Name: "nugetr", Valkey: o.Valkey, Env: append([]string{
		"TROVE_PROXY_OUTBOUND_TLS_CA_FILES=[" + strconv.Quote(ca) + "]",
		`TROVE_PROXY_OUTBOUND_ADDRESS_POLICY_ALLOW_CIDRS=["127.0.0.1/32","::1/128"]`,
		"TROVE_PROXY_LOCK_POLL_INTERVAL_MS=50",
	}, o.Env...)})
	e := &renv{t: t, st: st, stub: stub, srv: srv}
	// The test server listens on 127.0.0.1; the service index is asked for under that name.
	e.url = srv.URL + "/v3/index.json"

	rita, reader, nobody := st.User("rita"), st.User("reader"), st.User("nobody")
	st.Bind("admin", rita)
	e.tokAdmin = st.Token(rita, scope.Scope{Inherit: true, Admin: true})
	e.tokReader = st.Token(reader, scope.Default())
	e.tokNobody = st.Token(nobody, scope.Default())

	content := []any{map[string]any{"pattern": "localhost", "sendCredentials": false}}
	st.Repository(rRemote, "remote", "nuget", e.remoteConfig(300, content, nil))
	st.Repository(rFast, "remote", "nuget", e.remoteConfig(2, content, nil))
	st.Repository(rExclude, "remote", "nuget", e.remoteConfig(300, content, []string{"Contoso.Core.*"}))
	// This remote does not admit the content host: the service index names it, the engine refuses.
	st.Repository(rNoHost, "remote", "nuget", e.remoteConfig(300, []any{}, nil))
	st.Repository(rLocal, "local", "nuget", "{}")
	st.Policy("nuget-readers", []string{"read"}, []string{rRemote, rFast, rExclude, rNoHost, rLocal}, reader)
	return e
}

// remoteConfig is the canonical document of a remote repository that points to the test
// upstream: every member present, as the repository service stores it.
func (e *renv) remoteConfig(ttl int, extraHosts []any, exclude []string) string {
	remote := map[string]any{
		"url": e.url, "extraHosts": extraHosts, "authMode": "none", "username": "", "password": nil,
		"clientCertificate": nil, "headers": []any{}, "forwardAuthOnRedirect": false, "outboundProxy": map[string]any{"mode": "direct"},
		"connectTimeoutMs": 5000, "readTimeoutMs": 15000, "sourceNetwork": nil, "cacheFillAction": "read",
		"cache": map[string]any{
			"storeArtifacts": true, "metadataTtlSeconds": ttl, "metadataRefreshTimeoutSeconds": 10, "notFoundTtlSeconds": notFound(ttl),
			"maxStaleSeconds": 0, "unusedRetentionHours": 0,
		},
		"integrity":    map[string]any{"checksumPolicy": "verify-if-available", "blockUnexpectedContentTypes": true, "allowedContentTypes": []any{}},
		"availability": map[string]any{"mode": "online", "failureThreshold": 3, "circuitOpenSeconds": 30},
		"limits":       map[string]any{"maxArtifactBytes": nil, "maxMetadataBytes": nil, "maxConcurrentFetches": 16},
	}
	doc := map[string]any{"remote": remote}
	if exclude != nil {
		doc["excludePatterns"] = exclude
	}
	b, err := json.Marshal(doc)
	if err != nil {
		e.t.Fatal(err)
	}
	return string(b)
}

// start starts n web replicas.
func (e *renv) start(n int) []*testenv.Replica {
	e.t.Helper()
	out := make([]*testenv.Replica, n)
	for i := range out {
		out[i] = e.st.Start(e.t, testenv.ReplicaOptions{Name: "web-" + strconv.Itoa(len(e.st.Replicas())+1)})
	}
	return out
}

// get sends a GET of a path below a repository's registry URL as the reader.
func (e *renv) get(r *testenv.Replica, repo, path string, hdr ...string) testenv.Answer {
	e.t.Helper()
	return e.st.Do(r, "GET", "/api/nuget/"+repo+"/"+path, e.tokReader, nil, hdr...)
}

// feed is the v3 root of a repository as the documents of a replica name it.
func feed(repo string) string { return feedBase + "/api/nuget/" + repo + "/v3/" }

// versionsOf reads the version list of the flat container.
func versionsOf(t testing.TB, a testenv.Answer) []string {
	t.Helper()
	var d struct {
		Versions []string `json:"versions"`
	}
	if err := json.Unmarshal(a.Body, &d); err != nil {
		t.Fatalf("%d %s: %v", a.Status, a.Body, err)
	}
	return d.Versions
}

// registrationOf reads a registration index: the pages, and the leaves of the inlined ones.
type registrationDoc struct {
	Count int `json:"count"`
	Items []struct {
		ID    string `json:"@id"`
		Count int    `json:"count"`
		Lower string `json:"lower"`
		Upper string `json:"upper"`
		Items []struct {
			ID             string `json:"@id"`
			PackageContent string `json:"packageContent"`
			CatalogEntry   struct {
				ID      string `json:"id"`
				Version string `json:"version"`
				Listed  bool   `json:"listed"`
			} `json:"catalogEntry"`
		} `json:"items"`
	} `json:"items"`
}

func registrationOf(t testing.TB, a testenv.Answer) registrationDoc {
	t.Helper()
	body := a.Body
	if a.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(zr); err != nil {
			t.Fatal(err)
		}
		body = buf.Bytes()
	}
	var d registrationDoc
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatalf("%d %.300s: %v", a.Status, body, err)
	}
	return d
}

// itemCount counts the items of a repository below a path prefix.
func (e *renv) itemCount(repo, prefix string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT count(*) FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = $1 AND i.kind = 'file' AND i.path LIKE $2`,
		repo, prefix+"%")
}

// cacheRows counts the NuGet rows of origin cache of a repository.
func (e *renv) cacheRows(repo, id string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT count(*) FROM nuget_version nv JOIN repositories r ON r.id = nv.repository_id
		WHERE r.key = $1 AND nv.lower_id = $2 AND nv.origin = 'cache'`, repo, id)
}

// nupkgPath is the flat-container path of a package file.
func nupkgPath(id, version string) string {
	l := strings.ToLower(id)
	return "v3/flatcontainer/" + l + "/" + version + "/" + l + "." + version + ".nupkg"
}

// notFound is the not-found period of a repository whose metadata period is ttl: short for the
// fast repository, whose tests wait for the periods to pass.
func notFound(ttl int) int {
	if ttl <= 2 {
		return 2
	}
	return 60
}
