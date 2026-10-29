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
	ShutdownTimeout      time.Duration `yaml:"shutdown_timeout" default:"25s" validate:"min=1s" doc:"Server shutdown timeout; the chart derives it from the grace period."`
	TLS                  ServerTLS     `yaml:"tls"`
	ContentBaseURL       string        `yaml:"content_base_url" default:"" doc:"Separate content origin (P1); empty means one origin."`
	ContentURLTTL        time.Duration `yaml:"content_url_ttl" default:"300s" doc:"Lifetime of a signed content URL."`
	BaseURL              string        `yaml:"base_url" default:"" doc:"Canonical external origin; empty is allowed for development only."`
	TrustedProxies       []string      `yaml:"trusted_proxies" default:"[]"`
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
	for _, c := range s.TrustedProxies {
		if _, _, err := net.ParseCIDR(c); err != nil && net.ParseIP(c) == nil {
			r.Add("trusted_proxies", "%q is neither an address nor a CIDR", c)
		}
	}
}

// checkOrigin enforces R-OPS-005: scheme, host and optional port, nothing else.
func checkOrigin(r *Report, key, raw string) {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		r.Add(key, "%q is not a URL", raw)
	case u.Scheme != "http" && u.Scheme != "https":
		r.Add(key, "%q must use the scheme http or https", raw)
	case u.Host == "":
		r.Add(key, "%q has no host", raw)
	case u.User != nil:
		r.Add(key, "%q must not carry user information", raw)
	case u.Path != "" && u.Path != "/":
		r.Add(key, "%q must be an origin: its path %q is not allowed", raw, u.Path)
	case u.RawQuery != "" || u.ForceQuery:
		r.Add(key, "%q must not carry a query", raw)
	case u.Fragment != "":
		r.Add(key, "%q must not carry a fragment", raw)
	}
}
