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
