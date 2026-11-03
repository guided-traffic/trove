package v1

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
)

// The handshake operation of the Bastion interface (R-BAS-008, R-BAS-009), work package 2. The Bastion token was
// checked by the recognition stage before the handler runs; the shared authorization step admits the route on
// that proof (the declaration {bastion: true}) and the handler never sees a subject of the decision point.
// Every answer carries iface.HeaderInterface; the bodies are the types of internal/bastion/iface, decoded
// with iface.DecodeStrict.

// BastionHandshake implements GET /api/v1/bastion/handshake.
// Stub: work package 2 fills it (R-BAS-010): the handshake answer and the last-contact upsert (queries/bastion_contact.sql).
func (s *Server) BastionHandshake(w http.ResponseWriter, r *http.Request, _ gen.BastionHandshakeParams) {
	s.bastionStub(w, r, "R-BAS-010")
}
