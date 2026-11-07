//go:build integration

package projects

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/blob"
)

// seedBlob inserts a live blob of the size, keyed by the digest of its label; the triggers of migration 00003 count
// it into the physical totals.
func (f *fixture) seedBlob(label string, size int64) []byte {
	f.t.Helper()
	sum := sha256.Sum256([]byte(label))
	f.e.Exec(`INSERT INTO blobs (sha256, sha1, md5, sha512, size) VALUES ($1, $2, $3, $4, $5)`,
		sum[:], make([]byte, 20), make([]byte, 16), make([]byte, 64), size)
	return sum[:]
}

// seedFile inserts a file item of the repository that references the blob; the triggers append the logical delta.
func (f *fixture) seedFile(project, repo, path string, sha []byte, size int64) {
	f.t.Helper()
	f.e.Exec(`INSERT INTO items (repo_id, path, parent_path, kind, blob_sha256, size)
	          VALUES ($1, $2, '', 'file', $3, $4)`, f.repoID(project, repo), path, sha, size)
}

// fold runs the usage fold of a worker until the deltas are folded into the counters (R-STOR-003).
func (f *fixture) fold() {
	f.t.Helper()
	folder, err := blob.NewUsageFolder(blob.UsageFolderOptions{DB: f.a.DB, Interval: time.Hour, WatchdogTimeout: 30 * time.Second, CallTimeout: 10 * time.Second})
	if err != nil {
		f.t.Fatal(err)
	}
	for {
		n, err := folder.Fold(context.Background())
		if err != nil {
			f.t.Fatalf("fold: %v", err)
		}
		if n < blob.UsageFoldBatch {
			break
		}
	}
	if left := f.e.Number("SELECT count(*) FROM storage_usage_deltas"); left != 0 {
		f.t.Fatalf("%d deltas left after the fold", left)
	}
}

// logical reads the figure a project is charged through the API as the person.
func (f *fixture) logical(person string, project string, hdr ...string) int64 {
	f.t.Helper()
	r := f.cl.Do(person, "GET", base+"/"+project, nil, hdr...)
	wantStatus(f.t, r, 200, "")
	v, ok := r.JSON(f.t)["logicalBytes"].(float64)
	if !ok {
		f.t.Fatalf("%s carries no logicalBytes: %s", project, r.Body)
	}
	return int64(v)
}

// physical is the size the object store keeps, from the maintained totals (the admin dashboard's figure).
func (f *fixture) physical() int64 {
	return f.e.Number("SELECT physical_bytes FROM storage_usage_totals")
}

// TestR_STOR_049_AProjectIsChargedTheLogicalSizeOfItsFiles is the acceptance criteria of R-STOR-049 over HTTP and the
// real triggers and fold: a project with two repositories counts a deduplicated blob in both, another project that
// holds the same content is charged in full, the physical figure stays one copy, and a move takes the figure along.
func TestR_STOR_049_AProjectIsChargedTheLogicalSizeOfItsFiles(t *testing.T) {
	f := newFixture(t)
	f.create("web")
	f.create("data")
	f.seedRepo("web", "libs", "active")
	f.seedRepo("web", "tools", "active")
	f.seedRepo("data", "sets", "active")
	big, small := f.seedBlob("big", 1000), f.seedBlob("small", 250)

	f.seedFile("web", "libs", "big.bin", big, 1000)
	f.seedFile("web", "libs", "small.bin", small, 250)
	f.seedFile("web", "tools", "big.bin", big, 1000) // the same blob, a second reference in the same project
	f.seedFile("data", "sets", "big.bin", big, 1000) // and in another project
	f.fold()

	t.Run("R-STOR-049 a blob that two repositories of a project reference counts in both, the physical figure keeps one copy", func(t *testing.T) {
		if got := f.logical(f.admin.Name, "web"); got != 2250 {
			t.Errorf("web is charged %d, want 1000 + 250 + 1000", got)
		}
		if got := f.physical(); got != 1250 {
			t.Errorf("the store keeps %d, want the two blobs once: 1250", got)
		}
	})
	t.Run("R-STOR-049 another project holding the same content is charged in full", func(t *testing.T) {
		if got := f.logical(f.admin.Name, "data"); got != 1000 {
			t.Errorf("data is charged %d, want 1000", got)
		}
		if web, data := f.logical(f.admin.Name, "web"), f.logical(f.admin.Name, "data"); web+data != 3250 || f.physical() != 1250 {
			t.Errorf("the figures add up to %d for a store of %d", web+data, f.physical())
		}
	})
	t.Run("R-STOR-049 the list carries the figure of every project", func(t *testing.T) {
		r := f.do(f.admin, "GET", base, nil)
		wantStatus(t, r, 200, "")
		got := map[string]float64{}
		for _, it := range r.JSON(t)["items"].([]any) {
			m := it.(map[string]any)
			got[fmt.Sprint(m["key"])], _ = m["logicalBytes"].(float64)
		}
		if got["web"] != 2250 || got["data"] != 1000 {
			t.Errorf("the list shows %v", got)
		}
	})
	t.Run("R-STOR-049 a member reads the figure of its project, another project stays 404; the auditor reads every project", func(t *testing.T) {
		added := f.member("web", "user:bob", "reader")
		wantStatus(t, added, 201, "")
		epoch := fmt.Sprint(added.Epoch()) // the replica that answers has caught up with the membership (R-AUTHZ-032)
		if got := f.logical(f.bob.Name, "web", authz.HeaderMinEpoch, epoch); got != 2250 {
			t.Errorf("a member reads %d", got)
		}
		wantStatus(t, f.cl.Do(f.bob.Name, "GET", base+"/data", nil, authz.HeaderMinEpoch, epoch), 404, "")
		if got := f.logical(f.auditor.Name, "data"); got != 1000 {
			t.Errorf("the auditor reads %d", got)
		}
	})
	t.Run("R-STOR-049 a repository that moves takes its figure along, with no recount", func(t *testing.T) {
		wantStatus(t, f.do(f.admin, "POST", base+"/web/repositories/tools/move", map[string]any{"project": "data"}), 200, "")
		if web, data := f.logical(f.admin.Name, "web"), f.logical(f.admin.Name, "data"); web != 1250 || data != 2000 {
			t.Errorf("after the move web is charged %d and data %d, want 1250 and 2000", web, data)
		}
		f.fold()
		if web, data := f.logical(f.admin.Name, "web"), f.logical(f.admin.Name, "data"); web != 1250 || data != 2000 {
			t.Errorf("after a fold web is charged %d and data %d", web, data)
		}
	})
	t.Run("R-STOR-049 deleting a file in one project leaves the figure of the other, which holds the same content", func(t *testing.T) {
		f.e.Exec(`DELETE FROM items WHERE repo_id = $1`, f.repoID("data", "sets"))
		f.fold()
		if web, data := f.logical(f.admin.Name, "web"), f.logical(f.admin.Name, "data"); web != 1250 || data != 1000 {
			t.Errorf("web is charged %d and data %d, want 1250 and 1000", web, data)
		}
		if f.physical() != 1250 {
			t.Errorf("the blobs are still referenced: the store keeps %d", f.physical())
		}
	})
	t.Run("R-STOR-049 a project without a repository is charged nothing", func(t *testing.T) {
		f.create("empty")
		if got := f.logical(f.admin.Name, "empty"); got != 0 {
			t.Errorf("an empty project is charged %d", got)
		}
	})
}
