package formathost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/repository"
	vt "github.com/guided-traffic/trove/internal/virtual/virtualtest"
)

func virtualRequest(t *testing.T, v repository.Repository) *formats.Request {
	t.Helper()
	rq := &formats.Request{
		Request: httptest.NewRequest(http.MethodGet, "/api/probe/"+v.Name.String()+"/x", nil),
		Repo:    &v,
		Route:   &formats.Route{Name: "probe.get", Kind: formats.Download},
		Subject: authz.Subject{Name: "alice", Endpoint: "default"},
	}
	(&Host{}).Begin(rq)
	return rq
}

// TestR_AUTHZ_013_AMemberRequestCarriesTheOutermostAddressedKeyIntoBothCoordinates: the decision of
// a member read (coordinate, Host.Authorize) and the populate check of a remote member
// (populateCoordinate) name the virtual repository the client addressed (Q-AUTHZ-165, Q-ARCH-252).
func TestR_AUTHZ_013_AMemberRequestCarriesTheOutermostAddressedKeyIntoBothCoordinates(t *testing.T) {
	remote := vt.RemoteRepo(2, "member-remote")
	v := vt.VirtualRepo(9, "all", 60, nil, vt.LocalRepo(1, "member-local"), remote)
	v.Format = "probe"
	rq := virtualRequest(t, v)

	if got := rq.Addressed(); got != qr("all") {
		t.Fatalf("a request that is not derived addresses its own repository, got %q", got)
	}
	mrq, err := rq.Member(&remote)
	if err != nil {
		t.Fatal(err)
	}
	if mrq.Repo.Name != qr("member-remote") || mrq.Addressed() != qr("all") {
		t.Fatalf("member request on %q addressed %q", mrq.Repo.Name, mrq.Addressed())
	}
	if rq.Repo.Name != qr("all") || rq.Addressed() != qr("all") {
		t.Fatal("deriving a member request changed the request it came from")
	}

	if c := coordinate(mrq, formats.Target{Package: "left-pad", Actions: []formats.Action{formats.ActionRead}}); c.Repository != qr("member-remote") || c.Addressed != qr("all") {
		t.Fatalf("coordinate() = repository %q addressed %q", c.Repository, c.Addressed)
	}
	if c := populateCoordinate(mrq, formats.PathInfo{Package: "left-pad"}, formats.Path{Path: "left-pad"}); c.Repository != qr("member-remote") || c.Addressed != qr("all") {
		t.Fatalf("populateCoordinate() = repository %q addressed %q", c.Repository, c.Addressed)
	}

	d := &fakeDecider{}
	h := newTestHost(t, d)
	if err := h.Authorize(context.Background(), mrq, formats.Target{Package: "left-pad", Actions: []formats.Action{formats.ActionRead}}); err != nil {
		t.Fatal(err)
	}
	if d.got.Repository != qr("member-remote") || d.got.Addressed != qr("all") {
		t.Fatalf("the decision was made for %+v", d.got)
	}
}

// TestR_AUTHZ_013_TheOutermostAddressedKeyIsKeptOnADerivedRequest: a request derived from a derived
// request keeps the key the client named, not the intermediate repository.
func TestR_AUTHZ_013_TheOutermostAddressedKeyIsKeptOnADerivedRequest(t *testing.T) {
	leaf := vt.RemoteRepo(3, "leaf")
	inner := vt.VirtualRepo(8, "inner", 60, nil, leaf)
	outer := vt.VirtualRepo(9, "outer", 60, nil, inner)
	rq := virtualRequest(t, outer)
	m1, err := rq.Member(&outer.Members()[0])
	if err != nil {
		t.Fatal(err)
	}
	m2, err := m1.Member(&m1.Repo.Members()[0])
	if err != nil {
		t.Fatal(err)
	}
	if m2.Repo.Name != qr("leaf") || m2.Addressed() != qr("outer") {
		t.Fatalf("leaf request on %q addressed %q, want addressed outer", m2.Repo.Name, m2.Addressed())
	}
}

// TestR_AUTHZ_013_AMemberRequestIsRefusedForARepositoryThatIsNoMember: the addressed key is never
// lent to a repository the virtual repository does not hold.
func TestR_AUTHZ_013_AMemberRequestIsRefusedForARepositoryThatIsNoMember(t *testing.T) {
	v := vt.VirtualRepo(9, "all", 60, nil, vt.LocalRepo(1, "member-local"))
	stranger := vt.RemoteRepo(5, "stranger")
	rq := virtualRequest(t, v)
	if got, err := rq.Member(&stranger); err == nil || got != nil {
		t.Fatalf("a non-member was given the standing of a member: %v, %v", got, err)
	}
	if got, err := rq.Member(nil); err == nil || got != nil {
		t.Fatalf("nil accepted: %v, %v", got, err)
	}
	local := vt.LocalRepo(1, "member-local")
	direct := virtualRequest(t, local)
	if _, err := direct.Member(&local); err == nil {
		t.Fatal("a repository that is not virtual has no members")
	}
}

// TestR_AUTHZ_013_TheDeployTargetReadCarriesNoAddressedKey: a read that stands for the write is
// decided on the target itself, so a scope must name the target (Q-ARCH-252).
func TestR_AUTHZ_013_TheDeployTargetReadCarriesNoAddressedKey(t *testing.T) {
	target := vt.LocalRepo(1, "team-local")
	v := vt.VirtualRepo(9, "all", 60, &target, target)
	v.Format = "probe"
	rq := virtualRequest(t, v)
	trq := rq.WriteTarget(&target)
	if trq.Repo.Name != qr("team-local") || trq.Addressed() != qr("team-local") {
		t.Fatalf("target request on %q addressed %q", trq.Repo.Name, trq.Addressed())
	}
	// Also when the request it comes from was derived itself.
	mrq, err := rq.Member(&target)
	if err != nil {
		t.Fatal(err)
	}
	if got := mrq.WriteTarget(&target).Addressed(); got != qr("team-local") {
		t.Fatalf("addressed %q after the write re-target of a member request", got)
	}
	if c := coordinate(trq, formats.Target{Package: "left-pad", Actions: []formats.Action{formats.ActionRead}}); c.Addressed != qr("team-local") {
		t.Fatalf("coordinate() addressed %q", c.Addressed)
	}
}
