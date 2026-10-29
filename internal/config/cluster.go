package config

import "time"

// Cluster tunes the coordination between replicas (R-ARCH-017, R-ARCH-018,
// R-ARCH-033). The session host of the lease pool is database.session_host.
type Cluster struct {
	LeaseRetryInterval   time.Duration `yaml:"lease_retry_interval" default:"15s" validate:"min=1s"`
	EpochPollInterval    time.Duration `yaml:"epoch_poll_interval" default:"1s" validate:"min=100ms"`
	EpochMaxStaleness    time.Duration `yaml:"epoch_max_staleness" default:"20s" validate:"min=15s" doc:"The invalidation readiness check; at least the readiness window of R-HA-008."`
	ChangeRetention      time.Duration `yaml:"change_retention" default:"1h" validate:"min=1m" doc:"Retention of the cluster-change-prune task."`
	SignalCoalesceWindow time.Duration `yaml:"signal_coalesce_window" default:"50ms" validate:"min=0s"`
	FlightLockTTL        time.Duration `yaml:"flight_lock_ttl" default:"30s" validate:"min=3s" doc:"Extended every ttl/3 while the fetch runs."`
}

// validateWatchedLoops rejects a cadence that would fail liveness. The loops the
// coordination layer watches (R-ARCH-016) record progress once per iteration, so
// an interval at or above health.watchdog_timeout fails liveness on a healthy
// replica and the kubelet restarts it in a loop. Config.Validate (the root
// section's rules across sections, finalize.go) calls it.
func (c *Config) validateWatchedLoops(r *Report) {
	limit := c.Health.WatchdogTimeout
	for _, w := range []struct {
		key      string
		interval time.Duration
		loop     string
	}{
		{"jobs.poll_interval", c.Jobs.PollInterval, "jobs.claim"},
		{"jobs.schedule_tick", c.Jobs.ScheduleTick, "jobs.reaper and jobs.scheduler"},
		{"cluster.epoch_poll_interval", c.Cluster.EpochPollInterval, "cluster.changes"},
		{"database.lease_check_interval", c.Database.LeaseCheckInterval, "cluster.leases"},
		{"system.heartbeat_interval", c.System.HeartbeatInterval, "cluster.registry"},
	} {
		if limit > 0 && w.interval >= limit {
			r.Add(w.key, "(%s) must be below health.watchdog_timeout (%s): it is the cadence of the watched loop %s", w.interval, limit, w.loop)
		}
	}
}
