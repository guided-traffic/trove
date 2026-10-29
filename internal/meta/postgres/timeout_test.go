package postgres

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/guided-traffic/trove/internal/errs"
)

func TestUnavailableCodes(t *testing.T) {
	t.Run("R-ARCH-019 a statement timeout is database_timeout, a lost connection database_unavailable", func(t *testing.T) {
		for _, code := range []string{"57014", "55P03"} {
			e, ok := errs.As(Unavailable(&pgconn.PgError{Code: code}))
			if !ok || e.Code != errs.CodeDatabaseTimeout || e.Status() != 503 {
				t.Errorf("%s: %+v", code, e)
			}
		}
		e, _ := errs.As(Unavailable(errors.New("connection refused")))
		if e.Code != errs.CodeDatabaseUnavailable {
			t.Errorf("code = %s", e.Code)
		}
	})
}
