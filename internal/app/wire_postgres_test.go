package app

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/observability/health"
)

func TestMarkOutage(t *testing.T) {
	t.Run("R-OPS-001 an unreachable or restarting database is an outage the readiness grace applies to", func(t *testing.T) {
		for name, err := range map[string]error{
			"admin shutdown": &pgconn.PgError{Code: "57P01", Message: "terminating connection due to administrator command"},
			"refused":        fmt.Errorf("dial: %w", syscall.ECONNREFUSED),
			"standby":        postgres.ErrStandby,
			"check timeout":  context.DeadlineExceeded,
		} {
			if !health.IsTransient(markOutage(err)) {
				t.Errorf("%s: not marked transient", name)
			}
		}
	})
	t.Run("R-OPS-001 a schema that is behind, a rejected password and success are not outages", func(t *testing.T) {
		for name, err := range map[string]error{
			"schema behind": postgres.ErrSchemaBehind,
			"password":      &pgconn.PgError{Code: "28P01", Message: "password authentication failed"},
			"other":         errors.New("boom"),
		} {
			if got := markOutage(err); got == nil || health.IsTransient(got) {
				t.Errorf("%s: got %v", name, got)
			}
		}
		if markOutage(nil) != nil {
			t.Error("nil must stay nil")
		}
	})
}
