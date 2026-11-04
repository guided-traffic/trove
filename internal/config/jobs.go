package config

import (
	"slices"
	"strings"
	"time"
)

// Jobs configures the queue, workers and leases (R-ARCH-016, R-ARCH-017). Only
// `worker` replicas claim jobs; `web` replicas enqueue them.
type Jobs struct {
	Workers              JobsWorkers              `yaml:"workers" doc:"Concurrent jobs per replica and queue."`
	Queues               []string                 `yaml:"queues" default:"[]" doc:"Queues this worker claims; empty: every queue. Mutually exclusive with exclude_queues. As an environment variable the list is written as JSON, for example TROVE_JOBS_QUEUES='[\"index\"]'."`
	ExcludeQueues        []string                 `yaml:"exclude_queues" default:"[]" doc:"Queues this worker does not claim; empty: none. Mutually exclusive with queues. As an environment variable the list is written as JSON, for example TROVE_JOBS_EXCLUDE_QUEUES='[\"index\",\"mirror\"]'."`
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
	Mirror      int `yaml:"mirror" default:"2" validate:"min=0" doc:"The passes of mirrored remote repositories and their dispatch (R-REPO-053)."`
	Vuln        int `yaml:"vuln" default:"1" validate:"min=0" doc:"The advisory sync (R-VULN-004, R-VULN-008)."`
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
	j.validateQueues(r)
}

// validateQueues checks jobs.queues and jobs.exclude_queues (R-ARCH-016, Q-ARCH-296): every name is a queue of the
// release, the two lists are not both set, and the process is left with at least one queue.
func (j *Jobs) validateQueues(r *Report) {
	known := j.Workers.Queues()
	for _, key := range []struct {
		name  string
		names []string
	}{{"queues", j.Queues}, {"exclude_queues", j.ExcludeQueues}} {
		for _, n := range key.names {
			if !slices.Contains(known, n) {
				r.Add(key.name, "names the queue %q, which does not exist (the queues are %s)", n, strings.Join(known, ", "))
			}
		}
	}
	if len(j.Queues) > 0 && len(j.ExcludeQueues) > 0 {
		r.Add("exclude_queues", "must be empty while queues is set: a process claims the listed queues or all but the excluded ones, not both")
		return
	}
	if len(j.Queues) == 0 && len(j.ExcludeQueues) > 0 && len(j.ClaimedQueues()) == 0 {
		r.Add("exclude_queues", "excludes every queue (%s), so this process would claim nothing", strings.Join(known, ", "))
	}
}

// ClaimedQueues returns the queues this process claims, in the stable order of [JobsWorkers.Queues]: the listed ones
// (jobs.queues), else every queue but the excluded ones (jobs.exclude_queues), else every queue. A name that is not a
// queue is ignored here; [Jobs.Validate] refuses it at start-up.
func (j *Jobs) ClaimedQueues() []string {
	var out []string
	for _, n := range j.Workers.Queues() {
		switch {
		case len(j.Queues) > 0:
			if slices.Contains(j.Queues, n) {
				out = append(out, n)
			}
		case !slices.Contains(j.ExcludeQueues, n):
			out = append(out, n)
		}
	}
	return out
}

// Claims reports whether this process claims the queue.
func (j *Jobs) Claims(queue string) bool { return slices.Contains(j.ClaimedQueues(), queue) }

// Queues are the queue names the worker slots are configured for, in a stable
// order. A kind registered in another queue is a programming error that the
// jobs package reports at start-up.
func (w JobsWorkers) Queues() []string {
	return []string{"default", "maintenance", "index", "delivery", "mirror", "vuln"}
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
	case "mirror":
		return w.Mirror, true
	case "vuln":
		return w.Vuln, true
	}
	return 0, false
}
