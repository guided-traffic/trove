package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// SchemaState is schema_state: the newest applied step and the compatibility
// floor (R-HA-018).
type SchemaState struct {
	Applied     int
	CompatFloor string
}

// ErrSchemaBehind means the database has not yet reached this binary's
// requiredSchema. The replica waits not ready; the pre-upgrade Job normally
// migrates first (R-HA-008).
var ErrSchemaBehind = errors.New("schema is behind this binary")

// FloorError means the schema's compat_floor is above this binary's version: the
// process exits with its message (R-ARCH-026).
type FloorError struct{ Floor, Version string }

func (e *FloorError) Error() string { return "schema requires trove >= " + e.Floor }

// Gate is the compatibility gate of R-HA-018: a binary of version V serves iff
// applied >= requiredSchema and compat_floor <= V. Newer expand steps do not
// make an older binary incompatible, so there is no upper bound on applied.
// Below requiredSchema it returns an error wrapping ErrSchemaBehind; above the
// floor a *FloorError. The floor is checked first, because a binary that is too
// old will never become servable by waiting.
func Gate(st SchemaState, required int, version string) error {
	floor, err := parseVersion(st.CompatFloor)
	if err != nil {
		return fmt.Errorf("schema_state.compat_floor %q: %w", st.CompatFloor, err)
	}
	v, err := parseVersion(version)
	if err != nil {
		return fmt.Errorf("binary version: %w", err)
	}
	if floor.compare(v) > 0 {
		return &FloorError{Floor: st.CompatFloor, Version: version}
	}
	if st.Applied < required {
		return fmt.Errorf("%w: applied %d, this binary needs %d", ErrSchemaBehind, st.Applied, required)
	}
	return nil
}

// SchemaState reads schema_state on the request pool, for callers on a request
// path such as GET /system/info: a request must never queue behind the loops
// that hold the control connection, and the control connection must not be
// spent on a client's rate. A database without the table or the row counts as
// applied 0. It reads through Read, so it rides out a failover.
func (db *DB) SchemaState(ctx context.Context) (SchemaState, error) {
	var st SchemaState
	err := db.Read(ctx, func(c q.DBTX) (err error) {
		st, err = readSchemaState(ctx, c)
		return err
	})
	return st, err
}

// readSchemaState reads schema_state through any pool or connection.
func readSchemaState(ctx context.Context, conn q.DBTX) (SchemaState, error) {
	qs := q.New(conn)
	found, err := qs.TableExists(ctx, "schema_state")
	if err != nil || !found {
		return SchemaState{CompatFloor: zeroVersion}, err
	}
	row, err := qs.GetSchemaState(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return SchemaState{CompatFloor: zeroVersion}, nil
	}
	if err != nil {
		return SchemaState{}, err
	}
	return SchemaState{Applied: int(row.Applied), CompatFloor: row.CompatFloor}, nil
}

// CheckSchema is the `schema` readiness check (R-OPS-001): Gate applied to the
// database and this binary's embedded requiredSchema. It reads on the control
// connection, like the other checks readiness depends on.
func (db *DB) CheckSchema(ctx context.Context, version string) error {
	st, err := readSchemaState(ctx, db.control)
	if err != nil {
		return err
	}
	return Gate(st, RequiredSchema(), version)
}
