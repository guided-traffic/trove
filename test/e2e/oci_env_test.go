//go:build e2e

package e2e

// The harness of the OCI acceptance tests (R-OCI-035, T-16): the local repositories of the installation, a publisher with a token, the
// two clients of oci_clients_test.go (the Docker Engine with Buildx and Podman) driven through `kubectl exec`, the control client of the
// counting OCI test upstream, and the images the tests build (a `FROM scratch` image per test, so that no client needs a base image from
// a public registry). The scenarios are in oci_local_test.go (login, push, pull, anonymous pull, multi-platform images),
// oci_remote_test.go (pull-through, the flight of six concurrent pulls, the mirror), oci_virtual_test.go, oci_engine_test.go (the Docker
// Engine mirror of a bound host, the image pull secret of a Kubernetes pod), oci_snippets_test.go, oci_closing_test.go and
// oci_long_test.go (the sets oci-closing and oci-long). docs/developer/e2e.md describes the whole.
//
// Every client command runs in a directory of its own below /work, with a Docker configuration directory and a registry authentication
// file of its own, so no scenario meets the login of another. The credential reaches a command through the variable TROVE_TOKEN, as the
// setup snippets of R-OCI-030 read it.

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/ocistub"
)

const (
	ociLocalKey  = "oci-e2e"      // private; the repository of the clients with credentials
	ociOpenKey   = "oci-e2e-open" // anonymous read: the repository a client pulls from without a login
	ociPublisher = "e2e-oci"      // the publisher: read, deploy and delete on both repositories
)

// the media types a manifest request lists (R-OCI-013)
const ociManifestAccept = "application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, " +
	"application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json"

// ociFixture is the installation the OCI tests share: created once per run, idempotent over runs.
type ociFixture struct {
	adm   *api
	token string // the publisher's token
	run   string // six hex digits, part of every image name and tag this run pushes
}

var (
	ociFxOnce sync.Once
	ociFxVal  *ociFixture
	ociFxErr  error
)

// ociFx puts the cluster into the scatter profile and creates the repositories and the publisher. The profile is the one R-OCI-032
// names: three packages replicas, every request to another one.
func ociFx(t testing.TB) *ociFixture {
	t.Helper()
	useScatter(t)
	ociFxOnce.Do(func() {
		ociFxErr = fmt.Errorf("the fixture did not finish (see the first failure)")
		ociFxVal, ociFxErr = buildOCIFixture(t)
	})
	if ociFxErr != nil {
		t.Fatalf("OCI fixture: %v", ociFxErr)
	}
	return ociFxVal
}

func buildOCIFixture(t testing.TB) (*ociFixture, error) {
	adm := adminAPI(t)
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	fx := &ociFixture{adm: adm, run: hex.EncodeToString(b)}
	for _, body := range []map[string]any{
		{"key": ociLocalKey, "class": "local", "format": "oci"},
		{"key": ociOpenKey, "class": "local", "format": "oci"},
	} {
		r, err := adm.send(http.MethodPost, reposRoute, body)
		if err != nil {
			return nil, err
		}
		if r.Status != 201 && r.Status != 409 {
			return nil, fmt.Errorf("create %v: %s", body["key"], r)
		}
	}
	ensureSA(t, adm, ociPublisher)
	ensurePolicy(t, adm, ociPublisher, []string{ociLocalKey, ociOpenKey}, [2]any{"sa:" + ociPublisher, []string{"read", "deploy", "delete"}})
	ensurePolicy(t, adm, ociPublisher+"-anonymous", []string{ociOpenKey}, [2]any{"anonymous", []string{"read"}})
	fx.token = freshSAToken(t, adm, ociPublisher, 4*3600, map[string]any{"inherit": true}).Token
	return fx, nil
}

// image is the reference of an image of a repository of the suite's project: <host>/e2e/<key>/<name>[:<tag>].
func (fx *ociFixture) image(t testing.TB, key, name, tag string) string {
	t.Helper()
	ref := ociRegistryHost(t) + "/" + repoRef(key) + "/" + name
	if tag != "" {
		ref += ":" + tag
	}
	return ref
}

// name is an image name of this run: unique per scenario and run, so that a rerun on the same cluster never meets a tag that exists
// (a moved tag is refused without `overwrite`).
func (fx *ociFixture) name(group string) string { return "e2e-" + fx.run + "-" + group }

// registry is a client of the registry address of the edge with the publisher's token as a direct credential (R-OCI-004).
func (fx *ociFixture) registry(t testing.TB) *api { return newAPI(t).withToken(fx.token) }

// head asks the registry for a manifest and returns the reply.
func (fx *ociFixture) head(t testing.TB, repo, name, ref string) reply {
	t.Helper()
	return fx.registry(t).do(http.MethodHead, "/v2/"+repoRef(repo)+"/"+name+"/manifests/"+ref, nil, "Accept", ociManifestAccept)
}

// digestOf is the digest of the manifest a tag names in a repository, from the registry's own answer.
func (fx *ociFixture) digestOf(t testing.TB, repo, name, tag string) string {
	t.Helper()
	r := fx.head(t, repo, name, tag).want(t, "manifest of "+repo+"/"+name+":"+tag, 200)
	d := r.Header.Get("Docker-Content-Digest")
	if !strings.HasPrefix(d, "sha256:") {
		t.Fatalf("manifest of %s/%s:%s: Docker-Content-Digest %q", repo, name, tag, d)
	}
	return d
}

// withAnonymousRead turns the runtime switch anonymousAccess on (R-AUTH-040) for the test and puts the previous value back at its end.
// The repository oci-e2e-open grants `read` to anonymous; nothing else of the installation does.
func (fx *ociFixture) withAnonymousRead(t *testing.T, keys ...string) {
	t.Helper()
	cur := fx.adm.get("/api/v1/settings/auth").want(t, "read the authentication settings", 200)
	var s struct {
		AnonymousAccess bool `json:"anonymousAccess"`
	}
	cur.JSON(t, &s)
	if !s.AnonymousAccess {
		set := func(on bool) reply {
			g := fx.adm.get("/api/v1/settings/auth").want(t, "read the authentication settings", 200)
			return fx.adm.do(http.MethodPatch, "/api/v1/settings/auth", map[string]any{"anonymousAccess": on}, "If-Match", g.Header.Get("ETag"), "Content-Type", "application/merge-patch+json")
		}
		set(true).want(t, "anonymous access on", 200)
		t.Cleanup(func() { set(false).want(t, "anonymous access off", 200) })
	}
	if len(keys) == 0 {
		keys = []string{ociOpenKey}
	}
	// in force on every replica within the revocation bound: wait until every packages replica lets an anonymous client read
	seen := map[string]bool{}
	waitFor(t, "anonymous read of "+strings.Join(keys, ", ")+" on every packages replica", time.Minute, time.Second, func() bool {
		clear(seen)
		for range 9 {
			for _, key := range keys {
				r, err := newAPI(t).send(http.MethodGet, "/v2/"+repoRef(key)+"/zz-probe/tags/list", nil)
				if err != nil || r.Status == 401 || r.Status == 403 {
					return false
				}
				seen[r.Replica()] = true
			}
		}
		return len(seen) >= 3
	})
}

// ociReadableOnEveryReplica waits until each packages replica lets the token read the new repositories (the bound of R-AUTHZ-028 plus
// one second for the poll and the request, as awaitReadableOnEveryReplica does for npm). A manifest that does not exist in a repository the
// token reads answers 404 MANIFEST_UNKNOWN; before the replica knows the repository or the grant it answers NAME_UNKNOWN.
func ociReadableOnEveryReplica(t testing.TB, token string, keys ...string) {
	t.Helper()
	ociReadableOnEveryReplicaFor(t, token, "zz-probe", keys...)
}

// ociReadableOnEveryReplicaFor probes with an image name the repository admits (a mirror admits its include patterns only).
func ociReadableOnEveryReplicaFor(t testing.TB, token, name string, keys ...string) {
	t.Helper()
	addrs, stop := packagesForwards(t)
	defer stop()
	const within = 5 * time.Second
	for i, addr := range addrs {
		a := apiAtPod(t, addr, token)
		for _, key := range keys {
			start := time.Now()
			for {
				r := a.getOverForward("/v2/" + repoRef(key) + "/" + name + "/manifests/none")
				if r.Status == 404 && strings.Contains(string(r.Body), "MANIFEST_UNKNOWN") {
					break
				}
				if time.Since(start) > within {
					t.Fatalf("replica %d: repository %s answered %s after its creation, want 404 MANIFEST_UNKNOWN within %s (R-AUTHZ-028)", i, key, r, within)
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
	}
}

// ── the clients ──────────────────────────────────────────────────────────────

// ociClient is one scenario with one client in one clients pod. Everything it writes lives below /work/<tag> in the pod.
type ociClient struct {
	t     testing.TB
	pod   int
	tool  string // ociDocker or ociPodman
	tag   string
	token string // what $TROVE_TOKEN expands to in the scenario's commands
	n     int    // build contexts made so far
}

// client returns the scenario of the name with the tool in pod i, holding the publisher's token.
func (fx *ociFixture) client(t testing.TB, pod int, tool, name string) *ociClient {
	t.Helper()
	return &ociClient{t: t, pod: pod, tool: tool, tag: tool + "-" + name + "-" + fx.run, token: fx.token}
}

func (c *ociClient) dir() string { return "/work/" + c.tag }

// argv wraps a command: its working directory is the scenario's, its environment the scenario's Docker configuration directory and
// registry authentication file and the token.
func (c *ociClient) argv(cmd ...string) []string {
	return append([]string{
		"env",
		"DOCKER_CONFIG=" + c.dir() + "/docker-config",
		"REGISTRY_AUTH_FILE=" + c.dir() + "/auth.json",
		"TROVE_TOKEN=" + c.token,
		"sh", "-c", `mkdir -p "$1" && cd "$1" && shift && exec "$@"`, "sh", c.dir(),
	}, cmd...)
}

// podmanFlags keep Podman from needing systemd or a journal in a container.
var podmanFlags = []string{"--cgroup-manager=cgroupfs", "--events-backend=file"}

// cli is the client's command line up to its sub-command.
func (c *ociClient) cli(args ...string) []string {
	if c.tool == ociPodman {
		return append(append([]string{"podman"}, podmanFlags...), args...)
	}
	return append([]string{"docker"}, args...)
}

// exec runs a command of the client that must succeed and returns its output.
func (c *ociClient) exec(args ...string) string {
	c.t.Helper()
	out, err := c.try(args...)
	if err != nil {
		c.t.Fatalf("%s %s: %v\n%s", c.tool, strings.Join(args, " "), err, tailLines(out, 30))
	}
	return out
}

// try runs a command of the client and returns its output (both streams) and the error of a non-zero exit.
func (c *ociClient) try(args ...string) (string, error) {
	c.t.Helper()
	return ociClientTry(c.t, c.pod, c.tool, c.argv(c.cli(args...)...)...)
}

// shell runs a shell command line in the scenario's environment (a setup snippet).
func (c *ociClient) shell(script string) (string, error) {
	c.t.Helper()
	return ociClientTry(c.t, c.pod, c.tool, c.argv("sh", "-c", script)...)
}

// write puts a file below the scenario's directory.
func (c *ociClient) write(rel string, content []byte) {
	c.t.Helper()
	ociClientExec(c.t, c.pod, c.tool, "sh", "-c", `mkdir -p "$(dirname "$1")" && printf %s "$2" | base64 -d >"$1"`,
		"sh", c.dir()+"/"+rel, base64.StdEncoding.EncodeToString(content))
}

// login signs the client in to the registry with the token, as the snippet of R-OCI-030 does for the host.
func (c *ociClient) login(host string) string {
	c.t.Helper()
	out, err := c.shell(`printf '%s' "$TROVE_TOKEN" | ` + strings.Join(c.cli("login", host, "--username", "trove", "--password-stdin"), " "))
	if err != nil || !strings.Contains(out, "Login Succeeded") {
		c.t.Fatalf("%s login %s: %v\n%s", c.tool, host, err, tailLines(out, 20))
	}
	return out
}

// buildContext writes a build context with one file of the given content and a Dockerfile that copies it into a `FROM scratch` image, and
// returns the directory (below the scenario's).
func (c *ociClient) buildContext(payload string, dockerfile ...string) string {
	c.t.Helper()
	c.n++
	dir := fmt.Sprintf("ctx%d", c.n)
	c.write(dir+"/payload", []byte(payload))
	df := "FROM scratch\nCOPY payload /payload\n"
	if len(dockerfile) > 0 {
		df = dockerfile[0]
	}
	c.write(dir+"/Dockerfile", []byte(df))
	return dir
}

// build builds the image of the reference from a one-file context: the payload makes its digest the test's own.
func (c *ociClient) build(ref, payload string) {
	c.t.Helper()
	c.exec("build", "--tag", ref, c.buildContext(payload))
}

// push pushes the image of the reference and returns the output.
func (c *ociClient) push(ref string) string { c.t.Helper(); return c.exec("push", ref) }

// pull pulls the image of the reference and returns the output.
func (c *ociClient) pull(ref string) string { c.t.Helper(); return c.exec("pull", ref) }

// digests lists the manifest digests the client's store knows for the reference (the digest of the pushed or pulled manifest).
func (c *ociClient) digests(ref string) string {
	c.t.Helper()
	if c.tool == ociPodman {
		return c.exec("image", "inspect", "--format", "{{.Digest}} {{range .RepoDigests}}{{.}} {{end}}", ref)
	}
	return c.exec("image", "inspect", "--format", "{{.Id}} {{range .RepoDigests}}{{.}} {{end}}", ref)
}

// holds says whether the client's store holds the image with the manifest digest.
func (c *ociClient) holds(ref, digest string) bool {
	c.t.Helper()
	return strings.Contains(c.digests(ref), strings.TrimPrefix(digest, "sha256:"))
}

// pruneClients empties the image stores of the clients in pods 0 to n-1 (every image and its content): a layer a client holds is not pulled
// again, so a test that counts what the upstream delivered starts from clients that hold nothing, also in a second run on the same pods.
func pruneClients(t testing.TB, n int, tools ...string) {
	t.Helper()
	for i := range n {
		for _, tool := range tools {
			if tool == ociPodman {
				ociClientExec(t, i, ociPodman, "podman", "system", "prune", "--all", "--force")
			} else {
				ociClientExec(t, i, ociDocker, "docker", "system", "prune", "--all", "--force")
			}
		}
	}
}

// ── the counting test upstream ───────────────────────────────────────────────

var (
	ociStubOnce sync.Once
	ociStubVal  *stubControl
)

// ociStubHosts returns the three hosts of the OCI test upstream as deploy/dev/e2e.sh exports them: the registry, the token realm and the
// blob CDN.
func ociStubHosts(t testing.TB) (registry, realm, cdn string) {
	t.Helper()
	registry, realm, cdn = os.Getenv("TROVE_E2E_OCISTUB_HOST"), os.Getenv("TROVE_E2E_OCISTUB_REALM_HOST"), os.Getenv("TROVE_E2E_OCISTUB_CDN_HOST")
	if registry == "" || realm == "" || cdn == "" {
		t.Fatal("TROVE_E2E_OCISTUB_HOST, TROVE_E2E_OCISTUB_REALM_HOST and TROVE_E2E_OCISTUB_CDN_HOST are not set; run `deploy/dev/e2e.sh oci`")
	}
	return registry, realm, cdn
}

// ociStub is the control client of the counting OCI test upstream; the methods reset and resetAll of the npm stub's client work on it as
// well (the control API has the same shape), ociCounts, ociMode and ociImage are the OCI ones.
func ociStub(t testing.TB) *stubControl {
	t.Helper()
	host, _, _ := ociStubHosts(t)
	ociStubOnce.Do(func() { ociStubVal = newStubControl(t, "svc/ocistub", host, "trove-ocistub", 30*time.Second) })
	return ociStubVal
}

// ociMode replaces the switches of the OCI test upstream; the zero Mode clears them.
func (s *stubControl) ociMode(t testing.TB, m ocistub.Mode) {
	t.Helper()
	s.send(t, http.MethodPost, "/-/stub/mode", m)
}

// ociCounts reads the counters of the OCI test upstream.
func (s *stubControl) ociCounts(t testing.TB) ocistub.Counts {
	t.Helper()
	var c ocistub.Counts
	if err := json.Unmarshal(s.send(t, http.MethodGet, "/-/stub/counts", nil), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// ociImage reads the manifest of a tag of the stub with the manifests and blobs below it.
func (s *stubControl) ociImage(t testing.TB, name, reference string) ocistub.ImageInfo {
	t.Helper()
	var info ocistub.ImageInfo
	if err := json.Unmarshal(s.send(t, http.MethodGet, "/-/stub/image?name="+name+"&reference="+reference, nil), &info); err != nil {
		t.Fatal(err)
	}
	return info
}

// ociRemoteBody is a remote repository of the OCI test upstream, with the realm and the CDN host in its extraHosts (R-OCI-021).
func ociRemoteBody(t testing.TB, key string, settings map[string]any) map[string]any {
	t.Helper()
	registry, realm, cdn := ociStubHosts(t)
	remote := map[string]any{
		"url": "https://" + registry + "/",
		"extraHosts": []map[string]any{
			{"pattern": realm, "sendCredentials": false},
			{"pattern": cdn, "sendCredentials": false},
		},
	}
	for k, v := range settings {
		remote[k] = v
	}
	return map[string]any{"key": key, "class": "remote", "format": "oci", "remote": remote}
}

// ociUpstreamRepos are a local repository, a remote repository over the test upstream and a virtual one over both (the local member is the
// deployment target), created for one test so that the cache a test starts with is empty and no tag of an earlier run is in the way.
type ociUpstreamRepos struct{ local, remote, all string }

func (fx *ociFixture) upstreamRepos(t testing.TB, purpose string, settings map[string]any) ociUpstreamRepos {
	t.Helper()
	u := ociUpstreamRepos{
		local:  fmt.Sprintf("oci-%s-%s-local", purpose, fx.run),
		remote: fmt.Sprintf("oci-%s-%s-remote", purpose, fx.run),
		all:    fmt.Sprintf("oci-%s-%s-all", purpose, fx.run),
	}
	fx.adm.do(http.MethodPost, reposRoute, map[string]any{"key": u.local, "class": "local", "format": "oci"}).want(t, "create "+u.local, 201)
	fx.adm.do(http.MethodPost, reposRoute, ociRemoteBody(t, u.remote, settings)).want(t, "create "+u.remote, 201)
	fx.adm.do(http.MethodPost, reposRoute, map[string]any{
		"key": u.all, "class": "virtual", "format": "oci",
		"virtual": map[string]any{"members": []string{u.local, u.remote}, "deployTarget": u.local},
	}).want(t, "create "+u.all, 201)
	ensurePolicy(t, fx.adm, "e2e-oci-"+purpose+"-"+fx.run, []string{u.remote, u.all}, [2]any{"sa:" + ociPublisher, []string{"read"}})
	ensurePolicy(t, fx.adm, "e2e-oci-"+purpose+"-"+fx.run+"-local", []string{u.local}, [2]any{"sa:" + ociPublisher, []string{"read", "deploy", "delete", "manage"}}) // manage: a name a remote member of the virtual repository serves may be published only by its holder (R-REPO-048)
	ociReadableOnEveryReplica(t, fx.token, u.local, u.remote, u.all)
	// A remote that stays behind is asked by the shadowing check of every first push into a local member (R-REPO-048), and one whose upstream
	// was stopped keeps its circuit open: the repositories go with the test.
	t.Cleanup(func() {
		for _, key := range []string{u.all, u.remote, u.local} {
			deleteRepository(t, fx.adm, key)
		}
	})
	return u
}
