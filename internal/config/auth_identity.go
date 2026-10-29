package config

import (
	"net/netip"
	"time"
)

// The identity part of the auth section (R-AUTH-022 to R-AUTH-040): the
// credential caches and the revocation bound, token lifetimes, anonymous access,
// the client switches and the brute-force limits. The fields of Auth that use
// these structs are declared in m1.go inside the identity markers.

// AuthTokens configures token lifetimes, limits and retention (R-AUTH-023,
// R-AUTH-026, R-AUTH-032). The lifetimes are ceilings: the runtime settings of
// R-AUTH-040 may lower them, never raise them.
type AuthTokens struct {
	CacheTTL          time.Duration `yaml:"cache_ttl" default:"15s" validate:"min=1s,max=60s" doc:"SafetyTTL of the four auth caches; bounds staleness only if an invalidation were lost."`
	RetentionAfterEnd time.Duration `yaml:"retention_after_end" default:"2160h" validate:"min=1h" doc:"The token-cleanup task deletes a token row this long after expiry or revocation."`
	ExpiryNotice      time.Duration `yaml:"expiry_notice" default:"168h" validate:"min=1h" doc:"A token expiring within this period produces one token.expiring event; runtime-adjustable."`
	PAT               AuthTokenPAT  `yaml:"pat"`
	SAT               AuthTokenSAT  `yaml:"sat"`
	CIT               AuthTokenCIT  `yaml:"cit"`
	MaxActive         AuthTokenMax  `yaml:"max_active_per_principal"`
}

// AuthTokenPAT is the lifetime policy of personal access tokens (R-AUTH-026).
type AuthTokenPAT struct {
	DefaultTTL    time.Duration `yaml:"default_ttl" default:"2160h" validate:"min=1m"`
	MaxTTL        time.Duration `yaml:"max_ttl" default:"8760h" validate:"min=1m" doc:"Ceiling for the runtime setting."`
	AllowedGroups []string      `yaml:"allowed_groups" default:"[]" doc:"Empty: every user may create personal tokens; otherwise only members of these groups (R-AUTH-017)."`
}

// AuthTokenSAT is the lifetime policy of service-account tokens (R-AUTH-026).
type AuthTokenSAT struct {
	DefaultTTL       time.Duration `yaml:"default_ttl" default:"2160h" validate:"min=1m"`
	MaxTTL           time.Duration `yaml:"max_ttl" default:"8760h" validate:"min=1m" doc:"Ceiling for the runtime setting."`
	AllowNonExpiring bool          `yaml:"allow_non_expiring" default:"false" doc:"Lets an administrator create a service-account token without expiry."`
}

// AuthTokenCIT is the ceiling of CI tokens (R-AUTH-026).
type AuthTokenCIT struct {
	MaxTTL time.Duration `yaml:"max_ttl" default:"6h" validate:"min=1m,max=6h" doc:"Hard ceiling; the CI exchange sets the issued lifetime."`
}

// AuthTokenMax bounds the active tokens of one principal (R-AUTH-026).
type AuthTokenMax struct {
	PAT int `yaml:"pat" default:"50" validate:"min=1"`
	SAT int `yaml:"sat" default:"20" validate:"min=1"`
}

// AuthAnonymous is the operator's ceiling for anonymous access (R-AUTH-037).
type AuthAnonymous struct {
	Allowed bool `yaml:"allowed" default:"true" doc:"The operator's ceiling. The runtime switch anonymousAccess (R-AUTH-040) takes effect only while this is true."`
}

// AuthClients switches client authentication forms (R-AUTH-033).
type AuthClients struct {
	Basic             AuthClientBasic `yaml:"basic"`
	NuGetAPIKeyHeader bool            `yaml:"nuget_api_key_header" default:"true" doc:"Examine X-NuGet-ApiKey on the NuGet API-key endpoints (M2)."`
}

// AuthClientBasic switches Basic presentation of a token.
type AuthClientBasic struct {
	Enabled bool `yaml:"enabled" default:"true" doc:"Off breaks NuGet restore and _auth users; Basic with a password is impossible regardless."`
}

// AuthRateLimit holds the brute-force limits of R-AUTH-038.
type AuthRateLimit struct {
	FailuresPerAddress int           `yaml:"failures_per_address" default:"300" validate:"min=1" doc:"Rejected credentials per minute and client address, cluster-wide; a per-replica share while Valkey is unavailable."`
	StartsPerAddress   int           `yaml:"starts_per_address" default:"30" validate:"min=1" doc:"Requests to the unauthenticated row-creating endpoints per minute and address, cluster-wide."`
	DeviceCodeFailures int           `yaml:"device_code_failures" default:"5" validate:"min=1" doc:"Wrong device user codes per user within auth.cli_login.ttl, cluster-wide."`
	RecentTokenWindow  time.Duration `yaml:"recent_token_window" default:"10m" validate:"min=1s,max=1h" doc:"Throttle exemption of a credential validated on any replica."`
	MaxPendingFlows    int           `yaml:"max_pending_flows" default:"100000" validate:"min=1"`
	ExemptCIDRs        []string      `yaml:"exempt_cidrs" default:"[]" doc:"Client addresses or CIDRs that are never throttled."`
}

// Validate checks the lifetime pairs (R-AUTH-026).
func (t *AuthTokens) Validate(r *Report) {
	if t.PAT.DefaultTTL > t.PAT.MaxTTL {
		r.Add("pat.default_ttl", "(%s) must not exceed pat.max_ttl (%s)", t.PAT.DefaultTTL, t.PAT.MaxTTL)
	}
	if t.SAT.DefaultTTL > t.SAT.MaxTTL {
		r.Add("sat.default_ttl", "(%s) must not exceed sat.max_ttl (%s)", t.SAT.DefaultTTL, t.SAT.MaxTTL)
	}
}

// Validate checks that every exempt entry is an address or a CIDR (R-AUTH-038).
func (r *AuthRateLimit) Validate(rep *Report) {
	for _, e := range r.ExemptCIDRs {
		if _, err := netip.ParsePrefix(e); err == nil {
			continue
		}
		if _, err := netip.ParseAddr(e); err != nil {
			rep.Add("exempt_cidrs", "%q is neither an address nor a CIDR", e)
		}
	}
}

// ExemptPrefixes returns the parsed exempt_cidrs; a single address is a /32 or /128.
func (r *AuthRateLimit) ExemptPrefixes() []netip.Prefix {
	var out []netip.Prefix
	for _, e := range r.ExemptCIDRs {
		if p, err := netip.ParsePrefix(e); err == nil {
			out = append(out, p.Masked())
		} else if a, err := netip.ParseAddr(e); err == nil {
			out = append(out, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
		}
	}
	return out
}

// validateAuthIdentity checks the rule that spans sections: the revocation
// bound against the change-log poll (R-AUTH-023). Config.Validate calls it.
func (c *Config) validateAuthIdentity(r *Report) {
	b, poll := c.Auth.RevocationBound, c.Cluster.EpochPollInterval
	if b < 2*poll || b > 10*time.Second {
		r.Add("auth.revocation_bound", "(%s) must be at least 2 x cluster.epoch_poll_interval (%s) and at most 10s (R-AUTH-023)", b, poll)
	}
}
