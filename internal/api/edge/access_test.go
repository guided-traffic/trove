package edge

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/meta"
)

// fakeDecider records the index and the subject of every question it is asked.
type fakeDecider struct {
	authz.Decider // unimplemented forms panic: a test names what it uses

	mu      sync.Mutex
	begins  int
	nextIdx *authz.Index
	seen    []*authz.Index
	subs    []authz.Subject
	failure error
}

func (f *fakeDecider) record(idx *authz.Index, sub authz.Subject) {
	f.mu.Lock()
	f.seen, f.subs = append(f.seen, idx), append(f.subs, sub)
	f.mu.Unlock()
}

func (f *fakeDecider) Begin(_ context.Context, sub authz.Subject, _ ...string) (*authz.Index, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.begins++
	if f.failure != nil {
		return nil, f.failure
	}
	return f.nextIdx, nil
}

func (f *fakeDecider) Know(_ context.Context, idx *authz.Index, _ authz.Subject, _ string) (*authz.Index, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.nextIdx != nil && f.nextIdx != idx {
		return f.nextIdx, nil
	}
	return idx, nil
}

func (f *fakeDecider) DecideOn(_ context.Context, idx *authz.Index, sub authz.Subject, _ authz.Coordinate) authz.Decision {
	f.record(idx, sub)
	return authz.Decision{Outcome: authz.Allow}
}

func (f *fakeDecider) ReadableOn(_ context.Context, idx *authz.Index, sub authz.Subject, keys []string) ([]string, error) {
	f.record(idx, sub)
	return keys, nil
}

func (f *fakeDecider) ExposedOn(idx *authz.Index, sub authz.Subject, keys []string) []string {
	f.record(idx, sub)
	return keys
}

func (f *fakeDecider) PathsAllowedOn(_ context.Context, idx *authz.Index, sub authz.Subject, _ authz.Coordinate, _ authz.Action, paths []string, _ authz.Classifier) ([]bool, error) {
	f.record(idx, sub)
	return make([]bool, len(paths)), nil
}

func (f *fakeDecider) RequireOn(_ context.Context, idx *authz.Index, sub authz.Subject, _ string) error {
	f.record(idx, sub)
	return nil
}

func (f *fakeDecider) HoldsOn(idx *authz.Index, sub authz.Subject, _ string) bool {
	f.record(idx, sub)
	return true
}

func (f *fakeDecider) CheckOn(idx *authz.Index, sub authz.Subject, items []authz.CheckItem) ([]bool, error) {
	f.record(idx, sub)
	return make([]bool, len(items)), nil
}

func (f *fakeDecider) RecordAllowed(context.Context, meta.Tx, authz.Subject, authz.Coordinate, authz.Decision) error {
	return nil
}

// newIndex returns a distinct index; BuildIndex of an empty snapshot is the
// cheapest way to get one from outside the package.
func newIndex(epoch int64) *authz.Index {
	s := &authz.Snapshot{}
	s.Label.AuthzEpoch = epoch
	return authz.BuildIndex(s, nil, nil)
}

func TestR_AUTHZ_015_TheHolderAsksEveryQuestionOnOneIndex(t *testing.T) {
	idx := newIndex(1)
	d := &fakeDecider{nextIdx: idx}
	sub := authz.Subject{Name: "alice", Endpoint: "customers"}
	a, err := Begin(t.Context(), d, sub, "generic-local")
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	a.Decide(ctx, authz.Coordinate{Repository: "generic-local"})
	_, _ = a.Readable(ctx, []string{"generic-local"})
	a.Exposed([]string{"generic-local"})
	_, _ = a.PathsAllowed(ctx, authz.Coordinate{Repository: "generic-local"}, authz.Read, []string{"x"}, nil)
	_ = a.Require(ctx, "policies:read")
	a.Holds("policies:read")
	_, _ = a.Check(nil)
	if d.begins != 1 {
		t.Fatalf("%d indexes begun for one request", d.begins)
	}
	if len(d.seen) != 7 {
		t.Fatalf("%d questions recorded", len(d.seen))
	}
	for i := range d.seen {
		if d.seen[i] != idx {
			t.Fatalf("question %d was answered on another index", i)
		}
		if d.subs[i].Endpoint != "customers" {
			t.Fatalf("question %d lost the endpoint of the subject: %+v", i, d.subs[i])
		}
	}
	if a.Epoch() != 1 || a.Index() != idx {
		t.Fatalf("epoch %d", a.Epoch())
	}
}

func TestR_AUTHZ_015_ABeginRefusalIsTheRefusal(t *testing.T) {
	want := errors.New("stale")
	if _, err := Begin(t.Context(), &fakeDecider{failure: want}, authz.Subject{}); !errors.Is(err, want) {
		t.Fatalf("err = %v", err)
	}
}

func TestR_AUTHZ_015_KnowMovesTheRequestForwardOnly(t *testing.T) {
	first, second := newIndex(1), newIndex(2)
	d := &fakeDecider{nextIdx: first}
	a, err := Begin(t.Context(), d, authz.Subject{})
	if err != nil {
		t.Fatal(err)
	}
	d.nextIdx = second
	if err := a.Know(t.Context(), "learned"); err != nil {
		t.Fatal(err)
	}
	if a.Index() != second {
		t.Fatal("Know did not advance the request to the newer index")
	}
	d.nextIdx = first // a Know that answers with an older index must not move the request back
	if err := a.Know(t.Context(), "learned-too"); err != nil {
		t.Fatal(err)
	}
	if a.Index() != second {
		t.Fatal("the index moved backwards")
	}
	a.Decide(t.Context(), authz.Coordinate{})
	if d.seen[len(d.seen)-1] != second {
		t.Fatal("a later decision did not use the advanced index")
	}
}

func TestR_AUTHZ_015_TheHolderTravelsInTheContext(t *testing.T) {
	idx := newIndex(1)
	d := &fakeDecider{nextIdx: idx}
	sub := authz.Subject{Name: "alice"}
	if _, ok := AccessFrom(t.Context()); ok {
		t.Fatal("a context without the shared step carries a holder")
	}
	a, _ := Begin(t.Context(), d, sub)
	ctx := WithAccess(t.Context(), a)
	if got, ok := AccessFrom(ctx); !ok || got != a {
		t.Fatal("the stored holder was not returned")
	}
	t.Run("Resolve returns the stored holder and begins nothing", func(t *testing.T) {
		got, err := Resolve(ctx, d, sub, "generic-local")
		if err != nil || got != a || d.begins != 1 {
			t.Fatalf("holder %p (%p), begins %d, %v", got, a, d.begins, err)
		}
	})
	t.Run("Resolve begins a holder of its own when the context has none: a second index, never a lost ceiling", func(t *testing.T) {
		withEndpoint := authz.Subject{Name: "alice", Endpoint: "customers"}
		got, err := Resolve(t.Context(), d, withEndpoint)
		if err != nil || got == a || d.begins != 2 || got.Subject.Endpoint != "customers" {
			t.Fatalf("holder %+v begins %d err %v", got, d.begins, err)
		}
	})
}
