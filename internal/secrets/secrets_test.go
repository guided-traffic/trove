package secrets

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/config"
)

func resetRegistry(t *testing.T) {
	t.Helper()
	reg.mu.Lock()
	reg.cols, reg.frozen = nil, false
	reg.mu.Unlock()
	t.Cleanup(func() {
		reg.mu.Lock()
		reg.cols, reg.frozen = nil, false
		reg.mu.Unlock()
	})
}

// writeKey writes a key file named <id>.key with deterministic content and
// returns its path.
func writeKey(t *testing.T, dir, id string, fill byte) string {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = fill + byte(i)
	}
	p := filepath.Join(dir, id+".key")
	if err := os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(raw)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func ring(t *testing.T, active string, ids ...string) *KeyRing {
	t.Helper()
	dir := t.TempDir()
	var files []string
	for i, id := range ids {
		files = append(files, writeKey(t, dir, id, byte(i*40+1)))
	}
	r, err := Load(config.Secrets{EncryptionKeyFiles: files, ActiveKeyID: active})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSealOpen(t *testing.T) {
	resetRegistry(t)
	col := Register("remote_repositories", "id", "upstream_password")
	other := Register("webhooks", "id", "secret")

	t.Run("R-ARCH-021 seal and open round trip with the v1 envelope", func(t *testing.T) {
		r := ring(t, "2026-10", "2026-10")
		sealed, err := r.Seal(col, Row("row-1"), []byte("hunter2"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(sealed, "v1:2026-10:") {
			t.Errorf("envelope = %q", sealed)
		}
		if strings.Contains(sealed, "hunter2") || strings.Contains(sealed, base64.StdEncoding.EncodeToString([]byte("hunter2"))) {
			t.Error("plaintext in envelope")
		}
		pt, err := r.Open(col, Row("row-1"), sealed)
		if err != nil || string(pt) != "hunter2" {
			t.Errorf("open = %q, %v", pt, err)
		}
		again, _ := r.Seal(col, Row("row-1"), []byte("hunter2"))
		if again == sealed {
			t.Error("nonce reused: two seals of the same value are identical")
		}
	})

	t.Run("R-ARCH-021 empty plaintext round trips", func(t *testing.T) {
		r := ring(t, "k1", "k1")
		s, err := r.Seal(col, Row("r"), nil)
		if err != nil {
			t.Fatal(err)
		}
		pt, err := r.Open(col, Row("r"), s)
		if err != nil || len(pt) != 0 {
			t.Errorf("%q %v", pt, err)
		}
	})

	t.Run("R-ARCH-021 a value sealed for one row does not open for another row or column", func(t *testing.T) {
		r := ring(t, "k1", "k1")
		sealed, _ := r.Seal(col, Row("row-1"), []byte("pw"))
		if _, err := r.Open(col, Row("row-2"), sealed); !errors.Is(err, ErrAuthentication) {
			t.Errorf("other row: %v", err)
		}
		if _, err := r.Open(other, Row("row-1"), sealed); !errors.Is(err, ErrAuthentication) {
			t.Errorf("other column: %v", err)
		}
	})

	t.Run("R-ARCH-021 tampered ciphertext fails authentication", func(t *testing.T) {
		r := ring(t, "k1", "k1")
		sealed, _ := r.Seal(col, Row("row-1"), []byte("pw"))
		i := strings.LastIndex(sealed, ":") + 1
		raw, _ := base64.StdEncoding.DecodeString(sealed[i:])
		raw[len(raw)-1] ^= 0x01
		bad := sealed[:i] + base64.StdEncoding.EncodeToString(raw)
		if _, err := r.Open(col, Row("row-1"), bad); !errors.Is(err, ErrAuthentication) {
			t.Errorf("tampered: %v", err)
		}
	})

	t.Run("R-ARCH-021 an envelope that names another key ID fails", func(t *testing.T) {
		r := ring(t, "k1", "k1", "k2")
		sealed, _ := r.Seal(col, Row("row-1"), []byte("pw"))
		swapped := strings.Replace(sealed, "v1:k1:", "v1:k2:", 1)
		if _, err := r.Open(col, Row("row-1"), swapped); !errors.Is(err, ErrAuthentication) {
			t.Errorf("wrong key: %v", err)
		}
		unknown := strings.Replace(sealed, "v1:k1:", "v1:zz:", 1)
		err := func() error { _, err := r.Open(col, Row("row-1"), unknown); return err }()
		if !errors.Is(err, ErrUnknownKey) || !strings.Contains(err.Error(), `"zz"`) {
			t.Errorf("unknown key: %v", err)
		}
	})

	t.Run("R-ARCH-021 malformed envelopes are rejected without panics", func(t *testing.T) {
		r := ring(t, "k1", "k1")
		for _, bad := range []string{"", "plain", "v2:k1:AAAA", "v1::AAAA", "v1:k1:!!!", "v1:k1:", "v1:k1:AAAA"} {
			if _, err := r.Open(col, Row("r"), bad); err == nil {
				t.Errorf("%q accepted", bad)
			}
		}
	})

	t.Run("R-ARCH-021 an unregistered column cannot be sealed or opened", func(t *testing.T) {
		r := ring(t, "k1", "k1")
		if _, err := r.Seal(Column{}, Row("r"), []byte("x")); !errors.Is(err, ErrUnregistered) {
			t.Errorf("zero column: %v", err)
		}
		if _, err := r.Seal(Column{table: "t", pk: "id", column: "c"}, Row("r"), []byte("x")); !errors.Is(err, ErrUnregistered) {
			t.Errorf("forged column: %v", err)
		}
	})
}

func TestRotation(t *testing.T) {
	resetRegistry(t)
	col := Register("remote_repositories", "id", "upstream_password")

	t.Run("R-ARCH-021 rotation: the old key opens, the new key seals", func(t *testing.T) {
		dir := t.TempDir()
		oldF := writeKey(t, dir, "2026-10", 1)
		newF := writeKey(t, dir, "2027-04", 77)

		oldRing, err := Load(config.Secrets{EncryptionKeyFiles: []string{oldF}, ActiveKeyID: "2026-10"})
		if err != nil {
			t.Fatal(err)
		}
		sealedOld, _ := oldRing.Seal(col, Row("r1"), []byte("pw"))

		// step 1: new key appended, active unchanged
		step1, err := Load(config.Secrets{EncryptionKeyFiles: []string{oldF, newF}, ActiveKeyID: "2026-10"})
		if err != nil {
			t.Fatal(err)
		}
		if s, _ := step1.Seal(col, Row("r1"), []byte("pw")); !strings.HasPrefix(s, "v1:2026-10:") {
			t.Errorf("step 1 sealed with %q", s[:12])
		}
		// step 2: new key active
		step2, err := Load(config.Secrets{EncryptionKeyFiles: []string{newF, oldF}, ActiveKeyID: "2027-04"})
		if err != nil {
			t.Fatal(err)
		}
		sealedNew, _ := step2.Seal(col, Row("r1"), []byte("pw"))
		if !strings.HasPrefix(sealedNew, "v1:2027-04:") {
			t.Errorf("step 2 sealed with %q", sealedNew[:12])
		}
		// old and new replicas side by side: both open both values
		for name, rg := range map[string]*KeyRing{"step1 replica": step1, "step2 replica": step2} {
			for _, v := range []string{sealedOld, sealedNew} {
				if pt, err := rg.Open(col, Row("r1"), v); err != nil || string(pt) != "pw" {
					t.Errorf("%s: %v", name, err)
				}
			}
		}
		// an old-ring replica (without the new key) cannot open the new value
		if _, err := oldRing.Open(col, Row("r1"), sealedNew); !errors.Is(err, ErrUnknownKey) {
			t.Errorf("old ring: %v", err)
		}
		// step 3: reseal moves values to the active key, idempotently
		moved, changed, err := step2.Reseal(col, Row("r1"), sealedOld)
		if err != nil || !changed || !strings.HasPrefix(moved, "v1:2027-04:") {
			t.Fatalf("reseal: %q %v %v", moved, changed, err)
		}
		if pt, _ := step2.Open(col, Row("r1"), moved); string(pt) != "pw" {
			t.Error("resealed value does not open")
		}
		same, changed, _ := step2.Reseal(col, Row("r1"), moved)
		if changed || same != moved {
			t.Error("reseal is not idempotent")
		}
		// step 4: old key removed; resealed value still opens, old value does not
		step4, err := Load(config.Secrets{EncryptionKeyFiles: []string{newF}, ActiveKeyID: "2027-04"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := step4.Open(col, Row("r1"), moved); err != nil {
			t.Error(err)
		}
		if _, err := step4.Open(col, Row("r1"), sealedOld); !errors.Is(err, ErrUnknownKey) {
			t.Errorf("retired key: %v", err)
		}
	})

	t.Run("R-HA-027 a ring with multiple keys lists every key and marks the active one", func(t *testing.T) {
		r := ring(t, "b", "a", "b", "c")
		st := r.Status()
		if st.ActiveKeyID != "b" || strings.Join(st.KeyIDs, ",") != "a,b,c" || len(st.Keys) != 3 {
			t.Fatalf("status = %+v", st)
		}
		for _, k := range st.Keys {
			if !k.Loaded || k.Active != (k.KeyID == "b") {
				t.Errorf("key %+v", k)
			}
		}
		for _, id := range []string{"a", "b", "c"} {
			if !r.Has(id) {
				t.Errorf("key %s missing", id)
			}
		}
	})

	t.Run("R-HA-027 two replicas with the same files agree on keys and open each other's values", func(t *testing.T) {
		dir := t.TempDir()
		files := []string{writeKey(t, dir, "k1", 1), writeKey(t, dir, "k2", 90)}
		a, _ := Load(config.Secrets{EncryptionKeyFiles: files, ActiveKeyID: "k2"})
		b, _ := Load(config.Secrets{EncryptionKeyFiles: files, ActiveKeyID: "k1"})
		sa, _ := a.Seal(col, Row("r"), []byte("from-a"))
		sb, _ := b.Seal(col, Row("r"), []byte("from-b"))
		if pt, err := b.Open(col, Row("r"), sa); err != nil || string(pt) != "from-a" {
			t.Errorf("b opens a: %v", err)
		}
		if pt, err := a.Open(col, Row("r"), sb); err != nil || string(pt) != "from-b" {
			t.Errorf("a opens b: %v", err)
		}
	})

	t.Run("R-HA-027 a ring that lacks a referenced key ID names it without key material", func(t *testing.T) {
		r := ring(t, "k1", "k1")
		err := r.RequireKeys("database", []string{"k1", "2026-09", "k1"})
		var mm *MismatchError
		if !errors.As(err, &mm) || err.Error() != "key ring does not match database: missing key 2026-09" {
			t.Errorf("err = %v", err)
		}
		if err := r.RequireKeys("backup", []string{"k1"}); err != nil {
			t.Error(err)
		}
		err = r.RequireKeys("backup", []string{"y", "x"})
		if err == nil || err.Error() != "key ring does not match backup: missing keys x, y" {
			t.Errorf("err = %v", err)
		}
	})
}

func TestLoadErrors(t *testing.T) {
	t.Run("R-ARCH-021 an empty ring stops start-up with a clear message", func(t *testing.T) {
		_, err := Load(config.Secrets{})
		if err == nil || !strings.Contains(err.Error(), "no encryption key configured") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("R-ARCH-021 Q-HA-8 a missing key file names the file and the fix", func(t *testing.T) {
		_, err := Load(config.Secrets{EncryptionKeyFiles: []string{"/nonexistent/2026-10.key"}, ActiveKeyID: "2026-10"})
		if err == nil || !strings.Contains(err.Error(), "/nonexistent/2026-10.key does not exist") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("R-ARCH-021 wrong size, bad base64 and bad key IDs are errors without key material", func(t *testing.T) {
		dir := t.TempDir()
		short := filepath.Join(dir, "short.key")
		_ = os.WriteFile(short, []byte(base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))), 0o600)
		notB64 := filepath.Join(dir, "garbage.key")
		_ = os.WriteFile(notB64, []byte("!!! not base64 SECRETMARKER !!!"), 0o600)
		badID := filepath.Join(dir, "bad:id.key")
		_ = os.WriteFile(badID, []byte(base64.StdEncoding.EncodeToString(make([]byte, 32))), 0o600)
		for _, tc := range []struct{ file, id, want string }{
			{short, "short", "holds 16 bytes, want 32"},
			{notB64, "garbage", "is not base64"},
			{badID, "bad:id", "must match"},
		} {
			_, err := Load(config.Secrets{EncryptionKeyFiles: []string{tc.file}, ActiveKeyID: tc.id})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s: %v", tc.id, err)
			}
			if err != nil && strings.Contains(err.Error(), "SECRETMARKER") {
				t.Error("error contains key file content")
			}
		}
	})

	t.Run("R-ARCH-021 the active key must be in the ring and key IDs unique", func(t *testing.T) {
		dir := t.TempDir()
		f := writeKey(t, dir, "k1", 1)
		if _, err := Load(config.Secrets{EncryptionKeyFiles: []string{f}, ActiveKeyID: "other"}); err == nil ||
			!strings.Contains(err.Error(), `"other" is not among the loaded keys`) {
			t.Errorf("err = %v", err)
		}
		sub := filepath.Join(dir, "sub")
		_ = os.Mkdir(sub, 0o755)
		dup := writeKey(t, sub, "k1", 2)
		if _, err := Load(config.Secrets{EncryptionKeyFiles: []string{f, dup}, ActiveKeyID: "k1"}); err == nil ||
			!strings.Contains(err.Error(), "listed twice") {
			t.Errorf("err = %v", err)
		}
		if _, err := Load(config.Secrets{EncryptionKeyFiles: []string{f}}); err == nil {
			t.Error("empty active_key_id accepted")
		}
	})

	t.Run("R-ARCH-021 key material never appears in String or LogValue", func(t *testing.T) {
		dir := t.TempDir()
		f := writeKey(t, dir, "k1", 1)
		raw, _ := os.ReadFile(f)
		r, _ := Load(config.Secrets{EncryptionKeyFiles: []string{f}, ActiveKeyID: "k1"})
		rendered := r.String() + r.LogValue().String()
		if strings.Contains(rendered, strings.TrimSpace(string(raw))) || !strings.Contains(rendered, "k1") {
			t.Errorf("rendered = %s", rendered)
		}
	})
}

func TestRegistryAndStatus(t *testing.T) {
	t.Run("R-ARCH-021 the sealed-column registry is frozen at start-up", func(t *testing.T) {
		resetRegistry(t)
		Register("a", "id", "x")
		Register("b", "id", "y")
		if got := len(Columns()); got != 2 {
			t.Fatalf("columns = %d", got)
		}
		mustPanic(t, "duplicate", func() { Register("a", "id", "x") })
		mustPanic(t, "empty", func() { Register("", "id", "x") })
		Freeze()
		mustPanic(t, "after freeze", func() { Register("c", "id", "z") })
	})

	t.Run("R-ARCH-021 the canary seals and opens per key and is not a counted column", func(t *testing.T) {
		resetRegistry(t)
		r := ring(t, "k1", "k1", "k2")
		s, err := r.SealCanary("k2")
		if err != nil || !strings.HasPrefix(s, "v1:k2:") {
			t.Fatalf("%q %v", s, err)
		}
		if err := r.CheckCanary("k2", s); err != nil {
			t.Error(err)
		}
		if err := r.CheckCanary("k1", s); err == nil {
			t.Error("canary of another key ID accepted")
		}
		if len(Columns()) != 0 {
			t.Error("canary column in the registry")
		}
	})

	t.Run("R-ARCH-021 status reports key IDs and per-key counts, retired keys marked not loaded", func(t *testing.T) {
		resetRegistry(t)
		Register("remote_repositories", "id", "upstream_password")
		Register("webhooks", "id", "secret")
		r := ring(t, "new", "new", "old")
		st, err := r.StatusWithCounts(context.Background(), fakeCounter{
			"remote_repositories.upstream_password": {"old": 2, "gone": 1},
			"webhooks.secret":                       {"new": 4, "old": 1},
		})
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]KeyStatus{}
		for _, k := range st.Keys {
			got[k.KeyID] = k
		}
		if *got["old"].SealedValues != 3 || *got["new"].SealedValues != 4 {
			t.Errorf("counts = %+v", st.Keys)
		}
		if g := got["gone"]; g.Loaded || *g.SealedValues != 1 {
			t.Errorf("retired key = %+v", g)
		}
		if !got["new"].Active || got["old"].Active {
			t.Error("active flag wrong")
		}
		if strings.Join(st.Columns, ",") != "remote_repositories.upstream_password,webhooks.secret" {
			t.Errorf("columns = %v", st.Columns)
		}
	})

	t.Run("R-ARCH-021 fingerprints are stable per active key and change after rotation", func(t *testing.T) {
		dir := t.TempDir()
		files := []string{writeKey(t, dir, "k1", 1), writeKey(t, dir, "k2", 90)}
		a, _ := Load(config.Secrets{EncryptionKeyFiles: files, ActiveKeyID: "k1"})
		a2, _ := Load(config.Secrets{EncryptionKeyFiles: files, ActiveKeyID: "k1"})
		b, _ := Load(config.Secrets{EncryptionKeyFiles: files, ActiveKeyID: "k2"})
		fa, fa2, fb := a.Fingerprint([]byte("s")), a2.Fingerprint([]byte("s")), b.Fingerprint([]byte("s"))
		if string(fa) != string(fa2) || string(fa) == string(fb) || len(fa) != 32 {
			t.Error("fingerprint properties violated")
		}
	})
}

type fakeCounter map[string]map[string]int64

func (f fakeCounter) CountSealed(_ context.Context, c Column) (map[string]int64, error) {
	return f[c.String()], nil
}

func mustPanic(t *testing.T, name string, f func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: no panic", name)
		}
	}()
	f()
}

func TestCompositeRowKeys(t *testing.T) {
	resetRegistry(t)
	col := Register("repository_credentials", "repository_id,name", "password")
	single := Register("webhooks", "id", "secret")
	r := ring(t, "k1", "k1")

	t.Run("R-ARCH-021 a composite row key round trips with one part per primary key column", func(t *testing.T) {
		sealed, err := r.Seal(col, Row("0190-repo", "upstream"), []byte("pw"))
		if err != nil {
			t.Fatal(err)
		}
		if pt, err := r.Open(col, Row("0190-repo", "upstream"), sealed); err != nil || string(pt) != "pw" {
			t.Fatalf("open = %q, %v", pt, err)
		}
		if got := col.PrimaryKeyColumns(); len(got) != 2 || got[0] != "repository_id" || got[1] != "name" {
			t.Errorf("PrimaryKeyColumns = %v", got)
		}
	})

	t.Run("R-ARCH-021 parts that join to the same text are different rows", func(t *testing.T) {
		for _, pair := range [][2]RowKey{
			{Row("a:b", "c"), Row("a", "b:c")},
			{Row("ab", "c"), Row("a", "bc")},
			{Row("a", ""), Row("", "a")},
			{Row("a\x00b", "c"), Row("a", "b\x00c")},
		} {
			sealed, err := r.Seal(col, pair[0], []byte("pw"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.Open(col, pair[1], sealed); !errors.Is(err, ErrAuthentication) {
				t.Errorf("sealed for %s, opened for %s: %v", pair[0], pair[1], err)
			}
		}
		if string(aad(col, Row("a:b", "c"))) == string(aad(col, Row("a", "b:c"))) {
			t.Error("two row keys share one AAD")
		}
	})

	t.Run("R-ARCH-021 a row key with the wrong number of parts is refused before any crypto", func(t *testing.T) {
		if _, err := r.Seal(col, Row("only-one"), []byte("pw")); !errors.Is(err, ErrRowKey) {
			t.Errorf("seal with 1 of 2 parts: %v", err)
		}
		sealed, _ := r.Seal(single, Row("w1"), []byte("pw"))
		if _, err := r.Open(single, Row("w1", "extra"), sealed); !errors.Is(err, ErrRowKey) {
			t.Errorf("open with 2 of 1 parts: %v", err)
		}
		if _, err := r.Seal(single, Row(), []byte("pw")); !errors.Is(err, ErrRowKey) {
			t.Errorf("seal without parts: %v", err)
		}
	})

	t.Run("R-ARCH-021 Register refuses names that are not lower-case identifiers", func(t *testing.T) {
		mustPanic(t, "comma at the end", func() { Register("t1", "id,", "c") })
		mustPanic(t, "quoted", func() { Register(`t"1`, "id", "c") })
		mustPanic(t, "upper case", func() { Register("T1", "id", "c") })
	})
}
