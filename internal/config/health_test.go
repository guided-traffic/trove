package config

import (
	"testing"
	"time"
)

func TestDatabaseOutageGrace(t *testing.T) {
	t.Run("R-OPS-001 the default grace of 20 s covers a CloudNativePG switchover and stays below the 30 s retry bound", func(t *testing.T) {
		l, err := loadWith(t)
		if err != nil {
			t.Fatal(err)
		}
		g, m := l.Config.Health.DatabaseOutageGrace, l.Config.Database.Retry.MaxElapsed
		if g != 20*time.Second || g <= 15*time.Second || g >= m {
			t.Fatalf("grace %s, retry.max_elapsed %s", g, m)
		}
	})
	t.Run("R-OPS-001 a grace at or above retry.max_elapsed is refused naming the key", func(t *testing.T) {
		for _, env := range [][]string{
			{"TROVE_HEALTH_DATABASE_OUTAGE_GRACE=30s"},
			{"TROVE_DATABASE_RETRY_MAX_ELAPSED=10s"}, // the default grace of 20 s no longer fits
		} {
			_, err := loadWith(t, env...)
			if !hasIssue(err, "health.database_outage_grace") {
				t.Errorf("%v: err = %v", env, err)
			}
		}
	})
	t.Run("R-OPS-001 a grace of 0 switches the tolerance off and fits any retry bound", func(t *testing.T) {
		if _, err := loadWith(t, "TROVE_HEALTH_DATABASE_OUTAGE_GRACE=0s", "TROVE_DATABASE_RETRY_MAX_ELAPSED=2s"); err != nil {
			t.Fatal(err)
		}
	})
}
