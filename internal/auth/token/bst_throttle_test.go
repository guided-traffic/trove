package token

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/principal"
)

// askingThrottle records every question; exceeded is its answer.
type askingThrottle struct {
	exceeded bool
	asked    int
	counted  int
}

func (a *askingThrottle) Exceeded(context.Context) bool { a.asked++; return a.exceeded }
func (a *askingThrottle) Count(context.Context)         { a.counted++ }

// TestR_BAS_008_ABastionTokenAsksTheThrottleOnlyOnACacheMiss: the failed-credential throttle of the sender's
// address (a read of the shared counters in cluster mode) is asked after the replica's token cache, as for every
// other credential (R-AUTH-038, Q-BAS-65): a Bastion token the replica holds passes a throttled address without a
// question; one it does not hold is refused without a lookup, unless its marker says it was valid recently.
func TestR_BAS_008_ABastionTokenAsksTheThrottleOnlyOnACacheMiss(t *testing.T) {
	raw, _ := Generate(TypeBST)
	exp := time.Now().Add(time.Hour)
	held := cachedToken{info: Info{ID: "b1", Type: TypeBST, ExpiresAt: &exp, Owner: principal.Principal{ID: "reg", Kind: principal.KindBastion, Name: "partners", Status: principal.StatusActive}}}

	t.Run("a token in the cache costs no question, also from a throttled address", func(t *testing.T) {
		db := &downDB{}
		s := newTestService(t, db, &freshness{fresh: true}, nil)
		s.tokens.Set(Hash(raw), held)
		th := &askingThrottle{exceeded: true}
		info, err := s.AuthenticateBastion(context.Background(), raw, Request{Throttle: th, RetryAfter: time.Minute})
		if err != nil || info.TokenID != "b1" {
			t.Fatalf("info %+v, err %v", info, err)
		}
		if th.asked != 0 || db.reads != 0 {
			t.Fatalf("throttle asked %d times, %d reads", th.asked, db.reads)
		}
	})
	t.Run("a miss from a throttled address is refused without a lookup", func(t *testing.T) {
		db := &downDB{}
		markers := &noMarkers{}
		s := newTestService(t, db, &freshness{fresh: true}, markers)
		th := &askingThrottle{exceeded: true}
		_, err := s.AuthenticateBastion(context.Background(), raw, Request{Throttle: th, RetryAfter: time.Minute})
		var te *ThrottledError
		if !errors.As(err, &te) || te.RetryAfter != time.Minute {
			t.Fatalf("err = %v", err)
		}
		if th.asked != 1 || db.reads != 0 || markers.seen != 1 {
			t.Fatalf("asked %d, reads %d, marker lookups %d", th.asked, db.reads, markers.seen)
		}
		if th.counted != 0 {
			t.Fatalf("the service counted %d failures; the recognition counts what it refuses", th.counted)
		}
	})
	t.Run("a miss from an address that is not throttled goes to the store", func(t *testing.T) {
		db := &downDB{}
		s := newTestService(t, db, &freshness{fresh: true}, nil)
		th := &askingThrottle{}
		if _, err := s.AuthenticateBastion(context.Background(), raw, Request{Throttle: th}); err == nil {
			t.Fatal("a store that is down accepted the token")
		}
		if th.asked != 1 || db.reads == 0 {
			t.Fatalf("asked %d, reads %d", th.asked, db.reads)
		}
	})
}
