package config

import (
	"net"
	"net/url"
	"time"
)

// Server holds the listeners, timeouts and external addresses (R-ARCH-005,
// R-OPS-005).
type Server struct {
	Listen               string        `yaml:"listen" default:":8080" doc:"Client listener: UI, API, package protocols."`
	OpsListen            string        `yaml:"ops_listen" default:":8081" doc:"Ops listener: probes and metrics."`
	ReadHeaderTimeout    time.Duration `yaml:"read_header_timeout" default:"10s" validate:"min=1s"`
	IdleTimeout          time.Duration `yaml:"idle_timeout" default:"120s"`
	TransferStallTimeout time.Duration `yaml:"transfer_stall_timeout" default:"60s"`
	MaxHeaderBytes       int           `yaml:"max_header_bytes" default:"65536" validate:"min=4096"`
	MaxInFlight          int           `yaml:"max_in_flight" default:"4096" validate:"min=1" doc:"Concurrent requests per replica."`
	ShutdownTimeout      time.Duration `yaml:"shutdown_timeout" default:"0s" validate:"min=0s" doc:"Bound for in-flight requests and running work at SIGTERM. 0 derives it: runtime.termination_grace_period minus health.shutdown_drain_delay minus 5 s, or 25 s when the grace period is unknown. An explicit value must fit the grace period."`
	TLS                  ServerTLS     `yaml:"tls"`
	ContentBaseURL       string        `yaml:"content_base_url" default:"" doc:"Separate content origin (P1); empty means one origin."`
	ContentURLTTL        time.Duration `yaml:"content_url_ttl" default:"300s" doc:"Lifetime of a signed content URL."`
	BaseURL              string        `yaml:"base_url" default:"" doc:"Canonical external origin; empty is allowed for development only."`
	TrustedProxies       []string      `yaml:"trusted_proxies" default:"[]" doc:"Addresses or CIDRs of the edge that sets the forwarding header. A /0 prefix is refused."`
	ForwardedHeader      string        `yaml:"forwarded_header" default:"x-forwarded-for" validate:"oneof=x-forwarded-for|forwarded" doc:"The one header family the edge rewrites: X-Forwarded-* or Forwarded (RFC 7239). The other is never read."`
	AdditionalBaseURLs   []string      `yaml:"additional_base_urls" default:"[]"`
	ReplicaHeader        string        `yaml:"replica_header" default:"authenticated" validate:"oneof=authenticated|always|never"`
}

// ServerTLS terminates TLS on the listeners; empty means plain HTTP.
type ServerTLS struct {
	CertFile string `yaml:"cert_file" default:""`
	KeyFile  string `yaml:"key_file" default:"" secret:"file"`
}

// Validate checks the cross-field rules of the section.
func (s *Server) Validate(r *Report) {
	if (s.TLS.CertFile == "") != (s.TLS.KeyFile == "") {
		r.Add("tls", "cert_file and key_file must be set together")
	}
	for _, h := range []string{s.Listen, s.OpsListen} {
		if _, _, err := net.SplitHostPort(h); err != nil {
			r.Add("listen", "%q is not a host:port address", h)
		}
	}
	if s.Listen == s.OpsListen {
		r.Add("ops_listen", "must differ from listen")
	}
	if s.BaseURL != "" {
		checkOrigin(r, "base_url", s.BaseURL)
	}
	for _, u := range s.AdditionalBaseURLs {
		checkOrigin(r, "additional_base_urls", u)
	}
	if s.ShutdownTimeout != 0 && s.ShutdownTimeout < time.Second {
		r.Add("shutdown_timeout", "must be 0 (derived) or at least 1s")
	}
	for _, c := range s.TrustedProxies {
		_, n, err := net.ParseCIDR(c)
		switch {
		case err != nil && net.ParseIP(c) == nil:
			r.Add("trusted_proxies", "%q is neither an address nor a CIDR", c)
		case err == nil:
			if ones, _ := n.Mask.Size(); ones == 0 {
				r.Add("trusted_proxies", "%q covers every address and would trust every peer; list the edge's own addresses", c)
			}
		}
	}
}

// checkOrigin enforces R-OPS-005: scheme, host and optional port, nothing else.
func checkOrigin(r *Report, key, raw string) {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		r.Add(key, "%q is not a URL", redactUserinfo(raw))
	case u.Scheme != "http" && u.Scheme != "https":
		r.Add(key, "%q must use the scheme http or https", redactUserinfo(raw))
	case u.Host == "":
		r.Add(key, "%q has no host", redactUserinfo(raw))
	case u.User != nil:
		r.Add(key, "%q must not carry user information", redactUserinfo(raw))
	case u.Path != "" && u.Path != "/":
		r.Add(key, "%q must be an origin: its path %q is not allowed", redactUserinfo(raw), u.Path)
	case u.RawQuery != "" || u.ForceQuery:
		r.Add(key, "%q must not carry a query", redactUserinfo(raw))
	case u.Fragment != "":
		r.Add(key, "%q must not carry a fragment", redactUserinfo(raw))
	}
}
