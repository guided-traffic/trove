package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func borrow(h *holders, c *pgx.Conn) { h.acquired(context.Background(), c) }

// TestR_HA_011_AHeldConnectionIsNamedByItsBorrower: a connection that is checked out is listed with the function that acquired it, or
// with the label the borrower gave, and for how long; a returned one is gone (R-HA-011, R-ARCH-024 step 6).
func TestR_HA_011_AHeldConnectionIsNamedByItsBorrower(t *testing.T) {
	h := &holders{}
	c1, c2, c3 := &pgx.Conn{}, &pgx.Conn{}, &pgx.Conn{}
	borrow(h, c1)
	h.acquired(WithHolder(context.Background(), "readiness probe: database"), c2)
	borrow(h, c3)
	h.released(c3)
	got := h.describe(time.Now().Add(8700 * time.Millisecond))
	if len(got) != 2 {
		t.Fatalf("%v: want the two connections that are still held", got)
	}
	joined := strings.Join(got, "; ")
	if !strings.Contains(joined, "readiness probe: database for 8.7s") {
		t.Errorf("the label is missing: %s", joined)
	}
	// The first function outside the driver and this package: in a test of this package that is the test runner.
	if !strings.Contains(joined, "testing.tRunner for 8.7s") {
		t.Errorf("the calling function is missing: %s", joined)
	}
	h.released(c1)
	h.released(c2)
	if left := h.describe(time.Now()); len(left) != 0 {
		t.Errorf("released connections are still listed: %v", left)
	}
	var none *holders
	none.acquired(context.Background(), c1)
	if none.describe(time.Now()) != nil {
		t.Error("a nil table names a borrower")
	}
}

// TestR_HA_011_TheCloseErrorNamesTheBorrowers: the error of a close that cannot finish keeps the count and adds who holds the
// connections.
func TestR_HA_011_TheCloseErrorNamesTheBorrowers(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	pools := []closingPool{{
		name: "control", close: func() { <-release }, held: func() int32 { return 1 },
		holders: func() []string { return []string{"cluster.(*changeLog).poll for 8.7s"} },
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := closePools(ctx, pools)
	if err == nil || !strings.Contains(err.Error(), "control pool (1 connection held) by cluster.(*changeLog).poll for 8.7s") {
		t.Fatalf("closePools = %v", err)
	}
}
