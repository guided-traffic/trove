package v1

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
)

// indexDecider is a decision point that hands out a new index on every Begin
// and records which index each question was answered on, so a request that
// asked two indexes shows.
type indexDecider struct {
	authz.Decider // unimplemented forms panic: a test names what it uses
	authz.Authorizer

	mu      sync.Mutex
	begins  int
	keys    [][]string
	seen    map[string][]*authz.Index
	failure error
}

func (d *indexDecider) Begin(_ context.Context, _ authz.Subject, keys ...string) (*authz.Index, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failure != nil {
		return nil, d.failure
	}
	d.begins++
	d.keys = append(d.keys, keys)
	s := &authz.Snapshot{}
	s.Label.AuthzEpoch = int64(d.begins)
	return authz.BuildIndex(s, nil, nil), nil
}

func (d *indexDecider) Know(_ context.Context, idx *authz.Index, _ authz.Subject, _ string) (*authz.Index, error) {
	return idx, nil
}

func (d *indexDecider) record(question string, idx *authz.Index) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.seen == nil {
		d.seen = map[string][]*authz.Index{}
	}
	d.seen[question] = append(d.seen[question], idx)
}

func (d *indexDecider) RequireOn(_ context.Context, idx *authz.Index, _ authz.Subject, _ string) error {
	d.record("require", idx)
	return nil
}

func (d *indexDecider) HoldsOn(idx *authz.Index, _ authz.Subject, _ string) bool {
	d.record("holds", idx)
	return true
}

func (d *indexDecider) DecideOn(_ context.Context, idx *authz.Index, _ authz.Subject, _ authz.Coordinate) authz.Decision {
	d.record("decide", idx)
	return authz.Decision{Outcome: authz.Allow}
}

func (d *indexDecider) ReadableOn(_ context.Context, idx *authz.Index, _ authz.Subject, keys []string) ([]string, error) {
	d.record("readable", idx)
	return keys, nil
}

func (d *indexDecider) ExposedOn(idx *authz.Index, _ authz.Subject, keys []string) []string {
	d.record("exposed", idx)
	return keys
}

// stepServer is a Server whose one operation carries the declaration q, behind
// the shared step, with d as its decision point.
func stepServer(d *indexDecider, q Requirement) (*Server, http.Handler, *http.Handler) {
	const pattern = "GET /api/v1/things/{key}"
	doc := &Document{ops: map[string]Operation{pattern: {ID: "getThing", Pattern: pattern, Requirement: q}}}
	s := New(Options{Document: doc, Access: NewAccess(d), Repositories: RepositoryServices{Authz: d}})
	var inner http.Handler
	h := s.authorize(pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { inner.ServeHTTP(w, r) }))
	return s, h, &inner
}

func TestR_AUTHZ_015_TheSharedStepAndTheHandlerDecideOnOneIndex(t *testing.T) {
	d := &indexDecider{}
	s, step, handler := stepServer(d, Requirement{Permission: "repositories:write-config"})
	var stepEpoch, handlerEpoch int64
	*handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		held, ok := edge.AccessFrom(ctx)
		if !ok {
			t.Error("the shared step stored no holder")
			return
		}
		stepEpoch = held.Epoch()
		// What the handlers of the repository operations do: ask the capability through the
		// Access, take the holder, decide, filter.
		if _, err := s.holds(ctx, capabilityReadConfig); err != nil {
			t.Error(err)
		}
		ctx, acc, err := requestHolder(ctx, s.o.Repositories.Authz, "generic-local")
		if err != nil {
			t.Error(err)
			return
		}
		if acc != held {
			t.Error("the handler took a holder of its own")
		}
		acc.Decide(ctx, authz.Coordinate{Repository: "generic-local"})
		_, _ = acc.Readable(ctx, []string{"generic-local"})
		acc.Exposed([]string{"generic-local"})
		handlerEpoch = acc.Epoch()
	})
	step.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/things/generic-local", nil))

	if d.begins != 1 {
		t.Fatalf("%d indexes begun for one request", d.begins)
	}
	if stepEpoch != 1 || handlerEpoch != stepEpoch {
		t.Fatalf("the step decided on epoch %d, the handler on %d", stepEpoch, handlerEpoch)
	}
	var first *authz.Index
	for question, idxs := range d.seen {
		for _, idx := range idxs {
			if first == nil {
				first = idx
			}
			if idx != first {
				t.Errorf("%s was answered on another index", question)
			}
		}
	}
	for _, q := range []string{"require", "holds", "decide", "readable", "exposed"} {
		if len(d.seen[q]) != 1 {
			t.Errorf("%s was asked %d times", q, len(d.seen[q]))
		}
	}
}

func TestR_AUTHZ_015_TheStepBeginsForTheFormsThatDecide(t *testing.T) {
	run := func(q Requirement, d *indexDecider) *httptest.ResponseRecorder {
		_, step, handler := stepServer(d, q)
		*handler = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
		rec := httptest.NewRecorder()
		step.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/things/npm-internal?repository=npm-internal", nil))
		return rec
	}
	t.Run("a capability, a role and an action each take one index", func(t *testing.T) {
		for _, q := range []Requirement{{Permission: "settings:read"}, {Role: "admin"}, {Action: authz.Read, Repository: "query.repository"}} {
			d := &indexDecider{}
			run(q, d)
			if d.begins != 1 {
				t.Errorf("%s: %d indexes", q, d.begins)
			}
		}
	})
	t.Run("the action form makes the repository of the request known first", func(t *testing.T) {
		d := &indexDecider{}
		run(Requirement{Action: authz.Read, Repository: "query.repository"}, d)
		if len(d.keys) != 1 || !slices.Equal(d.keys[0], []string{"npm-internal"}) {
			t.Fatalf("keys = %v", d.keys)
		}
		if len(d.seen["decide"]) != 1 {
			t.Fatalf("decisions: %d", len(d.seen["decide"]))
		}
	})
	t.Run("a declaration that decides nothing takes no index, so it cannot start to answer 503", func(t *testing.T) {
		for _, q := range []Requirement{{Public: true}, {Authenticated: true}} {
			d := &indexDecider{failure: authz.Decision{Outcome: authz.DenyStale, Reason: authz.ReasonStale}.Err()}
			rec := run(q, d)
			if d.begins != 0 || rec.Code != http.StatusOK && rec.Code != http.StatusUnauthorized {
				t.Errorf("%s: %d begins, status %d", q, d.begins, rec.Code)
			}
		}
	})
	t.Run("the refusal to begin is the answer and the handler does not run", func(t *testing.T) {
		d := &indexDecider{failure: authz.Decision{Outcome: authz.DenyStale, Reason: authz.ReasonStale}.Err()}
		_, step, handler := stepServer(d, Requirement{Permission: "settings:read"})
		ran := false
		*handler = http.HandlerFunc(func(http.ResponseWriter, *http.Request) { ran = true })
		rec := httptest.NewRecorder()
		step.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/things/x", nil))
		if e, _ := errs.As(d.failure); e == nil || rec.Code != e.Status() || ran {
			t.Fatalf("status %d, handler ran %v", rec.Code, ran)
		}
	})
}

func TestR_AUTHZ_015_AHandlerThatReceivedNoHolderBeginsOneAndPassesItOn(t *testing.T) {
	d := &indexDecider{}
	s := New(Options{Access: NewAccess(d), Repositories: RepositoryServices{Authz: d}})
	ctx := context.Background()
	ctx, acc, err := requestHolder(ctx, s.o.Repositories.Authz, "generic-local")
	if err != nil || acc == nil {
		t.Fatalf("%v %v", acc, err)
	}
	// The permission questions the handler asks next use the holder of the context.
	if _, err := s.holds(ctx, capabilityReadConfig); err != nil {
		t.Fatal(err)
	}
	if err := s.o.Access.Require(ctx, "settings:read"); err != nil {
		t.Fatal(err)
	}
	if d.begins != 1 {
		t.Fatalf("%d indexes for one request", d.begins)
	}
	t.Run("without a decision point there is no holder and no failure of its own", func(t *testing.T) {
		got, nilAcc, err := requestHolder(context.Background(), nil)
		if err != nil || nilAcc != nil || got == nil {
			t.Fatalf("%v %v", nilAcc, err)
		}
	})
	t.Run("an Authorizer that cannot decide per request is no decision point", func(t *testing.T) {
		if DeciderOf(authorizerOnly{}) != nil {
			t.Fatal("an Authorizer without the per-request forms was taken for a Decider")
		}
		if DeciderOf(nil) != nil {
			t.Fatal("nil is a decision point")
		}
	})
}

type authorizerOnly struct{ authz.Authorizer }
