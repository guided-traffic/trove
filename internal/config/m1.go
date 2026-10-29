package config

import "time"

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

	// <<< M1 api
}

// Repositories is the repositories section (R-REPO-009 to R-REPO-015): batch
// limits, virtual depth. WP1.4 owns it.
type Repositories struct {
	// >>> M1 repository

	LookupWait time.Duration `yaml:"lookup_wait" default:"5s" validate:"min=100ms" doc:"How long a request for a repository key that PostgreSQL knows and the snapshot does not waits for the change-log poll before it answers 503 (R-ARCH-011 step 2)."`

	// <<< M1 repository
}
