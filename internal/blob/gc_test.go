package blob_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/blob"
)

func TestGCQueries(t *testing.T) {
	t.Run("R-STOR-024 the full sweep anti-joins every table with a foreign key to blobs", func(t *testing.T) {
		q := blob.FullSweepSQL([]string{"items", "retention_run_items", `"odd"."table"`}, []string{"blob_sha256", "blob_sha256", `"Col"`})
		for _, want := range []string{
			"NOT EXISTS (SELECT 1 FROM items r WHERE r.blob_sha256 = b.sha256)",
			"NOT EXISTS (SELECT 1 FROM retention_run_items r WHERE r.blob_sha256 = b.sha256)",
			`NOT EXISTS (SELECT 1 FROM "odd"."table" r WHERE r."Col" = b.sha256)`,
			"b.state = 'live'", "ORDER BY b.sha256 LIMIT $3",
		} {
			if !strings.Contains(q, want) {
				t.Errorf("missing %q in\n%s", want, q)
			}
		}
		// A table that carries the blob_sha256 column but no foreign key would not be
		// listed; the catalog read is what makes a newer release's table count.
		if strings.Contains(blob.FullSweepSQL(nil, nil), "NOT EXISTS") {
			t.Error("an anti-join without tables")
		}
	})
	t.Run("R-STOR-025 the result and the checkpoint serialize in the shape the job API shows", func(t *testing.T) {
		b, err := json.Marshal(blob.GCResult{DryRun: true, Candidates: 3, Kept: 1, Deleted: 2, BytesReclaimed: 10})
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{`"dryRun":true`, `"trashPurged":0`, `"candidates":3`, `"kept":1`, `"deleted":2`, `"bytesReclaimed":10`, `"errors":0`} {
			if !strings.Contains(string(b), k) {
				t.Errorf("missing %s in %s", k, b)
			}
		}
		c, _ := json.Marshal(blob.Cursor{After: "ab"})
		if string(c) != `{"after":"ab"}` {
			t.Fatalf("cursor %s", c)
		}
	})
}
