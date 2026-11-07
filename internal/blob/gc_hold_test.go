package blob_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/blob"
)

// The hold check itself needs PostgreSQL and is tested in test/integration/backup/create_hold_test.go; this is its result shape.
func TestGCResultHold(t *testing.T) {
	t.Run("R-BAK-007 a run that ended at a global hold says so and counts held candidates", func(t *testing.T) {
		b, err := json.Marshal(blob.GCResult{Held: 2, HoldSkipped: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{`"held":2`, `"holdSkipped":true`} {
			if !strings.Contains(string(b), k) {
				t.Errorf("missing %s in %s", k, b)
			}
		}
		if b, _ := json.Marshal(blob.GCResult{}); strings.Contains(string(b), "holdSkipped") {
			t.Errorf("holdSkipped must be omitted when false: %s", b)
		}
	})
}
