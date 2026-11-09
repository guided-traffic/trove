package jobs

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// failingDB fails its first n Exec calls with err, then answers one affected row.
type failingDB struct {
	n     int
	err   error
	calls int
}

func (f *failingDB) Exec(context.Context, string, ...interface{}) (pgconn.CommandTag, error) {
	f.calls++
	if f.calls <= f.n {
		return pgconn.CommandTag{}, f.err
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func (f *failingDB) Query(context.Context, string, ...interface{}) (pgx.Rows, error) {
	return nil, f.err
}
func (f *failingDB) QueryRow(context.Context, string, ...interface{}) pgx.Row { return nil }

var _ q.DBTX = (*failingDB)(nil)

func TestRunWritesRideOutAFailover(t *testing.T) {
	t.Run("R-HA-059 R-ARCH-016 a checkpoint and a progress write repeat on a failover error and the attempt is kept", func(t *testing.T) {
		db := &failingDB{n: 2, err: io.ErrUnexpectedEOF}
		run := &Run{Job: Job{Attempt: 1}, w: &Worker{o: WorkerOptions{Main: db, UID: "w"}}}
		if err := run.SaveCheckpoint(context.Background(), map[string]int{"step": 1}); err != nil {
			t.Fatalf("SaveCheckpoint: %v", err)
		}
		if db.calls != 3 {
			t.Errorf("%d statements, want 3 (two lost to the failover, one answered)", db.calls)
		}
		db = &failingDB{n: 1, err: io.ErrUnexpectedEOF}
		run.w.o.Main = db
		if err := run.SaveProgress(context.Background(), 1, 2, "steps"); err != nil {
			t.Fatalf("SaveProgress: %v", err)
		}
	})
	t.Run("R-ARCH-016 a write that fails for another reason is not repeated", func(t *testing.T) {
		boom := errors.New("syntax error")
		db := &failingDB{n: 5, err: boom}
		run := &Run{Job: Job{Attempt: 1}, w: &Worker{o: WorkerOptions{Main: db, UID: "w"}}}
		if err := run.SaveCheckpoint(context.Background(), 1); !errors.Is(err, boom) {
			t.Fatalf("got %v, want the error unchanged", err)
		}
		if db.calls != 1 {
			t.Errorf("%d statements, want 1", db.calls)
		}
	})
}
