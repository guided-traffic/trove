//go:build integration

// Package debremote tests the remote class of the Debian (APT) format against the integration stack and the counting stub archive of
// test/debstub (R-DEB-019 to R-DEB-023, T-17 WP6): the release documents passed through byte for byte and verified against
// deb.upstreamKeys, the index files served as one snapshot of the stored release, the package files verified against the parsed rows,
// the parse job and the shadowing check's question, the re-verification after a change of the keys, and the purge of a restore without
// remote caches. The replica is the production composition root in the role all, so that its jobs run; the stub runs in the test
// process on plain HTTP and the installation admits its loopback address as an operator would.
//
// What these tests do not prove, and who does: that a real APT client accepts what is served (apt-get update --error-on=any and
// apt-get install through the repository run once by hand on debian:13 and ubuntu:22.04, docs/developer/deb.md), and the acceptance
// criteria that need three replicas behind one address or a virtual Debian repository (WP8).
package debremote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/scope"
	_ "github.com/guided-traffic/trove/internal/formats/all"
	"github.com/guided-traffic/trove/test/debstub"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

const project = testenv.DefaultProject

// The repositories of the installation.
const (
	rKeyed    = "deb-keyed"    // upstreamKeys [a]
	rPlain    = "deb-plain"    // no upstreamKeys
	rFast     = "deb-fast"     // upstreamKeys [a], a metadata period of 2 s
	rFlat     = "deb-flat"     // the flat directory binary/ of the stub
	rFlatRoot = "deb-flatroot" // the flat directory that is the upstream URL itself
	rRequire  = "deb-require"  // checksumPolicy require
)

// scratchRepos is how many repositories of each kind (keyed, plain) newEnv makes for cases that need an empty cache.
const scratchRepos = 12

// denv is one installation with the repositories above, a stub and an administrator.
type denv struct {
	t     *testing.T
	st    *testenv.Stack
	stub  *debstub.Server
	srv   *httptest.Server
	r     *testenv.Replica
	admin string
	// keyA, keyB are the armored public keys of the stub's keys a and b.
	keyA, keyB string
	// used counts the scratch repositories handed out.
	used map[bool]int
}

// newEnv starts the stub, the installation and one replica in the role all.
func newEnv(t *testing.T) *denv {
	t.Helper()
	stub, err := debstub.New(debstub.Config{})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)
	e := &denv{t: t, stub: stub, srv: srv, used: map[bool]int{}}
	ring := func(names ...string) string {
		b, err := stub.Keyring(names...)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	e.keyA, e.keyB = ring("a"), ring("b")
	e.st = testenv.NewStack(t, testenv.StackOptions{Name: "debremote", Env: []string{
		`TROVE_PROXY_OUTBOUND_ADDRESS_POLICY_ALLOW_CIDRS=["127.0.0.1/32"]`,
		"TROVE_PROXY_LOCK_POLL_INTERVAL_MS=50",
		"TROVE_VULNERABILITIES_SOURCES_OSV_MODE=off",
		"TROVE_API_RATE_LIMITS_ENABLED=false",
	}})
	e.repo(rKeyed, "/debian/", e.keyA, 300, "verify-if-available")
	e.repo(rPlain, "/debian/", "", 300, "verify-if-available")
	e.repo(rFast, "/debian/", e.keyA, 2, "verify-if-available")
	e.repo(rFlat, "/flat/", "", 300, "verify-if-available")
	e.repo(rFlatRoot, "/flatroot/", "", 300, "verify-if-available")
	e.repo(rRequire, "/debian/", e.keyA, 300, "require")
	// Repositories nothing has been fetched through, one per case that needs an empty cache.
	for i := 1; i <= scratchRepos; i++ {
		e.repo("deb-s"+strconv.Itoa(i), "/debian/", e.keyA, 300, "verify-if-available")
		e.repo("deb-p"+strconv.Itoa(i), "/debian/", "", 300, "verify-if-available")
	}
	admin := e.st.User("rita")
	e.st.Bind("admin", admin)
	e.admin = e.st.Token(admin, scope.Scope{Inherit: true, Admin: true})
	e.r = e.st.Start(t, testenv.ReplicaOptions{Name: "all-1", Role: "all"})
	return e
}

// remoteDoc is the document of a remote Debian repository as the service stores it: the remote section with its defaults and the deb
// member.
func (e *denv) remoteDoc(path, keys string, ttl int, policy string) string {
	deb := `{}`
	if keys != "" {
		deb = `{"upstreamKeys":` + strconv.Quote(keys) + `}`
	}
	return `{"remote":{"url":"` + e.srv.URL + path + `","extraHosts":[],"authMode":"none","username":"","password":null,` +
		`"clientCertificate":null,"headers":[],"forwardAuthOnRedirect":false,"outboundProxy":{"mode":"direct"},` +
		`"connectTimeoutMs":5000,"readTimeoutMs":15000,"sourceNetwork":null,"cacheFillAction":"read",` +
		`"cache":{"storeArtifacts":true,"metadataTtlSeconds":` + strconv.Itoa(ttl) + `,"metadataRefreshTimeoutSeconds":10,"notFoundTtlSeconds":60,"maxStaleSeconds":0,"unusedRetentionHours":0},` +
		`"integrity":{"checksumPolicy":"` + policy + `","blockUnexpectedContentTypes":true,"allowedContentTypes":[]},` +
		`"availability":{"mode":"online","failureThreshold":3,"circuitOpenSeconds":30},` +
		`"limits":{"maxArtifactBytes":null,"maxMetadataBytes":null,"maxConcurrentFetches":16}},"deb":` + deb + `}`
}

func (e *denv) repo(key, path, keys string, ttl int, policy string) {
	e.t.Helper()
	e.st.Repository(key, "remote", "deb", e.remoteDoc(path, keys, ttl, policy))
}

// fresh returns a repository of the installation that nothing has been fetched through: with the key a, or without keys.
func (e *denv) fresh(keyed bool) string {
	e.t.Helper()
	e.used[keyed]++
	if e.used[keyed] > scratchRepos {
		e.t.Fatal("no scratch repository left")
	}
	if keyed {
		return "deb-s" + strconv.Itoa(e.used[keyed])
	}
	return "deb-p" + strconv.Itoa(e.used[keyed])
}

// get is a client's GET of a path below the repository URL, as the administrator.
func (e *denv) get(repo, path string, hdr ...string) testenv.Answer {
	e.t.Helper()
	return e.do(http.MethodGet, repo, path, hdr...)
}

func (e *denv) do(method, repo, path string, hdr ...string) testenv.Answer {
	e.t.Helper()
	return e.st.Do(e.r, method, "/api/deb/"+project+"/"+repo+"/"+path, e.admin, nil, hdr...)
}

// stubGet reads a path of the stub itself, uncounted by resetting afterwards is not needed: callers read it before they reset.
func (e *denv) stubGet(path string) []byte {
	e.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, e.srv.URL+path, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var sb strings.Builder
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return []byte(sb.String())
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// eventually polls until the condition holds.
func eventually(t *testing.T, within time.Duration, what string, ok func() bool) {
	t.Helper()
	testenv.WaitFor(t, what, within, ok)
}

// repoID is the database id of a repository of the default project.
func (e *denv) repoID(key string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT r.id FROM repositories r JOIN project p ON p.id = r.project_id WHERE p.key = $1 AND r.key = $2`, project, key)
}

// rows counts the rows of a remote table for a repository.
func (e *denv) rows(table, repo string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT count(*) FROM `+table+` WHERE repo_id = $1`, e.repoID(repo))
}

// requests is the number of GETs the stub saw for a path.
func (e *denv) requests(path string) int { return e.stub.Counts().Requests(http.MethodGet, path) }

const (
	inRelease = debstub.ArchiveBase + "/dists/stable/InRelease"
	suitePath = "dists/stable/"
	packages  = "main/binary-amd64/Packages"
)
