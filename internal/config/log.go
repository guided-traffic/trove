package config

import "time"

// Log configures the log stream and the request log (R-OPS-009, R-OPS-010,
// R-OPS-012).
type Log struct {
	Format              string            `yaml:"format" default:"json" validate:"oneof=json|text"`
	Level               string            `yaml:"level" default:"info" validate:"oneof=debug|info|warn|error"`
	Levels              map[string]string `yaml:"levels" default:"{}" doc:"Per-component level, e.g. {\"proxy\":\"debug\"}."`
	AddSource           bool              `yaml:"add_source" default:"false"`
	RingBufferRecords   int               `yaml:"ring_buffer_records" default:"5000" validate:"min=0"`
	RingBufferBytes     ByteSize          `yaml:"ring_buffer_bytes" default:"8MiB" validate:"min=0" doc:"Upper bound of the bytes the ring buffer holds; the oldest records go first. A record above 16 KiB is kept as a stub."`
	OverrideMaxDuration time.Duration     `yaml:"override_max_duration" default:"24h" validate:"min=1m"`
	Request             LogRequest        `yaml:"request"`
	Upstream            LogUpstream       `yaml:"upstream"`
}

// LogRequest configures the request log.
type LogRequest struct {
	Enabled           bool     `yaml:"enabled" default:"true"`
	OpsListener       bool     `yaml:"ops_listener" default:"false" doc:"Also log probe and metrics requests."`
	RedactQueryParams []string `yaml:"redact_query_params" default:"[\"token\",\"access_token\",\"password\",\"secret\",\"sig\",\"signature\",\"X-Amz-Signature\",\"X-Amz-Credential\",\"X-Amz-Security-Token\"]" doc:"Query parameters whose value is redacted in the request path and in logged URLs (R-OPS-010, R-OPS-013)."`
}

// LogUpstream configures the upstream request log (R-OPS-011).
type LogUpstream struct {
	Enabled bool `yaml:"enabled" default:"true"`
}

// Validate checks the component levels.
func (l *Log) Validate(r *Report) {
	for comp, lvl := range l.Levels {
		switch lvl {
		case "debug", "info", "warn", "error":
		default:
			r.Add("levels", "level %q of component %q must be debug, info, warn or error", lvl, comp)
		}
	}
}
