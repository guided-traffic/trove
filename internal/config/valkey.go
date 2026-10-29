package config

import (
	"strings"
	"time"
)

// Valkey configures the Valkey client (R-HA-056, R-ARCH-031). The credentials
// are file-only so a rotated Secret applies without a restart.
type Valkey struct {
	Mode                 string          `yaml:"mode" default:"sentinel" validate:"oneof=sentinel|standalone" doc:"standalone is for development only."`
	Sentinel             ValkeySentinel  `yaml:"sentinel"`
	Address              string          `yaml:"address" default:"" doc:"Standalone mode only."`
	Database             int             `yaml:"database" default:"0" validate:"min=0"`
	KeyPrefix            string          `yaml:"key_prefix" default:"trove:" validate:"required"`
	UsernameFile         string          `yaml:"username_file" default:"" doc:"Empty means the default user."`
	PasswordFile         string          `yaml:"password_file" default:"" secret:"file"`
	TLS                  ValkeyTLS       `yaml:"tls"`
	AllowInsecure        bool            `yaml:"allow_insecure" default:"false" doc:"Required for tls.mode disable."`
	ConnectTimeout       time.Duration   `yaml:"connect_timeout" default:"2s" validate:"min=10ms"`
	CommandTimeout       time.Duration   `yaml:"command_timeout" default:"500ms" validate:"min=10ms"`
	SentinelQueryTimeout time.Duration   `yaml:"sentinel_query_timeout" default:"300ms" validate:"min=10ms" doc:"Per Sentinel tried."`
	MaxConnLifetime      time.Duration   `yaml:"max_conn_lifetime" default:"30m" validate:"min=1s"`
	HealthCheckInterval  time.Duration   `yaml:"health_check_interval" default:"5s" validate:"min=100ms"`
	Reconnect            ValkeyReconnect `yaml:"reconnect"`
	CircuitBreaker       ValkeyBreaker   `yaml:"circuit_breaker"`
}

// ValkeySentinel locates the primary through Sentinel.
type ValkeySentinel struct {
	Addresses       []string      `yaml:"addresses" default:"[]" doc:"Sentinel host:port list; required in sentinel mode."`
	PrimaryName     string        `yaml:"primary_name" default:"" doc:"Name in the Sentinels' monitor line; required in sentinel mode."`
	UsernameFile    string        `yaml:"username_file" default:""`
	PasswordFile    string        `yaml:"password_file" default:"" secret:"file"`
	TLS             bool          `yaml:"tls" default:"true" doc:"Use valkey.tls for Sentinel connections too."`
	RefreshInterval time.Duration `yaml:"refresh_interval" default:"5s" validate:"min=1s" doc:"Periodic reconciliation with the Sentinels."`
}

// ValkeyTLS configures transport security towards Valkey and the Sentinels.
type ValkeyTLS struct {
	Mode       string `yaml:"mode" default:"verify-full" validate:"oneof=verify-full|verify-ca|disable"`
	CAFile     string `yaml:"ca_file" default:"" doc:"Empty means system roots plus the trusted CA bundle."`
	CertFile   string `yaml:"cert_file" default:""`
	KeyFile    string `yaml:"key_file" default:"" secret:"file"`
	ServerName string `yaml:"server_name" default:"" doc:"Empty means the host name of each address."`
}

// ValkeyReconnect is the backoff of the client's reconnects.
type ValkeyReconnect struct {
	InitialBackoff time.Duration `yaml:"initial_backoff" default:"100ms" validate:"min=1ms"`
	MaxBackoff     time.Duration `yaml:"max_backoff" default:"5s" validate:"min=1ms"`
}

// ValkeyBreaker is the circuit breaker around Valkey calls (R-ARCH-031).
type ValkeyBreaker struct {
	FailureThreshold int           `yaml:"failure_threshold" default:"5" validate:"min=1" doc:"Consecutive failed calls that open the circuit."`
	OpenDuration     time.Duration `yaml:"open_duration" default:"5s" validate:"min=100ms"`
}

// Validate checks the cross-field rules of the section.
func (v *Valkey) Validate(r *Report) {
	if !strings.HasSuffix(v.KeyPrefix, ":") {
		r.Add("key_prefix", "must end in \":\" (R-ARCH-030): the keyspace and the mechanism follow it")
	}
	if v.Mode == "sentinel" && !v.Sentinel.TLS && !v.AllowInsecure {
		r.Add("sentinel.tls", "false (plaintext Sentinel connections) requires valkey.allow_insecure: true")
	}
	if v.TLS.Mode == "disable" && !v.AllowInsecure {
		r.Add("tls.mode", "disable requires valkey.allow_insecure: true")
	}
	if (v.TLS.CertFile == "") != (v.TLS.KeyFile == "") {
		r.Add("tls", "cert_file and key_file must be set together")
	}
	if v.Reconnect.InitialBackoff > v.Reconnect.MaxBackoff {
		r.Add("reconnect.initial_backoff", "must not exceed reconnect.max_backoff")
	}
}

// ValidateFor requires the connection settings of the chosen mode where a
// replica connects to Valkey.
func (v *Valkey) ValidateFor(cmd Command, r *Report) {
	if !cmd.runsReplica() {
		return
	}
	switch v.Mode {
	case "sentinel":
		if len(v.Sentinel.Addresses) == 0 {
			r.Add("sentinel.addresses", "is required in sentinel mode")
		}
		if v.Sentinel.PrimaryName == "" {
			r.Add("sentinel.primary_name", "is required in sentinel mode")
		}
	case "standalone":
		if v.Address == "" {
			r.Add("address", "is required in standalone mode")
		}
	}
}
