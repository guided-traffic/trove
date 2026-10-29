package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/guided-traffic/trove/internal/backup/tables"
	"github.com/guided-traffic/trove/internal/config"
)

// VerifyTableRegistry is the migration test of R-BAK-004 run by `trove migrate`
// against the migrated database: every table of the schema must carry an export
// class in reg, the registry of the running binary, and the foreign keys among
// the exported tables must have no cycle without a registered break. A table
// that no linked package registered would be silently missing from every
// backup, so the migration Job fails and names it (a *Refusal, exit status 3:
// running it again changes nothing).
//
// The migrations have already been applied when it runs, as they must be for the
// catalog to be read.
func VerifyTableRegistry(ctx context.Context, cfg config.Database, opts MigrateOptions, reg *tables.Registry) error {
	opts.fill()
	conn, err := sessionConn(ctx, cfg, opts, classMigrate)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	problems, err := CheckTableRegistry(ctx, conn, cfg.Schema, reg)
	if err != nil {
		return err
	}
	if len(problems) == 0 {
		return nil
	}
	lines := make([]string, len(problems))
	for i, p := range problems {
		lines[i] = p.String()
	}
	return &Refusal{Reason: fmt.Sprintf("the table registry of this binary does not cover schema %q (R-BAK-004): %s; "+
		"register each table with its export class in the package that owns it, and make sure that package is linked into the binary",
		cfg.Schema, strings.Join(lines, "; "))}
}
