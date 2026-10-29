package backup

import "github.com/jackc/pgx/v5"

// Snapshot may run at REPEATABLE READ READ ONLY (R-BAK-005).
const Snapshot = "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY"

// Opts is the same through the pgx API.
var Opts = pgx.TxOptions{IsoLevel: pgx.RepeatableRead}
