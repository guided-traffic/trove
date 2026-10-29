package config

import "time"

// Audit configures audit recording (R-OPS-015, R-OPS-017). Only the keys that
// the migration Job and the buffers of M0 read exist yet; the owner of the audit
// subsystem adds the rest here.
type Audit struct {
	RetentionDays             int           `yaml:"retention_days" default:"365" validate:"min=1"`
	AsyncBuffer               int           `yaml:"async_buffer" default:"10000" validate:"min=1"`
	DenialDedupWindow         time.Duration `yaml:"denial_dedup_window" default:"1m" validate:"min=0s"`
	TokenRejectionDedupWindow time.Duration `yaml:"token_rejection_dedup_window" default:"5m" validate:"min=0s"`
	RecordAnonymousDenials    bool          `yaml:"record_anonymous_denials" default:"false"`
	RecordDownloads           bool          `yaml:"record_downloads" default:"false"`
}
