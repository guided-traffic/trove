package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Audit helpers (R-OPS-015, R-OPS-017): the error classes the audit writer
// tells apart without importing the driver, and the migrate runner's write of
// the audit retention.

// IsDataError reports a failure that belongs to the statement's data and that
// retrying the same data cannot fix: a data exception (SQLSTATE class 22) or an
// integrity violation (class 23). A connection that fails is not one.
func IsDataError(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && (strings.HasPrefix(pe.Code, "22") || strings.HasPrefix(pe.Code, "23"))
}

// IsPrivilegeError reports insufficient_privilege (42501): the role may not do
// what it tried, which is how the runtime role meets audit_policy (Q-OPS-4).
func IsPrivilegeError(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "42501"
}

// syncAuditPolicy writes audit.retention_days into audit_policy and calls
// audit_maintain_partitions() (R-OPS-015 "Retention is owner data"). The runner
// is the schema owner, so it is the one writer of the row in two-role mode: a
// compromised replica cannot shorten retention. A lowered value drops the
// partitions that are now expired in the same call, which is the documented
// effect of a changed value after the next trove migrate.
func syncAuditPolicy(ctx context.Context, db q.DBTX, days int, log *slog.Logger) error {
	qs := q.New(db)
	n, err := qs.SetAuditRetention(ctx, int32(days)) //nolint:gosec // validated by the config: min=1, a day count
	if err != nil {
		return fmt.Errorf("audit_policy: %w", err)
	}
	if n > 0 {
		log.Info("audit retention written", "retention_days", days)
	}
	if days > 0 && days < 90 {
		log.Warn("audit.retention_days is below 90", "retention_days", days)
	}
	if err := qs.MaintainAuditPartitions(ctx); err != nil {
		return fmt.Errorf("audit_maintain_partitions: %w", err)
	}
	return nil
}
