package config

import "time"

// Runtime carries the values the chart derives per Deployment (R-HA-036,
// R-OPS-028): they arrive as ordinary mapped environment variables.
type Runtime struct {
	MemoryLimitRatio       float64       `yaml:"memory_limit_ratio" default:"0.85" validate:"min=0.1,max=1" doc:"Share of the container memory limit given to the Go runtime."`
	MemoryLimitBytes       ByteSize      `yaml:"memory_limit_bytes" default:"0" validate:"min=0" doc:"Container memory limit; 0 means none known."`
	TerminationGracePeriod time.Duration `yaml:"termination_grace_period" default:"0s" validate:"min=0s" doc:"Pod grace period; 0 means unknown."`
}
