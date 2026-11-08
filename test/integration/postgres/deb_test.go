//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/backup/tables"
	debdata "github.com/guided-traffic/trove/internal/formatdata/deb"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/secrets"
)

var debTables = []string{
	"deb_suites", "deb_binaries", "deb_coordinates", "deb_generations", "deb_tombstones",
	"deb_signing_keys", "deb_remote_releases", "deb_remote_indexes", "deb_remote_entries",
}

// TestDebMigrations checks 00060_deb, 00061_deb_signing_keys and 00062_deb_remote against the migrated schema (R-DEB-008, R-DEB-015,
// R-DEB-023, R-BAK-004, R-HA-018) and the serve-facts kinds the item lookup reads from them (R-DEB-022, R-DEB-032, Q-ARCH-381).
func TestDebMigrations(t *testing.T) {
	st := getStack(t)
	_, _, admin := migrated(t, st)
	f := &m2Fixture{t: t, admin: admin}
	hash := func(b byte) []byte { return []byte(strings.Repeat(string([]byte{b}), 32)) }

	t.Run("R-BAK-004 every Debian table exists and is a data table", func(t *testing.T) {
		for _, name := range debTables {
			if got, ok := tables.Lookup(name); !ok || got.Class != tables.Data {
				t.Errorf("%s registered as %+v (%v), want data", name, got, ok)
			}
			if !scalar[bool](t, admin, "SELECT to_regclass($1) IS NOT NULL", schemaName+"."+name) {
				t.Errorf("table %s is not created by the migrations", name)
			}
		}
	})

	t.Run("the sequence numbers of a suite are revision columns, and the private key is a sealed column (R-ARCH-009, R-DEB-015)", func(t *testing.T) {
		if !meta.IsRevisionColumn("deb_suites", "change_seq") || !meta.IsRevisionColumn("deb_suites", "built_seq") {
			t.Error("deb_suites.change_seq and built_seq are not registered revision columns")
		}
		missing, err := postgres.CheckRevisionColumns(context.Background(), admin, schemaName, meta.DefaultRevisionRegistry())
		if err != nil || len(missing) != 0 {
			t.Errorf("revision columns: %v, %v", missing, err)
		}
		found := false
		for _, c := range secrets.Columns() {
			found = found || c.String() == debdata.SigningKeys.String()
		}
		if !found {
			t.Error("deb_signing_keys.private_key is not in the sealed-column registry")
		}
	})

	t.Run("R-HA-018 the Debian steps are expand steps", func(t *testing.T) {
		steps, err := postgres.EmbeddedSteps()
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, s := range steps {
			if strings.Contains(s.Name, "_deb") {
				got[s.Name] = s.Phase
			}
		}
		for _, name := range []string{"00060_deb", "00061_deb_signing_keys", "00062_deb_remote"} {
			if got[name] != "expand" {
				t.Errorf("%s: phase %q, want expand", name, got[name])
			}
		}
	})

	t.Run("suites, coordinates and generations hang together (R-DEB-008)", func(t *testing.T) {
		repo := f.repo("deb-local", "deb", "local")
		mustExec(t, admin, `INSERT INTO trove.deb_suites (repo_id, suite) VALUES ($1, 'stable')`, repo)
		if scalar[int64](t, admin, `SELECT change_seq FROM trove.deb_suites WHERE repo_id = $1`, repo) != 1 ||
			scalar[string](t, admin, `SELECT state FROM trove.deb_suites WHERE repo_id = $1`, repo) != "pending" {
			t.Error("a new suite starts at change_seq 1, pending")
		}
		wantState(t, admin, "23514", `INSERT INTO trove.deb_suites (repo_id, suite) VALUES ($1, 'Stable')`, repo)
		wantState(t, admin, "23514", `INSERT INTO trove.deb_suites (repo_id, suite, state) VALUES ($1, 'x1', 'broken')`, repo)
		wantState(t, admin, "23505", `INSERT INTO trove.deb_suites (repo_id, suite) VALUES ($1, 'stable')`, repo)

		item := f.item(repo, "pool/h/hello/hello_2.10-3_amd64.deb", f.blob())
		const bin = `INSERT INTO trove.deb_binaries (item_id, repo_id, package, version, architecture, source, control) VALUES ($1, $2, 'hello', '2.10-3', 'amd64', 'hello', 'Package: hello')`
		mustExec(t, admin, bin, item, repo)
		other := f.item(repo, "pool/h/hello/hello_2.10-3_amd64_x.deb", f.blob())
		wantState(t, admin, "23505", bin, other, repo)
		wantState(t, admin, "23514", `INSERT INTO trove.deb_binaries (item_id, repo_id, package, version, architecture, source, control) VALUES ($1, $2, 'p', '1', 'all', 'p', repeat('a', 1048577))`, other, repo)

		const coord = `INSERT INTO trove.deb_coordinates (item_id, repo_id, suite, component) VALUES ($1, $2, $3, $4)`
		mustExec(t, admin, coord, item, repo, "stable", "main")
		wantState(t, admin, "23505", coord, item, repo, "stable", "main")
		wantState(t, admin, "23503", coord, item, repo, "testing", "main")
		wantState(t, admin, "23514", coord, item, repo, "stable", "Main")

		const gen = `INSERT INTO trove.deb_generations (repo_id, suite, generation, built_seq, files, signed_by) VALUES ($1, 'stable', $2, 1, '{"main/binary-amd64/Packages.gz": "ab"}', ARRAY['3F2A']::text[])`
		mustExec(t, admin, gen, repo, 1)
		wantState(t, admin, "23505", gen, repo, 1)

		// A suite goes with its coordinates and generations, and a binary with the item.
		mustExec(t, admin, `DELETE FROM trove.deb_suites WHERE repo_id = $1`, repo)
		if n := f.count("deb_coordinates", "repo_id = $1", repo) + f.count("deb_generations", "repo_id = $1", repo); n != 0 {
			t.Errorf("%d rows of a deleted suite", n)
		}
		mustExec(t, admin, `DELETE FROM trove.items WHERE id = $1`, item)
		if n := f.count("deb_binaries", "repo_id = $1", repo); n != 0 {
			t.Errorf("%d binaries of a deleted item", n)
		}
	})

	t.Run("a repository has one primary and one next key, a fingerprint is known once, and only a retired key has no private part (R-DEB-015)", func(t *testing.T) {
		a := f.repo("deb-keys-a", "deb", "local")
		b := f.repo("deb-keys-b", "deb", "virtual")
		const key = `INSERT INTO trove.deb_signing_keys (id, repo_id, fingerprint, algorithm, bits, key_created, bound_since, state, origin, public_key, private_key)
			VALUES (gen_random_uuid(), $1, $2, $3, 256, now() - interval '1 hour', now() - interval '1 hour', $4, 'generated', 'pub', $5)`
		fpr := func(c string) string { return strings.Repeat(c, 40) }
		mustExec(t, admin, key, a, fpr("A"), 22, "primary", "v1:k:sealed")
		wantState(t, admin, "23505", key, a, fpr("B"), 22, "primary", "v1:k:sealed")
		mustExec(t, admin, key, a, fpr("B"), 22, "next", "v1:k:sealed")
		wantState(t, admin, "23505", key, a, fpr("C"), 22, "next", "v1:k:sealed")
		// Withheld and retired keys are not limited, and a fingerprint is unique in every state and repository.
		mustExec(t, admin, key, a, fpr("C"), 22, "withheld", "v1:k:sealed")
		mustExec(t, admin, key, a, fpr("D"), 1, "retired", nil)
		wantState(t, admin, "23505", key, b, fpr("D"), 22, "primary", "v1:k:sealed")
		wantState(t, admin, "23514", key, b, fpr("E"), 22, "retired", "v1:k:sealed")
		wantState(t, admin, "23514", key, b, fpr("F"), 22, "primary", nil)
		wantState(t, admin, "23514", key, b, fpr("a"), 22, "primary", "v1:k:sealed")
		wantState(t, admin, "23514", key, b, fpr("G"), 25, "primary", "v1:k:sealed")
		wantState(t, admin, "23514", key, b, fpr("H"), 22, "lost", "v1:k:sealed")
	})

	t.Run("release documents, parsed indexes and their entries (R-DEB-023)", func(t *testing.T) {
		repo := f.repo("deb-remote", "deb", "remote")
		const rel = `INSERT INTO trove.deb_remote_releases (repo_id, dir, document, sha256, release_date) VALUES ($1, $2, $3, $4, $5)`
		mustExec(t, admin, rel, repo, "dists/bookworm", "InRelease", hash(1), "2026-11-08T10:00:00Z")
		wantState(t, admin, "23505", rel, repo, "dists/bookworm", "InRelease", hash(2), "2026-11-08T10:00:00Z")
		// An absent record has neither a digest nor a date, every other document has both.
		mustExec(t, admin, `INSERT INTO trove.deb_remote_releases (repo_id, dir, document) VALUES ($1, 'dists/testing', 'absent')`, repo)
		wantState(t, admin, "23514", rel, repo, "dists/testing2", "absent", hash(1), "2026-11-08T10:00:00Z")
		wantState(t, admin, "23514", `INSERT INTO trove.deb_remote_releases (repo_id, dir, document) VALUES ($1, 'dists/x', 'InRelease')`, repo)
		wantState(t, admin, "23514", rel, repo, "dists/x", "Packages", hash(1), "2026-11-08T10:00:00Z")
		wantState(t, admin, "23514", rel, repo, "dists/x", "InRelease", []byte{1}, "2026-11-08T10:00:00Z")

		mustExec(t, admin, `INSERT INTO trove.deb_remote_indexes (repo_id, dir, path, sha256, entries) VALUES ($1, 'dists/bookworm', 'main/binary-amd64/Packages', $2, 1)`, repo, hash(3))
		const ent = `INSERT INTO trove.deb_remote_entries (repo_id, dir, index_path, package, version, architecture, path, sha256, size)
			VALUES ($1, 'dists/bookworm', $2, 'hello', '2.10-3', 'amd64', 'pool/main/h/hello/hello_2.10-3_amd64.deb', $3, $4)`
		mustExec(t, admin, ent, repo, "main/binary-amd64/Packages", hash(4), 56000)
		wantState(t, admin, "23503", ent, repo, "main/binary-arm64/Packages", hash(4), 56000)
		wantState(t, admin, "23514", ent, repo, "main/binary-amd64/Packages", hash(4), -1)
		wantState(t, admin, "23514", ent, repo, "main/binary-amd64/Packages", []byte{4}, 1)
		// Replacing an index drops its entries (deleting the index row cascades).
		mustExec(t, admin, `DELETE FROM trove.deb_remote_indexes WHERE repo_id = $1`, repo)
		if n := f.count("deb_remote_entries", "repo_id = $1", repo); n != 0 {
			t.Errorf("%d entries of a deleted index", n)
		}
	})

	t.Run("the item lookup reads the withheld mark of a package file and the newest parsed digest of a remote path (R-DEB-022, R-DEB-032)", func(t *testing.T) {
		qs := q.New(admin)
		local := f.repo("deb-facts-local", "deb", "local")
		path := "pool/h/hello/hello_2.10-3_amd64.deb"
		f.item(local, path, f.blob())
		lookup := func(repo int64, kind, p string) q.GetLiveItemRow {
			t.Helper()
			rows, err := qs.GetLiveItem(context.Background(), q.GetLiveItemParams{FactsKind: kind, FactsPackage: "hello", RepoID: repo, Path: p})
			if err != nil || len(rows) != 1 {
				t.Fatalf("GetLiveItem(%s, %s): %d rows, %v", kind, p, len(rows), err)
			}
			return rows[0]
		}
		if r := lookup(local, "deb_local", path); r.FactKind != "deb_local" || r.FactTombstoned {
			t.Errorf("before the mark: kind %q, tombstoned %v", r.FactKind, r.FactTombstoned)
		}
		mustExec(t, admin, `INSERT INTO trove.deb_tombstones (repo_id, path, sha256) VALUES ($1, $2, $3)`, local, path, hash(9))
		if r := lookup(local, "deb_local", path); !r.FactTombstoned {
			t.Error("a withheld file is not tombstoned")
		}
		if r := lookup(local, "npm_local", path); r.FactTombstoned {
			t.Error("another kind read the Debian mark")
		}
		wantState(t, admin, "23514", `INSERT INTO trove.deb_tombstones (repo_id, path, sha256) VALUES ($1, 'x', '\x01')`, local)

		remote := f.repo("deb-facts-remote", "deb", "remote")
		f.item(remote, path, f.blob())
		if r := lookup(remote, "deb_remote", path); r.FactKind != "deb_remote" || r.FactDebRemoteKnown {
			t.Errorf("no parsed index: kind %q, known %v", r.FactKind, r.FactDebRemoteKnown)
		}
		for i, idx := range []string{"main/binary-amd64/Packages", "updates/binary-amd64/Packages"} {
			mustExec(t, admin, `INSERT INTO trove.deb_remote_indexes (repo_id, dir, path, sha256, entries, parsed_at) VALUES ($1, 'dists/bookworm', $2, $3, 1, now() - make_interval(mins => $4))`,
				remote, idx, hash(byte(10+i)), 10-5*i)
			mustExec(t, admin, `INSERT INTO trove.deb_remote_entries (repo_id, dir, index_path, package, version, architecture, path, sha256, size)
				VALUES ($1, 'dists/bookworm', $2, 'hello', '2.10-3', 'amd64', $3, $4, $5)`, remote, idx, path, hash(byte(20+i)), 1000+i)
		}
		r := lookup(remote, "deb_remote", path)
		if !r.FactDebRemoteKnown || r.FactDebRemoteSize != 1001 || string(r.FactDebRemoteSha256) != string(hash(21)) {
			t.Errorf("the newest parsed index decides: known %v, size %d, sha256 %x", r.FactDebRemoteKnown, r.FactDebRemoteSize, r.FactDebRemoteSha256)
		}
		if r := lookup(remote, "", path); r.FactKind != "" || r.FactDebRemoteKnown {
			t.Errorf("a lookup without a kind read Debian rows: %q %v", r.FactKind, r.FactDebRemoteKnown)
		}
	})
}
