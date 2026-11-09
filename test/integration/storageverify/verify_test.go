//go:build integration

// Package storageverify tests `trove admin storage verify` (R-HA-024, R-HA-025) against the integration stack: the blob and item
// rows of an installation database and the objects in its prefix of the S3 store, read through the same code the command runs.
package storageverify

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/blob/verify"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

// seedItem stores an object and the blob and item rows that reference it, the way a committed upload leaves them, and
// returns the digest.
func seedItem(t *testing.T, s *testenv.Stack, repo, path string, data []byte) []byte {
	t.Helper()
	sum := sha256.Sum256(data)
	s.PutObject(t, sum[:], data)
	s.Exec(`INSERT INTO blobs (sha256, sha1, md5, sha512, size) VALUES ($1, decode(repeat('00', 20), 'hex'), decode(repeat('00', 16), 'hex'), decode(repeat('00', 64), 'hex'), $2) ON CONFLICT DO NOTHING`,
		sum[:], len(data))
	s.Exec(`INSERT INTO items (repo_id, path, parent_path, kind, blob_sha256, size)
		SELECT r.id, $2::text, '', 'file', $3, $4 FROM repositories r WHERE r.key = $1`, repo, path, sum[:], len(data))
	return sum[:]
}

func run(t *testing.T, s *testenv.Stack, o verify.Options) (*verify.Report, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := postgres.Open(ctx, s.DatabaseConfig(), "verify-it", meta.RoleWeb, testenv.Quiet())
	if err != nil {
		t.Fatalf("open the database: %v", err)
	}
	t.Cleanup(db.Close)
	objects, prefix := s.ObjectClient(t)
	o.DB, o.Objects, o.Prefix = verify.PGSource{DB: db}, objects, prefix
	return verify.Run(ctx, o)
}

// TestR_HA_024_StorageVerifyNamesTheItemsWhoseObjectIsGone: with objects deleted behind the installation's back, the report
// lists exactly the items that reference them (a trashed one included), and it is clean when every object is there. The table
// is read in pages smaller than itself.
func TestR_HA_024_StorageVerifyNamesTheItemsWhoseObjectIsGone(t *testing.T) {
	s := testenv.NewStack(t, testenv.StackOptions{Name: "verify"})
	s.Repository("npm-local", "local", "npm", "{}")
	var digests [][]byte
	for i := range 12 {
		digests = append(digests, seedItem(t, s, "npm-local", fmt.Sprintf("pkg-%02d/-/pkg-%02d-1.0.0.tgz", i, i), fmt.Appendf(nil, "tarball %d", i)))
	}
	rep, err := run(t, s, verify.Options{PageSize: 5})
	if err != nil || !rep.OK() || rep.Checked != 12 {
		t.Fatalf("clean store: rep = %+v, err = %v", rep, err)
	}

	s.DeleteObject(t, digests[2])
	s.DeleteObject(t, digests[9])
	s.Exec(`UPDATE items SET trashed_at = now() WHERE blob_sha256 = $1`, digests[9])
	rep, err = run(t, s, verify.Options{PageSize: 5})
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK() || rep.Missing != 2 || rep.SizeMismatch != 0 || rep.ItemsAffected != 2 || rep.Checked != 12 {
		t.Fatalf("rep = %+v, want 2 missing blobs and 2 affected items of 12 checked", rep)
	}
	seen := map[string]verify.Item{}
	for _, f := range rep.Findings {
		if f.Kind != verify.KindMissing || len(f.Items) != 1 {
			t.Fatalf("finding = %+v", f)
		}
		seen[f.Items[0].Path] = f.Items[0]
	}
	if it, ok := seen["pkg-02/-/pkg-02-1.0.0.tgz"]; !ok || it.Project != testenv.DefaultProject || it.Repository != "npm-local" || it.Trashed {
		t.Errorf("item of the first deleted object = %+v (found %v)", it, ok)
	}
	if it, ok := seen["pkg-09/-/pkg-09-1.0.0.tgz"]; !ok || !it.Trashed {
		t.Errorf("item of the second deleted object = %+v (found %v), want it reported as trashed", it, ok)
	}

	s.PutObject(t, digests[2], []byte("tarball 2"))
	s.PutObject(t, digests[9], []byte("tarball 9"))
	if rep, err = run(t, s, verify.Options{}); err != nil || !rep.OK() {
		t.Fatalf("after the objects are back: rep = %+v, err = %v", rep, err)
	}
}

// TestR_HA_025_StorageVerifySinceNarrowsTheRun: a cut-off after every row checks nothing, one before them checks all, and a blob
// of another size is reported as such.
func TestR_HA_025_StorageVerifySinceNarrowsTheRun(t *testing.T) {
	s := testenv.NewStack(t, testenv.StackOptions{Name: "verifysince"})
	s.Repository("npm-local", "local", "npm", "{}")
	old := seedItem(t, s, "npm-local", "old/-/old-1.0.0.tgz", []byte("old tarball"))
	s.Exec(`UPDATE blobs SET created_at = now() - interval '2 hours', state_changed_at = now() - interval '2 hours' WHERE sha256 = $1`, old)
	fresh := seedItem(t, s, "npm-local", "new/-/new-1.0.0.tgz", []byte("new tarball"))
	s.DeleteObject(t, old)
	s.PutObject(t, fresh, []byte("shorter"))

	rep, err := run(t, s, verify.Options{Since: time.Now().Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Checked != 1 || rep.Missing != 0 || rep.SizeMismatch != 1 {
		t.Fatalf("since one hour: rep = %+v, want only the fresh blob, of another size", rep)
	}
	rep, err = run(t, s, verify.Options{Since: time.Now().Add(-3 * time.Hour)})
	if err != nil || rep.Checked != 2 || rep.Missing != 1 || rep.SizeMismatch != 1 {
		t.Fatalf("since three hours: rep = %+v, err = %v", rep, err)
	}
}
