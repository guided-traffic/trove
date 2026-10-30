//go:build integration

package npm

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha1" //nolint:gosec // dist.shasum is a SHA-1
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"math/rand/v2"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// TestR_NPM_006_ProvenanceBundle: a bundle sent with a publish is a second item of the
// version at <name>/-/<basename>-<version>.sigstore, linked to it.
func TestR_NPM_006_ProvenanceBundle(t *testing.T) {
	e := newEnv(t)
	a := e.replica("web-a")
	bundle := []byte(`{"mediaType":"application/vnd.dev.sigstore.bundle+json;version=0.3"}`)
	e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "@acme/signed", Version: "1.0.0", Sigstore: bundle})
	path := "@acme/signed/-/signed-1.0.0.sigstore"
	raw := e.st.Do(a, "GET", "/files/npm-local/"+path, e.tokA, nil)
	if raw.Status != 200 || string(raw.Body) != string(bundle) {
		t.Fatalf("%d %q", raw.Status, raw.Body)
	}
	linked := e.st.Number(`SELECT count(*) FROM items i JOIN package_versions pv ON pv.id = i.version_id JOIN packages p ON p.id = pv.package_id
		WHERE i.path = $1 AND p.name = '@acme/signed' AND pv.lead_item_id <> i.id`, path)
	if linked != 1 {
		t.Fatalf("the bundle is not linked to the version as a non-lead item: %d", linked)
	}
	if got := e.st.Text(`SELECT i.path FROM package_versions pv JOIN items i ON i.id = pv.lead_item_id`); got != "@acme/signed/-/signed-1.0.0.tgz" {
		t.Fatalf("lead item %q", got)
	}
}

// streamedPublish writes a publish request of a tarball with one random file of size
// bytes into w, without ever holding it: the tar and gzip streams are generated, hashed
// and encoded as they go, and the manifest, whose integrity is known only at the end,
// follows the attachments in the document. pace sleeps between the megabytes.
func streamedPublish(w io.Writer, name, version string, size int64, pace time.Duration) (int64, error) {
	h512, h1 := sha512.New(), sha1.New() //nolint:gosec // see the import
	var tgzLen int64
	b64 := base64.NewEncoder(base64.StdEncoding, w)
	sink := io.MultiWriter(h512, h1, &countingWriter{n: &tgzLen}, b64)
	if _, err := fmt.Fprintf(w, `{"_id":%q,"name":%q,"dist-tags":{"latest":%q},"_attachments":{"%s-%s.tgz":{"content_type":"application/octet-stream","data":"`, name, name, version, name, version); err != nil {
		return 0, err
	}
	zw, _ := gzip.NewWriterLevel(sink, gzip.NoCompression)
	tw := tar.NewWriter(zw)
	pj := fmt.Sprintf(`{"name":%q,"version":%q}`, name, version)
	if err := tw.WriteHeader(&tar.Header{Name: "package/package.json", Mode: 0o644, Size: int64(len(pj)), Typeflag: tar.TypeReg}); err != nil {
		return 0, err
	}
	_, _ = tw.Write([]byte(pj))
	if err := tw.WriteHeader(&tar.Header{Name: "package/blob.bin", Mode: 0o644, Size: size, Typeflag: tar.TypeReg}); err != nil {
		return 0, err
	}
	rnd := rand.NewChaCha8([32]byte{7})
	chunk := make([]byte, 1<<20)
	for left := size; left > 0; {
		n := min(int64(len(chunk)), left)
		_, _ = rnd.Read(chunk[:n])
		if _, err := tw.Write(chunk[:n]); err != nil {
			return 0, err
		}
		left -= n
		time.Sleep(pace)
	}
	if err := tw.Close(); err != nil {
		return 0, err
	}
	if err := zw.Close(); err != nil {
		return 0, err
	}
	if err := b64.Close(); err != nil {
		return 0, err
	}
	integrity := "sha512-" + base64.StdEncoding.EncodeToString(h512.Sum(nil))
	_, err := fmt.Fprintf(w, `","length":%d}},"versions":{%q:{"name":%q,"version":%q,"dist":{"integrity":%q,"shasum":%q}}}}`,
		tgzLen, version, name, version, integrity, hex.EncodeToString(h1.Sum(nil)))
	return tgzLen, err
}

type countingWriter struct{ n *int64 }

func (c *countingWriter) Write(p []byte) (int, error) { *c.n += int64(len(p)); return len(p), nil }

// TestR_NPM_007_ASlowPublishOf200MiB: a 200 MiB tarball arrives slowly, in a document
// whose manifest follows the attachment. The request is read as a stream: no
// PostgreSQL connection is idle in a transaction while the bytes arrive (R-NPM-046),
// the process does not hold the body (R-ARCH-028), and the stored object has the digest
// the client computed.
func TestR_NPM_007_ASlowPublishOf200MiB(t *testing.T) {
	e := newEnv(t)
	a := e.replica("web-a")
	const size = 200 << 20
	var idle, peak atomic.Int64
	// done ends the heap watch when the answer arrives; bodySent ends the
	// transaction watch when the client has sent the last byte, so the commit
	// transaction that follows, whose statements pass through "idle in
	// transaction" between round trips, is not counted.
	done := make(chan struct{})
	bodySent := make(chan struct{})
	var base runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&base)
	go func() {
		var ms runtime.MemStats
		for {
			select {
			case <-done:
				return
			case <-time.After(50 * time.Millisecond):
			}
			select {
			case <-bodySent:
			default:
				n, err := e.st.TryNumber(`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND state LIKE 'idle in transaction%'`)
				if err == nil && n > idle.Load() {
					idle.Store(n)
				}
			}
			runtime.ReadMemStats(&ms)
			if h := int64(ms.HeapAlloc); h > peak.Load() {
				peak.Store(h)
			}
		}
	}()
	pr, pw := io.Pipe()
	var tgzLen int64
	go func() {
		n, err := streamedPublish(pw, "@acme/big", "1.0.0", size, 15*time.Millisecond)
		tgzLen = n
		close(bodySent)
		_ = pw.CloseWithError(err)
	}()
	start := time.Now()
	ans := rawPut(t, a, pubPath("npm-local", "@acme/big"), e.tokA, pr, -1, false)
	close(done)
	if ans.Status != 201 {
		t.Fatalf("%d %s", ans.Status, ans.Body)
	}
	if took := time.Since(start); took < 3*time.Second {
		t.Fatalf("the upload took %s: it was not slow", took)
	}
	if got := idle.Load(); got != 0 {
		t.Fatalf("%d connections were idle in transaction while the body arrived", got)
	}
	if grew := peak.Load() - int64(base.HeapAlloc); grew > 160<<20 {
		t.Fatalf("the heap grew by %d MiB for a %d MiB tarball", grew>>20, size>>20)
	}
	if got := e.st.Number(`SELECT i.size FROM items i WHERE i.path = '@acme/big/-/big-1.0.0.tgz'`); got != tgzLen {
		t.Fatalf("stored %d bytes, the client sent %d", got, tgzLen)
	}
	if e.versions("npm-local", "@acme/big") != 1 {
		t.Fatal("not published")
	}
}
