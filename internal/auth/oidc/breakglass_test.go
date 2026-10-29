package oidc

import (
	"context"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
)

func TestR_AUTH_014_VerificationSlots(t *testing.T) {
	pw, err := parsePassword([]byte("correct-horse-batt-1"))
	if err != nil {
		t.Fatal(err)
	}
	b := &BreakGlass{s: &Service{}, cfg: config.AuthBreakGlass{Username: "admin"}, slots: make(chan struct{}, 1)}

	t.Run("R-AUTH-014 an attempt that gets no verification slot within 1 s answers 503 server_busy with Retry-After 1", func(t *testing.T) {
		b.slots <- struct{}{} // the replica's only slot is taken
		defer func() { <-b.slots }()
		start := time.Now()
		ok, err := b.verify(context.Background(), pw, loginRequest{Username: "admin", Password: "correct-horse-batt-1"})
		e, isErr := errs.As(err)
		if ok || !isErr || e.Code != errs.CodeServerBusy || e.RetryAfter != time.Second {
			t.Fatalf("ok %v err %v", ok, err)
		}
		if took := time.Since(start); took < slotWait || took > slotWait+500*time.Millisecond {
			t.Fatalf("waited %s", took)
		}
	})

	t.Run("R-AUTH-014 the verification runs for an unknown username too and fails", func(t *testing.T) {
		ok, err := b.verify(context.Background(), pw, loginRequest{Username: "nobody", Password: "correct-horse-batt-1"})
		if ok || err != nil {
			t.Fatalf("ok %v err %v", ok, err)
		}
		if ok, err := b.verify(context.Background(), pw, loginRequest{Username: "admin", Password: "correct-horse-batt-1"}); !ok || err != nil {
			t.Fatalf("the right credentials: ok %v err %v", ok, err)
		}
	})
}
