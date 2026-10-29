// Package postgres is the PostgreSQL implementation of the metadata store: the
// connection classes and their pools (R-ARCH-019), the transaction runner with
// its retry of serialization failures and failovers, the migration runner with
// the compatibility gate (R-ARCH-026, R-HA-018, R-HA-019), the embedded
// migrations, the sqlc-generated static queries in q/, and the registration of
// the tables this package owns (R-BAK-004).
//
// Only this package and internal/cluster import the PostgreSQL driver
// (R-ARCH-002). Everything else reaches the database through meta.Tx or through
// the stores built on this package.
package postgres

//go:generate go tool sqlc generate
