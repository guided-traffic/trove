package cluster

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// stuckDB is a control pool whose every call waits for a connection that never frees up, as a saturated pool does under host
// load: it returns only when the context of the call ends, with that context's error.
type stuckDB struct{}

func (stuckDB) Exec(ctx context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	<-ctx.Done()
	return pgconn.CommandTag{}, ctx.Err()
}

func (stuckDB) Query(ctx context.Context, _ string, _ ...any) (pgx.Rows, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (stuckDB) QueryRow(ctx context.Context, _ string, _ ...any) pgx.Row {
	<-ctx.Done()
	return nil
}

// okDB is a pool that deletes at once.
type okDB struct{}

func (okDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.NewCommandTag("DELETE 1"), nil
}

func (okDB) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }

func (okDB) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

func stopAfter(t *testing.T, r *Registry, deadline time.Duration) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Stop(ctx) }()
	select {
	case err := <-done:
		return err
	case <-time.After(deadline + 500*time.Millisecond):
		t.Fatal("Stop outlived the deadline of the close phase")
		return nil
	}
}

// R-HA-011: the deletion of the replica's own row is a close step, so it ends with the close phase and the process exits
// inside the pod's grace period. Behind a pool that cannot hand out a connection it used to wait its own five seconds, past
// the close deadline, and the phase was abandoned (F-94).
func TestR_HA_011_RegistryStopEndsWithTheClosePhase(t *testing.T) {
	r, err := NewRegistry(RegistryOptions{DB: stuckDB{}, Identity: ReplicaIdentity{UID: "uid", Name: "a", Role: "web"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := stopAfter(t, r, 200*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop = %v, want the deadline of the close phase", err)
	}
}

// R-HA-011, R-ARCH-024 step 6, R-OPS-003: the final deletion does not queue on the control connection, which the change-log
// poll and the readiness checks still use during the close phase; it runs on the idle request pool and ends without an error
// (Q-HA-202).
func TestR_HA_011_RegistryStopDoesNotWaitForTheControlConnection(t *testing.T) {
	r, err := NewRegistry(RegistryOptions{DB: stuckDB{}, ShutdownDB: okDB{}, Identity: ReplicaIdentity{UID: "uid", Name: "a", Role: "web"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := stopAfter(t, r, 200*time.Millisecond); err != nil {
		t.Fatalf("Stop = %v, want the row deleted on the request pool", err)
	}
}

// R-ARCH-024 step 1, R-OPS-003 (Q-HA-283): marking the row draining is the first thing a stopping replica does, and it runs on
// the request pool like the deletion. On the control connection it queued behind the poll and the readiness checks for the
// whole call timeout (10 s) when the database stalled, and that time came out of the budget of every later phase.
func TestR_ARCH_024_RegistryDrainDoesNotWaitForTheControlConnection(t *testing.T) {
	r, err := NewRegistry(RegistryOptions{DB: stuckDB{}, ShutdownDB: okDB{}, Identity: ReplicaIdentity{UID: "uid", Name: "a", Role: "web"}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { r.Drain(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Drain waited for the control connection")
	}
}
