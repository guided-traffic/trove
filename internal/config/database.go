package config

import "time"

// Database configures the PostgreSQL connection classes (R-HA-009 to R-HA-016,
// R-HA-058). The credential is file-only: the files are read again on every new
// connection, so a rotated Secret applies without a restart (R-HA-017).
type Database struct {
	Host               string                  `yaml:"host" validate:"required" doc:"Host name; a comma-separated list is allowed (R-HA-009)."`
	Port               int                     `yaml:"port" default:"5432" validate:"min=1,max=65535"`
	SessionHost        string                  `yaml:"session_host" default:"" doc:"Direct host for session-scoped connections; empty means host."`
	SessionPort        int                     `yaml:"session_port" default:"0" validate:"min=0,max=65535" doc:"0 means port."`
	ControlHost        string                  `yaml:"control_host" default:"" doc:"Control pooler host; empty means host."`
	ControlPort        int                     `yaml:"control_port" default:"0" validate:"min=0,max=65535" doc:"0 means port."`
	ControlName        string                  `yaml:"control_name" default:"" doc:"Database entry of the control pooler; empty means name."`
	Name               string                  `yaml:"name" validate:"required"`
	Schema             string                  `yaml:"schema" default:"trove"`
	BackupRole         string                  `yaml:"backup_role" default:""`
	UsernameFile       string                  `yaml:"username_file" default:"/var/run/secrets/trove/database/username" secret:"file"`
	PasswordFile       string                  `yaml:"password_file" default:"/var/run/secrets/trove/database/password" secret:"file"`
	SSLMode            string                  `yaml:"ssl_mode" default:"verify-full" validate:"oneof=verify-full|verify-ca|require|disable|allow|prefer"`
	SSLRootCertFile    string                  `yaml:"ssl_root_cert_file" default:""`
	SSLCertFile        string                  `yaml:"ssl_cert_file" default:""`
	SSLKeyFile         string                  `yaml:"ssl_key_file" default:"" secret:"file"`
	AllowInsecure      bool                    `yaml:"allow_insecure" default:"false" doc:"Permits ssl_mode disable, allow and prefer."`
	ConnectTimeout     time.Duration           `yaml:"connect_timeout" default:"5s" validate:"min=100ms"`
	TargetSessionAttrs string                  `yaml:"target_session_attrs" default:"read-write" validate:"oneof=read-write|primary|any"`
	Pooler             DatabasePooler          `yaml:"pooler"`
	Pool               DatabasePool            `yaml:"pool"`
	LeasePoolSize      int                     `yaml:"lease_pool_size" default:"4" validate:"min=1" doc:"Concurrent singleton leases per replica."`
	LeaseCheckInterval time.Duration           `yaml:"lease_check_interval" default:"5s" validate:"min=1s"`
	Session            DatabaseSession         `yaml:"session"`
	Retry              DatabaseRetry           `yaml:"retry"`
	Keepalive          DatabaseKeepalive       `yaml:"keepalive"`
	ServerKeepalive    DatabaseServerKeepalive `yaml:"server_keepalive" doc:"Sent as tcp_keepalives_* startup parameters (R-HA-015)."`
	Preflight          DatabasePreflight       `yaml:"preflight"`
}

// DatabasePooler describes an external transaction-mode pooler (R-HA-058).
type DatabasePooler struct {
	Mode              string `yaml:"mode" default:"none" validate:"oneof=none|session|transaction"`
	ServerConnections int    `yaml:"server_connections" default:"0" validate:"min=0"`
}

// DatabasePool sizes the transaction-scoped main pool (R-HA-016).
type DatabasePool struct {
	MaxConns              int           `yaml:"max_conns" default:"20" validate:"min=2" doc:"At least 2: one is the control connection."`
	MinConns              int           `yaml:"min_conns" default:"2" validate:"min=0"`
	MaxConnLifetime       time.Duration `yaml:"max_conn_lifetime" default:"30m" validate:"min=1s"`
	MaxConnLifetimeJitter time.Duration `yaml:"max_conn_lifetime_jitter" default:"5m" validate:"min=0s"`
	MaxConnIdleTime       time.Duration `yaml:"max_conn_idle_time" default:"5m" validate:"min=1s"`
	HealthCheckPeriod     time.Duration `yaml:"health_check_period" default:"30s" validate:"min=1s"`
}

// DatabaseSession are the session parameters every connection sets.
type DatabaseSession struct {
	StatementTimeout                time.Duration `yaml:"statement_timeout" default:"30s"`
	LockTimeout                     time.Duration `yaml:"lock_timeout" default:"5s"`
	IdleInTransactionSessionTimeout time.Duration `yaml:"idle_in_transaction_session_timeout" default:"60s"`
}

// DatabaseRetry bounds the retry of an operation that hit a failover (R-HA-015).
type DatabaseRetry struct {
	InitialBackoff time.Duration `yaml:"initial_backoff" default:"100ms" validate:"min=1ms"`
	MaxBackoff     time.Duration `yaml:"max_backoff" default:"2s" validate:"min=1ms"`
	MaxElapsed     time.Duration `yaml:"max_elapsed" default:"30s" validate:"min=1s" doc:"Request deadlines may cut it shorter."`
}

// DatabaseKeepalive are the client-side TCP keepalive settings.
type DatabaseKeepalive struct {
	Idle        time.Duration `yaml:"idle" default:"15s"`
	Interval    time.Duration `yaml:"interval" default:"5s"`
	Count       int           `yaml:"count" default:"3" validate:"min=1"`
	UserTimeout time.Duration `yaml:"user_timeout" default:"30s" doc:"Linux TCP_USER_TIMEOUT."`
}

// DatabaseServerKeepalive are the server-side keepalive startup parameters.
type DatabaseServerKeepalive struct {
	Idle     time.Duration `yaml:"idle" default:"60s"`
	Interval time.Duration `yaml:"interval" default:"10s"`
	Count    int           `yaml:"count" default:"6" validate:"min=1"`
}

// DatabasePreflight is the connection budget check of the migration Job (R-HA-016).
type DatabasePreflight struct {
	ConnectionBudget       string `yaml:"connection_budget" default:"enforce" validate:"oneof=enforce|warn"`
	ReservedConnections    int    `yaml:"reserved_connections" default:"10" validate:"min=0"`
	AllowSuperuser         bool   `yaml:"allow_superuser" default:"false"`
	ExpectedWebReplicas    int    `yaml:"expected_web_replicas" default:"0" validate:"min=0" doc:"Set by the chart on the migration Job."`
	ExpectedWorkerReplicas int    `yaml:"expected_worker_replicas" default:"0" validate:"min=0" doc:"Set by the chart on the migration Job."`
}

// Validate checks the cross-field rules of the section.
func (d *Database) Validate(r *Report) {
	switch d.SSLMode {
	case "disable", "allow", "prefer":
		if !d.AllowInsecure {
			r.Add("ssl_mode", "%s requires database.allow_insecure: true", d.SSLMode)
		}
	}
	if d.Pool.MinConns > d.Pool.MaxConns {
		r.Add("pool.min_conns", "must not exceed pool.max_conns")
	}
	if d.Pooler.Mode == "transaction" {
		if d.SessionHost == "" {
			r.Add("session_host", "is required with pooler.mode transaction")
		}
		if d.ControlHost == "" {
			r.Add("control_host", "is required with pooler.mode transaction")
		}
	}
	if d.Retry.InitialBackoff > d.Retry.MaxBackoff {
		r.Add("retry.initial_backoff", "must not exceed retry.max_backoff")
	}
}
