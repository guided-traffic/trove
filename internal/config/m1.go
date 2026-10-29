package config

import (
	"strings"
	"time"
)

// The sections of the M1 areas (docs/developer/m1-contracts.md). Each struct is
// empty until its owner fills it inside its own markers; the field of the
// section in [Config] exists from the start so that parallel work only adds
// fields here and never edits the aggregate. Add the doc, default and validate
// tags like the sections around them, and run `make generate` for schema.json.

// Auth is the auth section (R-AUTH-004 to R-AUTH-040): providers, sessions,
// tokens, break-glass, rate limits. WP1.2 (identity) and WP1.1 (login) both add
// fields; each only inside its markers.
type Auth struct {
	// >>> M1 identity

	// RevocationBound is how long a replica may answer from its auth caches
	// after its last change-log poll started (R-AUTH-023, Q-AUTH-10).
	RevocationBound time.Duration `yaml:"revocation_bound" default:"2s" validate:"min=500ms,max=10s" doc:"Revocation bound; at least 2 x cluster.epoch_poll_interval."`
	Tokens          AuthTokens    `yaml:"tokens"`
	Anonymous       AuthAnonymous `yaml:"anonymous"`
	Clients         AuthClients   `yaml:"clients"`
	RateLimit       AuthRateLimit `yaml:"rate_limit"`

	// <<< M1 identity

	// >>> M1 login

	// <<< M1 login
}

// Authz is the authz section (R-AUTHZ-017, R-AUTHZ-028, R-AUTHZ-029): hiding,
// cache bounds, audit of reads. WP1.3 owns it.
type Authz struct {
	// >>> M1 authz

	HideExistence bool       `yaml:"hide_existence" default:"true" doc:"A refused read answers 404, indistinguishable from a missing resource (R-AUTHZ-016, R-AUTHZ-017); false answers 403 for a resource that exists, for troubleshooting only. A repository's hideExistence overrides it."`
	Cache         AuthzCache `yaml:"cache"`
	Audit         AuthzAudit `yaml:"audit"`

	// <<< M1 authz
}

// API is the api section (R-API-016 to R-API-022): body limits, page size,
// idempotency retention, CORS, rate limits. WP1.6 owns it.
type API struct {
	// >>> M1 api
	PageSizeDefault           int           `yaml:"page_size_default" default:"50" validate:"min=1" doc:"limit of a list request that sends none (R-API-010)."`
	PageSizeMax               int           `yaml:"page_size_max" default:"500" validate:"min=1" doc:"Largest limit; a larger one is clamped to it (R-API-010)."`
	IdempotencyRetention      time.Duration `yaml:"idempotency_retention" default:"24h" validate:"min=1m" doc:"How long the outcome of an Idempotency-Key is replayed (R-API-014)."`
	MaxRequestBodyBytes       ByteSize      `yaml:"max_request_body_bytes" default:"1MiB" validate:"min=1KiB" doc:"Largest JSON request body of /api/v1/; larger answers 413 (R-API-017)."`
	MaxConfigurationBodyBytes ByteSize      `yaml:"max_configuration_body_bytes" default:"4MiB" validate:"min=1KiB" doc:"Largest YAML configuration body (R-API-017)."`
	CORS                      APICORS       `yaml:"cors"`
	RateLimits                APIRateLimits `yaml:"rate_limits"`
	// <<< M1 api
}

// Validate checks the cross-field rules of the section.
func (a *API) Validate(r *Report) {
	if a.PageSizeDefault > a.PageSizeMax {
		r.Add("page_size_default", "must not exceed page_size_max (is %d, page_size_max is %d)", a.PageSizeDefault, a.PageSizeMax)
	}
	for i, o := range a.CORS.AllowedOrigins {
		if !validOrigin(o) {
			r.Add("cors.allowed_origins", "entry %d (%q) must be an exact origin: scheme://host[:port], no path, no wildcard", i, o)
		}
	}
}

// validOrigin reports whether s is an exact web origin without wildcard.
func validOrigin(s string) bool {
	scheme, rest, ok := strings.Cut(s, "://")
	if !ok || (scheme != "http" && scheme != "https") || rest == "" {
		return false
	}
	return !strings.ContainsAny(rest, "*/?#@ ")
}

// APICORS enables cross-origin access for requests that carry an Authorization
// header (R-API-018). Credentials are never allowed cross-origin.
type APICORS struct {
	AllowedOrigins []string `yaml:"allowed_origins" default:"[]" doc:"Exact origins (scheme://host[:port]), no wildcards. Empty: CORS is off and no Access-Control-Allow-* header is sent."`
}

// APIRateLimits are the general rate-limit classes of /api/v1/ and /files/
// (R-API-016). Each value is the budget of one bucket across the whole
// installation (R-ARCH-034).
type APIRateLimits struct {
	Enabled      bool                  `yaml:"enabled" default:"true"`
	ExemptAdmins bool                  `yaml:"exempt_admins" default:"false" doc:"Callers who hold the admin capability bypass the limits."`
	Read         APIRateLimitRead      `yaml:"read"`
	Write        APIRateLimitWrite     `yaml:"write"`
	Anonymous    APIRateLimitAnonymous `yaml:"anonymous"`
}

// APIRateLimitRead is the class of safe methods (R-API-016, limiter api_read).
type APIRateLimitRead struct {
	RequestsPerSecond float64 `yaml:"requests_per_second" default:"50" validate:"min=0.001" doc:"Sustained rate; the window is burst divided by this, at least 1s."`
	Burst             int     `yaml:"burst" default:"200" validate:"min=1" doc:"Most requests one window admits."`
}

// APIRateLimitWrite is the class of unsafe methods (R-API-016, limiter api_write).
type APIRateLimitWrite struct {
	RequestsPerSecond float64 `yaml:"requests_per_second" default:"10" validate:"min=0.001"`
	Burst             int     `yaml:"burst" default:"50" validate:"min=1"`
}

// APIRateLimitAnonymous is the class of requests without credentials, keyed by
// client address (R-API-016, limiter api_anonymous).
type APIRateLimitAnonymous struct {
	RequestsPerSecond float64 `yaml:"requests_per_second" default:"5" validate:"min=0.001"`
	Burst             int     `yaml:"burst" default:"20" validate:"min=1"`
}

// Repositories is the repositories section (R-REPO-009 to R-REPO-015): batch
// limits, virtual depth. WP1.4 owns it.
type Repositories struct {
	// >>> M1 repository

	LookupWait time.Duration `yaml:"lookup_wait" default:"5s" validate:"min=100ms" doc:"How long a request for a repository key that PostgreSQL knows and the snapshot does not waits for the change-log poll before it answers 503 (R-ARCH-011 step 2)."`

	// <<< M1 repository
}
