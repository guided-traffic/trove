package token

import (
	"context"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

func TestR_AUTH_031_UseIsBufferedOncePerToken(t *testing.T) {
	raw, _ := Generate(TypePAT)
	db := &downDB{}
	s := newTestService(t, db, &freshness{fresh: true}, nil)
	s.tokens.Set(Hash(raw), cachedToken{info: Info{ID: "t1", Type: TypePAT, Owner: principal.Principal{ID: "u", Status: principal.StatusActive}}})

	for i := 0; i < 3; i++ {
		info, err := s.Check(context.Background(), raw, Request{Source: audit.Source{ClientAddr: "fe80::1%eth0"}})
		if err != nil {
			t.Fatal(err)
		}
		s.NoteUse(info.ID, "fe80::1%eth0") // the stage does this after the whole authentication succeeded
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
	// Check alone is not a use: the stage may still refuse the token for its endpoint binding.
	raw, _ := Generate(TypePAT)
	s.tokens.Set(Hash(raw), cachedToken{info: Info{ID: "t2", Type: TypePAT, Owner: principal.Principal{ID: "u", Status: principal.StatusActive}}})
	if _, err := s.Check(context.Background(), raw, Request{}); err != nil {
		t.Fatal(err)
	}
	if n := len(s.used.take()); n != 0 {
		t.Fatalf("Check noted a use before the stage had finished (%d entries)", n)
	}
}

func TestR_AUTH_031_FailedFlushDropsTheBatch(t *testing.T) {
	s := newTestService(t, &downDB{}, &freshness{fresh: true}, nil)
	s.NoteUse("t1", "203.0.113.7")
	if err := s.FlushLastUsed(context.Background()); err == nil {
		t.Fatal("a flush against a database that is down reported success")
	}
	if n := len(s.used.take()); n != 0 {
		t.Fatalf("the failed batch was kept (%d entries): best effort drops it", n)
	}
}

// blockingDB holds a transaction open until released and records whether the
// context it ran under had been cancelled by then.
type blockingDB struct {
	started, release chan struct{}
	ctxErr           chan error
}

func (b *blockingDB) InTx(ctx context.Context, _ func(meta.Tx) error) error {
	b.started <- struct{}{}
	<-b.release
	b.ctxErr <- ctx.Err()
	return nil
}

func (b *blockingDB) Read(context.Context, func(q.DBTX) error) error { return nil }

// TestR_AUTH_031_ABatchInFlightSurvivesTheEndOfTheLoop: the shutdown cancels the
// loop while a periodic flush is writing; that write is not cancelled with it,
// so the batch is not lost (R-ARCH-024 step 6).
func TestR_AUTH_031_ABatchInFlightSurvivesTheEndOfTheLoop(t *testing.T) {
	db := &blockingDB{started: make(chan struct{}, 1), release: make(chan struct{}), ctxErr: make(chan error, 1)}
	s := newTestService(t, db, &freshness{fresh: true}, nil)
	s.lastUsedEvery = 5 * time.Millisecond
	s.NoteUse("t1", "")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.RunLastUsed(ctx); close(done) }()
	<-db.started // the periodic flush is writing
	cancel()
	select {
	case <-done:
		t.Fatal("the loop returned while its flush was still writing; Flush could not wait for it")
	case <-time.After(50 * time.Millisecond):
	}
	close(db.release)
	if err := <-db.ctxErr; err != nil {
		t.Fatalf("the write in flight was cancelled with the loop: %v", err)
	}
	<-done
}
