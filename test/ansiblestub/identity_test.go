package ansiblestub

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/test/ansiblestub/ansiblegen"
)

func TestR_ANS_032_WrongIdentityServesAnotherCollectionsArchiveWithItsOwnDigest(t *testing.T) {
	f := newFixture(t, Config{})
	if err := f.stub.SetMode(Mode{WrongIdentity: []string{"acme.base"}}); err != nil {
		t.Fatal(err)
	}
	art := obj(f.get(f.api("v3/plugin/ansible/content/published/collections/index/acme/base/versions/1.0.0/")).json(t)["artifact"])
	body := f.get(f.at(pluginRoot + "artifacts/acme-base-1.0.0.tar.gz")).body
	if sum, _ := ansiblegen.Sum(body); sum != art["sha256"] {
		t.Fatal("the digest of the document is not the digest of the bytes served: only the identity may be wrong")
	}
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err != nil {
			t.Fatal("no MANIFEST.json in the archive")
		}
		if h.Name != "MANIFEST.json" {
			continue
		}
		manifest, _ := io.ReadAll(tr)
		if !strings.Contains(string(manifest), `"namespace": "swapped"`) || !strings.Contains(string(manifest), `"version": "1.0.0"`) {
			t.Fatalf("manifest %s", manifest)
		}
		return
	}
}
