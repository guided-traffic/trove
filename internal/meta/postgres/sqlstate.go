package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// IsState reports whether err is a PostgreSQL error with the SQLSTATE code. The
// domain packages use it to turn the refusal of one of the integrity triggers of
// the schema (the AI codes, such as AI002 for a project service account in a group)
// into the answer of the API without importing the driver.
func IsState(err error, code string) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == code
}
