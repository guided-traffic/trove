package config

import (
	"strings"
	"testing"
	"time"
)

func TestJobsWorkersQueues(t *testing.T) {
	w := JobsWorkers{Default: 4, Maintenance: 1, Index: 2, Delivery: 8}
	t.Run("R-ARCH-016 every queue has its worker slots", func(t *testing.T) {
		want := map[string]int{"default": 4, "maintenance": 1, "index": 2, "delivery": 8}
		for _, q := range w.Queues() {
			n, ok := w.Count(q)
			if !ok || n != want[q] {
				t.Errorf("Count(%s) = %d, %v", q, n, ok)
			}
		}
		if len(w.Queues()) != len(want) {
			t.Errorf("Queues = %v", w.Queues())
		}
		if _, ok := w.Count("elsewhere"); ok {
			t.Error("an unknown queue was accepted")
		}
	})
}

func TestWatchedLoopIntervals(t *testing.T) {
	check := func(mut func(*Config)) []string {
		var c Config
		c.Health.WatchdogTimeout = 2 * time.Minute
		c.Jobs.PollInterval, c.Jobs.ScheduleTick = 2*time.Second, 10*time.Second
		c.Cluster.EpochPollInterval = time.Second
		c.Database.LeaseCheckInterval = 5 * time.Second
		c.System.HeartbeatInterval = 10 * time.Second
		mut(&c)
		var issues []Issue
		c.validateWatchedLoops(&Report{issues: &issues})
		out := make([]string, len(issues))
		for i, is := range issues {
			out[i] = is.Message
		}
		return out
	}
	t.Run("R-ARCH-016 the shipped cadences are accepted", func(t *testing.T) {
		if got := check(func(*Config) {}); len(got) != 0 {
			t.Fatalf("issues: %v", got)
		}
	})
	t.Run("R-ARCH-016 a cadence at or above health.watchdog_timeout is rejected naming both keys", func(t *testing.T) {
		for _, tc := range []struct {
			key string
			mut func(*Config)
		}{
			{"jobs.schedule_tick", func(c *Config) { c.Jobs.ScheduleTick = 2 * time.Minute }},
			{"jobs.poll_interval", func(c *Config) { c.Jobs.PollInterval = 3 * time.Minute }},
			{"cluster.epoch_poll_interval", func(c *Config) { c.Cluster.EpochPollInterval = 2 * time.Minute }},
			{"database.lease_check_interval", func(c *Config) { c.Database.LeaseCheckInterval = 5 * time.Minute }},
			{"system.heartbeat_interval", func(c *Config) { c.System.HeartbeatInterval = 2 * time.Minute }},
		} {
			got := check(tc.mut)
			if len(got) != 1 || !strings.Contains(got[0], tc.key) || !strings.Contains(got[0], "health.watchdog_timeout") {
				t.Errorf("%s: issues = %v", tc.key, got)
			}
		}
	})
}
