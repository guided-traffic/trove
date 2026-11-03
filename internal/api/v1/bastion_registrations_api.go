package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/bastionmain"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta"
)

// The Bastion registrations and their tokens (R-BAS-004, Q-BAS-24). The declarations are in the document:
// endpoints:read for the reads, endpoints:write for the writes, x-trove-if-match on the update, and for the
// revocation of one token the coarse authenticated form, because the handler admits endpoints:write or
// tokens:revoke. The rules are those of bastionmain.Registrations; the handlers decode, call it and encode.

// Capabilities a Bastion registration is managed with (Q-BAS-24).
const (
	capabilityEndpointsWrite = "endpoints:write"
	capabilityTokensRevoke   = "tokens:revoke"
)

// bastionsReady answers 503 and reports false when the replica has no registration service (a worker, or the
// HTTP shell of a test).
func (s *Server) bastionsReady(w http.ResponseWriter, r *http.Request) bool {
	if s.o.Bastions == nil {
		httpserver.Problem(w, r, unavailable("The Bastion registrations"))
		return false
	}
	return true
}

func toBastion(b bastionmain.Registration) gen.Bastion {
	out := gen.Bastion{
		Id: uuidOf(b.ID), Name: b.Name, Description: b.Description, Enabled: b.Enabled, CreatedAt: utc(b.CreatedAt),
		Tokens: make([]gen.BastionTokenSummary, 0, len(b.Tokens)), Contacts: make([]gen.BastionContact, 0, len(b.Contacts)),
	}
	for _, t := range b.Tokens {
		sum := gen.BastionTokenSummary{
			Id: uuidOf(t.ID), Hint: t.Hint, Description: t.Description, CreatedAt: utc(t.CreatedAt), ExpiresAt: utc(t.ExpiresAt),
		}
		if t.LastUsedAt != nil {
			sum.LastUsedAt = refTo(utc(*t.LastUsedAt))
		}
		out.Tokens = append(out.Tokens, sum)
	}
	for _, c := range b.Contacts {
		out.Contacts = append(out.Contacts, gen.BastionContact{
			Replica: c.Replica, ContactAt: utc(c.ContactAt), ClientAddress: optString(c.ClientAddress), Release: c.Release,
			InterfaceVersion: c.InterfaceVersion, BaseUrl: c.BaseURL,
		})
	}
	return out
}

// ListBastions implements GET /api/v1/bastions.
func (s *Server) ListBastions(w http.ResponseWriter, r *http.Request) {
	if !s.bastionsReady(w, r) {
		return
	}
	rows, err := s.o.Bastions.List(r.Context())
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	items := make([]gen.Bastion, 0, len(rows))
	for _, b := range rows {
		items = append(items, toBastion(b))
	}
	writeJSON(w, r, http.StatusOK, gen.BastionList{Items: items})
}

// CreateBastion implements POST /api/v1/bastions.
func (s *Server) CreateBastion(w http.ResponseWriter, r *http.Request) {
	if !s.bastionsReady(w, r) {
		return
	}
	body, err := ReadBody(w, r, MediaJSON, s.bodyLimit())
	var in gen.NewBastion
	if err == nil {
		err = StrictUnmarshal(body, &in)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	b, err := s.o.Bastions.Create(r.Context(), s.caller(r), bastionmain.NewRegistration{Name: in.Name, Description: derefString(in.Description), Source: "api"})
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	w.Header().Set("Location", location(r, "/api/v1/bastions/"+b.Name))
	SetETag(w, b.Revision, "")
	writeJSON(w, r, http.StatusCreated, toBastion(b))
}

// GetBastion implements GET /api/v1/bastions/{name}.
func (s *Server) GetBastion(w http.ResponseWriter, r *http.Request, name string, _ gen.GetBastionParams) {
	if !s.bastionsReady(w, r) {
		return
	}
	b, err := s.o.Bastions.Get(r.Context(), name)
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	if NotModified(w, r, ETag(b.Revision, "")) {
		return
	}
	SetETag(w, b.Revision, "")
	writeJSON(w, r, http.StatusOK, toBastion(b))
}

// bastionPatchBody is a merge patch of a registration. The read-only members are accepted and ignored.
type bastionPatchBody struct {
	Description json.RawMessage `json:"description"`
	Enabled     json.RawMessage `json:"enabled"`

	ID        json.RawMessage `json:"id"`
	Name      json.RawMessage `json:"name"`
	CreatedAt json.RawMessage `json:"createdAt"`
	Tokens    json.RawMessage `json:"tokens"`
	Contacts  json.RawMessage `json:"contacts"`
}

// UpdateBastion implements PATCH /api/v1/bastions/{name}.
func (s *Server) UpdateBastion(w http.ResponseWriter, r *http.Request, name string, _ gen.UpdateBastionParams) {
	if !s.bastionsReady(w, r) {
		return
	}
	pre, err := ParseIfMatch(r)
	var raw []byte
	if err == nil {
		raw, err = ReadMergePatch(w, r, s.bodyLimit())
	}
	var in bastionPatchBody
	if err == nil {
		err = StrictUnmarshal(raw, &in)
	}
	var patch bastionmain.Patch
	if err == nil {
		patch.Description, err = patchString(in.Description, "/description")
	}
	if err == nil {
		patch.Enabled, err = patchEnabled(in.Enabled, "/enabled")
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	b, err := s.o.Bastions.Update(r.Context(), s.caller(r), name, patch, expectedRevision(pre))
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	SetETag(w, b.Revision, "")
	writeJSON(w, r, http.StatusOK, toBastion(b))
}

// patchEnabled reads the member enabled of a merge patch: absent is unchanged; null and any other type are
// refused, because a null must never switch a registration off.
func patchEnabled(raw json.RawMessage, pointer string) (*bool, error) {
	if raw == nil {
		return nil, nil
	}
	var v bool
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, malformed(pointer, "type", "the value must be true or false", err)
	}
	return &v, nil
}

// DeleteBastion implements DELETE /api/v1/bastions/{name}.
func (s *Server) DeleteBastion(w http.ResponseWriter, r *http.Request, name string) {
	if !s.bastionsReady(w, r) {
		return
	}
	if err := s.o.Bastions.Delete(r.Context(), s.caller(r), name); err != nil {
		failIdentity(w, r, err)
		return
	}
	noContent(w)
}

// maxBastionTTLSeconds bounds ttlSeconds before it becomes a duration, so that the multiplication cannot
// overflow; the configured maximum is checked by the token service.
const maxBastionTTLSeconds = 1 << 31

// CreateBastionToken implements POST /api/v1/bastions/{name}/tokens (R-BAS-004, R-AUTH-022): the token is
// issued behind the Idempotency-Key and its value is in this answer only.
func (s *Server) CreateBastionToken(w http.ResponseWriter, r *http.Request, name string, _ gen.CreateBastionTokenParams) {
	if !s.bastionsReady(w, r) || !s.identityReady(w, r) {
		return
	}
	body, err := ReadBody(w, r, MediaJSON, s.bodyLimit())
	var in gen.NewBastionToken
	if err == nil {
		err = StrictUnmarshal(body, &in)
	}
	var ttl time.Duration
	if err == nil && in.TtlSeconds != nil {
		if *in.TtlSeconds < 1 || *in.TtlSeconds > maxBastionTTLSeconds {
			err = errs.E(errs.Invalid, errs.CodeInvalidTTL, "ttlSeconds must be at least 60 and at most the maximum of auth.tokens.bst.max_ttl.")
		} else {
			ttl = time.Duration(*in.TtlSeconds) * time.Second
		}
	}
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	c := s.caller(r)
	req := bastionmain.TokenRequest{Description: derefString(in.Description), TTL: ttl, Via: via(httpserver.PrincipalFrom(r.Context()))}
	err = s.idem.Do(w, r, body, IdempotentOp{
		Run: func(ctx context.Context, tx meta.Tx) (Outcome, error) {
			created, err := s.o.Bastions.CreateTokenTx(ctx, tx, c, name, req)
			if err != nil {
				return Outcome{}, identityErr(err)
			}
			return Outcome{
				Status: http.StatusCreated, Body: toBastionTokenCreated(created), SecretID: created.Record.ID,
				Header: map[string]string{"Location": location(r, "/api/v1/bastions/"+name)},
			}, nil
		},
		SecretReplay: tokenIssued,
	})
	if err != nil {
		failIdentity(w, r, err)
	}
}

func toBastionTokenCreated(c token.Created) gen.BastionTokenCreated {
	rec := c.Record
	out := gen.BastionTokenCreated{Id: uuidOf(rec.ID), Token: c.Secret, Hint: rec.Hint, Description: rec.Description, CreatedAt: utc(rec.CreatedAt)}
	if rec.ExpiresAt != nil {
		out.ExpiresAt = utc(*rec.ExpiresAt)
	}
	return out
}

// RevokeBastionToken implements DELETE /api/v1/bastions/{name}/tokens/{id}: endpoints:write or tokens:revoke
// (Q-BAS-24). A token that is revoked already answers 204 as well.
func (s *Server) RevokeBastionToken(w http.ResponseWriter, r *http.Request, name string, id openapi_types.UUID) {
	if !s.bastionsReady(w, r) {
		return
	}
	ctx := r.Context()
	if err := s.requireAny(ctx, capabilityEndpointsWrite, capabilityTokensRevoke); err != nil {
		failIdentity(w, r, err)
		return
	}
	if err := s.o.Bastions.RevokeToken(ctx, s.caller(r), name, id.String()); err != nil {
		failIdentity(w, r, err)
		return
	}
	noContent(w)
}

// requireAny admits a caller that holds at least one of the capabilities; a failure to decide is returned as it
// is, a caller that holds none gets 403.
func (s *Server) requireAny(ctx context.Context, capabilities ...string) error {
	for _, c := range capabilities {
		ok, err := s.holds(ctx, c)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	return errs.E(errs.Forbidden, errs.CodeForbidden, "The permission "+capabilities[0]+" or "+capabilities[len(capabilities)-1]+" is required.")
}
