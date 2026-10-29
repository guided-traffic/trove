package config

import (
	"net"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The login keys of the auth section (R-AUTH-003 to R-AUTH-014, WP1.1): the
// OpenID Connect providers, the login page, the UI sessions and the break-glass
// account. The fields of [Auth] that hold them are declared at the login marker
// of m1.go; the types live here.

// AuthOIDC is auth.oidc (R-AUTH-004).
type AuthOIDC struct {
	// Providers is keyed by the provider ID, the path segment of the redirect URI
	// (Q-AUTH-30). A map of structs is one key: in the environment it is JSON
	// (TROVE_AUTH_OIDC_PROVIDERS).
	Providers map[string]AuthOIDCProvider `yaml:"providers" default:"{}" doc:"Login providers by ID; see R-AUTH-004. Keys of an entry that are left out take the defaults of R-AUTH-004, R-AUTH-007 and R-AUTH-008."`
}

// AuthOIDCProvider is one login provider (R-AUTH-004, R-AUTH-007, R-AUTH-008).
// An element of a map is decoded as it is written, so a key left out is the zero
// value; [AuthOIDCProvider.Defaulted] fills in the defaults of the specification,
// and the pointer fields tell "false" from "not set".
type AuthOIDCProvider struct {
	// Order sorts the login buttons (R-AUTH-009); equal values sort by ID.
	Order       int    `yaml:"order"`
	DisplayName string `yaml:"display_name"`
	Issuer      string `yaml:"issuer"`
	ClientID    string `yaml:"client_id"`
	// ClientSecretFile is read again whenever its modification time changes
	// (R-AUTH-004); ClientSecretEnv names an environment variable read at start.
	ClientSecretFile   string            `yaml:"client_secret_file" secret:"file"`
	ClientSecretEnv    string            `yaml:"client_secret_env"`
	ClientAuthMethod   string            `yaml:"client_auth_method"`
	Scopes             []string          `yaml:"scopes"`
	ExtraAuthParams    map[string]string `yaml:"extra_auth_params"`
	AllowedSigningAlgs []string          `yaml:"allowed_signing_algs"`
	TrustedAudiences   []string          `yaml:"trusted_audiences"`
	ClockSkew          time.Duration     `yaml:"clock_skew"`
	MaxIDTokenAge      time.Duration     `yaml:"max_id_token_age"`
	UseUserInfo        *bool             `yaml:"use_user_info"`
	DiscoveryRefresh   time.Duration     `yaml:"discovery_refresh"`
	RPInitiatedLogout  *bool             `yaml:"rp_initiated_logout"`

	Claims               AuthOIDCClaims    `yaml:"claims"`
	UsernamePrefix       string            `yaml:"username_prefix"`
	SyncUsername         *bool             `yaml:"sync_username"`
	JIT                  *bool             `yaml:"jit"`
	RequireVerifiedEmail bool              `yaml:"require_verified_email"`
	LoginRequiresGroups  []string          `yaml:"login_requires_groups"`
	PublicIssuer         bool              `yaml:"public_issuer"`
	GroupSync            AuthOIDCGroupSync `yaml:"group_sync"`
}

// AuthOIDCClaims names the claims of the mapped attributes (R-AUTH-007). A name
// may be a dotted path into nested objects (realm_access.roles).
type AuthOIDCClaims struct {
	Username    string `yaml:"username"`
	Email       string `yaml:"email"`
	DisplayName string `yaml:"display_name"`
	Groups      string `yaml:"groups"`
}

// AuthOIDCGroupSync is the group synchronization of a provider (R-AUTH-008).
type AuthOIDCGroupSync struct {
	Enabled        *bool               `yaml:"enabled"`
	OnMissingClaim string              `yaml:"on_missing_claim"`
	CreateGroups   bool                `yaml:"create_groups"`
	Rules          []AuthOIDCGroupRule `yaml:"rules"`
}

// AuthOIDCGroupRule maps claim values to a trove group: exactly one of Match
// (exact, case-sensitive) and MatchRegex (RE2, anchored to the whole value;
// Group may use $1 or ${name}).
type AuthOIDCGroupRule struct {
	Match      string `yaml:"match"`
	MatchRegex string `yaml:"match_regex"`
	Group      string `yaml:"group"`
}

// Client authentication methods at the token endpoint (R-AUTH-004).
const (
	OIDCClientAuthBasic = "client_secret_basic"
	OIDCClientAuthPost  = "client_secret_post"
	OIDCClientAuthNone  = "none"
)

// Values of group_sync.on_missing_claim (Q-AUTH-4).
const (
	OnMissingClear = "clear"
	OnMissingKeep  = "keep"
	OnMissingDeny  = "deny"
)

// SupportedSigningAlgs are the ID-token algorithms the verifier implements
// (R-AUTH-006, Q-AUTH-31). none and HS* are never among them.
func SupportedSigningAlgs() []string {
	return []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384"}
}

// ReservedAuthParams are the authorization request parameters that
// extra_auth_params must not set (R-AUTH-004).
func ReservedAuthParams() []string {
	return []string{"response_type", "client_id", "redirect_uri", "scope", "state", "nonce", "code_challenge", "code_challenge_method"}
}

// KnownPublicIssuers are treated as public_issuer whatever the entry says
// (R-AUTH-007).
func KnownPublicIssuers() []string {
	return []string{"https://gitlab.com", "https://accounts.google.com"}
}

// ReservedUsernames are the names no user may carry besides the break-glass
// username (R-AUTH-007).
func ReservedUsernames() []string { return []string{"anonymous", "authenticated", "system", "me"} }

var (
	providerIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	usernamePattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._@+-]{0,127}$`)
	groupNamePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
)

// ValidProviderID reports whether id may name a provider (R-AUTH-004).
func ValidProviderID(id string) bool { return providerIDPattern.MatchString(id) }

// ValidUsername reports whether name may be a user name (R-AUTH-007): the
// pattern, and none of the reserved names.
func ValidUsername(name string) bool {
	return usernamePattern.MatchString(name) && !slices.Contains(ReservedUsernames(), name)
}

// ValidGroupName reports whether name may be a group name (R-AUTH-018).
func ValidGroupName(name string) bool { return groupNamePattern.MatchString(name) }

func boolPtr(b bool) *bool { return &b }

// sortedMapKeys returns the keys of m in order, so reports are stable.
func sortedMapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// Defaulted returns the entry with every key that was left out set to its
// default (R-AUTH-004, R-AUTH-007, R-AUTH-008).
func (p AuthOIDCProvider) Defaulted() AuthOIDCProvider {
	if p.ClientAuthMethod == "" {
		p.ClientAuthMethod = OIDCClientAuthBasic
	}
	if p.Scopes == nil {
		p.Scopes = []string{"openid", "profile", "email"}
	}
	if p.ExtraAuthParams == nil {
		p.ExtraAuthParams = map[string]string{}
	}
	if p.AllowedSigningAlgs == nil {
		p.AllowedSigningAlgs = []string{"RS256", "PS256", "ES256"}
	}
	if p.TrustedAudiences == nil {
		p.TrustedAudiences = []string{}
	}
	if p.ClockSkew == 0 {
		p.ClockSkew = 60 * time.Second
	}
	if p.MaxIDTokenAge == 0 {
		p.MaxIDTokenAge = 10 * time.Minute
	}
	if p.UseUserInfo == nil {
		p.UseUserInfo = boolPtr(true)
	}
	if p.DiscoveryRefresh == 0 {
		p.DiscoveryRefresh = 24 * time.Hour
	}
	if p.RPInitiatedLogout == nil {
		p.RPInitiatedLogout = boolPtr(true)
	}
	if p.Claims.Username == "" {
		p.Claims.Username = "preferred_username"
	}
	if p.Claims.Email == "" {
		p.Claims.Email = "email"
	}
	if p.Claims.DisplayName == "" {
		p.Claims.DisplayName = "name"
	}
	if p.Claims.Groups == "" {
		p.Claims.Groups = "groups"
	}
	if p.SyncUsername == nil {
		p.SyncUsername = boolPtr(true)
	}
	if p.JIT == nil {
		p.JIT = boolPtr(true)
	}
	if p.LoginRequiresGroups == nil {
		p.LoginRequiresGroups = []string{}
	}
	if p.GroupSync.Enabled == nil {
		p.GroupSync.Enabled = boolPtr(true)
	}
	if p.GroupSync.OnMissingClaim == "" {
		p.GroupSync.OnMissingClaim = OnMissingClear
	}
	if p.GroupSync.Rules == nil {
		p.GroupSync.Rules = []AuthOIDCGroupRule{}
	}
	if p.PublicIssuer || slices.Contains(KnownPublicIssuers(), strings.TrimSuffix(p.Issuer, "/")) {
		p.PublicIssuer = true
	}
	return p
}

// Validate checks every provider entry (R-AUTH-004, R-AUTH-007, R-AUTH-008).
// The rule that a provider needs server.base_url spans two sections and is
// checked where the login is built (oidc.CheckBaseURL).
func (o *AuthOIDC) Validate(r *Report) {
	for _, id := range sortedMapKeys(o.Providers) {
		key := "providers." + id
		if !ValidProviderID(id) {
			r.Add(key, "the provider ID must match ^[a-z0-9][a-z0-9-]{0,31}$ (it is part of the redirect URI)")
			continue
		}
		o.Providers[id].validate(r, key)
	}
}

func (p AuthOIDCProvider) validate(r *Report, key string) {
	d := p.Defaulted()
	if strings.TrimSpace(d.DisplayName) == "" {
		r.Add(key+".display_name", "is required")
	}
	if msg := checkIssuer(d.Issuer); msg != "" {
		r.Add(key+".issuer", "%s", msg)
	}
	if d.ClientID == "" {
		r.Add(key+".client_id", "is required")
	}
	p.validateClientAuth(r, key, d)
	if !slices.Contains(d.Scopes, "openid") {
		r.Add(key+".scopes", "must contain openid")
	}
	for _, k := range sortedMapKeys(d.ExtraAuthParams) {
		if slices.Contains(ReservedAuthParams(), k) {
			r.Add(key+".extra_auth_params", "must not set %q: trove sets it itself", k)
		}
	}
	if len(d.AllowedSigningAlgs) == 0 {
		r.Add(key+".allowed_signing_algs", "must name at least one algorithm")
	}
	for _, a := range d.AllowedSigningAlgs {
		if !slices.Contains(SupportedSigningAlgs(), a) {
			r.Add(key+".allowed_signing_algs", "%q is not supported (supported: %s; none and HS* are always refused)", a, strings.Join(SupportedSigningAlgs(), ", "))
		}
	}
	if d.ClockSkew < 0 || d.ClockSkew > 5*time.Minute {
		r.Add(key+".clock_skew", "must be between 0s and 5m")
	}
	if d.MaxIDTokenAge < time.Minute || d.MaxIDTokenAge > 24*time.Hour {
		r.Add(key+".max_id_token_age", "must be between 1m and 24h")
	}
	if d.DiscoveryRefresh < time.Minute {
		r.Add(key+".discovery_refresh", "must be at least 1m")
	}
	for name, v := range map[string]string{"username": d.Claims.Username, "email": d.Claims.Email, "display_name": d.Claims.DisplayName, "groups": d.Claims.Groups} {
		if strings.HasPrefix(v, ".") || strings.HasSuffix(v, ".") || strings.Contains(v, "..") {
			r.Add(key+".claims."+name, "%q is not a claim path", v)
		}
	}
	switch d.GroupSync.OnMissingClaim {
	case OnMissingClear, OnMissingKeep, OnMissingDeny:
	default:
		r.Add(key+".group_sync.on_missing_claim", "must be clear, keep or deny")
	}
	p.validateRules(r, key, d)
}

func (p AuthOIDCProvider) validateClientAuth(r *Report, key string, d AuthOIDCProvider) {
	switch d.ClientAuthMethod {
	case OIDCClientAuthBasic, OIDCClientAuthPost:
		switch {
		case d.ClientSecretFile == "" && d.ClientSecretEnv == "":
			r.Add(key+".client_secret_file", "or client_secret_env is required for client_auth_method %s", d.ClientAuthMethod)
		case d.ClientSecretFile != "" && d.ClientSecretEnv != "":
			r.Add(key+".client_secret_file", "and client_secret_env are both set; set one")
		}
	case OIDCClientAuthNone:
		if d.ClientSecretFile != "" || d.ClientSecretEnv != "" {
			r.Add(key+".client_auth_method", "is none, so no client secret may be configured")
		}
	default:
		r.Add(key+".client_auth_method", "must be client_secret_basic, client_secret_post or none")
	}
}

// validateRules checks the group rules and, for a public issuer, the gates of
// R-AUTH-007: no just-in-time users without login_requires_groups, and no rule
// that a self-registered group can satisfy.
func (p AuthOIDCProvider) validateRules(r *Report, key string, d AuthOIDCProvider) {
	for i, rule := range d.GroupSync.Rules {
		rk := key + ".group_sync.rules"
		switch {
		case (rule.Match == "") == (rule.MatchRegex == ""):
			r.Add(rk, "rule %d: set exactly one of match and match_regex", i)
		case rule.MatchRegex != "":
			re, err := regexp.Compile(`^(?:` + rule.MatchRegex + `)$`)
			if err != nil {
				r.Add(rk, "rule %d: match_regex: %v", i, err)
				break
			}
			if d.PublicIssuer && !namespacedPrefix(re) {
				r.Add(rk, "rule %d: provider %s has a public issuer, so match_regex %q must start with a literal namespace ending in / (for example acme/): anyone can create a group the pattern matches", i, strings.TrimPrefix(key, "providers."), rule.MatchRegex)
			}
		}
		if rule.Group == "" {
			r.Add(rk, "rule %d: group is required", i)
		}
	}
	if d.PublicIssuer && *d.JIT && len(d.LoginRequiresGroups) == 0 {
		r.Add(key+".login_requires_groups", "must not be empty: provider %s has a public issuer and jit is true, so every account of the issuer would become a trove user", strings.TrimPrefix(key, "providers."))
	}
}

// namespacedPrefix reports whether the literal prefix of an anchored pattern
// holds a non-empty namespace ending in "/" (acme/ in acme/trove-(.+)).
func namespacedPrefix(re *regexp.Regexp) bool {
	prefix, _ := re.LiteralPrefix()
	return strings.Index(prefix, "/") > 0
}

// checkIssuer returns why an issuer URL is not acceptable, or "": https, or
// http for a loopback host (tests), no query and no fragment (R-AUTH-004).
func checkIssuer(raw string) string {
	if raw == "" {
		return "is required"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "must be an absolute URL"
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "must not carry a query or a fragment"
	}
	if u.Scheme == "https" || (u.Scheme == "http" && LoopbackHost(u.Hostname())) {
		return ""
	}
	return "must be https (http only for a loopback host)"
}

// LoopbackHost reports whether host is localhost or a loopback address.
func LoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// AuthLoginPage is auth.login_page (R-AUTH-009).
type AuthLoginPage struct {
	AutoRedirect bool `yaml:"auto_redirect" default:"true" doc:"Redirect the login page to the provider when exactly one is configured."`
}

// AuthSessions is auth.sessions (R-AUTH-010, R-AUTH-011).
type AuthSessions struct {
	IdleTimeout      time.Duration `yaml:"idle_timeout" default:"1h" validate:"min=5m,max=24h" doc:"Ends a session this long after its last request (R-AUTH-010)."`
	AbsoluteTimeout  time.Duration `yaml:"absolute_timeout" default:"12h" validate:"min=5m,max=168h" doc:"Ends a session this long after its creation."`
	MaxPerUser       int           `yaml:"max_per_user" default:"20" validate:"min=1,max=1000" doc:"Concurrent sessions of one user; one more ends the oldest."`
	RecentAuthWindow time.Duration `yaml:"recent_auth_window" default:"1h" validate:"min=1m,max=24h" doc:"Credential-minting actions need an interactive authentication this recent (R-AUTH-011)."`
}

// Validate keeps the idle timeout within the absolute one.
func (s *AuthSessions) Validate(r *Report) {
	if s.IdleTimeout > s.AbsoluteTimeout {
		r.Add("idle_timeout", "(%s) must not exceed absolute_timeout (%s)", s.IdleTimeout, s.AbsoluteTimeout)
	}
}

// AuthBreakGlass is auth.break_glass (R-AUTH-014, R-AUTH-038).
type AuthBreakGlass struct {
	Enabled  bool   `yaml:"enabled" default:"true" doc:"The one local account; disable it once an OIDC administrator exists."`
	Username string `yaml:"username" default:"admin"`
	// PasswordFile holds an Argon2id PHC hash or a plaintext password; it is read
	// again when its modification time changes (Q-AUTH-14).
	PasswordFile    string        `yaml:"password_file" secret:"file" doc:"Argon2id PHC hash or plaintext password; web replicas only (R-AUTH-042)."`
	ShowOnLoginPage bool          `yaml:"show_on_login_page" default:"false"`
	IdleTimeout     time.Duration `yaml:"idle_timeout" default:"15m" validate:"min=1m,max=24h"`
	AbsoluteTimeout time.Duration `yaml:"absolute_timeout" default:"1h" validate:"min=1m,max=168h"`
	RotationHold    time.Duration `yaml:"rotation_hold" default:"2m" validate:"min=0s,max=1h" doc:"Local login is refused this long after a password rotation was noticed (Q-AUTH-14)."`
	// MaxConcurrentVerifications bounds the Argon2id verifications of a replica.
	MaxConcurrentVerifications int           `yaml:"max_concurrent_verifications" default:"2" validate:"min=1,max=64"`
	Lockout                    AuthLockout   `yaml:"lockout"`
	CheckInterval              time.Duration `yaml:"check_interval" default:"10s" validate:"min=1s,max=5m" doc:"How often a web replica looks at the password file for a rotation."`
}

// AuthLockout is auth.break_glass.lockout (R-AUTH-038).
type AuthLockout struct {
	Threshold int           `yaml:"threshold" default:"5" validate:"min=1,max=1000"`
	Window    time.Duration `yaml:"window" default:"15m" validate:"min=1m,max=24h"`
	Duration  time.Duration `yaml:"duration" default:"15m" validate:"min=1m,max=24h"`
}

// Validate checks the break-glass username and the timeouts (R-AUTH-007,
// R-AUTH-014).
func (b *AuthBreakGlass) Validate(r *Report) {
	if slices.Contains(ReservedUsernames(), b.Username) {
		r.Add("username", "%q is reserved (anonymous, authenticated, system and me)", b.Username)
	} else if !usernamePattern.MatchString(b.Username) {
		r.Add("username", "%q must match ^[a-z0-9][a-z0-9._@+-]{0,127}$", b.Username)
	}
	if b.IdleTimeout > b.AbsoluteTimeout {
		r.Add("idle_timeout", "(%s) must not exceed absolute_timeout (%s)", b.IdleTimeout, b.AbsoluteTimeout)
	}
}
