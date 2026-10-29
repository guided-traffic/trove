package config

import "time"

// Audit configures audit recording (R-OPS-015, R-OPS-017). RetentionDays 0 keeps
// every partition; `trove migrate` writes it into audit_policy on every run.
type Audit struct {
	RetentionDays             int           `yaml:"retention_days" default:"365" validate:"min=0"`
	AsyncBuffer               int           `yaml:"async_buffer" default:"10000" validate:"min=1"`
	DenialDedupWindow         time.Duration `yaml:"denial_dedup_window" default:"1m" validate:"min=0s"`
	TokenRejectionDedupWindow time.Duration `yaml:"token_rejection_dedup_window" default:"5m" validate:"min=0s"`
	RecordAnonymousDenials    bool          `yaml:"record_anonymous_denials" default:"false"`
	RecordDownloads           bool          `yaml:"record_downloads" default:"false"`
}
