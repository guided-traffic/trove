//go:build integration

package coordination

import (
	"testing"

	"github.com/guided-traffic/trove/internal/cluster"
)

// TestChangeAreasAreSeeded: a change area without its cluster_epoch row cannot
// propagate (the poll would only log that the row is missing), so the schema
// after the migrations must have one row per registered change area.
func TestChangeAreasAreSeeded(t *testing.T) {
	e := newEnv(t)
	t.Run("R-ARCH-018 every registered change area has its cluster_epoch row after the migrations", func(t *testing.T) {
		areas := cluster.Areas(cluster.ChangeArea)
		if len(areas) == 0 {
			t.Fatal("no change area is registered; the test would prove nothing")
		}
		for _, a := range areas {
			if n := e.scalar(`SELECT count(*) FROM cluster_epoch WHERE area = $1`, a.Name); n != 1 {
				t.Errorf("change area %q has %d cluster_epoch rows after the migrations; the migration that ships it must INSERT INTO cluster_epoch (area) VALUES ('%s')", a.Name, n, a.Name)
			}
		}
	})
}
