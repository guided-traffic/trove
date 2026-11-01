package v1

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
)

// scopeDecider counts the audited decisions: DecideOn writes an audit event and bumps the decision
// counters, which the exposure check must not do for each version it lists (Q-EP-66).
type scopeDecider struct {
	authz.Decider
	decisions atomic.Int64
}

func (d *scopeDecider) DecideOn(context.Context, *authz.Index, authz.Subject, authz.Coordinate) authz.Decision {
	d.decisions.Add(1)
	return authz.Decision{Outcome: authz.Allow}
}

// checkScoper hands out a read scope that reads everything except one version of one package.
type checkScoper struct {
	err   error
	asked atomic.Int64
	keys  []string
}

func (s *checkScoper) ReadScopeOn(_ context.Context, _ *authz.Index, _ authz.Subject, keys []string) (authz.ReadScope, error) {
	s.keys = keys
	if s.err != nil {
		return nil, s.err
	}
	return checkScope{asked: &s.asked}, nil
}

type checkScope struct{ asked *atomic.Int64 }

func (checkScope) Shape(string) authz.ReadShape { return authz.ReadRows }

func (s checkScope) MayRead(repo, pkg, version string) bool {
	s.asked.Add(1)
	return repo == "npm-local" && (pkg != "@acme/sdk" || version != "2.0.0")
}

func checkServer(sc authz.ReadScoper) *Server {
	return &Server{o: Options{Endpoints: EndpointServices{Scoper: sc}}}
}

// TestR_EP_016_TheExposureCheckKeepsFromTheListingWhatTheReadScopeRefuses: the filter handed to the
// service is the caller's read scope, so a version it refuses is dropped. A filter that allowed
// everything would list "2.0.0" and fail here (Q-EP-62, Q-EP-66).
func TestR_EP_016_TheExposureCheckKeepsFromTheListingWhatTheReadScopeRefuses(t *testing.T) {
	sc := &checkScoper{}
	dec := &scopeDecider{}
	acc := &edge.Access{Subject: authz.Subject{Name: "alice", Endpoint: "customers"}, D: dec}
	may, err := checkServer(sc).checkReadFilter(context.Background(), acc, "npm-local")
	if err != nil || may == nil {
		t.Fatalf("filter %v, err %v", may != nil, err)
	}
	if !may("@acme/sdk", "") || !may("@acme/sdk", "1.0.0") {
		t.Error("a package and a version the scope reads are refused")
	}
	if may("@acme/sdk", "2.0.0") {
		t.Error("a version the scope refuses is listed")
	}
	if len(sc.keys) != 1 || sc.keys[0] != "npm-local" {
		t.Errorf("the scope was opened for %v, want the checked repository only", sc.keys)
	}
}

// TestR_EP_016_TheExposureCheckWritesNoDecisionPerListedVersion: a thousand listed versions are a
// thousand questions to the unaudited scope and not one audited decision (Q-EP-66).
func TestR_EP_016_TheExposureCheckWritesNoDecisionPerListedVersion(t *testing.T) {
	sc := &checkScoper{}
	dec := &scopeDecider{}
	acc := &edge.Access{Subject: authz.Subject{Name: "alice", Endpoint: "customers"}, D: dec}
	may, err := checkServer(sc).checkReadFilter(context.Background(), acc, "npm-local")
	if err != nil {
		t.Fatal(err)
	}
	for range 1001 {
		may("@acme/sdk", "1.0.0")
	}
	if got := dec.decisions.Load(); got != 0 {
		t.Fatalf("%d audited decisions for the listed versions, want none", got)
	}
	if got := sc.asked.Load(); got != 1001 {
		t.Fatalf("the scope was asked %d times", got)
	}
}

// TestR_EP_016_TheExposureCheckWithoutAReadScopeListsNothing: no scoper, no filter, and the
// service reads a nil filter as "nothing" (Q-EP-66); a refusal to scope is the refusal of the check.
func TestR_EP_016_TheExposureCheckWithoutAReadScopeListsNothing(t *testing.T) {
	acc := &edge.Access{Subject: authz.Subject{Name: "alice"}, D: &scopeDecider{}}
	if may, err := checkServer(nil).checkReadFilter(context.Background(), acc, "npm-local"); may != nil || err != nil {
		t.Fatalf("filter %v, err %v", may != nil, err)
	}
	refusal := errs.E(errs.Unavailable, errs.CodeUnavailable, "no index")
	if _, err := checkServer(&checkScoper{err: refusal}).checkReadFilter(context.Background(), acc, "npm-local"); err == nil {
		t.Fatal("a refusal to scope was swallowed")
	}
}
