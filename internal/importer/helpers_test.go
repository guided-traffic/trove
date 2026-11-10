package importer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha1" //nolint:gosec // the digest of an old registry's shasum
	"crypto/sha256"
	"crypto/sha512"
	"io"
	"net/http"
	"os"
	"testing"
)

func readAll(r *http.Request) ([]byte, error) { return io.ReadAll(r.Body) }

// spool puts bytes into a spool file as a download does.
func spool(t *testing.T, b []byte) *File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "spool")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(b); err != nil {
		t.Fatal(err)
	}
	s512, s256, s1 := sha512.Sum512(b), sha256.Sum256(b), sha1.Sum(b) //nolint:gosec // see the import
	return &File{File: f, Size: int64(len(b)), Sums: Sums{"sha512": s512[:], "sha256": s256[:], "sha1": s1[:]}}
}

// tarball builds an npm tarball whose package.json names the package.
func tarball(t *testing.T, name, version string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	pj := []byte(`{"name":"` + name + `","version":"` + version + `"}`)
	for _, f := range []struct {
		name string
		body []byte
	}{{"package/index.js", []byte("module.exports = 1")}, {"package/package.json", pj}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(f.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func pkgOf(t *testing.T, rep *Report, name string) PackageResult {
	t.Helper()
	for _, p := range rep.Packages {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("the report has no package %s", name)
	return PackageResult{}
}
