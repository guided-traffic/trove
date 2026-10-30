//go:build integration

package npm

import (
	"bytes"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/npmstub"
	"github.com/guided-traffic/trove/test/npmstub/corpus"
)

// The remote and virtual classes (R-NPM-024 to R-NPM-033) against the integration stack,
// with the counting test upstream of test/npmstub in the test process: a TLS server the
// replicas trust and may reach (the address policy admits 127.0.0.1/32 for the test), whose
// counters say what the replicas asked of it. The repositories are rows of the seed, so
// every replica builds its first snapshot with them.

// renv is one installation with the repositories below, the upstream and four people.
type renv struct {
	*env
	stub *npmstub.Server
	run  *npmstub.Running
	ss   *npmstub.SelfSigned
	lead testenv.Person
	tokL string
	// opsPAT administers through the default endpoint; acmeSAT is customer-acme's token, bound to
	// the endpoint customers (the fixture of R-EP-012, exposure_test.go).
	opsPAT, acmeSAT string
}

// remoteOptions are the settings a test changes.
type remoteOptions struct {
	// Valkey: the replicas use the stack's Valkey; false is "Valkey stopped".
	Valkey bool
	// MetadataTTL is the metadata period of npm-remote in seconds (default 3600).
	MetadataTTL int
	// Remote is merged into the remote section of npm-remote and npm-gated.
	Remote map[string]any
	// NPM is the npm section of npm-remote.
	NPM map[string]any
	// Env is further environment of every replica.
	Env []string
}

// The repositories of the installation.
const (
	rLocal   = "npm-local"
	rClaims  = "npm-claims"
	rLocal2  = "npm-local2"
	rLocal3  = "npm-local3"
	rRemote  = "npm-remote"
	rGated   = "npm-gated"
	rFast    = "npm-fast"
	rAll     = "npm-all"
	rClaimed = "npm-claimed-all"
	rGatedV  = "npm-gated-all"
	rTwo     = "npm-two"
	rTwoHS   = "npm-two-hs"
	rNoDeply = "npm-nodeploy"
	rTTL     = "npm-ttl"
	rTTL0    = "npm-ttl0"
	// rCustomers is the virtual repository the endpoint customers exposes: one remote member.
	rCustomers = "npm-customers"
)

func newRemoteEnv(t *testing.T, o remoteOptions) *renv {
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
	st := testenv.NewStack(t, testenv.StackOptions{Name: "npmr", Valkey: o.Valkey, Env: append([]string{
		"TROVE_PROXY_OUTBOUND_TLS_CA_FILES=[" + strconv.Quote(ca) + "]",
		`TROVE_PROXY_OUTBOUND_ADDRESS_POLICY_ALLOW_CIDRS=["127.0.0.1/32"]`,
		"TROVE_PROXY_LOCK_POLL_INTERVAL_MS=50",
	}, o.Env...)})
	e := &env{t: t, st: st}
	e.alice, e.bob, e.carol = st.User("alice"), st.User("bob"), st.User("carol")
	lead := st.User("lead")
	r := &renv{env: e, stub: stub, run: run, ss: ss, lead: lead}

	ttl := o.MetadataTTL
	if ttl == 0 {
		ttl = 3600
	}
	local := func(key string, autoClaim bool) {
		st.Repository(key, "local", "npm", fmt.Sprintf(`{"local":{"autoClaim":%t,"packageOwnership":"enforce"}}`, autoClaim))
	}
	local(rLocal, false)
	local(rClaims, true)
	local(rLocal2, false)
	local(rLocal3, false)
	st.Repository(rRemote, "remote", "npm", r.remoteConfig(ttl, o.Remote, o.NPM))
	st.Repository(rGated, "remote", "npm", r.remoteConfig(ttl, merged(o.Remote, map[string]any{"cacheFillAction": "deploy"}), nil))
	st.Repository(rFast, "remote", "npm", r.remoteConfig(2, o.Remote, nil))
	virtual := func(key string, cfg string, target string, members ...string) {
		st.Repository(key, "virtual", "npm", cfg)
		for i, m := range members {
			st.Exec(`INSERT INTO virtual_members (virtual_id, position, member_id)
				SELECT v.id, $2, m.id FROM repositories v, repositories m WHERE v.key = $1 AND m.key = $3`, key, i, m)
		}
		if target != "" {
			st.Exec(`INSERT INTO virtual_deploy_targets (virtual_id, target_id)
				SELECT v.id, t.id FROM repositories v, repositories t WHERE v.key = $1 AND t.key = $2`, key, target)
		}
	}
	virtual(rAll, `{}`, rLocal, rLocal, rRemote)
	virtual(rClaimed, `{}`, rClaims, rClaims, rRemote)
	virtual(rGatedV, `{}`, rLocal, rLocal, rGated)
	virtual(rTwo, `{}`, rLocal, rLocal, rLocal2, rLocal3)
	virtual(rTwoHS, `{"npm":{"distTagStrategy":"highest-semver"}}`, rLocal, rLocal, rLocal2, rLocal3)
	virtual(rNoDeply, `{}`, "", rLocal, rRemote)
	virtual(rTTL, `{"virtual":{"mergedMetadataTtlSeconds":600}}`, rLocal, rLocal, rFast)
	virtual(rTTL0, `{"virtual":{"mergedMetadataTtlSeconds":0}}`, rLocal, rLocal, rFast)
	virtual(rCustomers, `{}`, "", rRemote)

	locals := []string{rLocal, rClaims, rLocal2, rLocal3}
	others := []string{rRemote, rGated, rFast, rAll, rClaimed, rGatedV, rTwo, rTwoHS, rNoDeply, rTTL, rTTL0, rCustomers}
	st.Policy("npm-writers", []string{"read", "deploy", "delete"}, locals, e.alice, e.bob, lead)
	st.Policy("npm-managers", []string{"manage"}, []string{rLocal, rClaims}, lead)
	st.Policy("npm-readers", []string{"read"}, others, e.alice, e.bob, e.carol, lead)
	st.Policy("npm-fillers", []string{"deploy", "delete"}, []string{rGated}, e.bob)

	e.tokA, e.tokB, e.tokC = st.Token(e.alice, scope.Default()), st.Token(e.bob, scope.Default()), st.Token(e.carol, scope.Default())
	r.tokL = st.Token(lead, scope.Default())
	ops, acme := st.User("ops"), st.ServiceAccount("customer-acme")
	st.Bind("admin", ops)
	st.Policy("customers-read", []string{"read"}, []string{rCustomers, rRemote}, acme)
	r.opsPAT = st.Token(ops, scope.Scope{Inherit: true, Admin: true})
	r.acmeSAT = st.Token(acme, scope.Scope{Inherit: true, Endpoints: []string{"customers"}})
	return r
}

// merged returns a copy of a with the members of b.
func merged(a, b map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// remoteConfig is the canonical document of a remote repository that points to the test
// upstream: every member present, as the repository service stores it.
func (r *renv) remoteConfig(ttl int, over, npm map[string]any) string {
	remote := map[string]any{
		"url": r.run.URL + "/", "extraHosts": []any{}, "authMode": "none", "username": "", "password": nil,
		"clientCertificate": nil, "headers": []any{}, "forwardAuthOnRedirect": false, "outboundProxy": map[string]any{"mode": "direct"},
		"connectTimeoutMs": 5000, "readTimeoutMs": 15000, "sourceNetwork": nil, "cacheFillAction": "read",
		"cache": map[string]any{
			"storeArtifacts": true, "metadataTtlSeconds": ttl, "metadataRefreshTimeoutSeconds": 10, "notFoundTtlSeconds": 60,
			"maxStaleSeconds": 0, "unusedRetentionHours": 0,
		},
		"integrity":    map[string]any{"checksumPolicy": "verify-if-available", "blockUnexpectedContentTypes": true, "allowedContentTypes": []any{}},
		"availability": map[string]any{"mode": "online", "failureThreshold": 3, "circuitOpenSeconds": 30},
		"limits":       map[string]any{"maxArtifactBytes": nil, "maxMetadataBytes": nil, "maxConcurrentFetches": 16},
	}
	for k, v := range over {
		remote[k] = v
	}
	doc := map[string]any{"remote": remote}
	if npm != nil {
		doc["npm"] = npm
	}
	b, err := json.Marshal(doc)
	if err != nil {
		r.t.Fatal(err)
	}
	return string(b)
}

// start starts n web replicas.
func (r *renv) start(n int) []*testenv.Replica {
	r.t.Helper()
	out := make([]*testenv.Replica, n)
	for i := range out {
		out[i] = r.st.Start(r.t, testenv.ReplicaOptions{Name: fmt.Sprintf("web-%d", len(r.st.Replicas())+1), Endpoints: customersEndpoint})
	}
	return out
}

// view fetches the package document of a repository.
func (r *renv) view(rep *testenv.Replica, repo, name, token string, hdr ...string) testenv.Answer {
	r.t.Helper()
	return r.get(rep, repo, name, token, hdr...)
}

// versionsOf reads the version numbers a document lists.
func versionsOf(t testing.TB, a testenv.Answer) []string {
	t.Helper()
	var d struct {
		Versions map[string]json.RawMessage `json:"versions"`
	}
	if err := json.Unmarshal(a.Body, &d); err != nil {
		t.Fatalf("%d %s: %v", a.Status, a.Body, err)
	}
	out := make([]string, 0, len(d.Versions))
	for v := range d.Versions {
		out = append(out, v)
	}
	return out
}

// tarballsOf reads the dist.tarball of every version.
func tarballsOf(t testing.TB, a testenv.Answer) map[string]string {
	t.Helper()
	var d struct {
		Versions map[string]struct {
			Dist struct {
				Tarball string `json:"tarball"`
			} `json:"dist"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(a.Body, &d); err != nil {
		t.Fatalf("%d %s: %v", a.Status, a.Body, err)
	}
	out := map[string]string{}
	for v, e := range d.Versions {
		out[v] = e.Dist.Tarball
	}
	return out
}

func tagsOf(t testing.TB, a testenv.Answer) map[string]string {
	t.Helper()
	var d struct {
		Tags map[string]string `json:"dist-tags"`
	}
	if err := json.Unmarshal(a.Body, &d); err != nil {
		t.Fatalf("%d %s: %v", a.Status, a.Body, err)
	}
	return d.Tags
}

// pathOf is the path of a tarball address below the replica's address.
func pathOf(t testing.TB, address string) string {
	t.Helper()
	const base = "http://trove.test"
	if !strings.HasPrefix(address, base) {
		t.Fatalf("%q is not on the base URL %s", address, base)
	}
	return strings.TrimPrefix(address, base)
}

// install fetches a tarball by the address a document gave, as an installer does.
func (r *renv) install(rep *testenv.Replica, address, token string) testenv.Answer {
	r.t.Helper()
	return r.st.Do(rep, http.MethodGet, pathOf(r.t, address), token, nil)
}

// integrityOf is the SRI of bytes.
func integrityOf(b []byte) string {
	sum := sha512.Sum512(b)
	return "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
}

// upstreamPackage returns a plain package of the upstream's corpus with at least n versions.
func upstreamPackage(t testing.TB, n int) *corpus.Package {
	t.Helper()
	for _, p := range corpus.Default().Packages {
		if !strings.HasPrefix(p.Name, "@") && len(p.Versions) >= n && len(p.Versions) < 30 {
			return p
		}
	}
	t.Fatal("the corpus has no such package")
	return nil
}

// scopedPackage returns a scoped package of the upstream's corpus.
func scopedPackage(t testing.TB) *corpus.Package {
	t.Helper()
	for _, p := range corpus.Default().Packages {
		if strings.HasPrefix(p.Name, "@") && len(p.Versions) >= 2 && len(p.Versions) < 30 {
			return p
		}
	}
	t.Fatal("the corpus has no scoped package")
	return nil
}

// upstreamDocs is the number of document requests the upstream saw for a package.
func (r *renv) upstreamDocs(name string) int { return r.stub.Counts().Documents(name) }

// publishAs publishes a version to a repository with the token and fails the test unless 201.
func (r *renv) publishTo(rep *testenv.Replica, repo, token string, p pkg) {
	r.t.Helper()
	if a := r.publish(rep, repo, token, p); a.Status != 201 {
		r.t.Fatalf("publish %s@%s to %s: %d %s", p.Name, p.Version, repo, a.Status, a.Body)
	}
}

var _ = bytes.NewReader
