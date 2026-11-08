//go:build integration

package postgres

import (
	"io/fs"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/guided-traffic/trove/internal/backup/tables"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

// TestOCIMigrations checks 00054_upload_sessions, 00055_oci, 00056_key_grammar, 00057_upload_session_tails and 00058_upload_session_indexes against the migrated schema (R-STOR-011, R-OCI-002,
// R-OCI-007, R-BAK-004, R-HA-018).
func TestOCIMigrations(t *testing.T) {
	st := getStack(t)
	_, _, admin := migrated(t, st)
	f := &m2Fixture{t: t, admin: admin}

	t.Run("R-BAK-004 the upload session tables are transient and the OCI tables are data", func(t *testing.T) {
		want := map[string]tables.Class{
			"upload_sessions": tables.Transient, "upload_session_parts": tables.Transient, "upload_session_tails": tables.Transient,
			"oci_manifests": tables.Data, "oci_references": tables.Data,
		}
		for name, class := range want {
			if got, ok := tables.Lookup(name); !ok || got.Class != class {
				t.Errorf("%s registered as %+v (%v), want %s", name, got, ok, class)
			}
			if !scalar[bool](t, admin, "SELECT to_regclass($1) IS NOT NULL", schemaName+"."+name) {
				t.Errorf("table %s is not created by the migrations", name)
			}
		}
	})

	t.Run("R-STOR-011 00058 gives the cleaning an ordered index and the cap on open sessions an index to count with", func(t *testing.T) {
		if !scalar[bool](t, admin, `SELECT indexdef LIKE '%(state, last_activity_at)%' FROM pg_indexes WHERE schemaname = $1 AND indexname = 'upload_sessions_cleanup'`, schemaName) {
			t.Error("upload_sessions_cleanup does not order by last_activity_at inside a state")
		}
		if !scalar[bool](t, admin, `SELECT indexdef LIKE '%(repo_id, COALESCE(principal_id%' FROM pg_indexes WHERE schemaname = $1 AND indexname = 'upload_sessions_open_per_principal'`, schemaName) {
			t.Error("upload_sessions_open_per_principal is missing")
		}
	})

	t.Run("R-HA-018 the table steps are expand steps and the key grammar is a named contract step", func(t *testing.T) {
		steps, err := postgres.EmbeddedSteps()
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, s := range steps {
			if s.Version >= 54 && s.Version <= 58 {
				got[s.Name] = s.Phase
				if s.Phase == "contract" && s.RequiresApp == "" {
					t.Errorf("%s: a contract step without requires-app", s.Name)
				}
			}
		}
		want := map[string]string{"00054_upload_sessions": "expand", "00055_oci": "expand", "00056_key_grammar": "contract", "00057_upload_session_tails": "contract", "00058_upload_session_indexes": "contract"}
		for name, phase := range want {
			if got[name] != phase {
				t.Errorf("%s: phase %q, want %q", name, got[name], phase)
			}
		}
	})

	t.Run("an upload session belongs to the repository it writes to and the one addressed", func(t *testing.T) {
		target := f.repo("oci-target", "oci", "local")
		addressed := f.repo("oci-virtual", "oci", "virtual")
		const ins = `INSERT INTO trove.upload_sessions (id, repo_id, addressed_repo_id, binding, hash_state, part_size, s3_upload_id, state%s)
			VALUES ($1, $2, $3, $4, '\x00', 16777216, 'upload-1', $5%s)`
		plain := strings.NewReplacer("%s", "")
		mustExec(t, admin, plain.Replace(ins), "0e4b8d38-39a4-4d3e-9c1e-5a0d0f6c2a11", target, addressed, "team/app", "open")
		wantState(t, admin, "23514", plain.Replace(ins), "0e4b8d38-39a4-4d3e-9c1e-5a0d0f6c2a12", target, addressed, strings.Repeat("a", 513), "open")
		wantState(t, admin, "23514", plain.Replace(ins), "0e4b8d38-39a4-4d3e-9c1e-5a0d0f6c2a13", target, addressed, "app", "paused")
		wantState(t, admin, "23503", plain.Replace(ins), "0e4b8d38-39a4-4d3e-9c1e-5a0d0f6c2a14", target, addressed+1000, "app", "open")
		// A tail is shorter than a part, and a segment of it has a size and a checksum (00057).
		wantState(t, admin, "23514", `INSERT INTO trove.upload_sessions (id, repo_id, addressed_repo_id, hash_state, part_size, s3_upload_id, state, tail_size)
			VALUES ('0e4b8d38-39a4-4d3e-9c1e-5a0d0f6c2a15', $1, $2, '\x00', 1000, '', 'open', 1000)`, target, addressed)
		mustExec(t, admin, `INSERT INTO trove.upload_sessions (id, repo_id, addressed_repo_id, hash_state, part_size, s3_upload_id, state, tail_size)
			VALUES ('0e4b8d38-39a4-4d3e-9c1e-5a0d0f6c2a17', $1, $2, '\x00', 1000, '', 'open', 999)`, target, addressed)
		const seg = `INSERT INTO trove.upload_session_tails (session_id, seq, writer, size, sha256) VALUES ($1, $2, gen_random_uuid(), $3, decode(repeat('ab', $4), 'hex'))`
		mustExec(t, admin, seg, "0e4b8d38-39a4-4d3e-9c1e-5a0d0f6c2a17", 1, 10, 32)
		wantState(t, admin, "23505", seg, "0e4b8d38-39a4-4d3e-9c1e-5a0d0f6c2a17", 1, 10, 32)
		wantState(t, admin, "23514", seg, "0e4b8d38-39a4-4d3e-9c1e-5a0d0f6c2a17", 2, 0, 32)
		wantState(t, admin, "23514", seg, "0e4b8d38-39a4-4d3e-9c1e-5a0d0f6c2a17", 3, 10, 31)

		const part = `INSERT INTO trove.upload_session_parts (session_id, part_number, size, etag, sha256) VALUES ($1, $2, 16777216, 'etag', decode(repeat('ab', 32), 'hex'))`
		mustExec(t, admin, part, "0e4b8d38-39a4-4d3e-9c1e-5a0d0f6c2a11", 1)
		wantState(t, admin, "23505", part, "0e4b8d38-39a4-4d3e-9c1e-5a0d0f6c2a11", 1)
		wantState(t, admin, "23514", part, "0e4b8d38-39a4-4d3e-9c1e-5a0d0f6c2a11", 10001)
		wantState(t, admin, "23503", part, "0e4b8d38-39a4-4d3e-9c1e-5a0d0f6c2aff", 1)

		// Deleting the repository the client addressed ends the session and its parts.
		mustExec(t, admin, "DELETE FROM trove.repositories WHERE id = $1", addressed)
		if n := f.count("upload_sessions", "repo_id = $1", target); n != 0 {
			t.Errorf("%d sessions of a deleted repository", n)
		}
		if n := f.count("upload_session_parts", "true"); n != 0 {
			t.Errorf("%d parts of a deleted session", n)
		}
	})

	t.Run("the manifest rows and the reference graph", func(t *testing.T) {
		repo := f.repo("oci-local", "oci", "local")
		const man = `INSERT INTO trove.oci_manifests (repo_id, name, digest, media_type, size%s) VALUES ($1, $2, $3, 'application/vnd.oci.image.manifest.v1+json', 10%s)`
		plain := strings.NewReplacer("%s", "")
		d := func(b byte) []byte { return []byte(strings.Repeat(string([]byte{b}), 32)) }
		mustExec(t, admin, plain.Replace(man), repo, "team/app", d(1))
		wantState(t, admin, "23505", plain.Replace(man), repo, "team/app", d(1))
		wantState(t, admin, "23514", plain.Replace(man), repo, "team/app", []byte{1, 2})
		wantState(t, admin, "23514", plain.Replace(man), repo, "", d(2))
		wantState(t, admin, "23514", plain.Replace(man), repo, strings.Repeat("a", 256), d(2))
		wantState(t, admin, "23503", plain.Replace(man), repo+1000, "team/app", d(2))
		wantState(t, admin, "23514", `INSERT INTO trove.oci_manifests (repo_id, name, digest, media_type, size, subject) VALUES ($1, 'team/app', $2, 'x', 1, '\x01')`, repo, d(3))
		wantState(t, admin, "23514", `INSERT INTO trove.oci_manifests (repo_id, name, digest, media_type, size, annotations) VALUES ($1, 'team/app', $2, 'x', 1, to_jsonb(repeat('a', 70000)))`, repo, d(4))

		const ref = `INSERT INTO trove.oci_references (repo_id, name, manifest, target, kind) VALUES ($1, $2, $3, $4, $5)`
		mustExec(t, admin, ref, repo, "team/app", d(1), d(9), "blob")
		wantState(t, admin, "23505", ref, repo, "team/app", d(1), d(9), "blob")
		wantState(t, admin, "23514", ref, repo, "team/app", d(1), d(8), "layer")
		wantState(t, admin, "23503", ref, repo, "team/app", d(7), d(8), "blob")  // the manifest row does not exist
		wantState(t, admin, "23503", ref, repo, "other/app", d(1), d(8), "blob") // the same digest under another name is another row

		// A manifest takes its references with it, and a repository takes its manifests.
		mustExec(t, admin, "DELETE FROM trove.oci_manifests WHERE repo_id = $1 AND digest = $2", repo, d(1))
		if n := f.count("oci_references", "repo_id = $1", repo); n != 0 {
			t.Errorf("%d references of a deleted manifest", n)
		}
		mustExec(t, admin, plain.Replace(man), repo, "team/app", d(5))
		mustExec(t, admin, "DELETE FROM trove.repositories WHERE id = $1", repo)
		if n := f.count("oci_manifests", "repo_id = $1", repo); n != 0 {
			t.Errorf("%d manifests of a deleted repository", n)
		}
	})

	t.Run("repository keys follow the component grammar of the distribution specification", func(t *testing.T) {
		const ins = `INSERT INTO trove.repositories (key, format, class, project_id) VALUES ($1, 'generic', 'local', (SELECT id FROM trove.project WHERE key = 'it'))`
		for _, key := range []string{"docker-local", "docker.hub", "a__b", "a_b", "a--b", "ab", "0a", strings.Repeat("a", 64)} {
			wantState(t, admin, "", ins, key)
		}
		for _, key := range []string{"docker-", "a.-b", "a._b", "a___b", "-ab", ".ab", "a.", "a..b", "a", "Ab", "a b", "a/b", "a~cache", strings.Repeat("a", 65)} {
			wantState(t, admin, "23514", ins, key)
		}
	})

	t.Run("project keys follow the component grammar without dots and underscores", func(t *testing.T) {
		const ins = `INSERT INTO trove.project (id, key, display_name) VALUES (gen_random_uuid(), $1, 'x')`
		for _, key := range []string{"web", "web-app", "web--app", "a1", "ab", "a" + strings.Repeat("b", 31)} {
			wantState(t, admin, "", ins, key)
		}
		for _, key := range []string{"web-", "-web", "1web", "web_app", "web.app", "Web", "a", "a" + strings.Repeat("b", 32), "web app"} {
			wantState(t, admin, "23514", ins, key)
		}
	})

	t.Run("the composite names that embed a project key follow its grammar", func(t *testing.T) {
		const principal = `INSERT INTO trove.principal (id, kind, name) VALUES (gen_random_uuid(), $1, $2)`
		for _, c := range [][2]string{{"service_account", "web/sa1"}, {"service_account", "web-app/sa1"}, {"service_account", "sa1"}, {"ci_mapping", "gitlab-a/web/release"}, {"ci_mapping", "gitlab-a/release"}} {
			wantState(t, admin, "", principal, c[0], c[1])
		}
		for _, c := range [][2]string{{"service_account", "web-/sa1"}, {"service_account", "web_x/sa1"}, {"service_account", "w/sa1"}, {"service_account", strings.Repeat("a", 33) + "/sa1"}, {"ci_mapping", "gitlab-b/web-/release"}} {
			wantState(t, admin, "23514", principal, c[0], c[1])
		}
		// A policy name and a mapping tier carry the project part behind triggers and foreign keys that answer first, so the
		// constraints are read: both hold the new grammar and none the old character class.
		for _, c := range []string{"authz_policy_name_check", "ci_mappings_tier_check"} {
			def := scalar[string](t, admin, "SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = $1 AND connamespace = $2::regnamespace", c, schemaName)
			if !strings.Contains(def, "(?:-+[a-z0-9]+)*") || strings.Contains(def, "{1,31}") {
				t.Errorf("%s = %s", c, def)
			}
		}
	})
}

// TestKeyGrammarStopsAtAKeyItRefuses: the step validates the new checks and stops at an existing key the grammar refuses, naming the
// constraint, and applies once the key is gone.
func TestKeyGrammarStopsAtAKeyItRefuses(t *testing.T) {
	st := getStack(t)
	name, admin := st.newDatabase(t)
	cfg := st.config(t, name)
	old := fstest.MapFS{}
	later := 0
	emb := postgres.EmbeddedFS()
	entries, err := fs.ReadDir(emb, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		// 00056 and the steps after it (00057, 00058 build on tables of 00054 only) are the ones applied last
		if n, _ := strconv.Atoi(e.Name()[:5]); n >= 56 {
			later++
			continue
		}
		data, err := fs.ReadFile(emb, e.Name())
		if err != nil {
			t.Fatal(err)
		}
		old[e.Name()] = &fstest.MapFile{Data: data}
	}
	if _, err := postgres.Migrate(ctxT(t, 60*time.Second), cfg, opts(old)); err != nil {
		t.Fatal(err)
	}
	mustExec(t, admin, "INSERT INTO trove.project (id, key, display_name) VALUES (gen_random_uuid(), 'it', 'it')")
	mustExec(t, admin, "INSERT INTO trove.repositories (key, format, class, project_id) VALUES ('docker-', 'generic', 'local', (SELECT id FROM trove.project WHERE key = 'it'))")
	_, err = postgres.Migrate(ctxT(t, 60*time.Second), cfg, opts(nil))
	if err == nil || !strings.Contains(err.Error(), "repositories_key_check") {
		t.Fatalf("err = %v", err)
	}
	mustExec(t, admin, "DELETE FROM trove.repositories")
	if res, err := postgres.Migrate(ctxT(t, 60*time.Second), cfg, opts(nil)); err != nil || len(res.Applied) != later {
		t.Fatalf("after the key is gone: %+v, %v", res, err)
	}
}
