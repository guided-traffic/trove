package config

import (
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"time"
)

// CI is the ci section (R-CI-001, R-CI-003, R-CI-007, R-CI-009, R-CI-016, R-CI-018, R-CI-020,
// R-CI-021, R-CI-022): the GitLab CI OIDC federation's limits, lifetimes, retention and the issuers
// the configuration declares. The defaults are the specification's (proposed). The behaviour is
// docs/developer/ci-oidc.md's.
type CI struct {
	SigningKeys CISigningKeys `yaml:"signing_keys"`
	Exchange    CIExchange    `yaml:"exchange"`
	Mappings    CIMappings    `yaml:"mappings"`
	Token       CIToken       `yaml:"token"`
	Replay      CIReplay      `yaml:"replay"`
	Issuances   CIIssuances   `yaml:"issuances"`
	// JobTokenFallback is the P2 fallback of R-CI-022: the key exists so that a configuration that
	// enables it is refused by name, not read as an unknown key.
	JobTokenFallback CIJobTokenFallback `yaml:"job_token_fallback"`

	Issuers []CIIssuer `yaml:"issuers" default:"[]" doc:"CI issuers declared here (R-CI-001, Q-CI-16): trove migrate applies them by name with source configuration, in one transaction, and disables the configuration issuers no longer listed; the API and the UI show them read-only. Each entry carries the fields of the issuer API in snake_case with the same validation and defaults; name, kind and issuer are required."`
}

// CISigningKeys are the discovery and key-set settings of R-CI-003.
type CISigningKeys struct {
	DefaultRefreshInterval    time.Duration `yaml:"default_refresh_interval" default:"1h" validate:"min=1m,max=168h" doc:"Next refresh of a key set whose response carries no Cache-Control max-age."`
	MinRefreshInterval        time.Duration `yaml:"min_refresh_interval" default:"5m" validate:"min=10s,max=24h" doc:"Lower clamp of the refresh interval; also the retry delay after a failed fetch."`
	MaxRefreshInterval        time.Duration `yaml:"max_refresh_interval" default:"24h" validate:"min=1m,max=168h" doc:"Upper clamp of the refresh interval."`
	UnknownKIDRefreshInterval time.Duration `yaml:"unknown_kid_refresh_interval" default:"60s" validate:"min=1s,max=1h" doc:"At most one on-demand fetch per issuer and replica for a token whose kid is unknown (R-CI-003)."`
	MaxStale                  time.Duration `yaml:"max_stale" default:"24h" validate:"min=1m,max=720h" doc:"Age of the stored key set after which exchanges answer 503 signing_keys_unavailable."`
	FetchTimeout              time.Duration `yaml:"fetch_timeout" default:"5s" validate:"min=1s,max=60s" doc:"Timeout of one discovery or key request."`
	MaxResponseBytes          int           `yaml:"max_response_bytes" default:"1048576" validate:"min=4096,max=16777216" doc:"Largest discovery or key response read."`
	CacheSafetyTTL            time.Duration `yaml:"cache_safety_ttl" default:"10m" validate:"min=10s,max=24h" doc:"SafetyTTL of the in-process copy of the key sets (R-ARCH-018)."`
}

// CIExchange configures the token exchange (R-CI-007, R-CI-008, R-CI-021).
type CIExchange struct {
	Enabled              bool        `yaml:"enabled" default:"true" doc:"false answers every exchange with 404 and registers no limiter state."`
	ExposeReason         bool        `yaml:"expose_reason" default:"true" doc:"false leaves the reason member out of the 400 answers; logs and audit keep it (R-CI-008)."`
	MaxBodyBytes         int         `yaml:"max_body_bytes" default:"65536" validate:"min=1024,max=1048576" doc:"Largest request body of the exchange."`
	MaxSubjectTokenBytes int         `yaml:"max_subject_token_bytes" default:"16384" validate:"min=512,max=65536" doc:"Largest subject token (R-CI-004 step 1)."`
	RateLimit            CIRateLimit `yaml:"rate_limit"`
}

// CIRateLimit holds the two cluster-wide limiters of the exchange (R-CI-021, Q-CI-14).
type CIRateLimit struct {
	PerClientIPPerMinute int      `yaml:"per_client_ip_per_minute" default:"600" validate:"min=1" doc:"Limiter ci_exchange_address: requests per client address and minute, cluster-wide."`
	PerClientIPBurst     int      `yaml:"per_client_ip_burst" default:"100" validate:"min=1" doc:"Burst of the per-address limiter."`
	PerSubjectPerMinute  int      `yaml:"per_subject_per_minute" default:"300" validate:"min=1" doc:"Limiter ci_exchange_subject: exchanges per CI issuer and subject (the first pin claim, usually project_id) and minute, cluster-wide."`
	PerSubjectBurst      int      `yaml:"per_subject_burst" default:"100" validate:"min=1" doc:"Burst of the per-subject limiter."`
	ExemptCIDRs          []string `yaml:"exempt_cidrs" default:"[]" doc:"Client addresses or CIDRs that skip the per-address limiter, for example a runner network behind NAT. They stay bounded by server.max_in_flight and the per-subject limiter."`
}

// CIMappings bounds the identity mappings (R-CI-009).
type CIMappings struct {
	MaxPerIssuer   int           `yaml:"max_per_issuer" default:"1000" validate:"min=1,max=100000" doc:"Most mappings of one CI issuer."`
	MaxPerProject  int           `yaml:"max_per_project" default:"100" validate:"min=1,max=100000" doc:"Most mappings of one project's tier on one CI issuer, at most max_per_issuer: no project fills the issuer's limit alone (Q-CI-49)."`
	CacheSafetyTTL time.Duration `yaml:"cache_safety_ttl" default:"10m" validate:"min=10s,max=24h" doc:"SafetyTTL of the in-process copy of the issuers and their mappings (R-ARCH-018)."`
}

// CIToken sets the lifetime of CI tokens (R-CI-016).
type CIToken struct {
	DefaultTTL time.Duration `yaml:"default_ttl" default:"15m" validate:"min=30s,max=6h" doc:"Lifetime of a CI token when the request names none."`
	MaxTTL     time.Duration `yaml:"max_ttl" default:"1h" validate:"min=30s,max=6h" doc:"Upper bound of every CI token; at most auth.tokens.cit.max_ttl (R-CI-016)."`
	MinTTL     time.Duration `yaml:"min_ttl" default:"30s" validate:"min=1s,max=1h" doc:"Shortest lifetime worth issuing; a subject token with less left answers subject_token_expires_too_soon."`
}

// CIReplay configures the replay set of subject tokens (R-CI-018).
type CIReplay struct {
	RetentionAfterExpiry time.Duration `yaml:"retention_after_expiry" default:"15m" validate:"min=1m,max=168h" doc:"How long a recorded jti is kept after the subject token's exp plus the issuer's clock skew; at least 1m, so a replica whose clock lags the database's by less than that never accepts a single-use subject token whose row ci-replay-cleanup deleted (Q-CI-51)."`
	WarnAfterExchanges   int           `yaml:"warn_after_exchanges" default:"10" validate:"min=1" doc:"From this exchange count of one subject token on, every further exchange of an issuer without single use records ci.exchange.repeated."`
}

// CIIssuances configures the retention of the issuance records (R-CI-020).
type CIIssuances struct {
	Retention time.Duration `yaml:"retention" default:"8760h" validate:"min=24h,max=87600h" doc:"Issuance records whose token expired longer ago than this are deleted by the task ci-issuance-retention. Go durations have no day unit."`
}

// CIJobTokenFallback is the key of R-CI-022 (P2).
type CIJobTokenFallback struct {
	Enabled bool `yaml:"enabled" default:"false" doc:"The CI_JOB_TOKEN fallback is not built (R-CI-022, Q-CI-5); true stops the start."`
}

// CIIssuer is one entry of ci.issuers (R-CI-001, Q-CI-16): the issuer fields of the API in snake_case.
// An element of a list is decoded as it is written, so a key left out is the zero value and the
// pointer fields tell "false" and "0" from "not set"; the defaults of R-CI-001 are applied by trove
// migrate when it stores the issuer.
type CIIssuer struct {
	Name                   string   `yaml:"name" json:"name"`
	Kind                   string   `yaml:"kind" json:"kind"`
	Issuer                 string   `yaml:"issuer" json:"issuer"`
	Description            string   `yaml:"description" json:"description,omitempty"`
	Enabled                *bool    `yaml:"enabled" json:"enabled,omitempty"`
	DiscoveryURL           *string  `yaml:"discovery_url" json:"discovery_url,omitempty"`
	JWKSURI                *string  `yaml:"jwks_uri" json:"jwks_uri,omitempty"`
	AllowedAudiences       []string `yaml:"allowed_audiences" json:"allowed_audiences,omitempty"`
	ExclusiveAudience      *bool    `yaml:"exclusive_audience" json:"exclusive_audience,omitempty"`
	AllowedAlgorithms      []string `yaml:"allowed_algorithms" json:"allowed_algorithms,omitempty"`
	AllowedTyp             []string `yaml:"allowed_typ" json:"allowed_typ,omitempty"`
	AllowMissingTyp        *bool    `yaml:"allow_missing_typ" json:"allow_missing_typ,omitempty"`
	ClockSkewSeconds       *int     `yaml:"clock_skew_seconds" json:"clock_skew_seconds,omitempty"`
	RequiredClaims         []string `yaml:"required_claims" json:"required_claims,omitempty"`
	PublicIssuer           *bool    `yaml:"public_issuer" json:"public_issuer,omitempty"`
	PinClaims              []string `yaml:"pin_claims" json:"pin_claims,omitempty"`
	SingleUseSubjectTokens *bool    `yaml:"single_use_subject_tokens" json:"single_use_subject_tokens,omitempty"`
	MaxTTLSeconds          *int64   `yaml:"max_ttl_seconds" json:"max_ttl_seconds,omitempty"`
	CABundleRef            *string  `yaml:"ca_bundle_ref" json:"ca_bundle_ref,omitempty"`
	UseOutboundProxy       *bool    `yaml:"use_outbound_proxy" json:"use_outbound_proxy,omitempty"`
}

var ciNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,63}$`)

// CI issuer kinds a configuration entry may declare; github and generic are P1 (R-CI-025, R-CI-026).
const ciKindGitLab = "gitlab"

// validateCI checks the ci section: the lifetime ceiling against auth.tokens.cit.max_ttl (R-CI-016),
// the refresh bounds, the exempt networks, the retired key and the declared issuers.
func (c *Config) validateCI(r *Report) {
	ci := c.CI
	if ci.Token.MaxTTL > c.Auth.Tokens.CIT.MaxTTL {
		r.Add("ci.token.max_ttl", "%s exceeds auth.tokens.cit.max_ttl (%s), the one ceiling of every CI token (R-CI-016); lower ci.token.max_ttl or raise auth.tokens.cit.max_ttl",
			ci.Token.MaxTTL, c.Auth.Tokens.CIT.MaxTTL)
	}
	if ci.Token.DefaultTTL > ci.Token.MaxTTL {
		r.Add("ci.token.default_ttl", "%s exceeds ci.token.max_ttl (%s)", ci.Token.DefaultTTL, ci.Token.MaxTTL)
	}
	if ci.Token.MinTTL > ci.Token.DefaultTTL {
		r.Add("ci.token.min_ttl", "%s exceeds ci.token.default_ttl (%s): a default exchange would always be refused", ci.Token.MinTTL, ci.Token.DefaultTTL)
	}
	sk := ci.SigningKeys
	if sk.MinRefreshInterval > sk.MaxRefreshInterval {
		r.Add("ci.signing_keys.min_refresh_interval", "%s exceeds ci.signing_keys.max_refresh_interval (%s)", sk.MinRefreshInterval, sk.MaxRefreshInterval)
	}
	if sk.DefaultRefreshInterval < sk.MinRefreshInterval || sk.DefaultRefreshInterval > sk.MaxRefreshInterval {
		r.Add("ci.signing_keys.default_refresh_interval", "%s lies outside [ci.signing_keys.min_refresh_interval, ci.signing_keys.max_refresh_interval] = [%s, %s]",
			sk.DefaultRefreshInterval, sk.MinRefreshInterval, sk.MaxRefreshInterval)
	}
	if sk.MaxStale < sk.MaxRefreshInterval {
		r.Add("ci.signing_keys.max_stale", "%s is shorter than ci.signing_keys.max_refresh_interval (%s): a key set would fail closed before its next scheduled refresh", sk.MaxStale, sk.MaxRefreshInterval)
	}
	if ci.Mappings.MaxPerProject > ci.Mappings.MaxPerIssuer {
		r.Add("ci.mappings.max_per_project", "(%d) must not exceed ci.mappings.max_per_issuer (%d)", ci.Mappings.MaxPerProject, ci.Mappings.MaxPerIssuer)
	}
	if ci.Exchange.MaxSubjectTokenBytes > ci.Exchange.MaxBodyBytes {
		r.Add("ci.exchange.max_subject_token_bytes", "(%d) must not exceed ci.exchange.max_body_bytes (%d)", ci.Exchange.MaxSubjectTokenBytes, ci.Exchange.MaxBodyBytes)
	}
	for i, cidr := range ci.Exchange.RateLimit.ExemptCIDRs {
		if _, err := netip.ParsePrefix(cidr); err == nil {
			continue
		}
		if _, err := netip.ParseAddr(cidr); err != nil {
			r.Add(fmt.Sprintf("ci.exchange.rate_limit.exempt_cidrs[%d]", i), "%q is neither an address nor a CIDR", cidr)
		}
	}
	if ci.JobTokenFallback.Enabled {
		r.Add("ci.job_token_fallback.enabled", "the CI_JOB_TOKEN fallback is not built in this release (R-CI-022, Q-CI-5); remove the key or set it to false")
	}
	ci.validateIssuers(r)
}

func (c CI) validateIssuers(r *Report) {
	names, urls := map[string]int{}, map[string]int{}
	for i, is := range c.Issuers {
		key := fmt.Sprintf("ci.issuers[%d]", i)
		if !ciNamePattern.MatchString(is.Name) {
			r.Add(key+".name", "%q must match %s", is.Name, ciNamePattern)
		} else if j, dup := names[is.Name]; dup {
			r.Add(key+".name", "%q is also the name of ci.issuers[%d]", is.Name, j)
		}
		names[is.Name] = i
		switch is.Kind {
		case ciKindGitLab:
		case "github", "generic":
			r.Add(key+".kind", "%q is not available in this release (R-CI-025, R-CI-026); the kind is gitlab", is.Kind)
		default:
			r.Add(key+".kind", "%q is not a CI issuer kind; the kind is gitlab", is.Kind)
		}
		if !strings.HasPrefix(is.Issuer, "https://") {
			r.Add(key+".issuer", "%q must be an https URL (R-CI-001)", is.Issuer)
		} else if j, dup := urls[is.Issuer]; dup {
			r.Add(key+".issuer", "%q is also the issuer of ci.issuers[%d]", is.Issuer, j)
		}
		urls[is.Issuer] = i
		for _, alg := range is.AllowedAlgorithms {
			if !slices.Contains(CIAlgorithms(), alg) {
				r.Add(key+".allowed_algorithms", "%q is not an accepted algorithm (%s); none and the HMAC algorithms never are (RFC 8725)", alg, strings.Join(CIAlgorithms(), ", "))
			}
		}
		if is.ClockSkewSeconds != nil && (*is.ClockSkewSeconds < 0 || *is.ClockSkewSeconds > 300) {
			r.Add(key+".clock_skew_seconds", "(%d) must be between 0 and 300", *is.ClockSkewSeconds)
		}
		if is.AllowedAudiences != nil && len(is.AllowedAudiences) == 0 {
			r.Add(key+".allowed_audiences", "needs at least one entry, or leave it out for the default audiences")
		}
		if is.MaxTTLSeconds != nil {
			if d := time.Duration(*is.MaxTTLSeconds) * time.Second; d < max(c.Token.MinTTL, time.Second) || d > c.Token.MaxTTL {
				r.Add(key+".max_ttl_seconds", "(%d) must be between ci.token.min_ttl (%s) and ci.token.max_ttl (%s)", *is.MaxTTLSeconds, c.Token.MinTTL, c.Token.MaxTTL)
			}
		}
	}
}

// CIAlgorithms are the JWS algorithms an issuer may accept (R-CI-001, RFC 8725).
func CIAlgorithms() []string {
	return []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "EdDSA"}
}

// retiredKeys are keys of earlier designs: a configuration that sets one is refused with the name
// of its replacement instead of "unknown key" (R-CI-021).
func retiredKeys() map[string]string {
	return map[string]string{
		"ci.exchange.rate_limit.per_issuer_per_second": "the per-issuer bucket of the per-replica design no longer exists (R-CI-021, Q-CI-14); use ci.exchange.rate_limit.per_client_ip_per_minute and ci.exchange.rate_limit.per_subject_per_minute, which count cluster-wide",
	}
}

// retiredEnv finds the retired key an environment variable name belongs to.
func retiredEnv(name string) (path, why string, ok bool) {
	for p, w := range retiredKeys() {
		if envName(p) == name {
			return p, w, true
		}
	}
	return "", "", false
}
