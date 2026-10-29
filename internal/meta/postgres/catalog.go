package postgres

import (
	"context"
	"fmt"
	"regexp"
	"sort"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/guided-traffic/trove/internal/backup/tables"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

func textOf(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

// CheckTableRegistry is the migration test of R-BAK-004: it reads the tables and
// foreign keys of a migrated schema from the catalog and returns the findings of
// reg.Check: a table without an export class, and a foreign-key cycle among the
// exported tables without a registered CycleBreak column.
func CheckTableRegistry(ctx context.Context, db q.DBTX, schema string, reg *tables.Registry) ([]tables.Problem, error) {
	qs := q.New(db)
	names, err := qs.SchemaTables(ctx, schema)
	if err != nil {
		return nil, err
	}
	rows, err := qs.SchemaForeignKeys(ctx, schema)
	if err != nil {
		return nil, err
	}
	fks := make([]tables.ForeignKey, 0, len(rows))
	for _, r := range rows {
		fks = append(fks, tables.ForeignKey{Table: r.TableName, Column: r.ColumnName, RefTable: r.RefTable})
	}
	return reg.Check(names, fks), nil
}

// revisionLike matches the names R-ARCH-009 reserves for revision columns.
var revisionLike = regexp.MustCompile(`^(rev_seq|revision|epoch|.+_version)$`)

// CheckRevisionColumns finds bigint columns named rev_seq, revision, epoch or
// *_version that are not registered with meta.RegisterRevisionColumn, because a
// restore raises only registered columns (R-ARCH-009, R-BAK-022). It returns one
// "table.column" per finding.
func CheckRevisionColumns(ctx context.Context, db q.DBTX, schema string, reg *meta.RevisionRegistry) ([]string, error) {
	rows, err := q.New(db).SchemaBigintColumns(ctx, schema)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range rows {
		if revisionLike.MatchString(r.ColumnName) && !reg.Has(r.TableName, r.ColumnName) {
			out = append(out, fmt.Sprintf("%s.%s", r.TableName, r.ColumnName))
		}
	}
	sort.Strings(out)
	return out, nil
}
