package rawfiles

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/bastionmain"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/files"
	"github.com/guided-traffic/trove/internal/items"
)

// refusingDecider answers every question with its outcome and records, in one sequence with the observer, when
// it was asked.
type refusingDecider struct {
	decider
	outcome authz.Outcome
	seq     *sequence
}

func (d *refusingDecider) DecideOn(ctx context.Context, idx *authz.Index, sub authz.Subject, c authz.Coordinate) authz.Decision {
	d.decider.DecideOn(ctx, idx, sub, c)
	d.seq.add("decide")
	return authz.Decision{Outcome: d.outcome}
}

type sequence struct {
	mu    sync.Mutex
	steps []string
}

func (s *sequence) add(step string) {
	s.mu.Lock()
	s.steps = append(s.steps, step)
	s.mu.Unlock()
}

func (s *sequence) count(step string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, x := range s.steps {
		if x == step {
			n++
		}
	}
	return n
}

// decidedObserver records when the handler told it the decision.
type decidedObserver struct {
	recordingObserver
	seq *sequence
}

func (o *decidedObserver) Decided(*http.Request, bastionmain.RouteDecision) { o.seq.add("decided") }

// TestR_BAS_013_AFilesReadTellsTheObserverOnlyAnAllowedDecision: the observer learns the decision of a /files/
// read after the service allowed it, so a refused read carries no Trove-Bastion-Read (Q-BAS-71): the marking
// asks the shape only in Decided. An allowed read tells it once, after the decision, and before the 404 of a
// missing item, which a full reader's marking may need for `absent`.
func TestR_BAS_013_AFilesReadTellsTheObserverOnlyAnAllowedDecision(t *testing.T) {
	var sum blob.Digest
	sum[0] = 1
	file := items.Item{ID: 3, RepoID: 5, Repository: "generic-local", Path: "a.bin", Kind: items.KindFile, Size: 3, Digest: sum, SHA512: make([]byte, 64)}
	serve := func(outcome authz.Outcome, st *store, path string) (*httptest.ResponseRecorder, *sequence) {
		seq := &sequence{}
		d := &refusingDecider{outcome: outcome, seq: seq}
		o := &decidedObserver{seq: seq}
		svc, err := files.New(files.Options{Items: st, Repos: repos{}})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req = req.WithContext(bastionmain.WithObserver(req.Context(), o))
		rec := httptest.NewRecorder()
		New(Options{Service: svc, Decider: d}).ServeHTTP(rec, req)
		return rec, seq
	}
	for _, outcome := range []authz.Outcome{authz.DenyHidden, authz.DenyVisible, authz.DenyUnauthenticated} {
		rec, seq := serve(outcome, &store{file: file, body: []byte("abc")}, "/files/generic-local/a.bin")
		if rec.Code < 400 {
			t.Fatalf("outcome %d: status %d", outcome, rec.Code)
		}
		if n := seq.count("decided"); n != 0 {
			t.Errorf("outcome %d: a refused read told the observer a decision %d times (%v)", outcome, n, seq.steps)
		}
	}
	rec, seq := serve(authz.Allow, &store{file: file, body: []byte("abc")}, "/files/generic-local/a.bin")
	if rec.Code != http.StatusOK || seq.count("decided") != 1 || seq.steps[0] != "decide" {
		t.Fatalf("an allowed read: %d, steps %v", rec.Code, seq.steps)
	}
	rec, seq = serve(authz.Allow, &store{}, "/files/generic-local/missing.bin")
	if rec.Code != http.StatusNotFound || seq.count("decided") != 1 {
		t.Fatalf("an allowed read of a missing item: %d, steps %v", rec.Code, seq.steps)
	}
}
