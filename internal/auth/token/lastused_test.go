package token

import (
	"context"
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
)

func TestR_AUTH_031_UseIsBufferedOncePerToken(t *testing.T) {
	raw, _ := Generate(TypePAT)
	db := &downDB{}
	s := newTestService(t, db, &freshness{fresh: true}, nil)
	s.tokens.Set(Hash(raw), cachedToken{info: Info{ID: "t1", Type: TypePAT, Owner: principal.Principal{ID: "u", Status: principal.StatusActive}}})

	for i := 0; i < 3; i++ {
		if _, err := s.Check(context.Background(), raw, Request{Source: audit.Source{ClientAddr: "fe80::1%eth0"}}); err != nil {
			t.Fatal(err)
		}
	}
	batch := s.used.take()
	if len(batch) != 1 || batch["t1"].addr != "fe80::1" {
		t.Fatalf("three uses of one token are one entry with the address without its zone, got %+v", batch)
	}
	if db.reads != 0 {
		t.Fatal("recording a use read the database")
	}
}

func TestR_AUTH_031_RejectedCredentialIsNotAUse(t *testing.T) {
	s := newTestService(t, &downDB{}, &freshness{fresh: true}, nil)
	if _, err := s.Check(context.Background(), "hunter2", Request{}); err == nil {
		t.Fatal("a malformed credential was accepted")
	}
	if n := len(s.used.take()); n != 0 {
		t.Fatalf("a rejected credential was recorded as a use (%d entries)", n)
	}
}

func TestR_AUTH_031_FailedFlushDropsTheBatch(t *testing.T) {
	s := newTestService(t, &downDB{}, &freshness{fresh: true}, nil)
	s.noteUse("t1", "203.0.113.7")
	if err := s.FlushLastUsed(context.Background()); err == nil {
		t.Fatal("a flush against a database that is down reported success")
	}
	if n := len(s.used.take()); n != 0 {
		t.Fatalf("the failed batch was kept (%d entries): best effort drops it", n)
	}
}
