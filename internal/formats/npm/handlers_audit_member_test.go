package npm

import (
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/repository"
)

// TestR_AUTHZ_013_TheAuditForwarderAsksForTheMemberOnBehalfOfTheAddressedVirtualRepository: the
// read of the forwarding member is decided with the virtual repository the client addressed, so a
// token scoped to that repository by its exact key runs the audit through it (Q-AUTHZ-163,
// Q-ARCH-252). The integration case is TestR_AUTHZ_013_ASetupTokenRunsTheAuditThroughTheVirtualRepository.
func TestR_AUTHZ_013_TheAuditForwarderAsksForTheMemberOnBehalfOfTheAddressedVirtualRepository(t *testing.T) {
	h, _, _ := auditHost(t, formats.Virtual, `{}`)
	member := repository.Repository{ID: "2", Name: qr("up"), Class: formats.Remote, Format: "npm", Lifecycle: repository.Active, Config: []byte(`{}`)}
	*h.Repo() = repository.Assemble(*h.Repo(), []repository.Repository{member}, nil, 60)

	r := audit(t, h, auditBulkPath, `{"lodash":["4.17.21"]}`)
	if r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	var seen bool
	for _, c := range h.Authorizations() {
		if c.Repo != "it/up" {
			continue
		}
		seen = true
		if c.Addressed != "it/main" {
			t.Errorf("the read of the member %q was decided with the addressed repository %q, want it/main", c.Repo, c.Addressed)
		}
	}
	if !seen {
		t.Fatalf("the forwarding member was never asked: %+v", h.Authorizations())
	}
}
