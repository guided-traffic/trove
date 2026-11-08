package secrets

import (
	"bytes"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/guided-traffic/trove/internal/config"
)

func TestR_ARCH_021_DerivedKeysPerPurpose(t *testing.T) {
	dir := t.TempDir()
	files := []string{writeKey(t, dir, "k1", 1), writeKey(t, dir, "k2", 90)}
	r1, err := Load(config.Secrets{EncryptionKeyFiles: files, ActiveKeyID: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := Load(config.Secrets{EncryptionKeyFiles: files, ActiveKeyID: "k2"})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("the derivation is HKDF-SHA-256 of the key file with the purpose as info and no salt", func(t *testing.T) {
		d, err := r1.Derive("trove registry token v1", 32)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(mustRead(t, files[0]))))
		want, _ := hkdf.Key(sha256.New, raw, nil, "trove registry token v1", 32)
		id, got := d.Active()
		if id != "k1" || !bytes.Equal(got, want) {
			t.Fatalf("active %s, key equal %v", id, bytes.Equal(got, want))
		}
	})
	t.Run("every key of the ring derives, the active one signs; a rotation keeps the old derivation", func(t *testing.T) {
		a, _ := r1.Derive("p", 32)
		b, _ := r2.Derive("p", 32)
		k1a, ok1 := a.Key("k1")
		k1b, ok2 := b.Key("k1")
		if !ok1 || !ok2 || !bytes.Equal(k1a, k1b) {
			t.Fatal("the derivation of k1 differs between two rings that hold it")
		}
		if id, _ := b.Active(); id != "k2" {
			t.Fatalf("active = %s", id)
		}
		if _, ok := a.Key("absent"); ok {
			t.Fatal("a key the ring lacks derived")
		}
	})
	t.Run("two purposes never share a key, and neither is the key file itself", func(t *testing.T) {
		a, _ := r1.Derive("purpose a", 32)
		b, _ := r1.Derive("purpose b", 32)
		_, ka := a.Active()
		_, kb := b.Active()
		raw, _ := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(mustRead(t, files[0]))))
		if bytes.Equal(ka, kb) || bytes.Equal(ka, raw) {
			t.Fatal("purposes share a key or the key file is used directly")
		}
	})
	t.Run("an empty or reserved purpose and a size outside 16 to 64 bytes are refused", func(t *testing.T) {
		for _, c := range []struct {
			info string
			size int
		}{{"", 32}, {fingerprintInfo, 32}, {"p", 15}, {"p", 65}} {
			if _, err := r1.Derive(c.info, c.size); err == nil {
				t.Errorf("Derive(%q, %d) succeeded", c.info, c.size)
			}
		}
	})
	t.Run("the derived keys render no key material", func(t *testing.T) {
		d, _ := r1.Derive("p", 32)
		_, k := d.Active()
		for _, s := range []string{d.String(), fmt.Sprint(d.LogValue()), fmt.Sprintf("%v", d)} {
			if bytes.Contains([]byte(s), k) || bytes.Contains([]byte(s), []byte(base64.StdEncoding.EncodeToString(k))) {
				t.Fatalf("%q holds the key", s)
			}
		}
	})
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
