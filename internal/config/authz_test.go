package config

import (
	"strings"
	"testing"
	"time"
)

func TestR_AUTHZ_028_StalenessFloor(t *testing.T) {
	check := func(staleness, poll time.Duration) []string {
		var c Config
		c.Authz.Cache.MaxStaleness = staleness
		c.Cluster.EpochPollInterval = poll
		var issues []Issue
		c.ValidateAuthz(&Report{issues: &issues})
		out := make([]string, len(issues))
		for i, is := range issues {
			out[i] = is.Message
		}
		return out
	}
	t.Run("R-AUTHZ-028 the shipped values are accepted", func(t *testing.T) {
		if got := check(10*time.Second, time.Second); len(got) != 0 {
			t.Fatalf("issues: %v", got)
		}
	})
	t.Run("R-AUTHZ-028 a value below three poll intervals is refused and names the key", func(t *testing.T) {
		got := check(2*time.Second, time.Second)
		if len(got) != 1 || !strings.Contains(got[0], "authz.cache.max_staleness") || !strings.Contains(got[0], "cluster.epoch_poll_interval") {
			t.Fatalf("issues: %v", got)
		}
	})
	t.Run("R-AUTHZ-028 exactly three poll intervals are accepted", func(t *testing.T) {
		if got := check(3*time.Second, time.Second); len(got) != 0 {
			t.Fatalf("issues: %v", got)
		}
	})
}
