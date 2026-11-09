//go:build integration

// Package debvirtual tests the virtual class of the Debian (APT) format against the integration stack, a local member and a remote member
// that reads the counting stub archive of test/debstub (R-DEB-025 to R-DEB-028, T-17 WP8): the merge of the members' indexes in member
// order, the claims, the rules for the release of a remote member, the gate and the member paths, uploads through the deployment target,
// the shadowing check through a real virtual repository, and the delay of a build over the large index of the stub. The replica is the
// production composition root in the role all, so that its jobs run.
package debvirtual

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/scope"
	_ "github.com/guided-traffic/trove/internal/formats/all"
	"github.com/guided-traffic/trove/internal/formats/deb/control"
	"github.com/guided-traffic/trove/internal/openpgp"
	"github.com/guided-traffic/trove/test/debstub"
	"github.com/guided-traffic/trove/test/debstub/debgen"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

const project = testenv.DefaultProject

// The repositories of the installation.
const (
	rLocal    = "deb-local" // the local member and the deployment target
	rRemote   = "debian"    // the remote member: the stub, upstreamKeys [a]
	rApt      = "apt"       // virtual: deb-local, debian; stable/main/amd64; deploy target deb-local
	rNoDeploy = "apt-ro"    // virtual: debian; no deployment target
	rBulk     = "apt-bulk"  // virtual: deb-local, debian; the suite bulk with xz
)

const debMedia = "application/vnd.debian.binary-package"

// venv is one installation.
type venv struct {
	t    *testing.T
	st   *testenv.Stack
	stub *debstub.Server
	srv  *httptest.Server
	r    *testenv.Replica

	admin, alice, bob, carol, dave, erin, frank string
	keyA                                        string
}

type options struct {
	// remoteTTL is the metadata period of the remote member in seconds.
	remoteTTL int
	// mergeTTL is virtual.mergedMetadataTtlSeconds of the virtual repositories.
	mergeTTL int
	// bulk is the number of paragraphs of the stub's large index.
	bulk int
	// remoteCfg changes the remote member's document.
	fillAction string
}

func newEnv(t *testing.T, o options) *venv {
	t.Helper()
	if o.remoteTTL == 0 {
		o.remoteTTL = 2
	}
	if o.mergeTTL == 0 {
		o.mergeTTL = 3600
	}
	if o.fillAction == "" {
		o.fillAction = "read"
	}
	stub, err := debstub.New(debstub.Config{BulkParagraphs: o.bulk})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)
	e := &venv{t: t, stub: stub, srv: srv}
	ring, err := stub.Keyring("a")
	if err != nil {
		t.Fatal(err)
	}
	e.keyA = string(ring)
	e.st = testenv.NewStack(t, testenv.StackOptions{Name: "debvirtual", Env: []string{
		`TROVE_PROXY_OUTBOUND_ADDRESS_POLICY_ALLOW_CIDRS=["127.0.0.1/32"]`,
		"TROVE_PROXY_LOCK_POLL_INTERVAL_MS=50",
		"TROVE_VULNERABILITIES_SOURCES_OSV_MODE=off",
		"TROVE_API_RATE_LIMITS_ENABLED=false",
		"TROVE_DEB_INDEX_DELAY=1s",
		"TROVE_DEB_VIRTUAL_INDEX_INTERVAL=10s",
		"TROVE_DEB_INDEX_WAIT_MAX=2s",
	}})
	e.st.Repository(rLocal, "local", "deb", `{"local":{"packageOwnership":"off"}}`)
	e.st.Repository(rRemote, "remote", "deb", e.remoteDoc(o.remoteTTL, o.fillAction))
	e.virtual(rApt, `{"deb":{"suites":["stable"],"components":["main"],"architectures":["amd64"]},"virtual":{"mergedMetadataTtlSeconds":`+strconv.Itoa(o.mergeTTL)+`}}`, rLocal, rLocal, rRemote)
	e.virtual(rNoDeploy, `{"deb":{"suites":["stable"],"components":["main"],"architectures":["amd64"]},"virtual":{"mergedMetadataTtlSeconds":`+strconv.Itoa(o.mergeTTL)+`}}`, "", rRemote)
	e.virtual(rBulk, `{"deb":{"suites":["bulk"],"components":["main"],"architectures":["amd64"],"compression":["xz"]},"virtual":{"mergedMetadataTtlSeconds":`+strconv.Itoa(o.mergeTTL)+`}}`, rLocal, rLocal, rRemote)

	admin, alice, bob, carol, dave := e.st.User("rita"), e.st.User("alice"), e.st.User("bob"), e.st.User("carol"), e.st.User("dave")
	e.st.Bind("admin", admin)
	all := []string{rLocal, rRemote, rApt, rNoDeploy, rBulk}
	e.st.Policy("alice-reads", []string{"read"}, all, alice)
	e.st.Policy("alice-deploys", []string{"deploy", "delete"}, []string{rLocal}, alice)
	e.st.Policy("bob-reads", []string{"read"}, []string{rApt, rLocal}, bob)         // no read on the remote member
	e.st.Policy("carol-reads", []string{"read"}, []string{rApt}, carol)             // no read on any member
	e.st.Policy("dave-deploys", []string{"read", "deploy"}, []string{rLocal}, dave) // a target writer without the gate
	erin, frank := e.st.User("erin"), e.st.User("frank")
	e.st.Policy("erin-reads", []string{"read"}, []string{rLocal, rRemote}, erin)             // every member, not the virtual repository
	e.st.Policy("frank-grants", []string{"read", "deploy", "delete"}, []string{rApt}, frank) // deploy on the virtual repository itself
	e.admin = e.st.Token(admin, scope.Scope{Inherit: true, Admin: true})
	e.erin, e.frank = e.st.Token(erin, scope.Default()), e.st.Token(frank, scope.Default())
	e.alice, e.bob = e.st.Token(alice, scope.Default()), e.st.Token(bob, scope.Default())
	e.carol, e.dave = e.st.Token(carol, scope.Default()), e.st.Token(dave, scope.Default())
	e.r = e.st.Start(t, testenv.ReplicaOptions{Name: "all-1", Role: "all"})
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		// The tail of the replica's log is full of state polls; the lines of the build and the warnings are what explains a failure.
		var keep []string
		for _, l := range strings.Split(e.r.Logs.String(), "\n") {
			if strings.Contains(l, `"component":"deb-index"`) || strings.Contains(l, `"level":"ERROR"`) || strings.Contains(l, `"level":"WARN"`) {
				keep = append(keep, l)
			}
		}
		t.Logf("build and warning lines of the log:\n%s", testenv.Tail(strings.Join(keep, "\n"), 25))
	})
	for _, repo := range []string{rLocal, rApt, rNoDeploy, rBulk} {
		e.primaryKey(repo)
	}
	return e
}

// virtual seeds a virtual repository with its members in order and its deployment target ("" for none).
func (e *venv) virtual(key, cfg, target string, members ...string) {
	e.t.Helper()
	e.st.Repository(key, "virtual", "deb", cfg)
	for _, m := range members {
		pos := e.st.Number(`SELECT count(*) FROM virtual_members vm JOIN repositories v ON v.id = vm.virtual_id WHERE v.key = $1`, key)
		e.st.Exec(`INSERT INTO virtual_members (virtual_id, position, member_id)
			SELECT v.id, $2, m.id FROM repositories v, repositories m WHERE v.key = $1 AND m.key = $3`, key, pos, m)
	}
	if target != "" {
		e.st.Exec(`INSERT INTO virtual_deploy_targets (virtual_id, target_id) SELECT v.id, t.id FROM repositories v, repositories t WHERE v.key = $1 AND t.key = $2`, key, target)
	}
}

// remoteDoc is the document of the remote member: the stub's archive with the key a.
func (e *venv) remoteDoc(ttl int, fill string) string {
	return `{"remote":{"url":"` + e.srv.URL + debstub.ArchiveBase + `/","extraHosts":[],"authMode":"none","username":"","password":null,` +
		`"clientCertificate":null,"headers":[],"forwardAuthOnRedirect":false,"outboundProxy":{"mode":"direct"},` +
		`"connectTimeoutMs":5000,"readTimeoutMs":15000,"sourceNetwork":null,"cacheFillAction":"` + fill + `",` +
		`"cache":{"storeArtifacts":true,"metadataTtlSeconds":` + strconv.Itoa(ttl) + `,"metadataRefreshTimeoutSeconds":10,"notFoundTtlSeconds":60,"maxStaleSeconds":0,"unusedRetentionHours":0},` +
		`"integrity":{"checksumPolicy":"verify-if-available","blockUnexpectedContentTypes":true,"allowedContentTypes":[]},` +
		`"availability":{"mode":"online","failureThreshold":3,"circuitOpenSeconds":30},` +
		`"limits":{"maxArtifactBytes":null,"maxMetadataBytes":null,"maxConcurrentFetches":16}},"deb":{"upstreamKeys":` + strconv.Quote(e.keyA) + `}}`
}

// primaryKey gives a repository its first signing key (the stack seeds repositories by SQL, so the creation hook did not run).
func (e *venv) primaryKey(repo string) {
	e.t.Helper()
	keys := "/api/v1/projects/" + project + "/repositories/" + repo + "/deb/signing-keys"
	if a := e.st.Do(e.r, "POST", keys, e.admin, map[string]any{"generate": true}); a.Status/100 != 2 {
		e.t.Fatalf("create the first key of %s: %d %s", repo, a.Status, a.Body)
	}
	if a := e.st.Do(e.r, "POST", keys+"/promote", e.admin, nil); a.Status/100 != 2 {
		e.t.Fatalf("promote the first key of %s: %d %s", repo, a.Status, a.Body)
	}
}

func (e *venv) url(repo, path string) string {
	return "/api/deb/" + project + "/" + repo + "/" + path
}

func (e *venv) get(repo, path, token string, hdr ...string) testenv.Answer {
	e.t.Helper()
	return e.st.Do(e.r, "GET", e.url(repo, path), token, nil, hdr...)
}

func codeOf(a testenv.Answer) string {
	var p struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(a.Body, &p)
	return p.Code
}

// upload sends a package to a repository's upload route.
func (e *venv) upload(repo, token, name, version string, dists ...string) testenv.Answer {
	e.t.Helper()
	d, err := debgen.Build(debgen.Hello(name, version, "amd64"))
	if err != nil {
		e.t.Fatal(err)
	}
	return e.uploadBytes(repo, token, name+"_"+version+"_amd64.deb", d.Bytes, dists...)
}

func (e *venv) uploadBytes(repo, token, file string, body []byte, dists ...string) testenv.Answer {
	e.t.Helper()
	path := e.url(repo, "upload/"+file+"?distribution="+strings.Join(dists, "&distribution="))
	return e.st.Do(e.r, "PUT", path, token, body, "Content-Type", debMedia)
}

// suiteState reads the index state of a repository.
type suiteState struct {
	Distribution string `json:"distribution"`
	PublishedSeq int64  `json:"publishedSeq"`
	ChangeSeq    int64  `json:"changeSeq"`
	Generation   int64  `json:"generation"`
	State        string `json:"state"`
	Reason       string `json:"reason"`
	ReleaseDate  string `json:"releaseDate"`
}

func (e *venv) suite(repo, suite string) (suiteState, bool) {
	e.t.Helper()
	a := e.st.Do(e.r, "GET", "/api/v1/projects/"+project+"/repositories/"+repo+"/deb/index", e.admin, nil)
	if a.Status != 200 {
		e.t.Fatalf("index state of %s = %d %s", repo, a.Status, a.Body)
	}
	var body struct {
		Suites []suiteState `json:"suites"`
	}
	if err := json.Unmarshal(a.Body, &body); err != nil {
		e.t.Fatal(err)
	}
	for _, s := range body.Suites {
		if s.Distribution == suite {
			return s, true
		}
	}
	return suiteState{}, false
}

// published waits until the virtual repository has a generation of the suite for which cond holds (nil: any).
func (e *venv) published(repo, suite string, within time.Duration, cond func(suiteState) bool) suiteState {
	e.t.Helper()
	var last suiteState
	testenv.WaitFor(e.t, repo+" "+suite+" to publish", within, func() bool {
		s, ok := e.suite(repo, suite)
		last = s
		return ok && s.Generation > 0 && s.State != "failed" && (cond == nil || cond(s))
	})
	return last
}

// packages reads the merged Packages.gz of a suite as paragraphs.
func (e *venv) packages(repo, suite, token string) []control.Paragraph {
	e.t.Helper()
	a := e.get(repo, "dists/"+suite+"/main/binary-amd64/Packages.gz", token)
	if a.Status != 200 {
		e.t.Fatalf("Packages.gz of %s = %d %s", repo, a.Status, a.Body)
	}
	zr, err := gzip.NewReader(bytes.NewReader(a.Body))
	if err != nil {
		e.t.Fatal(err)
	}
	plain, err := io.ReadAll(zr)
	if err != nil {
		e.t.Fatal(err)
	}
	r := control.NewReader(bytes.NewReader(plain), false)
	var out []control.Paragraph
	for {
		p, err := r.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			e.t.Fatal(err)
		}
		out = append(out, p)
	}
}

func find(ps []control.Paragraph, name, version string) (control.Paragraph, bool) {
	for _, p := range ps {
		if p.Value("Package") == name && (version == "" || p.Value("Version") == version) {
			return p, true
		}
	}
	return control.Paragraph{}, false
}

// verifies checks InRelease and Release.gpg of a suite against the signing key of the repository.
func (e *venv) verifies(repo, suite string) control.Paragraph {
	e.t.Helper()
	key := e.get(repo, "signing-key.asc", e.admin)
	certs, err := openpgp.ParseCertificates(key.Body, time.Now(), openpgp.DefaultLimits())
	if err != nil {
		e.t.Fatal(err)
	}
	in, rel, gpg := e.get(repo, "dists/"+suite+"/InRelease", e.admin), e.get(repo, "dists/"+suite+"/Release", e.admin), e.get(repo, "dists/"+suite+"/Release.gpg", e.admin)
	for _, a := range []testenv.Answer{in, rel, gpg} {
		if a.Status != 200 {
			e.t.Fatalf("a release document of %s = %d %s", repo, a.Status, a.Body)
		}
	}
	now := time.Now()
	text, _, err := openpgp.VerifyClear(in.Body, certs, now, openpgp.DefaultLimits())
	if err != nil {
		e.t.Fatalf("InRelease of %s does not verify with the repository's key: %v", repo, err)
	}
	if !bytes.Equal(text, rel.Body) {
		e.t.Error("InRelease does not sign Release byte for byte")
	}
	if _, err := openpgp.VerifyDetached(rel.Body, gpg.Body, certs, now, openpgp.DefaultLimits()); err != nil {
		e.t.Errorf("Release.gpg of %s does not verify: %v", repo, err)
	}
	r := control.NewReader(bytes.NewReader(rel.Body), false)
	p, err := r.Next()
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

var _ = http.MethodGet
