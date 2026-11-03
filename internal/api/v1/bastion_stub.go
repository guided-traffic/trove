package v1

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/bastionmain"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// bastionStub answers an operation of the Bastion work that its package has not built yet: 501
// bastion_not_implemented when the composition root says so (Options.BastionStubs), 503 as for an unwired
// service otherwise, so that the tests of this package, which wire none, see an operation that is not
// available. requirement names the R-ID that fills the stub. The helper and the flag leave with the last stub.
func (s *Server) bastionStub(w http.ResponseWriter, r *http.Request, requirement string) {
	if !s.o.BastionStubs {
		httpserver.Problem(w, r, unavailable("The Bastion operation of "+requirement))
		return
	}
	httpserver.Problem(w, r, bastionmain.Stub(requirement))
}
