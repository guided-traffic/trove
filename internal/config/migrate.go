package config

import "time"

// Migrate tunes the migration runner (R-ARCH-026, R-HA-019).
type Migrate struct {
	AdvisoryLockWait time.Duration `yaml:"advisory_lock_wait" default:"5m" validate:"min=1s" doc:"How long to wait for another migrator."`
	DDLLockTimeout   time.Duration `yaml:"ddl_lock_timeout" default:"5s" validate:"min=100ms"`
	DDLLockRetries   int           `yaml:"ddl_lock_retries" default:"20" validate:"min=1"`
}
