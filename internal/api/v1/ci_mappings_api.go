package v1

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/auth/jws"
	"github.com/guided-traffic/trove/internal/ci"
	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// The identity-mapping operations and the dry run (R-CI-009, R-CI-019), work package W3. The declarations are
// in the document: ci:read for the reads and the dry run, ci:write with recent authentication for create,
// replace and delete, x-trove-if-match required on the replace. The handlers are thin: the service
// ([ci.Mappings]) owns the rules, the transaction, the principal, the audit events and the revocation.
//
//   - createCiMapping, replaceCiMapping: the answer carries `warnings` (R-CI-010, R-CI-011); 409 priority_taken;
//     the 422 codes carry the pointer of the member. The condition is decoded with claims.ParseCondition, so
//     its pointers are the pointers of the body ("/match/all/1"); the grants are the wire form of claims.Spec.
//   - replaceCiMapping: the ETag is the mapping's version, 428 comes from the binder, 412 carries currentEtag.
//   - deleteCiMapping: If-Match optional.
//   - evaluateCiMappings: takes claims, never a token (R-CI-019); it issues nothing and audits nothing.

func (s *Server) ciMappingService() (ci.Mappings, error) {
	if s.o.CI.Mappings == nil {
		return nil, unavailable("The CI mapping service")
	}
	return s.o.CI.Mappings, nil
}

// ciMappingIn is the request body of a create and a replace (CiMappingInput). The members a GET shows and
// the client may send back are accepted and ignored (R-CI-019).
type ciMappingIn struct {
	Name         string          `json:"name"`
	Description  *string         `json:"description"`
	Priority     int             `json:"priority"`
	Enabled      *bool           `json:"enabled"`
	SubjectKinds *[]string       `json:"subjectKinds"`
	Match        json.RawMessage `json:"match"`
	Grants       claims.Spec     `json:"grants"`
	MaxTTL       *int64          `json:"maxTtlSeconds"`
	Options      *claims.Options `json:"options"`
	Issuer       *string         `json:"issuer"`

	// The members a read shows and a client may send back; they are ignored.
	ReadOnlyTier      json.RawMessage `json:"tier"`
	ReadOnlyProject   json.RawMessage `json:"project"`
	ReadOnlyVersion   json.RawMessage `json:"version"`
	ReadOnlyCreatedBy json.RawMessage `json:"createdBy"`
	ReadOnlyCreatedAt json.RawMessage `json:"createdAt"`
	ReadOnlyUpdatedAt json.RawMessage `json:"updatedAt"`
	ReadOnlyWarnings  json.RawMessage `json:"warnings"`
}

// decodeMapping reads and checks the shape of a mapping body: the condition goes through the parser of the
// claim engine, so a violation answers 422 condition_invalid with its pointer instead of a 400 syntax error.
func (s *Server) decodeMapping(w http.ResponseWriter, r *http.Request) (ci.MappingInput, error) {
	var body ciMappingIn
	if err := DecodeJSON(w, r, &body, s.bodyLimit()); err != nil {
		return ci.MappingInput{}, err
	}
	in := ci.MappingInput{
		Issuer: derefStr(body.Issuer), Name: body.Name, Description: derefStr(body.Description), Priority: body.Priority,
		Enabled: body.Enabled, Grants: body.Grants, MaxTTLSeconds: body.MaxTTL, Options: body.Options,
	}
	if body.SubjectKinds != nil {
		in.SubjectKinds = *body.SubjectKinds
	}
	if len(body.Match) == 0 || string(body.Match) == "null" {
		return ci.MappingInput{}, errs.E(errs.Invalid, errs.CodeValidation, "The member match is required.",
			errs.WithFields(errs.FieldError{Pointer: "/match", Code: "required", Detail: "A mapping has a condition (R-CI-010)."}))
	}
	node, err := claims.ParseCondition(body.Match)
	if err != nil {
		var ce *claims.Error
		if asClaimsError(err, &ce) {
			return ci.MappingInput{}, errs.E(errs.Invalid, string(ce.Reason), ce.Detail,
				errs.WithFields(errs.FieldError{Pointer: "/match" + ce.Pointer, Code: string(ce.Reason), Detail: ce.Detail}))
		}
		return ci.MappingInput{}, err
	}
	in.Match = node
	return in, nil
}

func asClaimsError(err error, target **claims.Error) bool {
	ce, ok := err.(*claims.Error) //nolint:errorlint // ParseCondition returns an *Error itself
	if ok {
		*target = ce
	}
	return ok
}

// ciGrantsOut is the grants of a mapping as the API shows them: the three lists always present.
type ciGrantsOut struct {
	Policies []string             `json:"policies"`
	Groups   []string             `json:"groups"`
	Inline   []claims.InlineGrant `json:"inline"`
}

func grantsOut(g claims.Spec) ciGrantsOut {
	out := ciGrantsOut{Policies: orEmpty(g.Policies), Groups: orEmpty(g.Groups), Inline: g.Inline}
	if out.Inline == nil {
		out.Inline = []claims.InlineGrant{}
	}
	return out
}

type ciOptionsOut struct {
	CrossProjectSource   string `json:"crossProjectSource"`
	AllowUnprotectedRefs bool   `json:"allowUnprotectedRefs"`
}

// ciMappingOut is a mapping as the API shows it (CiMapping): the resolved defaults, the version, and on the
// answer of a save the warnings.
type ciMappingOut struct {
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	Priority      int               `json:"priority"`
	Enabled       bool              `json:"enabled"`
	SubjectKinds  []string          `json:"subjectKinds"`
	Match         claims.Node       `json:"match"`
	Grants        ciGrantsOut       `json:"grants"`
	MaxTTLSeconds *int64            `json:"maxTtlSeconds"`
	Options       ciOptionsOut      `json:"options"`
	Tier          string            `json:"tier"`
	Project       string            `json:"project,omitempty"`
	Issuer        string            `json:"issuer"`
	Version       int64             `json:"version"`
	CreatedBy     string            `json:"createdBy"`
	CreatedAt     time.Time         `json:"createdAt"`
	UpdatedAt     time.Time         `json:"updatedAt"`
	Warnings      *[]claims.Warning `json:"warnings,omitempty"`
}

func mappingOut(m ci.Mapping, warnings []claims.Warning, saved bool) ciMappingOut {
	out := ciMappingOut{
		Name: m.Name, Description: m.Description, Priority: m.Priority, Enabled: m.Enabled, SubjectKinds: orEmpty(m.SubjectKinds),
		Match: m.Match, Grants: grantsOut(m.Grants), MaxTTLSeconds: m.MaxTTLSeconds,
		Options: ciOptionsOut{CrossProjectSource: m.Options.CrossProjectSource, AllowUnprotectedRefs: m.Options.AllowUnprotectedRefs},
		Tier:    m.Tier, Project: m.Project, Issuer: m.Issuer, Version: m.Version, CreatedBy: m.CreatedBy,
		CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
	}
	if saved {
		if warnings == nil {
			warnings = []claims.Warning{}
		}
		out.Warnings = &warnings
	}
	return out
}

func mappingListOut(ms []ci.Mapping) map[string]any {
	items := make([]ciMappingOut, 0, len(ms))
	for _, m := range ms {
		items = append(items, mappingOut(m, nil, false))
	}
	return map[string]any{"items": items}
}

func mappingHref(r *http.Request, issuer, name string) string {
	return location(r, "/api/v1/ci/issuers/"+issuer+"/mappings/"+name)
}

// ListCiMappings implements GET /api/v1/ci/issuers/{issuer}/mappings.
func (s *Server) ListCiMappings(w http.ResponseWriter, r *http.Request, issuer string) {
	svc, err := s.ciMappingService()
	var list []ci.Mapping
	if err == nil {
		list, err = svc.List(r.Context(), issuer)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, mappingListOut(list))
}

// CreateCiMapping implements POST /api/v1/ci/issuers/{issuer}/mappings.
func (s *Server) CreateCiMapping(w http.ResponseWriter, r *http.Request, issuer string) {
	svc, err := s.ciMappingService()
	var in ci.MappingInput
	if err == nil {
		in, err = s.decodeMapping(w, r)
	}
	var res ci.MappingResult
	if err == nil {
		res, err = svc.Create(r.Context(), caller(r), issuer, in)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	w.Header().Set("Location", mappingHref(r, issuer, res.Mapping.Name))
	SetETag(w, res.Mapping.Version, "")
	writeJSON(w, r, http.StatusCreated, mappingOut(res.Mapping, res.Warnings, true))
}

// GetCiMapping implements GET /api/v1/ci/issuers/{issuer}/mappings/{name}.
func (s *Server) GetCiMapping(w http.ResponseWriter, r *http.Request, issuer, name string) {
	svc, err := s.ciMappingService()
	var m ci.Mapping
	if err == nil {
		m, err = svc.Get(r.Context(), issuer, name)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	if NotModified(w, r, ETag(m.Version, "")) {
		return
	}
	SetETag(w, m.Version, "")
	writeJSON(w, r, http.StatusOK, mappingOut(m, nil, false))
}

// ReplaceCiMapping implements PUT /api/v1/ci/issuers/{issuer}/mappings/{name}.
func (s *Server) ReplaceCiMapping(w http.ResponseWriter, r *http.Request, issuer, name string, _ gen.ReplaceCiMappingParams) {
	svc, err := s.ciMappingService()
	var pre Precondition
	if err == nil {
		pre, err = ParseIfMatch(r)
	}
	if err == nil {
		err = pre.Require()
	}
	var in ci.MappingInput
	if err == nil {
		in, err = s.decodeMapping(w, r)
	}
	var res ci.MappingResult
	if err == nil {
		res, err = svc.Replace(r.Context(), caller(r), issuer, name, expectedRevision(pre), in)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	SetETag(w, res.Mapping.Version, "")
	writeJSON(w, r, http.StatusOK, mappingOut(res.Mapping, res.Warnings, true))
}

// DeleteCiMapping implements DELETE /api/v1/ci/issuers/{issuer}/mappings/{name}.
func (s *Server) DeleteCiMapping(w http.ResponseWriter, r *http.Request, issuer, name string, _ gen.DeleteCiMappingParams) {
	svc, err := s.ciMappingService()
	var pre Precondition
	if err == nil {
		pre, err = ParseIfMatch(r)
	}
	if err == nil {
		err = svc.Delete(r.Context(), caller(r), issuer, name, expectedRevision(pre))
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ciEvaluateIn is the request body of the dry run (CiEvaluateRequest).
type ciEvaluateIn struct {
	SubjectKind *string         `json:"subjectKind"`
	Claims      json.RawMessage `json:"claims"`
	Scope       *string         `json:"scope"`
	Now         *int64          `json:"now"`
}

// decodeEvaluate reads the claim set of a dry run. The claims keep their JSON numbers (jws.DecodeObject), so
// a NumericDate is not rounded and the time check of the request reads them as the exchange does.
func (s *Server) decodeEvaluate(w http.ResponseWriter, r *http.Request) (ci.EvaluateRequest, error) {
	var body ciEvaluateIn
	if err := DecodeJSON(w, r, &body, s.bodyLimit()); err != nil {
		return ci.EvaluateRequest{}, err
	}
	if len(body.Claims) == 0 || string(body.Claims) == "null" {
		return ci.EvaluateRequest{}, errs.E(errs.Invalid, errs.CodeValidation, "The member claims is required.",
			errs.WithFields(errs.FieldError{Pointer: "/claims", Code: "required", Detail: "The dry run takes a claim set, never a token."}))
	}
	cl, err := jws.DecodeObject(body.Claims)
	if err != nil {
		return ci.EvaluateRequest{}, errs.E(errs.Invalid, errs.CodeValidation, "The claims are not a JSON object.",
			errs.WithFields(errs.FieldError{Pointer: "/claims", Code: "invalid_value", Detail: "The claim set is a JSON object without duplicate member names."}))
	}
	return ci.EvaluateRequest{SubjectKind: derefStr(body.SubjectKind), Claims: cl, Scope: derefStr(body.Scope), Now: body.Now}, nil
}

type ciTraceOut struct {
	Tier        string              `json:"tier"`
	Mapping     string              `json:"mapping"`
	Matched     bool                `json:"matched"`
	Contributes bool                `json:"contributes"`
	FailedLeaf  *string             `json:"failedLeaf,omitempty"`
	Detail      string              `json:"detail,omitempty"`
	Leaves      []claims.LeafResult `json:"leaves"`
}

type ciEvaluateOut struct {
	CrossProject bool             `json:"crossProject"`
	Trace        []ciTraceOut     `json:"trace"`
	Selected     []string         `json:"selected"`
	Failure      string           `json:"failure,omitempty"`
	Detail       string           `json:"detail,omitempty"`
	Grants       *ciGrantsOut     `json:"grants,omitempty"`
	Scope        *string          `json:"scope,omitempty"`
	TTLSeconds   *int64           `json:"ttlSeconds,omitempty"`
	Warnings     []claims.Warning `json:"warnings"`
}

func evaluateOut(res ci.EvaluateResult) ciEvaluateOut {
	out := ciEvaluateOut{
		CrossProject: res.CrossProject, Trace: make([]ciTraceOut, 0, len(res.Trace)), Selected: orEmpty(res.Selected),
		Failure: string(res.Failure), Detail: res.Detail, Warnings: res.Warnings,
	}
	if out.Warnings == nil {
		out.Warnings = []claims.Warning{}
	}
	for _, t := range res.Trace {
		e := ciTraceOut{Tier: t.Tier, Mapping: t.Mapping, Matched: t.Matched, Contributes: t.Contributes, Detail: t.Detail, Leaves: t.Leaves}
		if !t.Matched {
			leaf := t.FailedLeaf // "" is the root leaf
			e.FailedLeaf = &leaf
		}
		if e.Leaves == nil {
			e.Leaves = []claims.LeafResult{}
		}
		for i := range e.Leaves {
			if e.Leaves[i].Expected == nil {
				e.Leaves[i].Expected = []string{}
			}
			if e.Leaves[i].Actual == nil {
				e.Leaves[i].Actual = []string{}
			}
		}
		out.Trace = append(out.Trace, e)
	}
	if res.Failure == "" && res.Grants != nil {
		g := grantsOut(claims.Spec{Policies: res.Grants.Policies, Groups: res.Grants.Groups, Inline: res.Grants.Inline})
		out.Grants = &g
		scope, ttl := res.Scope, res.TTLSeconds
		out.Scope, out.TTLSeconds = &scope, &ttl
	}
	return out
}

// EvaluateCiMappings implements POST /api/v1/ci/issuers/{issuer}/evaluate.
func (s *Server) EvaluateCiMappings(w http.ResponseWriter, r *http.Request, issuer string) {
	svc, err := s.ciMappingService()
	var req ci.EvaluateRequest
	if err == nil {
		req, err = s.decodeEvaluate(w, r)
	}
	var res ci.EvaluateResult
	if err == nil {
		res, err = svc.Evaluate(r.Context(), issuer, req)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, evaluateOut(res))
}
