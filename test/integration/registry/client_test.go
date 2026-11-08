//go:build integration

package registry

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/formats/oci"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
)

// req is one request of a registry client.
type req struct {
	method, path string
	host         string
	auth         string // the whole Authorization header value
	header       map[string]string
	body         []byte
}

// send sends the request through the whole handler of an endpoint.
func (e *env) send(endpoint string, q req) answer {
	e.t.Helper()
	r := httptest.NewRequestWithContext(e.t.Context(), q.method, q.path, bytes.NewReader(q.body))
	r.RemoteAddr = "203.0.113.7:4711"
	r.Host = q.host
	if r.Host == "" {
		r.Host = "trove.test"
	}
	if q.auth != "" {
		r.Header.Set("Authorization", q.auth)
	}
	for k, v := range q.header {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.handler(endpoint).ServeHTTP(w, r)
	return answer{w}
}

func digestOf(b []byte) string {
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

// image is a small OCI image: a config, one layer and the manifest that names them.
type image struct {
	config, layer, manifest []byte
}

func newImage(seed string) image {
	cfg := []byte(`{"architecture":"amd64","os":"linux","seed":"` + seed + `"}`)
	layer := []byte("layer of " + seed + strings.Repeat(".", 64))
	m, _ := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config":        map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": digestOf(cfg), "size": len(cfg)},
		"layers":        []any{map[string]any{"mediaType": "application/vnd.oci.image.layer.v1.tar", "digest": digestOf(layer), "size": len(layer)}},
	})
	return image{config: cfg, layer: layer, manifest: m}
}

const manifestType = "application/vnd.oci.image.manifest.v1+json"

// seedBlob stores a blob under an image name of a repository the way a finished upload leaves it. The upload routes are not served
// yet (R-OCI-011 belongs to another work package), so the blob is committed through the item store of the running replica with the
// payload the format's commit hook admits for a seeded item; everything after it - the manifest push, the pulls, the decisions -
// goes through /v2/.
func (e *env) seedBlob(repoPath string, b []byte) {
	e.t.Helper()
	parts := strings.SplitN(repoPath, "/", 3)
	rp, err := e.rep.App.Repositories().ByName(e.t.Context(), qname.Repo{Project: parts[0], Key: parts[1]})
	if err != nil {
		e.t.Fatal(err)
	}
	sp, err := e.rep.App.Items().Spool(e.t.Context(), bytes.NewReader(b), items.IngestOptions{ContentLength: int64(len(b))})
	if err != nil {
		e.t.Fatal(err)
	}
	defer sp.Close()
	if _, err := e.rep.App.Items().Commit(e.t.Context(), items.CommitSpec{
		Repo: rp, Path: repository.Cleaned{Path: oci.BlobPath(parts[2], digestOf(b))}, Spooled: sp, MediaType: "application/octet-stream",
		Origin: items.OriginFill, UpstreamURL: "https://seed.invalid/", MayOverwrite: true, Payload: oci.SeededItem{},
		Actor: audit.Actor{Kind: "system", Name: "push"},
	}); err != nil {
		e.t.Fatalf("commit blob: %v", err)
	}
}

// push seeds the blobs of an image and pushes its manifest through /v2/; it stores an image under repoPath ("web/docker-local/<name>") with the tag and returns the answer of the manifest PUT.
func (e *env) push(auth, repoPath, tag string, img image) answer {
	e.t.Helper()
	e.seedBlob(repoPath, img.config)
	e.seedBlob(repoPath, img.layer)
	return e.send("default", req{
		method: "PUT", path: "/v2/" + repoPath + "/manifests/" + tag, auth: auth, body: img.manifest,
		header: map[string]string{"Content-Type": manifestType},
	})
}

func bearer(t string) string { return "Bearer " + t }

// jsonField reads a string member of a JSON answer.
func jsonField(t *testing.T, a answer, name string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(a.Body.Bytes(), &m); err != nil {
		t.Fatalf("answer %d is no JSON object: %v", a.Code, err)
	}
	s, _ := m[name].(string)
	return s
}

func basic(user, secret string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+secret))
}
