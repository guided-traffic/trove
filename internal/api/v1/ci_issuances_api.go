package v1

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/ci"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// The issuance operations (R-CI-020), work package W5: tokens:read for the list and the read, tokens:revoke for
// the bulk revoke (the declarations are in the document; the auditor role holds tokens:read, R-AUTHZ-011). The
// list pages by cursor (R-API-010) newest first; the bulk revoke needs `issuer` and at least one of projectId,
// pipelineId, jobId, mapping (422 otherwise) and answers {"revoked": n}. A record holds no token, no hash and no
// claim outside the allowlist; the service ([ci.Issuances], [ci.Revoker]) owns the queries, the transaction and
// the events.

// issuanceListSpec is what the list accepts besides limit and cursor (R-API-011).
var issuanceListSpec = ListSpec{Filters: []string{"issuer", "projectId", "projectPath", "pipelineId", "jobId", "mapping", "active", "from", "to"}}

func (s *Server) ciIssuanceService() (ci.Issuances, error) {
	if s.o.CI.Issuances == nil {
		return nil, unavailable("The CI issuance service")
	}
	return s.o.CI.Issuances, nil
}

// ListCiIssuances implements GET /api/v1/ci/issuances.
func (s *Server) ListCiIssuances(w http.ResponseWriter, r *http.Request, _ gen.ListCiIssuancesParams) {
	svc, err := s.ciIssuanceService()
	var lq ListQuery
	var f ci.IssuanceFilter
	if err == nil {
		lq, f, err = s.issuanceQuery(r, issuanceListSpec)
	}
	var page ci.IssuancePage
	if err == nil {
		page, err = svc.List(r.Context(), f)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	WritePage(w, r, MapPage(NewPage(lq, page.Items, ci.IssuanceSortKey), issuanceBody))
}

// GetCiIssuance implements GET /api/v1/ci/issuances/{tokenId}.
func (s *Server) GetCiIssuance(w http.ResponseWriter, r *http.Request, tokenID openapi_types.UUID) {
	svc, err := s.ciIssuanceService()
	var is ci.Issuance
	var use ci.Usage
	if err == nil {
		is, use, err = svc.Get(r.Context(), tokenID.String())
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	out := gen.CiIssuanceDetail{Issuance: issuanceBody(is)}
	out.Usage.Requests, out.Usage.LastUsedAt, out.Usage.PackagesDeployed = use.Requests, use.LastUsedAt, orEmpty(use.PackagesDeployed)
	writeJSON(w, r, http.StatusOK, out)
}

// RevokeCiIssuances implements POST /api/v1/ci/issuances/revoke.
func (s *Server) RevokeCiIssuances(w http.ResponseWriter, r *http.Request) {
	if s.o.CI.Revoker == nil {
		httpserver.Problem(w, r, unavailable("The CI revocation service"))
		return
	}
	var body gen.CiRevokeRequest
	err := DecodeJSON(w, r, &body, s.bodyLimit())
	var done ci.Revoked
	if err == nil {
		done, err = s.o.CI.Revoker.RevokeMatching(r.Context(), caller(r), ci.RevokeFilter{
			Issuer: body.Issuer, ProjectID: ciOptional(body.ProjectId), PipelineID: ciOptional(body.PipelineId), JobID: ciOptional(body.JobId),
			Mapping: ciOptional(body.Mapping),
		})
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, gen.CiRevokeResult{Revoked: len(done.TokenIDs)})
}

// issuanceQuery parses the list query: the filters, active as a boolean, from and to as RFC 3339 instants, and
// the cursor as the sort key of the last record of the previous page.
func (s *Server) issuanceQuery(r *http.Request, spec ListSpec) (ListQuery, ci.IssuanceFilter, error) {
	lq, err := ParseList(r, spec, s.o.API.Config)
	if err != nil {
		return lq, ci.IssuanceFilter{}, err
	}
	f := ci.IssuanceFilter{
		Issuer: lq.First("issuer"), ProjectID: lq.First("projectId"), ProjectPath: lq.First("projectPath"),
		PipelineID: lq.First("pipelineId"), JobID: lq.First("jobId"), Mapping: lq.First("mapping"), Limit: lq.FetchLimit(),
	}
	switch lq.First("active") {
	case "":
	case "true", "false":
		b := lq.First("active") == "true"
		f.Active = &b
	default:
		return lq, f, errs.E(errs.Invalid, errs.CodeInvalid, "The query parameter active must be true or false.")
	}
	for name, dst := range map[string]**time.Time{"from": &f.From, "to": &f.To} {
		if lq.First(name) == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, lq.First(name))
		if err != nil {
			return lq, f, errs.E(errs.Invalid, errs.CodeInvalid, "The query parameter "+name+" must be an RFC 3339 time.")
		}
		*dst = &t
	}
	if len(lq.After) > 0 {
		if len(lq.After) != 2 {
			return lq, f, invalidCursor()
		}
		f.Cursor = strings.Join(lq.After, ",")
	}
	return lq, f, nil
}

// issuanceBody renders a record as the API shows it (CiIssuance).
func issuanceBody(is ci.Issuance) gen.CiIssuance {
	out := gen.CiIssuance{
		TokenId: parseUUID(is.TokenID), Issuer: is.Issuer, MappingNames: orEmpty(is.MappingNames),
		SubjectKind: gen.CiIssuanceSubjectKind(is.SubjectKind), ProjectId: is.ProjectID, JobId: is.JobID,
		Claims: is.Claims, GrantedScope: is.GrantedScope, CrossProject: is.CrossProject,
		IssuedAt: is.IssuedAt, ExpiresAt: is.ExpiresAt, RevokedAt: is.RevokedAt, Active: is.Active(), State: gen.CiIssuanceState(is.State),
		Jti: nonEmpty(is.JTI), ProjectPath: nonEmpty(is.ProjectPath), NamespaceId: nonEmpty(is.NamespaceID),
		PipelineId: nonEmpty(is.PipelineID), Ref: nonEmpty(is.Ref), RefType: nonEmpty(is.RefType), RefProtected: is.RefProtected,
		PipelineSource: nonEmpty(is.PipelineSource), Environment: nonEmpty(is.Environment), Sha: nonEmpty(is.SHA),
		UserId: nonEmpty(is.UserID), UserLogin: nonEmpty(is.UserLogin), RequestedScope: nonEmpty(is.RequestedScope),
		ClientIp: nonEmpty(is.ClientIP), UserAgent: nonEmpty(is.UserAgent),
	}
	if out.Claims == nil {
		out.Claims = map[string]any{}
	}
	// The bundle is shown in its stored form, the JSON of claims.Frozen (Q-CI-17).
	raw, _ := json.Marshal(is.Grants)
	_ = json.Unmarshal(raw, &out.Grants)
	if out.Grants == nil {
		out.Grants = map[string]any{}
	}
	return out
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ciOptional is the value of an optional string member, "" when absent.
func ciOptional(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
