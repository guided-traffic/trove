package config

import "os"

// Tracing configures OpenTelemetry export (R-OPS-018). The YAML section is
// named tracing; this file is the "trace" section of the package layout.
type Tracing struct {
	Enabled              bool    `yaml:"enabled" default:"false"`
	Endpoint             string  `yaml:"endpoint" default:"" doc:"Collector endpoint, e.g. http://otel-collector.observability:4318."`
	Protocol             string  `yaml:"protocol" default:"http/protobuf" validate:"oneof=grpc|http/protobuf"`
	CAFile               string  `yaml:"ca_file" default:""`
	HeadersFile          string  `yaml:"headers_file" default:"" secret:"file" doc:"One \"Name: value\" per line."`
	SampleRatio          float64 `yaml:"sample_ratio" default:"0.1" validate:"min=0,max=1"`
	IncomingContext      string  `yaml:"incoming_context" default:"trusted_proxies" validate:"oneof=trusted_proxies|all|none"`
	PropagateToUpstreams bool    `yaml:"propagate_to_upstreams" default:"false"`
	QueueSize            int     `yaml:"queue_size" default:"2048" validate:"min=1"`
}

// Validate checks the cross-field rules of the section.
func (t *Tracing) Validate(r *Report) {
	// With the trove key unset, the standard OTEL_EXPORTER_OTLP_* variables
	// apply (R-OPS-018), so a missing endpoint is an error only when they are
	// unset too.
	if t.Enabled && t.Endpoint == "" && os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" {
		r.Add("endpoint", "is required with enabled true, unless OTEL_EXPORTER_OTLP_ENDPOINT is set")
	}
}
