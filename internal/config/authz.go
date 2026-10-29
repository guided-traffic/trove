package config

import "time"

// AuthzCache bounds the staleness of the compiled grant index (R-AUTHZ-028).
type AuthzCache struct {
	MaxStaleness time.Duration `yaml:"max_staleness" default:"10s" validate:"min=1s" doc:"No decision is made on a grant index that was not confirmed current for longer; a request waits for a confirmation, then is answered 503 authz_stale (Q-AUTHZ-13). At least three times cluster.epoch_poll_interval; start-up refuses a lower value."`
	SafetyTTL    time.Duration `yaml:"safety_ttl" default:"10m" validate:"min=10s" doc:"Full rebuild of the grant index at least this often, jittered by up to 10 percent; it bounds a write path that changed an input without notifying."`
}

// AuthzAudit selects which decisions are audited besides the refused writes and
// management operations (R-AUTHZ-029).
type AuthzAudit struct {
	Reads string `yaml:"reads" default:"denied" validate:"oneof=none|denied|all" doc:"Which reads are audited: none, denied (refused reads) or all (allowed reads too; the volume follows the downloads, Q-AUTHZ-5)."`
}

// AuthzAuditReads are the values of authz.audit.reads.
const (
	AuthzReadsNone   = "none"
	AuthzReadsDenied = "denied"
	AuthzReadsAll    = "all"
)

// MinAuthzStaleness is the factor between cluster.epoch_poll_interval and the
// smallest authz.cache.max_staleness: one slow poll must not stop decisions
// (R-AUTHZ-028).
const MinAuthzStaleness = 3

// ValidateAuthz reports an authz.cache.max_staleness below three poll intervals
// (R-AUTHZ-028). The composition root calls it at start-up and names the key.
func (c *Config) ValidateAuthz(r *Report) {
	if min := MinAuthzStaleness * c.Cluster.EpochPollInterval; c.Authz.Cache.MaxStaleness < min {
		r.Add("authz.cache.max_staleness", "%s is below %d times cluster.epoch_poll_interval (%s): a single slow change-log poll must not stop authorization decisions",
			c.Authz.Cache.MaxStaleness, MinAuthzStaleness, c.Cluster.EpochPollInterval)
	}
}
