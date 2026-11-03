package token

import (
	"context"
	"errors"
	"testing"
)

// TestR_BAS_005_ABastionTokenIsUnknownBeforeAnyLookup: the type bst passes the syntax and is refused as an
// unknown token without a read of the token table, counted against the client address (R-BAS-005, R-AUTH-038).
func TestR_BAS_005_ABastionTokenIsUnknownBeforeAnyLookup(t *testing.T) {
	raw, err := Generate(TypeBST)
	if err != nil {
		t.Fatal(err)
	}
	if typ, err := Check(raw); err != nil || typ != TypeBST || len(raw) != Length || raw[:8] != "trv_bst_" {
		t.Fatalf("Check(%q) = %v, %v", raw, typ, err)
	}
	db := &downDB{}
	s := newTestService(t, db, &freshness{fresh: true}, nil)
	th := &countThrottle{}
	if _, err := s.Check(context.Background(), raw, Request{Throttle: th}); !errors.Is(err, ErrUnknown) {
		t.Fatalf("err = %v, want ErrUnknown", err)
	}
	if db.reads != 0 || th.counted != 1 {
		t.Fatalf("reads = %d, counted = %d, want 0 and 1", db.reads, th.counted)
	}
}
