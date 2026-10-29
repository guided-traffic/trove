package config

import "time"

// Defaults of a listener's TLS block. The default tags below carry the same
// values; a unit test keeps them equal.
const (
	DefaultTLSMinVersion     = "1.2"
	DefaultTLSReloadInterval = 10 * time.Second
	// ClientAuthNone requests no client certificate.
	ClientAuthNone = "none"
	// ClientAuthVerifyIfGiven verifies a certificate the client presents and
	// admits a client without one.
	ClientAuthVerifyIfGiven = "verify_if_given"
	// ClientAuthRequire refuses a client without a certificate that chains to
	// client_ca_file, in the handshake.
	ClientAuthRequire = "require"
)

// ServerTLS is the TLS block of a listener (R-ARCH-035). The listener serves
// TLS when Certificates is non-empty and plain HTTP when it is empty. Every file
// is read again every ReloadInterval and compared by content, so a rotated
// certificate serves new handshakes without a restart (Q-ARCH-31).
type ServerTLS struct {
	Certificates   []TLSCertificate `yaml:"certificates" default:"[]" doc:"Certificate/key pairs (PEM). SNI selects the pair whose names match, wildcard certificates included; the first pair is the default. Empty means plain HTTP."`
	ClientCAFile   string           `yaml:"client_ca_file" default:"" doc:"PEM bundle of the CAs a client certificate must chain to (mutual TLS for the edge). Required unless client_auth is none."`
	ClientAuth     string           `yaml:"client_auth" default:"none" validate:"oneof=none|verify_if_given|require" doc:"none: no client certificate is requested; verify_if_given: a presented certificate must chain to client_ca_file; require: a client without one is refused in the handshake."`
	MinVersion     string           `yaml:"min_version" default:"1.2" validate:"oneof=1.2|1.3" doc:"Lowest TLS version accepted; TLS 1.3 is preferred either way."`
	ReloadInterval time.Duration    `yaml:"reload_interval" default:"10s" validate:"min=1s" doc:"How often every file of the block is read and compared by content. A changed certificate serves new handshakes within this interval."`
}

// TLSCertificate is one certificate/key pair of a listener.
type TLSCertificate struct {
	CertFile string `yaml:"cert_file" json:"cert_file" validate:"required" doc:"PEM certificate chain, leaf first."`
	KeyFile  string `yaml:"key_file" json:"key_file" validate:"required" secret:"file" doc:"PEM private key of the certificate; read again on every reload, never logged."`
}

// ServerOpsTLS is the TLS of the ops listener (Q-ARCH-33). With Inherit it
// serves the certificates, min_version and reload_interval of server.tls and
// asks no client for a certificate: the kubelet's probes cannot present one.
// With Inherit false the block below is the ops listener's own.
type ServerOpsTLS struct {
	Inherit        bool             `yaml:"inherit" default:"true" doc:"true: the ops listener serves server.tls's certificates, min_version and reload_interval without client certificates; false: the keys below are its own block."`
	Certificates   []TLSCertificate `yaml:"certificates" default:"[]" doc:"With inherit false: the ops listener's certificate/key pairs; empty means plain HTTP, allowed only while server.tls is plain too."`
	ClientCAFile   string           `yaml:"client_ca_file" default:"" doc:"With inherit false: CAs a client certificate on the ops listener must chain to."`
	ClientAuth     string           `yaml:"client_auth" default:"none" validate:"oneof=none|verify_if_given|require" doc:"With inherit false: client certificates on the ops listener; probes present none, so require breaks HTTPS probes."`
	MinVersion     string           `yaml:"min_version" default:"1.2" validate:"oneof=1.2|1.3" doc:"With inherit false: lowest TLS version of the ops listener."`
	ReloadInterval time.Duration    `yaml:"reload_interval" default:"10s" validate:"min=1s" doc:"With inherit false: reload interval of the ops listener's files."`
}

// Enabled reports whether the block serves TLS.
func (t ServerTLS) Enabled() bool { return len(t.Certificates) > 0 }

// own is the ops listener's own block (Inherit false).
func (o ServerOpsTLS) own() ServerTLS {
	return ServerTLS{
		Certificates: o.Certificates, ClientCAFile: o.ClientCAFile, ClientAuth: o.ClientAuth,
		MinVersion: o.MinVersion, ReloadInterval: o.ReloadInterval,
	}
}

// OpsListenerTLS is the effective TLS block of the ops listener.
func (s *Server) OpsListenerTLS() ServerTLS {
	if !s.OpsTLS.Inherit {
		return s.OpsTLS.own()
	}
	b := s.TLS
	b.ClientCAFile, b.ClientAuth = "", ClientAuthNone
	return b
}

// validateTLS checks both listeners' blocks and how they combine.
func (s *Server) validateTLS(r *Report) {
	validateTLSBlock(r, "tls", s.TLS)
	if !s.OpsTLS.Inherit {
		validateTLSBlock(r, "ops_tls", s.OpsTLS.own())
	} else {
		o := s.OpsTLS
		var set []string
		if len(o.Certificates) > 0 {
			set = append(set, "certificates")
		}
		if o.ClientCAFile != "" {
			set = append(set, "client_ca_file")
		}
		if o.ClientAuth != ClientAuthNone {
			set = append(set, "client_auth")
		}
		if o.MinVersion != DefaultTLSMinVersion {
			set = append(set, "min_version")
		}
		if o.ReloadInterval != DefaultTLSReloadInterval {
			set = append(set, "reload_interval")
		}
		if len(set) > 0 {
			r.Add("ops_tls.inherit", "is true, so the ops listener takes server.tls's certificates, min_version and reload_interval; ops_tls %v would be ignored: set inherit: false to give the ops listener its own block", set)
		}
	}
	if s.TLS.Enabled() && !s.OpsListenerTLS().Enabled() {
		r.Add("ops_tls.certificates", "is empty with inherit: false while server.tls serves TLS: the ops listener would serve plain HTTP; name its certificates or set inherit: true")
	}
}

func validateTLSBlock(r *Report, key string, b ServerTLS) {
	seen := map[string]int{}
	for i, c := range b.Certificates {
		if c.CertFile == "" || c.KeyFile == "" {
			r.Add(key+".certificates", "entry %d needs both cert_file and key_file", i)
			continue
		}
		if j, dup := seen[c.CertFile]; dup {
			r.Add(key+".certificates", "entry %d repeats the cert_file %q of entry %d", i, c.CertFile, j)
		}
		seen[c.CertFile] = i
	}
	switch {
	case b.ClientAuth != ClientAuthNone && b.ClientAuth != "" && b.ClientCAFile == "":
		r.Add(key+".client_ca_file", "is required with client_auth %s: without it no client certificate can verify", b.ClientAuth)
	case b.ClientCAFile != "" && b.ClientAuth == ClientAuthNone:
		r.Add(key+".client_auth", "is none while client_ca_file is set, so no client certificate would be requested; set client_auth to require or verify_if_given, or remove client_ca_file")
	}
	if !b.Enabled() && (b.ClientCAFile != "" || (b.ClientAuth != ClientAuthNone && b.ClientAuth != "")) {
		r.Add(key+".certificates", "is empty: client certificates need TLS on the listener")
	}
}
