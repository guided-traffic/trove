package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// R-HA-011, R-ARCH-024 step 6: a pool does not close while a connection is acquired from it, so the close of the pools ends with
// the close phase instead of holding it, and says which pool is held (F-97).
func TestR_HA_011_ClosePoolsEndsWithTheClosePhaseWhileAConnectionIsHeld(t *testing.T) {
	release := make(chan struct{})
	closed := make(chan struct{})
	pools := []closingPool{
		{name: "main", close: func() { <-release; close(closed) }, held: func() int32 { return 2 }},
		{name: "control", close: func() {}, held: func() int32 { return 0 }},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	res := make(chan error, 1)
	go func() { res <- closePools(ctx, pools) }()
	select {
	case err := <-res:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("closePools = %v, want the deadline of the close phase", err)
		}
		if !strings.Contains(err.Error(), "main pool (2 connections held)") || strings.Contains(err.Error(), "control") {
			t.Fatalf("the error names the wrong pools: %v", err)
		}
	case <-time.After(700 * time.Millisecond):
		t.Fatal("the close of the pools outlived the deadline of the close phase")
	}
	close(release) // the holder returns the connection: the pool finishes closing in the background
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("the pool did not finish closing once the connection was returned")
	}
}

func TestR_HA_011_ClosePoolsWithoutAHolderReturnsNil(t *testing.T) {
	pools := []closingPool{{name: "main", close: func() {}, held: func() int32 { return 0 }}}
	if err := closePools(context.Background(), pools); err != nil {
		t.Fatal(err)
	}
}

// A pool that is open at the deadline but holds nothing (the deadline had passed before the step began) is not named, and the
// single connection reads in the singular.
func TestR_HA_011_ClosePoolsNamesOnlyThePoolsThatHoldAConnection(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	idle := []closingPool{{name: "main", close: func() { <-release }, held: func() int32 { return 0 }}}
	if err := closePools(ctx, idle); err != nil {
		t.Fatalf("closePools = %v, want nil: no pool holds a connection", err)
	}
	one := []closingPool{
		{name: "main", close: func() { <-release }, held: func() int32 { return 1 }},
		{name: "lease", close: func() { <-release }, held: func() int32 { return 0 }},
	}
	err := closePools(ctx, one)
	if err == nil || !strings.Contains(err.Error(), "main pool (1 connection held)") || strings.Contains(err.Error(), "lease") {
		t.Fatalf("closePools = %v", err)
	}
}
