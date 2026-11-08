//go:build integration

package postgres

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	debdata "github.com/guided-traffic/trove/internal/formatdata/deb"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/secrets"
)

func ringWith(t *testing.T, active string, ids ...string) *secrets.KeyRing {
	t.Helper()
	dir := t.TempDir()
	var files []string
	for i, id := range ids {
		raw := make([]byte, 32)
		for j := range raw {
			raw[j] = byte(i*37 + j + 1)
		}
		p := filepath.Join(dir, id+".key")
		if err := os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(raw)), 0o600); err != nil {
			t.Fatal(err)
		}
		files = append(files, p)
	}
	ring, err := secrets.Load(config.Secrets{EncryptionKeyFiles: files, ActiveKeyID: active})
	if err != nil {
		t.Fatal(err)
	}
	return ring
}

// TestDebSigningKeysAreResealed: the walk of secrets-reencrypt over deb_signing_keys.private_key (R-ARCH-021 rotation step 3) seals each
// key under the active key of the ring, leaves a key that is under it already, counts a value the ring cannot open and touches no
// retired key, which has no private part.
func TestDebSigningKeysAreResealed(t *testing.T) {
	st := getStack(t)
	cfg, _, admin := migrated(t, st)
	f := &m2Fixture{t: t, admin: admin}
	db, err := postgres.Open(ctxT(t, 30*time.Second), cfg, "it-deb-reseal", meta.RoleWorker, quiet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)

	old := ringWith(t, "2027-04", "2027-04")
	both := ringWith(t, "2027-10", "2027-04", "2027-10")
	repo := f.repo("deb-reseal", "deb", "local")
	ids := []string{"0192f0c4-7a1e-7c3b-9d2e-000000000011", "0192f0c4-7a1e-7c3b-9d2e-000000000012", "0192f0c4-7a1e-7c3b-9d2e-000000000013"}
	const ins = `INSERT INTO trove.deb_signing_keys (id, repo_id, fingerprint, algorithm, bits, key_created, bound_since, state, origin, public_key, private_key)
		VALUES ($1, $2, $3, 22, 256, now() - interval '1 hour', now() - interval '1 hour', $4, 'generated', 'pub', $5)`
	seal := func(ring *secrets.KeyRing, id, secret string) string {
		s, err := ring.Seal(debdata.SigningKeys, secrets.Row(id), []byte(secret))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	mustExec(t, admin, ins, ids[0], repo, strings.Repeat("A", 40), "primary", seal(old, ids[0], "secret one"))
	mustExec(t, admin, ins, ids[1], repo, strings.Repeat("B", 40), "next", seal(both, ids[1], "secret two"))
	mustExec(t, admin, ins, ids[2], repo, strings.Repeat("C", 40), "withheld", seal(old, ids[2], "secret three"))
	mustExec(t, admin, `INSERT INTO trove.deb_signing_keys (id, repo_id, fingerprint, algorithm, bits, key_created, bound_since, state, origin, public_key)
		VALUES (gen_random_uuid(), $1, $2, 22, 256, now(), now(), 'retired', 'generated', 'pub')`, repo, strings.Repeat("D", 40))

	res, err := debdata.ResealSigningKeys(ctxT(t, 30*time.Second), db, both, 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Resealed != 2 || res.Current != 1 || res.Failed != 0 {
		t.Fatalf("result = %+v, want 2 resealed, 1 current", res)
	}
	for i, secret := range []string{"secret one", "secret two", "secret three"} {
		sealed := scalar[string](t, admin, `SELECT private_key FROM trove.deb_signing_keys WHERE id = $1`, ids[i])
		if !strings.HasPrefix(sealed, "v1:2027-10:") {
			t.Errorf("key %d is sealed %q, want the active key", i, sealed[:min(len(sealed), 16)])
		}
		got, err := both.Open(debdata.SigningKeys, secrets.Row(ids[i]), sealed)
		if err != nil || string(got) != secret {
			t.Errorf("key %d opens as %q, %v", i, got, err)
		}
	}
	// A second walk finds nothing to do.
	res, err = debdata.ResealSigningKeys(ctxT(t, 30*time.Second), db, both, 0)
	if err != nil || res.Resealed != 0 || res.Current != 3 {
		t.Fatalf("second walk = %+v, %v", res, err)
	}
	// A key sealed under a key the ring lacks is counted and left as it is.
	other := ringWith(t, "2026-01", "2026-01")
	foreign := seal(other, ids[0], "foreign")
	mustExec(t, admin, `UPDATE trove.deb_signing_keys SET private_key = $2 WHERE id = $1`, ids[0], foreign)
	res, err = debdata.ResealSigningKeys(ctxT(t, 30*time.Second), db, both, 0)
	if err != nil || res.Failed != 1 || res.Current != 2 {
		t.Fatalf("walk with a foreign key = %+v, %v", res, err)
	}
	if scalar[string](t, admin, `SELECT private_key FROM trove.deb_signing_keys WHERE id = $1`, ids[0]) != foreign {
		t.Error("a value the ring cannot open was changed")
	}
}
