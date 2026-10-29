package config

import "time"

// Metrics configures the metrics endpoint (R-OPS-006, R-OPS-007). The bearer
// token is a file-only secret.
type Metrics struct {
	Enabled                bool               `yaml:"enabled" default:"true"`
	Auth                   string             `yaml:"auth" default:"none" validate:"oneof=none|static"`
	BearerTokenFile        string             `yaml:"bearer_token_file" default:"" secret:"file"`
	LabelLimits            MetricsLabelLimits `yaml:"label_limits"`
	RepositoryLabelInclude []string           `yaml:"repository_label_include" default:"[]"`
	SlowQueryThreshold     time.Duration      `yaml:"slow_query_threshold" default:"500ms" validate:"min=0s"`
	DBQueryHistograms      bool               `yaml:"db_query_histograms" default:"false"`
	NativeHistograms       bool               `yaml:"native_histograms" default:"false"`
	DBGaugeInterval        time.Duration      `yaml:"db_gauge_interval" default:"30s" validate:"min=1s"`
}

// MetricsLabelLimits caps the distinct values of high-cardinality labels.
type MetricsLabelLimits struct {
	Repository   int `yaml:"repository" default:"500" validate:"min=0"`
	Subscription int `yaml:"subscription" default:"100" validate:"min=0"`
}

// Validate checks the cross-field rules of the section.
func (m *Metrics) Validate(r *Report) {
	if m.Auth == "static" && m.BearerTokenFile == "" {
		r.Add("bearer_token_file", "is required with auth static")
	}
}
