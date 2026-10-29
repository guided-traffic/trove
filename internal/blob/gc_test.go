package blob_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/blob"
)

func TestGCResult(t *testing.T) {
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
