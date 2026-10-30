//go:build integration

// Package npm tests the npm format against the integration stack: PostgreSQL, the
// object store and in-process replicas started through the composition root
// (internal/app), so the real chain, the real authentication and authorization, the
// format dispatcher and the npm format run. Nothing in the path of a request is
// stood in for. Valkey is not part of these tests: nothing a publish needs is in it
// (R-NPM-046), and every propagation here is the one that works with Valkey stopped.
package npm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha1" //nolint:gosec // dist.shasum is a SHA-1
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

// pkg describes a publish request. The zero value of every field is the common case.
type pkg struct {
	Name    string
	Version string
	Tag     string // the dist-tag of the request; "latest"
	// Deps become dependencies of both the manifest and the package.json of the tarball.
	Deps map[string]string
	// Tarball replaces the generated tarball.
	Tarball []byte
	// Sigstore adds a provenance bundle attachment.
	Sigstore []byte
	// Manifest edits the manifest of the request (not the tarball's package.json).
	Manifest func(m map[string]any)
	// Integrity and Shasum replace the digests the manifest announces.
	Integrity, Shasum string
	// Files are further files of the generated tarball, by path below package/.
	Files map[string]string
}

func (p *pkg) defaults() {
	if p.Tag == "" {
		p.Tag = "latest"
	}
	if p.Version == "" {
		p.Version = "1.0.0"
	}
}

// tarballOf builds a package tarball: package/package.json first, then the files.
func tarballOf(t testing.TB, p pkg) []byte {
	t.Helper()
	p.defaults()
	pj := map[string]any{"name": p.Name, "version": p.Version}
	if len(p.Deps) > 0 {
		pj["dependencies"] = p.Deps
	}
	raw, _ := json.Marshal(pj)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	add := func(name, content string) {
		if err := tw.WriteHeader(&tar.Header{Name: "package/" + name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(content))
	}
	add("package.json", string(raw))
	add("README.md", "# "+p.Name+"\n")
	for n, c := range p.Files {
		add(n, c)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// digests returns the SRI string and the SHA-1 hex of a tarball.
func digests(tgz []byte) (integrity, shasum string) {
	s512 := sha512.Sum512(tgz)
	s1 := sha1.Sum(tgz) //nolint:gosec // see the import
	return "sha512-" + base64.StdEncoding.EncodeToString(s512[:]), hex.EncodeToString(s1[:])
}

// body builds the request body the way libnpmpublish does.
func body(t testing.TB, p pkg) []byte {
	t.Helper()
	p.defaults()
	tgz := p.Tarball
	if tgz == nil {
		tgz = tarballOf(t, p)
	}
	integrity, shasum := digests(tgz)
	if p.Integrity != "" {
		integrity = p.Integrity
	}
	if p.Shasum != "" {
		shasum = p.Shasum
	}
	base := baseName(p.Name)
	file := fmt.Sprintf("%s-%s.tgz", p.Name, p.Version)
	m := map[string]any{
		"name": p.Name, "version": p.Version, "_id": p.Name + "@" + p.Version,
		"dist": map[string]any{"integrity": integrity, "shasum": shasum, "tarball": "http://x/" + p.Name + "/-/" + base + "-" + p.Version + ".tgz"},
	}
	if len(p.Deps) > 0 {
		m["dependencies"] = p.Deps
	}
	if p.Manifest != nil {
		p.Manifest(m)
	}
	att := map[string]any{
		file: map[string]any{"content_type": "application/octet-stream", "data": base64.StdEncoding.EncodeToString(tgz), "length": len(tgz)},
	}
	if p.Sigstore != nil {
		att[fmt.Sprintf("%s-%s.sigstore", p.Name, p.Version)] = map[string]any{
			"content_type": "application/vnd.dev.sigstore.bundle+json;version=0.3",
			"data":         base64.StdEncoding.EncodeToString(p.Sigstore), "length": len(p.Sigstore),
		}
	}
	doc := map[string]any{
		"_id": p.Name, "name": p.Name, "dist-tags": map[string]string{p.Tag: p.Version},
		"versions": map[string]any{p.Version: m}, "access": nil, "_attachments": att,
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func baseName(name string) string {
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '/' {
			return name[i+1:]
		}
	}
	return name
}

// path is the request path of a publish.
func pubPath(repo, name string) string { return "/api/npm/" + repo + "/" + escapeName(name) }

// escapeName writes a scoped name the way npm does: the slash as %2f.
func escapeName(name string) string {
	out := bytes.ReplaceAll([]byte(name), []byte("/"), []byte("%2f"))
	return string(out)
}
