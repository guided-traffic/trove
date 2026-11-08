//go:build integration

package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

// R-HA-011, R-ARCH-024 step 6: a connection still acquired from a pool must not hold the close of the pools past the close
// phase; the error names the pool (F-97).
func TestR_HA_011_CloseContextEndsWithTheClosePhaseWhileAConnectionIsHeld(t *testing.T) {
	st := getStack(t)
	cfg, _, _ := migrated(t, st)
	db, err := postgres.Open(ctxT(t, 30*time.Second), cfg, "pod-close-held", meta.RoleWeb, quiet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	held, err := db.Main().Acquire(ctxT(t, 10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = db.CloseContext(ctx)
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("CloseContext took %s with a connection held, want the 300 ms deadline", took)
	}
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "main pool (1 connection held)") {
		t.Fatalf("CloseContext = %v, want the deadline and the held main pool", err)
	}
	held.Release()
}
