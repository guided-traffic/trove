package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestGate(t *testing.T) {
	t.Run("R-HA-018 a binary serves when applied reaches requiredSchema and the floor is not above it", func(t *testing.T) {
		if err := Gate(SchemaState{Applied: 5, CompatFloor: "0.3.0"}, 5, "0.3.0"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-HA-018 newer expand steps do not make an older binary incompatible", func(t *testing.T) {
		if err := Gate(SchemaState{Applied: 60, CompatFloor: "0.0.0"}, 57, "0.3.2"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-HA-018 below requiredSchema the replica waits", func(t *testing.T) {
		err := Gate(SchemaState{Applied: 4, CompatFloor: "0.0.0"}, 5, "0.3.0")
		if !errors.Is(err, ErrSchemaBehind) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-ARCH-026 an N-2 binary against schema N exits with the floor", func(t *testing.T) {
		err := Gate(SchemaState{Applied: 60, CompatFloor: "0.4.0"}, 50, "0.2.1")
		var fe *FloorError
		if !errors.As(err, &fe) || err.Error() != "schema requires trove >= 0.4.0" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-ARCH-026 a development build passes every floor", func(t *testing.T) {
		if err := Gate(SchemaState{Applied: 5, CompatFloor: "9.9.9"}, 5, "dev"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-HA-018 floors compare as versions, not as strings", func(t *testing.T) {
		if err := Gate(SchemaState{Applied: 1, CompatFloor: "0.9.0"}, 1, "0.10.0"); err != nil {
			t.Fatalf("0.10.0 must satisfy a 0.9.0 floor: %v", err)
		}
		var fe *FloorError
		if err := Gate(SchemaState{Applied: 1, CompatFloor: "0.10.0"}, 1, "0.9.0"); !errors.As(err, &fe) {
			t.Fatalf("0.9.0 must not satisfy a 0.10.0 floor: %v", err)
		}
	})
	t.Run("R-HA-018 a pre-release is below its release", func(t *testing.T) {
		var fe *FloorError
		if err := Gate(SchemaState{Applied: 1, CompatFloor: "0.4.0"}, 1, "0.4.0-rc1"); !errors.As(err, &fe) {
			t.Fatalf("err = %v", err)
		}
	})
}

type fakeDB struct{ pids []int32 }

func (f *fakeDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (f *fakeDB) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }
func (f *fakeDB) QueryRow(context.Context, string, ...any) pgx.Row {
	pid := f.pids[0]
	if len(f.pids) > 1 {
		f.pids = f.pids[1:]
	}
	return pidRow(pid)
}

type pidRow int32

func (r pidRow) Scan(dest ...any) error { *dest[0].(*int32) = int32(r); return nil }

func TestSessionGuard(t *testing.T) {
	t.Run("R-ARCH-017 five identical backend pids pass", func(t *testing.T) {
		if err := sessionGuard(context.Background(), &fakeDB{pids: []int32{7}}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-ARCH-017 a differing backend pid exits with the session-state message", func(t *testing.T) {
		err := sessionGuard(context.Background(), &fakeDB{pids: []int32{7, 7, 7, 8, 7}})
		if !errors.Is(err, ErrSessionState) ||
			err.Error() != "session connection must keep session state; set database.session_host to a direct or session-mode endpoint" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-ARCH-017 a pooler's refused startup parameter is reported as the same condition", func(t *testing.T) {
		raw := &pgconn.PgError{Code: "08P01", Message: "unsupported startup parameter: statement_timeout"}
		err := refusedStartupParameter(raw)
		if !errors.Is(err, ErrSessionState) || !errors.Is(err, raw) || err.Error() != ErrSessionState.Error() {
			t.Fatalf("err = %v", err)
		}
		if classify(raw) != retryNo {
			t.Fatal("a refused startup parameter must not be retried")
		}
	})
}
