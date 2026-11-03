package v1

import (
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
)

// The Bastion registrations and their tokens (R-BAS-004, Q-BAS-24), work package 1. Every operation is a stub
// until the package fills it. The declarations are in the document: endpoints:read for the reads,
// endpoints:write for the writes, x-trove-if-match on the update, and for the revocation of one token the
// coarse authenticated form, because the handler admits endpoints:write or tokens:revoke. The service behind
// them writes the principal (queries IdentityInsertPrincipal with kind bastion), the registration row and the
// tokens (queries/bastion_registration.sql, IdentityInsertToken) and the audit events bastion.created,
// bastion.updated, bastion.deleted and token.created / token.revoked with resource_type bastion.

// ListBastions implements GET /api/v1/bastions.
// Stub: work package 1 fills it (R-BAS-004): decode the request into gen types, call the registration service, encode the answer, record the audit event.
func (s *Server) ListBastions(w http.ResponseWriter, r *http.Request) {
	s.bastionStub(w, r, "R-BAS-004")
}

// CreateBastion implements POST /api/v1/bastions.
// Stub: work package 1 fills it (R-BAS-004): decode the request into gen types, call the registration service, encode the answer, record the audit event.
func (s *Server) CreateBastion(w http.ResponseWriter, r *http.Request) {
	s.bastionStub(w, r, "R-BAS-004")
}

// GetBastion implements GET /api/v1/bastions/{name}.
// Stub: work package 1 fills it (R-BAS-004): decode the request into gen types, call the registration service, encode the answer, record the audit event.
func (s *Server) GetBastion(w http.ResponseWriter, r *http.Request, _ string, _ gen.GetBastionParams) {
	s.bastionStub(w, r, "R-BAS-004")
}

// UpdateBastion implements PATCH /api/v1/bastions/{name}.
// Stub: work package 1 fills it (R-BAS-004): decode the request into gen types, call the registration service, encode the answer, record the audit event.
func (s *Server) UpdateBastion(w http.ResponseWriter, r *http.Request, _ string, _ gen.UpdateBastionParams) {
	s.bastionStub(w, r, "R-BAS-004")
}

// DeleteBastion implements DELETE /api/v1/bastions/{name}.
// Stub: work package 1 fills it (R-BAS-004): decode the request into gen types, call the registration service, encode the answer, record the audit event.
func (s *Server) DeleteBastion(w http.ResponseWriter, r *http.Request, _ string) {
	s.bastionStub(w, r, "R-BAS-004")
}

// CreateBastionToken implements POST /api/v1/bastions/{name}/tokens.
// Stub: work package 1 fills it (R-BAS-004): decode the request into gen types, call the registration service, encode the answer, record the audit event.
func (s *Server) CreateBastionToken(w http.ResponseWriter, r *http.Request, _ string, _ gen.CreateBastionTokenParams) {
	s.bastionStub(w, r, "R-BAS-004")
}

// RevokeBastionToken implements DELETE /api/v1/bastions/{name}/tokens/{id}.
// Stub: work package 1 fills it (R-BAS-004): decode the request into gen types, call the registration service, encode the answer, record the audit event.
func (s *Server) RevokeBastionToken(w http.ResponseWriter, r *http.Request, _ string, _ openapi_types.UUID) {
	s.bastionStub(w, r, "R-BAS-004")
}
