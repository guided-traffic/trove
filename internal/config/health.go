package config

import "time"

// Health configures the probes (R-OPS-001, R-ARCH-024).
type Health struct {
	CheckInterval      time.Duration `yaml:"check_interval" default:"5s" validate:"min=100ms"`
	CheckTimeout       time.Duration `yaml:"check_timeout" default:"2s" validate:"min=100ms"`
	FailureThreshold   int           `yaml:"failure_threshold" default:"2" validate:"min=1"`
	SuccessThreshold   int           `yaml:"success_threshold" default:"1" validate:"min=1"`
	RequireObjectStore bool          `yaml:"require_object_store" default:"false" doc:"True makes the object store gate readiness continuously."`
	WatchdogTimeout    time.Duration `yaml:"watchdog_timeout" default:"2m" validate:"min=1s" doc:"Bound for the watched loops of liveness."`
	ShutdownDrainDelay time.Duration `yaml:"shutdown_drain_delay" default:"5s" validate:"min=0s" doc:"Must exceed the edge's endpoint removal time."`
}

// Validate checks the cross-field rules of the section.
func (h *Health) Validate(r *Report) {
	if h.CheckTimeout > h.CheckInterval {
		r.Add("check_timeout", "must not exceed check_interval")
	}
}
