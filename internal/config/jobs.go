package config

import "time"

// Jobs configures the queue, workers and leases (R-ARCH-016, R-ARCH-017). Only
// `worker` replicas claim jobs; `web` replicas enqueue them.
type Jobs struct {
	Workers              JobsWorkers              `yaml:"workers" doc:"Concurrent jobs per replica and queue."`
	PollInterval         time.Duration            `yaml:"poll_interval" default:"2s" validate:"min=100ms"`
	LeaseDuration        time.Duration            `yaml:"lease_duration" default:"60s" validate:"min=1s"`
	HeartbeatInterval    time.Duration            `yaml:"heartbeat_interval" default:"15s" validate:"min=1s"`
	ScheduleTick         time.Duration            `yaml:"schedule_tick" default:"10s" validate:"min=1s"`
	Retention            time.Duration            `yaml:"retention" default:"720h" validate:"min=1h" doc:"How long finished jobs are kept."`
	BackgroundMigrations JobsBackgroundMigrations `yaml:"background_migrations"`
}

// JobsWorkers is the number of concurrent jobs per queue.
type JobsWorkers struct {
	Default     int `yaml:"default" default:"4" validate:"min=0"`
	Maintenance int `yaml:"maintenance" default:"1" validate:"min=0"`
	Index       int `yaml:"index" default:"2" validate:"min=0"`
	Delivery    int `yaml:"delivery" default:"8" validate:"min=0" doc:"Webhook deliveries (P1)."`
}

// JobsBackgroundMigrations paces data migrations that run as jobs (R-ARCH-026).
type JobsBackgroundMigrations struct {
	BatchSize           int           `yaml:"batch_size" default:"1000" validate:"min=1"`
	PauseBetweenBatches time.Duration `yaml:"pause_between_batches" default:"100ms" validate:"min=0s"`
}

// Validate checks the cross-field rules of the section.
func (j *Jobs) Validate(r *Report) {
	if j.LeaseDuration < 3*j.HeartbeatInterval {
		r.Add("lease_duration", "must be at least 3 times heartbeat_interval (is %s, heartbeat_interval is %s)", j.LeaseDuration, j.HeartbeatInterval)
	}
}

// Queues are the queue names the worker slots are configured for, in a stable
// order. A kind registered in another queue is a programming error that the
// jobs package reports at start-up.
func (w JobsWorkers) Queues() []string {
	return []string{"default", "maintenance", "index", "delivery"}
}

// Count returns the concurrent jobs of a queue and whether the queue exists.
func (w JobsWorkers) Count(queue string) (int, bool) {
	switch queue {
	case "default":
		return w.Default, true
	case "maintenance":
		return w.Maintenance, true
	case "index":
		return w.Index, true
	case "delivery":
		return w.Delivery, true
	}
	return 0, false
}
