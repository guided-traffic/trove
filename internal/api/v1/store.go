package v1

import (
	"context"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Store is the database access of the management API: *postgres.DB. A handler
// reads through Read (retry and error mapping of R-ARCH-019, R-ARCH-023) and
// writes through InTx; it never holds a connection between requests, and a read
// always asks PostgreSQL, never a cache of its own (R-API-052).
type Store interface {
	meta.Runner
	// Read runs fn on one pooled connection outside a transaction.
	Read(ctx context.Context, fn func(q.DBTX) error) error
}
