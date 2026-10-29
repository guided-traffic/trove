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

	// <<< M1 identity

	// >>> M1 login

	// <<< M1 login
}

// Authz is the authz section (R-AUTHZ-017, R-AUTHZ-028, R-AUTHZ-029): hiding,
// cache bounds, audit of reads. WP1.3 owns it.
type Authz struct {
	// >>> M1 authz

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
