package v1

import (
	"net/http"
	"time"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/ci"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// The CI issuer and signing-key operations (R-CI-001 to R-CI-003, R-CI-005, Q-CI-16), work package W1.
// The declarations are in the document: ci:read for the reads and ci:write for the writes, recent
// authentication for create, replace and delete, x-trove-if-match required on the replace. The handlers
// are thin: the service ([ci.Issuers]) owns the defaults, the validation, the transaction, the audit
// events and the revocation.
//
//   - createCiIssuer does not use the idempotency store: the name is the key and a repeat is 409. It
//     answers 201 with Location and ETag; validate defaults to true.
//   - replaceCiIssuer: the ETag is the issuer's version, 428 comes from the binder, 412 carries
//     currentEtag; a configuration issuer answers 409 issuer_managed_by_configuration.
//   - deleteCiIssuer: cascade=true deletes the mappings; If-Match is optional.
//   - refreshCiIssuerKeys: 200 with the key status; an issuer that cannot be fetched is 502.

func (s *Server) ciIssuerService() (ci.Issuers, error) {
	if s.o.CI.Issuers == nil {
		return nil, unavailable("The CI issuer service")
	}
	return s.o.CI.Issuers, nil
}

// issuerBody renders an issuer as the API shows it.
func issuerBody(is ci.Issuer) gen.CiIssuer {
	out := gen.CiIssuer{
		Name: is.Name, Kind: gen.CiIssuerKind(is.Kind), Issuer: is.Issuer, Description: is.Description, Enabled: is.Enabled,
		DiscoveryUrl: is.DiscoveryURL, JwksUri: is.JWKSURI,
		AllowedAudiences: orEmpty(is.AllowedAudiences), ExclusiveAudience: is.ExclusiveAudience,
		AllowedTyp: orEmpty(is.AllowedTyp), AllowMissingTyp: is.AllowMissingTyp, ClockSkewSeconds: is.ClockSkewSeconds,
		RequiredClaims: orEmpty(is.RequiredClaims), PublicIssuer: is.PublicIssuer, PinClaims: orEmpty(is.PinClaims),
		SingleUseSubjectTokens: is.SingleUseSubjectTokens, MaxTtlSeconds: is.MaxTTLSeconds, CaBundleRef: is.CABundleRef,
		UseOutboundProxy: is.UseOutboundProxy, Source: gen.CiIssuerSource(is.Source),
		RemovedFromConfiguration: is.RemovedFromConfiguration, Version: is.Version,
		CreatedAt: is.CreatedAt, UpdatedAt: is.UpdatedAt,
	}
	out.AllowedAlgorithms = make([]gen.CiIssuerAllowedAlgorithms, 0, len(is.AllowedAlgorithms))
	for _, a := range is.AllowedAlgorithms {
		out.AllowedAlgorithms = append(out.AllowedAlgorithms, gen.CiIssuerAllowedAlgorithms(a))
	}
	enabled := is.JobTokenFallback.Enabled
	out.JobTokenFallback.Enabled = &enabled
	if is.Keys != nil || is.MappingCount > 0 {
		n := is.MappingCount
		out.MappingCount = &n
	}
	if is.Keys != nil {
		sum := gen.CiKeySummary{Usable: is.Keys.Usable, Ignored: is.Keys.Ignored, FetchedAt: is.Keys.FetchedAt, LastErrorAt: is.Keys.LastErrorAt}
		if is.Keys.LastError != "" {
			e := is.Keys.LastError
			sum.LastError = &e
		}
		out.Keys = &sum
	}
	return out
}

// ciKeyBody is the answer of the key status operations (CiKeyStatus).
type ciKeyBody struct {
	Issuer        string      `json:"issuer"`
	JWKSURI       *string     `json:"jwksUri,omitempty"`
	Keys          []ciKeyInfo `json:"keys"`
	FetchedAt     *time.Time  `json:"fetchedAt,omitempty"`
	NextRefreshAt *time.Time  `json:"nextRefreshAt,omitempty"`
	LastError     *string     `json:"lastError,omitempty"`
	LastErrorAt   *time.Time  `json:"lastErrorAt,omitempty"`
}

type ciKeyInfo struct {
	Kid           string  `json:"kid"`
	Kty           string  `json:"kty"`
	Alg           *string `json:"alg,omitempty"`
	Ignored       bool    `json:"ignored"`
	IgnoredReason *string `json:"ignoredReason,omitempty"`
}

func keyBody(st ci.KeyStatus) ciKeyBody {
	out := ciKeyBody{Issuer: st.Issuer, Keys: make([]ciKeyInfo, 0, len(st.Keys)), FetchedAt: st.FetchedAt, NextRefreshAt: st.NextRefreshAt, LastErrorAt: st.LastErrorAt}
	if st.JWKSURI != "" {
		u := st.JWKSURI
		out.JWKSURI = &u
	}
	if st.LastError != "" {
		e := st.LastError
		out.LastError = &e
	}
	for _, k := range st.Keys {
		info := ciKeyInfo{Kid: k.KID, Kty: k.KTY, Ignored: k.Ignored}
		if k.Alg != "" {
			a := k.Alg
			info.Alg = &a
		}
		if k.IgnoredReason != "" {
			r := k.IgnoredReason
			info.IgnoredReason = &r
		}
		out.Keys = append(out.Keys, info)
	}
	return out
}

// issuerFields are the members of a create or a replace body, which have one shape; each of the two
// generated types converts to it.
type issuerFields struct {
	Name, Kind, Issuer                                                 string
	Description                                                        *string
	Enabled                                                            *bool
	DiscoveryURL, JWKSURI, CABundleRef                                 *string
	AllowedAudiences, AllowedTyp, RequiredClaims, PinClaims            *[]string
	AllowedAlgorithms                                                  []string
	ExclusiveAudience, AllowMissingTyp, PublicIssuer, SingleUse, Proxy *bool
	ClockSkewSeconds                                                   *int
	MaxTTLSeconds                                                      *int64
	JobTokenFallback                                                   *ci.JobTokenFallback
}

// input converts to the service's input. A member that is absent stays nil (its default); a present empty
// list stays an empty list, which the validation refuses where a list needs an entry.
func (f issuerFields) input() ci.IssuerInput {
	return ci.IssuerInput{
		Name: f.Name, Kind: f.Kind, Issuer: f.Issuer, Description: derefStr(f.Description), Enabled: f.Enabled,
		DiscoveryURL: f.DiscoveryURL, JWKSURI: f.JWKSURI, CABundleRef: f.CABundleRef,
		AllowedAudiences: deref(f.AllowedAudiences), AllowedTyp: deref(f.AllowedTyp),
		AllowedAlgorithms: f.AllowedAlgorithms,
		RequiredClaims:    deref(f.RequiredClaims), PinClaims: deref(f.PinClaims),
		ExclusiveAudience: f.ExclusiveAudience, AllowMissingTyp: f.AllowMissingTyp, PublicIssuer: f.PublicIssuer,
		SingleUseSubjectTokens: f.SingleUse, UseOutboundProxy: f.Proxy, ClockSkewSeconds: f.ClockSkewSeconds,
		MaxTTLSeconds: f.MaxTTLSeconds, JobTokenFallback: f.JobTokenFallback,
	}
}

func algorithms[T ~string](p *[]T) []string {
	if p == nil {
		return nil
	}
	out := make([]string, 0, len(*p))
	for _, a := range *p {
		out = append(out, string(a))
	}
	return out
}

func createFields(b gen.CiIssuerCreate) issuerFields {
	f := issuerFields{
		Name: b.Name, Kind: string(b.Kind), Issuer: b.Issuer, Description: b.Description, Enabled: b.Enabled,
		DiscoveryURL: b.DiscoveryUrl, JWKSURI: b.JwksUri, CABundleRef: b.CaBundleRef,
		AllowedAudiences: b.AllowedAudiences, AllowedTyp: b.AllowedTyp, RequiredClaims: b.RequiredClaims, PinClaims: b.PinClaims,
		AllowedAlgorithms: algorithms(b.AllowedAlgorithms),
		ExclusiveAudience: b.ExclusiveAudience, AllowMissingTyp: b.AllowMissingTyp, PublicIssuer: b.PublicIssuer,
		SingleUse: b.SingleUseSubjectTokens, Proxy: b.UseOutboundProxy, ClockSkewSeconds: b.ClockSkewSeconds, MaxTTLSeconds: b.MaxTtlSeconds,
	}
	if b.JobTokenFallback != nil {
		f.JobTokenFallback = &ci.JobTokenFallback{Enabled: b.JobTokenFallback.Enabled != nil && *b.JobTokenFallback.Enabled}
	}
	return f
}

func inputFields(b gen.CiIssuerInput) issuerFields {
	f := issuerFields{
		Name: b.Name, Kind: string(b.Kind), Issuer: b.Issuer, Description: b.Description, Enabled: b.Enabled,
		DiscoveryURL: b.DiscoveryUrl, JWKSURI: b.JwksUri, CABundleRef: b.CaBundleRef,
		AllowedAudiences: b.AllowedAudiences, AllowedTyp: b.AllowedTyp, RequiredClaims: b.RequiredClaims, PinClaims: b.PinClaims,
		AllowedAlgorithms: algorithms(b.AllowedAlgorithms),
		ExclusiveAudience: b.ExclusiveAudience, AllowMissingTyp: b.AllowMissingTyp, PublicIssuer: b.PublicIssuer,
		SingleUse: b.SingleUseSubjectTokens, Proxy: b.UseOutboundProxy, ClockSkewSeconds: b.ClockSkewSeconds, MaxTTLSeconds: b.MaxTtlSeconds,
	}
	if b.JobTokenFallback != nil {
		f.JobTokenFallback = &ci.JobTokenFallback{Enabled: b.JobTokenFallback.Enabled != nil && *b.JobTokenFallback.Enabled}
	}
	return f
}

func issuerHref(r *http.Request, name string) string { return location(r, "/api/v1/ci/issuers/"+name) }

// ListCiIssuers implements GET /api/v1/ci/issuers.
func (s *Server) ListCiIssuers(w http.ResponseWriter, r *http.Request) {
	svc, err := s.ciIssuerService()
	var list []ci.Issuer
	if err == nil {
		list, err = svc.List(r.Context())
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	items := make([]gen.CiIssuer, 0, len(list))
	for _, is := range list {
		items = append(items, issuerBody(is))
	}
	writeJSON(w, r, http.StatusOK, gen.CiIssuerList{Items: items})
}

// CreateCiIssuer implements POST /api/v1/ci/issuers.
func (s *Server) CreateCiIssuer(w http.ResponseWriter, r *http.Request) {
	svc, err := s.ciIssuerService()
	var body gen.CiIssuerCreate
	if err == nil {
		err = DecodeJSON(w, r, &body, s.bodyLimit())
	}
	var is ci.Issuer
	if err == nil {
		validate := body.Validate == nil || *body.Validate
		is, err = svc.Create(r.Context(), caller(r), createFields(body).input(), validate)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	w.Header().Set("Location", issuerHref(r, is.Name))
	SetETag(w, is.Version, "")
	writeJSON(w, r, http.StatusCreated, issuerBody(is))
}

// GetCiIssuer implements GET /api/v1/ci/issuers/{name}.
func (s *Server) GetCiIssuer(w http.ResponseWriter, r *http.Request, name string) {
	svc, err := s.ciIssuerService()
	var is ci.Issuer
	if err == nil {
		is, err = svc.Get(r.Context(), name)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	if NotModified(w, r, ETag(is.Version, "")) {
		return
	}
	SetETag(w, is.Version, "")
	writeJSON(w, r, http.StatusOK, issuerBody(is))
}

// ReplaceCiIssuer implements PUT /api/v1/ci/issuers/{name}.
func (s *Server) ReplaceCiIssuer(w http.ResponseWriter, r *http.Request, name string, _ gen.ReplaceCiIssuerParams) {
	svc, err := s.ciIssuerService()
	var pre Precondition
	if err == nil {
		pre, err = ParseIfMatch(r)
	}
	if err == nil {
		err = pre.Require()
	}
	var body gen.CiIssuerInput
	if err == nil {
		err = DecodeJSON(w, r, &body, s.bodyLimit())
	}
	var is ci.Issuer
	if err == nil {
		is, err = svc.Replace(r.Context(), caller(r), name, expectedRevision(pre), inputFields(body).input())
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	SetETag(w, is.Version, "")
	writeJSON(w, r, http.StatusOK, issuerBody(is))
}

// DeleteCiIssuer implements DELETE /api/v1/ci/issuers/{name}.
func (s *Server) DeleteCiIssuer(w http.ResponseWriter, r *http.Request, name string, params gen.DeleteCiIssuerParams) {
	svc, err := s.ciIssuerService()
	var pre Precondition
	if err == nil {
		pre, err = ParseIfMatch(r)
	}
	if err == nil {
		err = svc.Delete(r.Context(), caller(r), name, params.Cascade != nil && *params.Cascade, expectedRevision(pre))
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetCiIssuerKeys implements GET /api/v1/ci/issuers/{name}/keys.
func (s *Server) GetCiIssuerKeys(w http.ResponseWriter, r *http.Request, name string) {
	svc, err := s.ciIssuerService()
	var st ci.KeyStatus
	if err == nil {
		st, err = svc.Keys(r.Context(), name)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, keyBody(st))
}

// RefreshCiIssuerKeys implements POST /api/v1/ci/issuers/{name}/keys/refresh.
func (s *Server) RefreshCiIssuerKeys(w http.ResponseWriter, r *http.Request, name string) {
	svc, err := s.ciIssuerService()
	var st ci.KeyStatus
	if err == nil {
		st, err = svc.RefreshKeys(r.Context(), caller(r), name, ci.TriggerAdmin)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, keyBody(st))
}
