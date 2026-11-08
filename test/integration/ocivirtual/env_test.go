//go:build integration

// Package ocivirtual tests the virtual class of the OCI format (R-OCI-027, R-OCI-028) against the integration stack and the counting stub
// registry of test/ocistub: a virtual repository over local and remote members, pulled and pushed through /v2/ with the registry tokens
// the token service issues, as a container client sends them. The remote members reach the stubs through app.Options.DialOverride.
//
// What these tests do not prove, and who does: the acceptance criteria that run `docker pull` and `docker push` need a container client,
// which is the e2e rows of WP11; the same requests are sent here over HTTP (a push is a chunked upload of every blob and the manifest, the
// HEAD of a blob before it) and counted at the stub.
package ocivirtual

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/scope"
	_ "github.com/guided-traffic/trove/internal/formats/all"
	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/ocistub"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

const (
	manifestTypes = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, " +
		"application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"
	ctManifest = "application/vnd.oci.image.manifest.v1+json"
)

// The repositories of the installation.
const (
	rLocal    = "docker-local"    // a local repository
	rHubA     = "hub-a"           // a remote repository: the first stub
	rHubB     = "hub-b"           // a remote repository: the second stub, whose tags differ
	rVirtual  = "docker"          // virtual: docker-local, hub-a; deploy target docker-local
	rReverse  = "docker-reverse"  // virtual: hub-a, docker-local; deploy target docker-local
	rTwo      = "docker-remotes"  // virtual: hub-a, hub-b; no deploy target
	rNoTarget = "docker-readonly" // virtual: docker-local, hub-a; no deploy target
)

// venv is one installation with the repositories above, two stubs, a reader and a writer.
type venv struct {
	t            *testing.T
	st           *testenv.Stack
	stubA, stubB *ocistub.Server
	reader       string
	writer       string
	mu           sync.Mutex
	tokens       map[string]cachedToken
	rep          *testenv.Replica
}

type options struct {
	// claim claims the pattern for docker-local (R-REPO-023).
	claim string
	// plain takes manage from the writer, who then meets the shadowing check of R-REPO-048 for a name a remote member serves.
	plain bool
	// autoClaim leaves the local repository its default, which claims the name of a first push (R-REPO-023).
	autoClaim bool
}

type cachedToken struct {
	token string
	at    time.Time
}

func newEnv(t *testing.T, o options) *venv {
	t.Helper()
	ss, err := ocistub.NewSelfSigned(ocistub.LoopbackHosts...)
	if err != nil {
		t.Fatal(err)
	}
	e := &venv{t: t, tokens: map[string]cachedToken{}}
	dial := map[string]string{}
	var urls [2]string
	for i, dst := range []**ocistub.Server{&e.stubA, &e.stubB} {
		if *dst, err = ocistub.New(ocistub.Config{}); err != nil {
			t.Fatal(err)
		}
		run, err := (*dst).Start("127.0.0.1:0", ss.ServerConfig())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = run.Close() })
		for k, v := range run.DialOverride() {
			dial[k] = v
		}
		urls[i] = run.URL
	}
	// The second stub's tags differ from the first's: app:1 is another image there.
	if _, err := e.stubB.MoveTag("app", "1"); err != nil {
		t.Fatal(err)
	}

	ca := testenv.WriteFile(t, t.TempDir(), "stub-ca.pem", string(ss.CAPEM))
	e.st = testenv.NewStack(t, testenv.StackOptions{Name: "ocivirtual", Env: []string{
		"TROVE_PROXY_OUTBOUND_TLS_CA_FILES=[" + strconv.Quote(filepath.ToSlash(ca)) + "]",
		`TROVE_PROXY_OUTBOUND_ADDRESS_POLICY_ALLOW_CIDRS=["127.0.0.1/32"]`,
		"TROVE_PROXY_LOCK_POLL_INTERVAL_MS=50",
		"TROVE_VULNERABILITIES_SOURCES_OSV_MODE=off",
		"TROVE_API_RATE_LIMITS_ENABLED=false",
	}})

	host := func(h string) map[string]any { return map[string]any{"pattern": h, "sendCredentials": false} }
	e.remote(rHubA, urls[0], host(ocistub.LoopbackRealmHost), host(ocistub.LoopbackCDNHost))
	e.remote(rHubB, urls[1], host(ocistub.LoopbackRealmHost), host(ocistub.LoopbackCDNHost))
	local := `{"local":{"autoClaim":false}}`
	if o.autoClaim {
		local = "{}"
	}
	e.st.Repository(rLocal, "local", "oci", local)
	for key, members := range map[string][]string{
		rVirtual: {rLocal, rHubA}, rReverse: {rHubA, rLocal}, rTwo: {rHubA, rHubB}, rNoTarget: {rLocal, rHubA},
	} {
		e.st.Repository(key, "virtual", "oci", "{}")
		for i, m := range members {
			e.st.Exec(`INSERT INTO virtual_members (virtual_id, position, member_id)
				SELECT v.id, $3, m.id FROM repositories v, repositories m WHERE v.key = $1 AND m.key = $2`, key, m, i)
		}
	}
	for _, key := range []string{rVirtual, rReverse} {
		e.st.Exec(`INSERT INTO virtual_deploy_targets (virtual_id, target_id)
			SELECT v.id, m.id FROM repositories v, repositories m WHERE v.key = $1 AND m.key = $2`, key, rLocal)
	}
	if o.claim != "" {
		e.st.Exec(`INSERT INTO namespace_claims (repo_id, pattern, source, scope) SELECT id, $1, 'manual', 'members' FROM repositories WHERE key = $2`, o.claim, rLocal)
	}

	rita, reader, writer := e.st.User("rita"), e.st.User("reader"), e.st.User("writer")
	e.st.Bind("admin", rita)
	e.reader = e.st.Token(reader, scope.Default())
	e.writer = e.st.Token(writer, scope.Default())
	all := []string{rLocal, rHubA, rHubB, rVirtual, rReverse, rTwo, rNoTarget}
	e.st.Policy("oci-readers", []string{"read"}, all, reader)
	writes := []string{"read", "deploy", "delete"}
	if !o.plain {
		writes = append(writes, "manage")
	}
	e.st.Policy("oci-writers", writes, []string{rLocal, rVirtual, rReverse, rNoTarget}, writer)
	e.st.Policy("oci-writers-read", []string{"read"}, []string{rHubA, rHubB, rTwo}, writer)
	e.rep = e.st.Start(t, testenv.ReplicaOptions{Name: "web-1", DialOverride: dial})
	return e
}

// remote creates a remote repository of the format oci that reads an anonymous stub.
func (e *venv) remote(key, upstream string, extra ...map[string]any) {
	e.t.Helper()
	hosts := make([]any, len(extra))
	for i, h := range extra {
		hosts[i] = h
	}
	doc, err := json.Marshal(map[string]any{"remote": map[string]any{
		"url": upstream, "extraHosts": hosts, "authMode": "none", "username": "", "password": nil,
		"clientCertificate": nil, "headers": []any{}, "forwardAuthOnRedirect": false, "outboundProxy": map[string]any{"mode": "direct"},
		"connectTimeoutMs": 5000, "readTimeoutMs": 15000, "sourceNetwork": nil, "cacheFillAction": "read",
		"cache": map[string]any{
			"storeArtifacts": true, "metadataTtlSeconds": 300, "metadataRefreshTimeoutSeconds": 10, "notFoundTtlSeconds": 60,
			"maxStaleSeconds": 0, "unusedRetentionHours": 0,
		},
		"integrity":    map[string]any{"checksumPolicy": "verify-if-available", "blockUnexpectedContentTypes": true, "allowedContentTypes": []any{}},
		"availability": map[string]any{"mode": "online", "failureThreshold": 3, "circuitOpenSeconds": 30},
		"limits":       map[string]any{"maxArtifactBytes": nil, "maxMetadataBytes": nil, "maxConcurrentFetches": 16},
	}})
	if err != nil {
		e.t.Fatal(err)
	}
	e.st.Repository(key, "remote", "oci", string(doc))
}

// do sends a request below /v2/<project>/<repo>/ with a registry token for the image the path names, as the trove token's holder.
func (e *venv) do(who, actions, method, repo, path string, body []byte, hdr ...string) testenv.Answer {
	e.t.Helper()
	tok := e.registryToken(who, repo, imageOf(path), actions)
	return e.st.Do(e.rep, method, "/v2/"+testenv.DefaultProject+"/"+repo+"/"+path, tok, body, append([]string{"Accept", manifestTypes}, hdr...)...)
}

func (e *venv) get(repo, path string, hdr ...string) testenv.Answer {
	e.t.Helper()
	return e.do(e.reader, "pull", http.MethodGet, repo, path, nil, hdr...)
}

// imageOf is the image name of a protocol path, read from the right: a name may hold a component that is a protocol word.
func imageOf(path string) string {
	path, _, _ = strings.Cut(path, "?")
	path = strings.TrimSuffix(path, "/")
	if i := strings.Index(path, "/blobs/uploads"); i >= 0 {
		return path[:i]
	}
	segs := strings.Split(path, "/")
	n := len(segs)
	switch {
	case n >= 3 && segs[n-2] == "tags" && segs[n-1] == "list", n >= 3 && (segs[n-2] == "manifests" || segs[n-2] == "blobs" || segs[n-2] == "referrers"):
		return strings.Join(segs[:n-2], "/")
	}
	return path
}

// registryToken asks the token service for a token for the actions on an image of a repository, as `docker login` and a pull or a push do.
func (e *venv) registryToken(trove, repo, image, actions string) string {
	e.t.Helper()
	key := trove + "|" + repo + "|" + image + "|" + actions
	e.mu.Lock()
	if c, ok := e.tokens[key]; ok && time.Since(c.at) < time.Minute {
		e.mu.Unlock()
		return c.token
	}
	e.mu.Unlock()
	q := url.Values{"scope": {"repository:" + testenv.DefaultProject + "/" + repo + "/" + image + ":" + actions}}
	a := e.st.Do(e.rep, http.MethodGet, "/v2/_token?"+q.Encode(), trove, nil)
	var body struct {
		Token string `json:"token"`
	}
	if a.Status != http.StatusOK || json.Unmarshal(a.Body, &body) != nil || body.Token == "" {
		e.t.Fatalf("token for %s: %d %s", q.Get("scope"), a.Status, a.Body)
	}
	e.mu.Lock()
	e.tokens[key] = cachedToken{token: body.Token, at: time.Now()}
	e.mu.Unlock()
	return body.Token
}

// mountToken is the token of a cross-repository mount: pull and push on the target image and pull on the source, as a client asks for both scopes.
func (e *venv) mountToken(repo, image, from string) string {
	e.t.Helper()
	q := url.Values{"scope": {
		"repository:" + testenv.DefaultProject + "/" + repo + "/" + image + ":pull,push",
		"repository:" + from + ":pull",
	}}
	a := e.st.Do(e.rep, http.MethodGet, "/v2/_token?"+q.Encode(), e.writer, nil)
	var body struct {
		Token string `json:"token"`
	}
	if a.Status != http.StatusOK || json.Unmarshal(a.Body, &body) != nil || body.Token == "" {
		e.t.Fatalf("mount token: %d %s", a.Status, a.Body)
	}
	return body.Token
}

// image is an OCI image manifest with its blobs, built from a seed.
type image struct {
	config   []byte
	layers   [][]byte
	manifest []byte
}

func digestOf(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

func (i image) digest() string { return digestOf(i.manifest) }

// newImage builds an image of n layers of a few kilobytes each that no other seed shares.
func newImage(seed string, n int) image {
	im := image{config: []byte(fmt.Sprintf(`{"architecture":"amd64","os":"linux","seed":%q}`, seed))}
	for i := range n {
		im.layers = append(im.layers, bytes.Repeat([]byte(fmt.Sprintf("%s layer %d ", seed, i)), 700))
	}
	im.manifest = manifestOf(im.config, im.layers)
	return im
}

// manifestOf is the manifest over a config and layers.
func manifestOf(config []byte, layers [][]byte) []byte {
	type desc struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
		Size      int    `json:"size"`
	}
	m := struct {
		SchemaVersion int    `json:"schemaVersion"`
		MediaType     string `json:"mediaType"`
		Config        desc   `json:"config"`
		Layers        []desc `json:"layers"`
	}{2, ctManifest, desc{"application/vnd.oci.image.config.v1+json", digestOf(config), len(config)}, nil}
	for _, l := range layers {
		m.Layers = append(m.Layers, desc{"application/vnd.oci.image.layer.v1.tar+gzip", digestOf(l), len(l)})
	}
	b, _ := json.Marshal(m)
	return b
}

// manifestBlobs are the config and layer digests a manifest names.
func manifestBlobs(t *testing.T, manifest []byte) []string {
	t.Helper()
	var m struct {
		Config *struct{ Digest string }
		Layers []struct{ Digest string }
	}
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	var out []string
	if m.Config != nil {
		out = append(out, m.Config.Digest)
	}
	for _, l := range m.Layers {
		out = append(out, l.Digest)
	}
	return out
}

// relative is the path of a Location below /v2/<project>/<repo>/, which says which repository it names.
func (e *venv) relative(loc, repo string) string {
	e.t.Helper()
	u, err := url.Parse(loc)
	if err != nil {
		e.t.Fatal(err)
	}
	prefix := "/v2/" + testenv.DefaultProject + "/" + repo + "/"
	rest, ok := strings.CutPrefix(u.Path, prefix)
	if !ok {
		e.t.Fatalf("Location %q does not name %s", loc, prefix)
	}
	if u.RawQuery != "" {
		rest += "?" + u.RawQuery
	}
	return rest
}

// uploadBlob pushes a blob as a client does: a session, the bytes in two chunks, the finishing PUT. It returns the Location of the session.
func (e *venv) uploadBlob(repo, name string, data []byte) string {
	e.t.Helper()
	a := e.do(e.writer, "pull,push", http.MethodPost, repo, name+"/blobs/uploads/", nil)
	if a.Status != http.StatusAccepted {
		e.t.Fatalf("start an upload of %s through %s: %d %s", name, repo, a.Status, a.Body)
	}
	loc := a.Header.Get("Location")
	half := len(data) / 2
	for _, c := range [][2]int{{0, half}, {half, len(data)}} {
		path := e.relative(loc, repo)
		a = e.do(e.writer, "pull,push", http.MethodPatch, repo, path, data[c[0]:c[1]],
			"Content-Type", "application/octet-stream", "Content-Range", fmt.Sprintf("%d-%d", c[0], c[1]-1))
		if a.Status != http.StatusAccepted {
			e.t.Fatalf("chunk %d-%d: %d %s", c[0], c[1], a.Status, a.Body)
		}
		loc = a.Header.Get("Location")
	}
	a = e.do(e.writer, "pull,push", http.MethodPut, repo, e.relative(loc, repo)+"?digest="+digestOf(data), nil)
	if a.Status != http.StatusCreated {
		e.t.Fatalf("finish the upload of %s: %d %s", name, a.Status, a.Body)
	}
	return a.Header.Get("Location")
}

// push pushes an image the way a container client does: every blob the registry does not answer 200 for, then the manifest. It returns
// the digests of the blobs it uploaded.
func (e *venv) push(repo, name, tag string, im image) (uploaded []string) {
	e.t.Helper()
	blobs := append([][]byte{im.config}, im.layers...)
	for _, b := range blobs {
		d := digestOf(b)
		if e.do(e.writer, "pull,push", http.MethodHead, repo, name+"/blobs/"+d, nil).Status == http.StatusOK {
			continue
		}
		e.uploadBlob(repo, name, b)
		uploaded = append(uploaded, d)
	}
	a := e.putManifest(repo, name, tag, im.manifest)
	if a.Status != http.StatusCreated {
		e.t.Fatalf("push %s:%s through %s: %d %s", name, tag, repo, a.Status, a.Body)
	}
	return uploaded
}

func (e *venv) putManifest(repo, name, ref string, manifest []byte) testenv.Answer {
	e.t.Helper()
	return e.do(e.writer, "pull,push", http.MethodPut, repo, name+"/manifests/"+ref, manifest, "Content-Type", ctManifest)
}

// pull is what a client does for a tag: the manifest and every blob it names, all through the repository. It returns the manifest.
func (e *venv) pull(repo, name, ref string) testenv.Answer {
	e.t.Helper()
	a := e.get(repo, name+"/manifests/"+ref)
	if a.Status != http.StatusOK {
		e.t.Fatalf("GET %s:%s through %s: %d %s", name, ref, repo, a.Status, a.Body)
	}
	for _, d := range manifestBlobs(e.t, a.Body) {
		if b := e.get(repo, name+"/blobs/"+d); b.Status != http.StatusOK {
			e.t.Fatalf("GET blob %s of %s through %s: %d %s", d, name, repo, b.Status, b.Body)
		}
	}
	return a
}

// items counts the live files of a repository below a path prefix.
func (e *venv) items(repo, prefix string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT count(*) FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = $1 AND i.kind = 'file' AND i.path LIKE $2`, repo, prefix+"%")
}
