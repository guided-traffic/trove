//go:build integration

package vulnfindings

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
	"strings"
	"testing"
)

// pkg describes a publish request of one npm package version.
type pkg struct {
	Name    string
	Version string
}

// tarballOf builds a package tarball: package/package.json first.
func tarballOf(t testing.TB, p pkg) []byte {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"name": p.Name, "version": p.Version})
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, f := range []struct{ name, content string }{{"package.json", string(raw)}, {"README.md", "# " + p.Name + "\n"}} {
		if err := tw.WriteHeader(&tar.Header{Name: "package/" + f.name, Mode: 0o644, Size: int64(len(f.content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(f.content))
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// body builds the request body the way libnpmpublish does.
func body(t testing.TB, p pkg) []byte {
	t.Helper()
	tgz := tarballOf(t, p)
	s512 := sha512.Sum512(tgz)
	s1 := sha1.Sum(tgz) //nolint:gosec // see the import
	integrity, shasum := "sha512-"+base64.StdEncoding.EncodeToString(s512[:]), hex.EncodeToString(s1[:])
	base := p.Name
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	file := fmt.Sprintf("%s-%s.tgz", p.Name, p.Version)
	m := map[string]any{
		"name": p.Name, "version": p.Version, "_id": p.Name + "@" + p.Version,
		"dist": map[string]any{"integrity": integrity, "shasum": shasum, "tarball": "http://x/" + p.Name + "/-/" + base + "-" + p.Version + ".tgz"},
	}
	att := map[string]any{
		file: map[string]any{"content_type": "application/octet-stream", "data": base64.StdEncoding.EncodeToString(tgz), "length": len(tgz)},
	}
	doc := map[string]any{
		"_id": p.Name, "name": p.Name, "dist-tags": map[string]string{"latest": p.Version},
		"versions": map[string]any{p.Version: m}, "access": nil, "_attachments": att,
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// pubPath is the request path of a publish: a scoped name is written the way npm writes it, the slash as %2f.
func pubPath(repo, name string) string {
	return "/api/npm/it/" + repo + "/" + strings.ReplaceAll(name, "/", "%2f")
}
