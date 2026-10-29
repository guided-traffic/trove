package config

import (
	"net"
	"os"
)

// Tracing configures OpenTelemetry export (R-OPS-018). The YAML section is
// named tracing; this file is the "trace" section of the package layout.
//
// Incoming trace context is trusted on its own list, tracing.trusted_peers,
// never on server.trusted_proxies: an edge proxy is trusted to report the
// client address, but it forwards the client's own traceparent unchanged, so
// trusting it for trace context would let every client choose the trace ID
// that the audit trail and X-Request-Id carry.
type Tracing struct {
	Enabled              bool     `yaml:"enabled" default:"false"`
	Endpoint             string   `yaml:"endpoint" default:"" doc:"Collector endpoint, e.g. http://otel-collector.observability:4318. Without user information: credentials go into headers_file."`
	Protocol             string   `yaml:"protocol" default:"http/protobuf" validate:"oneof=grpc|http/protobuf"`
	CAFile               string   `yaml:"ca_file" default:""`
	HeadersFile          string   `yaml:"headers_file" default:"" secret:"file" doc:"One \"Name: value\" per line."`
	SampleRatio          float64  `yaml:"sample_ratio" default:"0.1" validate:"min=0,max=1"`
	IncomingContext      string   `yaml:"incoming_context" default:"trusted_peers" validate:"oneof=trusted_peers|all|none" doc:"trusted_peers: continue an incoming traceparent only from tracing.trusted_peers; all: from every client; none: never. A context that is not continued becomes a span link."`
	TrustedPeers         []string `yaml:"trusted_peers" default:"[]" doc:"Addresses or CIDRs of peers that set a trace context trove may continue (a mesh sidecar, an internal caller). Empty: every request starts a new trace. Never the edge proxy, which passes the client's header through."`
	PropagateToUpstreams bool     `yaml:"propagate_to_upstreams" default:"false"`
	QueueSize            int      `yaml:"queue_size" default:"2048" validate:"min=1"`
}

// Validate checks the cross-field rules of the section.
func (t *Tracing) Validate(r *Report) {
	// With the trove key unset, the standard OTEL_EXPORTER_OTLP_* variables
	// apply (R-OPS-018), so a missing endpoint is an error only when they are
	// unset too.
	if t.Enabled && t.Endpoint == "" && os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" {
		r.Add("endpoint", "is required with enabled true, unless OTEL_EXPORTER_OTLP_ENDPOINT is set")
	}
	if t.Endpoint != "" {
		rejectUserinfo(r, "endpoint", t.Endpoint, "tracing.headers_file")
	}
	for _, c := range t.TrustedPeers {
		_, n, err := net.ParseCIDR(c)
		switch {
		case err != nil && net.ParseIP(c) == nil:
			r.Add("trusted_peers", "%q is neither an address nor a CIDR", c)
		case err == nil:
			if ones, _ := n.Mask.Size(); ones == 0 {
				r.Add("trusted_peers", "%q covers every address and would let every client choose its trace ID", c)
			}
		}
	}
}
