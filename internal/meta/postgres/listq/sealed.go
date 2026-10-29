// Package listq holds the SQL that cannot be a fixed sqlc statement because its
// table or column names come from a registry (R-ARCH-019): the confined builder
// the architecture test allows to assemble SQL text. Every identifier is quoted
// with pgx.Identifier and no value is ever put into the text.
package listq

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/secrets"
)

// SealedCounter counts the sealed values of a registered column per key ID. It
// implements secrets.ValueCounter for the key-ring status of R-API-049 and for
// `trove admin secrets status`.
type SealedCounter struct {
	DB q.DBTX
}

var _ secrets.ValueCounter = SealedCounter{}

// CountSealed returns, for the column, the number of non-NULL values under each
// key ID. The key ID is the second field of the envelope v1:<key id>:<payload>;
// a value that is not an envelope counts under the empty key ID, so the status
// shows it instead of hiding it.
func (c SealedCounter) CountSealed(ctx context.Context, col secrets.Column) (map[string]int64, error) {
	table := pgx.Identifier{col.Table()}.Sanitize()
	column := pgx.Identifier{col.Name()}.Sanitize()
	sql := fmt.Sprintf(`SELECT CASE WHEN %[1]s LIKE 'v1:%%:%%' THEN split_part(%[1]s, ':', 2) ELSE '' END AS key_id, count(*)
  FROM %[2]s WHERE %[1]s IS NOT NULL GROUP BY 1`, column, table)
	rows, err := c.DB.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var id string
		var n int64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}
