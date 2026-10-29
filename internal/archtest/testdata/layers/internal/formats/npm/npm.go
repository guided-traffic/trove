package npm

import "example.test/fix/internal/meta/postgres"

// Bad reaches infrastructure from a format.
var _ = postgres.Store{}
