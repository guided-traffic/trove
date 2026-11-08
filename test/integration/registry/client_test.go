//go:build integration

package registry

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

// seedBlob uploads a blob under an image name of a repository as a client does, by a monolithic upload through /v2/ as the administrator.
func (e *env) seedBlob(repoPath string, b []byte) {
	e.t.Helper()
	a := e.send("default", req{
		method: "POST", path: "/v2/" + repoPath + "/blobs/uploads/?digest=" + digestOf(b), auth: bearer(e.admin), body: b,
		header: map[string]string{"Content-Type": "application/octet-stream"},
	})
	if a.Code != http.StatusCreated {
		e.t.Fatalf("upload of a blob to %s: %d %s", repoPath, a.Code, a.Body.String())
	}
}

// push uploads the blobs of an image and pushes its manifest through /v2/; it stores an image under repoPath ("web/docker-local/<name>") with the tag and returns the answer of the manifest PUT.
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
