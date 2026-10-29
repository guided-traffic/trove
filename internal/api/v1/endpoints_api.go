package v1

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta"
)

// The endpoint and exposure-rule operations (R-EP-016, R-EP-017): the configured
// endpoints read only, and the exposure rules of an endpoint as a keyed
// collection with list, get, create, replace, merge patch and delete, plus the
// dry run check-exposure. The handlers are thin: authz.ExposureService validates,
// stores, notifies the change area authz and audits; every read comes from
// PostgreSQL (R-API-052).

// HeaderAuthzEpoch is the answer header of a write that changed authorization
// state (R-AUTHZ-032).
const HeaderAuthzEpoch = "Trove-Authz-Epoch"

// EndpointServices are what the endpoint operations need.
type EndpointServices struct {
	// Endpoints are the configured endpoints, the default first (R-EP-001).
	Endpoints []config.ResolvedEndpoint
	// RateLimits are the classes of server.endpoint_rate_limits (R-EP-015).
	RateLimits map[string]config.EndpointRateLimit
	// Exposure is the rule service; nil answers the operations with 503.
	Exposure *authz.ExposureService
	// Authorizer decides read on the repository of check-exposure; nil refuses.
	Authorizer authz.Authorizer
}

func (s *Server) exposure() (*authz.ExposureService, error) {
	if s.o.Endpoints.Exposure == nil {
		return nil, unavailable("The exposure rules")
	}
	return s.o.Endpoints.Exposure, nil
}

// exposurePatterns is the include and exclude list of one dimension.
type exposurePatterns struct {
	Include []string `json:"include"`
	Exclude []string `json:"exclude"`
}

type exposureVersions struct {
	Include []string `json:"include"`
	Exclude []string `json:"exclude"`
	Range   *string  `json:"range"`
}

// exposureRuleDoc is the representation of a rule: the same type is answered by
// GET and read by POST, PUT and as the base of a PATCH, so a body obtained by GET
// can be sent back; its read-only members are ignored on a write (R-API-003).
// Pointers mark the members whose absence takes the default.
type exposureRuleDoc struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Repository  string            `json:"repository"`
	Enabled     *bool             `json:"enabled"`
	Packages    *exposurePatterns `json:"packages"`
	Paths       *exposurePatterns `json:"paths"`
	Versions    *exposureVersions `json:"versions"`
	Prereleases bool              `json:"prereleases"`
	DistTags    *exposurePatterns `json:"distTags"`

	Endpoint  string     `json:"endpoint"`
	Inert     bool       `json:"inert"`
	Warnings  []string   `json:"warnings"`
	Revision  int64      `json:"revision"`
	CreatedAt *time.Time `json:"createdAt"`
	CreatedBy *string    `json:"createdBy"`
	UpdatedAt *time.Time `json:"updatedAt"`
	UpdatedBy *string    `json:"updatedBy"`
}

func listsOf(p *exposurePatterns) authz.PatternLists {
	if p == nil {
		return authz.PatternLists{}
	}
	return authz.PatternLists{Include: p.Include, Exclude: p.Exclude}
}

func patternsOf(l authz.PatternLists) *exposurePatterns {
	return &exposurePatterns{Include: orEmpty(l.Include), Exclude: orEmpty(l.Exclude)}
}

func orEmpty(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

// input reads the writable members; an absent enabled is true (R-EP-012).
func (d exposureRuleDoc) input() authz.ExposureRuleInput {
	in := authz.ExposureRuleInput{
		Name: d.Name, Description: d.Description, Repository: d.Repository, Enabled: d.Enabled == nil || *d.Enabled,
		Packages: listsOf(d.Packages), Paths: listsOf(d.Paths), Prereleases: d.Prereleases, DistTags: listsOf(d.DistTags),
	}
	if d.Versions != nil {
		in.Versions = authz.VersionLists{Include: d.Versions.Include, Exclude: d.Versions.Exclude, Range: d.Versions.Range}
	}
	return in
}

func docOfRule(r authz.ExposureRule) exposureRuleDoc {
	enabled := r.Enabled
	created, updated := r.CreatedAt, r.UpdatedAt
	return exposureRuleDoc{
		Name: r.Name, Description: r.Description, Repository: r.Repository, Enabled: &enabled,
		Packages: patternsOf(r.Packages), Paths: patternsOf(r.Paths),
		Versions:    &exposureVersions{Include: orEmpty(r.Versions.Include), Exclude: orEmpty(r.Versions.Exclude), Range: r.Versions.Range},
		Prereleases: r.Prereleases, DistTags: patternsOf(r.DistTags),
		Endpoint: r.Endpoint, Inert: r.Inert, Warnings: orEmpty(r.Warnings), Revision: r.Revision,
		CreatedAt: &created, CreatedBy: optString(r.CreatedBy), UpdatedAt: &updated, UpdatedBy: optString(r.UpdatedBy),
	}
}

func ruleURL(ctx context.Context, endpoint, name string) string {
	return strings.TrimSuffix(httpserver.InfoFrom(ctx).BaseURL, "/") + "/api/v1/endpoints/" + endpoint + "/exposure-rules/" + name
}

// staleOr turns the service's 412 into the convention's, with currentEtag.
func staleOr(err error) error {
	if cur, ok := authz.IsStale(err); ok {
		return Failed(cur, "")
	}
	return err
}

// expectedRevision reads If-Match into the service's expected revision:
// authz.AnyVersion for no header or *, the revision otherwise (0 for a tag the
// server never issued, which matches nothing).
func expectedRevision(p Precondition) int64 {
	if rev, ok := p.Revision(""); ok {
		return rev
	}
	return authz.AnyVersion
}

// ---- endpoints ----

// endpointDoc is the representation of an endpoint.
type endpointDoc struct {
	Name             string         `json:"name"`
	Inert            bool           `json:"inert"`
	RuleCount        int64          `json:"ruleCount"`
	EnabledRuleCount int64          `json:"enabledRuleCount"`
	Configuration    *endpointConfD `json:"configuration"`
	sortKey          string
}

type endpointConfD struct {
	Default       bool              `json:"default"`
	Port          *int              `json:"port"`
	BaseURL       string            `json:"baseUrl"`
	Hosts         []string          `json:"hosts"`
	WildcardHost  *string           `json:"wildcardHost"`
	HostRoots     map[string]string `json:"hostRoots"`
	WildcardRoot  string            `json:"wildcardRoot"`
	Pool          *string           `json:"pool"`
	Surface       []string          `json:"surface"`
	Auth          endpointAuthD     `json:"auth"`
	Exposure      string            `json:"exposure"`
	CacheFill     bool              `json:"cacheFill"`
	RateLimit     *endpointLimitD   `json:"rateLimit"`
	ReplicaHeader string            `json:"replicaHeader"`
}

type endpointAuthD struct {
	TokenTypes      []string `json:"tokenTypes"`
	BoundTokensOnly bool     `json:"boundTokensOnly"`
	Anonymous       bool     `json:"anonymous"`
}

type endpointLimitD struct {
	Class             string  `json:"class"`
	RequestsPerSecond float64 `json:"requestsPerSecond"`
	Burst             int     `json:"burst"`
	Key               string  `json:"key"`
}

func listenPort(addr string) *int {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil
	}
	n, err := strconv.Atoi(port)
	if err != nil || n == 0 {
		return nil
	}
	return &n
}

func (s *Server) configurationOf(e config.ResolvedEndpoint) *endpointConfD {
	c := &endpointConfD{
		Default: e.Default, Port: listenPort(e.Listen), BaseURL: e.BaseURL, Hosts: orEmpty(e.Hosts),
		WildcardHost: optString(e.WildcardHost), HostRoots: e.HostRoots, WildcardRoot: e.WildcardRoot,
		Pool: optString(e.Pool), Surface: orEmpty(e.Surface),
		Auth:     endpointAuthD{TokenTypes: orEmpty(e.TokenTypes), BoundTokensOnly: e.BoundTokensOnly, Anonymous: e.Anonymous},
		Exposure: e.Exposure, CacheFill: e.CacheFill, ReplicaHeader: e.ReplicaHeader,
	}
	if c.HostRoots == nil {
		c.HostRoots = map[string]string{}
	}
	if e.RateLimitClass != "" {
		l := s.o.Endpoints.RateLimits[e.RateLimitClass]
		c.RateLimit = &endpointLimitD{Class: e.RateLimitClass, RequestsPerSecond: l.RequestsPerSecond, Burst: l.Burst, Key: l.Key}
	}
	return c
}

// endpointDocs lists the configured endpoints, the default first, then the inert
// names, each with its rule counts.
func (s *Server) endpointDocs(ctx context.Context) ([]endpointDoc, error) {
	svc, err := s.exposure()
	if err != nil {
		return nil, err
	}
	counts, err := svc.Counts(ctx)
	if err != nil {
		return nil, errs.Wrap(err, "api.endpoints")
	}
	var out []endpointDoc
	seen := map[string]bool{}
	for _, e := range s.o.Endpoints.Endpoints {
		c := counts[e.Name]
		key := "1:" + e.Name
		if e.Default {
			key = "0:" + e.Name
		}
		out = append(out, endpointDoc{Name: e.Name, RuleCount: c.Rules, EnabledRuleCount: c.Enabled, Configuration: s.configurationOf(e), sortKey: key})
		seen[e.Name] = true
	}
	var inert []string
	for name := range counts {
		if !seen[name] {
			inert = append(inert, name)
		}
	}
	slices.Sort(inert)
	for _, name := range inert {
		c := counts[name]
		out = append(out, endpointDoc{Name: name, Inert: true, RuleCount: c.Rules, EnabledRuleCount: c.Enabled, sortKey: "2:" + name})
	}
	slices.SortFunc(out, func(a, b endpointDoc) int { return strings.Compare(a.sortKey, b.sortKey) })
	return out, nil
}

// ListEndpoints implements GET /api/v1/endpoints (R-EP-016).
func (s *Server) ListEndpoints(w http.ResponseWriter, r *http.Request, _ gen.ListEndpointsParams) {
	lq, err := ParseList(r, ListSpec{}, s.o.API.Config)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	docs, err := s.endpointDocs(r.Context())
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	rows := make([]endpointDoc, 0, len(docs))
	for _, d := range docs {
		if len(lq.After) == 1 && d.sortKey <= lq.After[0] {
			continue
		}
		rows = append(rows, d)
		if len(rows) == lq.FetchLimit() {
			break
		}
	}
	WritePage(w, r, NewPage(lq, rows, func(d endpointDoc) []string { return []string{d.sortKey} }))
}

// GetEndpoint implements GET /api/v1/endpoints/{endpoint}.
func (s *Server) GetEndpoint(w http.ResponseWriter, r *http.Request, endpoint string) {
	docs, err := s.endpointDocs(r.Context())
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	for _, d := range docs {
		if d.Name == endpoint {
			writeJSON(w, r, http.StatusOK, d)
			return
		}
	}
	httpserver.Problem(w, r, errs.E(errs.NotFound, errs.CodeNotFound, fmt.Sprintf("No endpoint %q is configured or has exposure rules.", endpoint)))
}

// ---- exposure rules ----

// ListExposureRules implements GET /api/v1/endpoints/{endpoint}/exposure-rules:
// one bounded read of the endpoint's rules (at most 1,000), the filters, and the
// keyset page over (name, id) (R-API-010).
func (s *Server) ListExposureRules(w http.ResponseWriter, r *http.Request, endpoint string, _ gen.ListExposureRulesParams) {
	lq, err := ParseList(r, ListSpec{Filters: []string{"repository", "package", "enabled"}, Sorts: []string{"name"}, DefaultSort: "name"}, s.o.API.Config)
	if err == nil {
		switch lq.First("enabled") {
		case "", "true", "false":
		default:
			err = errs.E(errs.Invalid, errs.CodeInvalid, "The query parameter \"enabled\" must be true or false.")
		}
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	svc, err := s.exposure()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	rules, err := svc.List(r.Context(), endpoint)
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.listExposureRules"))
		return
	}
	repos, pkg, enabled := lq.Filter["repository"], lq.First("package"), lq.First("enabled")
	kept := rules[:0:0]
	for _, rule := range rules {
		switch {
		case len(repos) > 0 && !slices.Contains(repos, rule.Repository):
		case enabled != "" && strconv.FormatBool(rule.Enabled) != enabled:
		case pkg != "" && !svc.AdmitsPackage(rule, pkg):
		default:
			kept = append(kept, rule)
		}
	}
	if lq.Desc {
		slices.Reverse(kept)
	}
	after := func(x authz.ExposureRule) bool {
		if len(lq.After) != 2 {
			return true
		}
		c := strings.Compare(x.Name, lq.After[0])
		if c == 0 {
			c = strings.Compare(x.ID, lq.After[1])
		}
		if lq.Desc {
			return c < 0
		}
		return c > 0
	}
	page := make([]authz.ExposureRule, 0, lq.FetchLimit())
	for _, x := range kept {
		if after(x) {
			page = append(page, x)
			if len(page) == lq.FetchLimit() {
				break
			}
		}
	}
	p := NewPage(lq, page, func(x authz.ExposureRule) []string { return []string{x.Name, x.ID} })
	WritePage(w, r, MapPage(p, docOfRule))
}

// GetExposureRule implements GET .../exposure-rules/{name} with ETag and 304.
func (s *Server) GetExposureRule(w http.ResponseWriter, r *http.Request, endpoint, name string, _ gen.GetExposureRuleParams) {
	svc, err := s.exposure()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	rule, err := svc.Get(r.Context(), endpoint, name)
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.getExposureRule"))
		return
	}
	if NotModified(w, r, ETag(rule.Revision, "")) {
		return
	}
	SetETag(w, rule.Revision, "")
	writeJSON(w, r, http.StatusOK, docOfRule(rule))
}

// CreateExposureRule implements POST .../exposure-rules: strict body, then the
// create inside the idempotent operation, so the row, the change-log note, the
// audit event and the stored outcome commit together (R-API-014).
func (s *Server) CreateExposureRule(w http.ResponseWriter, r *http.Request, endpoint string, _ gen.CreateExposureRuleParams) {
	svc, err := s.exposure()
	if err == nil && s.idem == nil {
		err = unavailable("The exposure rules")
	}
	var body []byte
	if err == nil {
		body, err = ReadBody(w, r, MediaJSON, int64(s.o.API.Config.MaxRequestBodyBytes))
	}
	var doc exposureRuleDoc
	if err == nil {
		err = StrictUnmarshal(body, &doc)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	actor := caller(r)
	in := doc.input()
	err = s.idem.Do(w, r, body, IdempotentOp{Run: func(ctx context.Context, tx meta.Tx) (Outcome, error) {
		// The epoch is known when the change log raised it, just before the
		// commit; the hook fills the header of this answer then. A replay of the
		// stored outcome carries no epoch: the rule itself is current on every
		// replica within the bound.
		hdr := map[string]string{}
		rule, err := svc.Create(ctx, tx, actor, endpoint, in, func(epoch int64) { hdr[HeaderAuthzEpoch] = strconv.FormatInt(epoch, 10) })
		if err != nil {
			return Outcome{}, err
		}
		hdr["Location"], hdr["ETag"] = ruleURL(ctx, endpoint, rule.Name), ETag(rule.Revision, "")
		return Outcome{Status: http.StatusCreated, Body: docOfRule(rule), Header: hdr}, nil
	}})
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.createExposureRule"))
	}
}

// replaceRule runs a replacement and answers it.
func (s *Server) replaceRule(w http.ResponseWriter, r *http.Request, svc *authz.ExposureService, endpoint, name string, pre Precondition, in authz.ExposureRuleInput) {
	rule, epoch, err := svc.Replace(r.Context(), caller(r), endpoint, name, expectedRevision(pre), in)
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(staleOr(err), "api.replaceExposureRule"))
		return
	}
	SetETag(w, rule.Revision, "")
	w.Header().Set(HeaderAuthzEpoch, strconv.FormatInt(epoch, 10))
	writeJSON(w, r, http.StatusOK, docOfRule(rule))
}

// ReplaceExposureRule implements PUT .../exposure-rules/{name}: 428 without
// If-Match, a strict body of the representation, the replacement with the
// revision.
func (s *Server) ReplaceExposureRule(w http.ResponseWriter, r *http.Request, endpoint, name string, _ gen.ReplaceExposureRuleParams) {
	svc, err := s.exposure()
	var pre Precondition
	if err == nil {
		pre, err = ParseIfMatch(r)
	}
	if err == nil {
		err = pre.Require()
	}
	var doc exposureRuleDoc
	if err == nil {
		err = DecodeJSON(w, r, &doc, int64(s.o.API.Config.MaxRequestBodyBytes))
	}
	if err == nil && doc.Name != "" && doc.Name != name {
		err = errs.E(errs.Invalid, errs.CodeValidation, "An immutable member was changed.", errs.WithFields(errs.FieldError{
			Pointer: "/name", Code: "immutable", Detail: "the name of a rule is fixed at creation",
		}))
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	s.replaceRule(w, r, svc, endpoint, name, pre, doc.input())
}

// PatchExposureRule implements PATCH .../exposure-rules/{name}: the merge patch
// on the stored representation, the immutable members, then the replacement
// with the revision of If-Match (R-API-013).
func (s *Server) PatchExposureRule(w http.ResponseWriter, r *http.Request, endpoint, name string, _ gen.PatchExposureRuleParams) {
	svc, err := s.exposure()
	var pre Precondition
	if err == nil {
		pre, err = ParseIfMatch(r)
	}
	if err == nil {
		err = pre.Require()
	}
	var raw []byte
	if err == nil {
		raw, err = ReadMergePatch(w, r, int64(s.o.API.Config.MaxRequestBodyBytes))
	}
	var current authz.ExposureRule
	if err == nil {
		current, err = svc.Get(r.Context(), endpoint, name)
	}
	var next exposureRuleDoc
	if err == nil {
		next, err = ApplyMergePatch(docOfRule(current), raw)
	}
	if err == nil {
		err = CheckImmutable(docOfRule(current), next, "name", "repository")
	}
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.patchExposureRule"))
		return
	}
	s.replaceRule(w, r, svc, endpoint, name, pre, next.input())
}

// DeleteExposureRule implements DELETE .../exposure-rules/{name}; If-Match is
// honoured when sent.
func (s *Server) DeleteExposureRule(w http.ResponseWriter, r *http.Request, endpoint, name string, _ gen.DeleteExposureRuleParams) {
	svc, err := s.exposure()
	var pre Precondition
	if err == nil {
		pre, err = ParseIfMatch(r)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	epoch, err := svc.Delete(r.Context(), caller(r), endpoint, name, expectedRevision(pre))
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(staleOr(err), "api.deleteExposureRule"))
		return
	}
	w.Header().Set(HeaderAuthzEpoch, strconv.FormatInt(epoch, 10))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// exposureCheckRequest is the body of check-exposure.
type exposureCheckRequest struct {
	Repository string           `json:"repository"`
	Package    string           `json:"package"`
	Versions   []string         `json:"versions"`
	Paths      []string         `json:"paths"`
	Rule       *exposureRuleDoc `json:"rule"`
}

type checkVersion struct {
	Version string  `json:"version"`
	Exposed bool    `json:"exposed"`
	Rule    *string `json:"rule"`
	Reason  string  `json:"reason"`
}

type checkPath struct {
	Path    string  `json:"path"`
	Exposed bool    `json:"exposed"`
	Rule    *string `json:"rule"`
	Reason  string  `json:"reason"`
}

type checkTag struct {
	Tag     string `json:"tag"`
	Version string `json:"version"`
	Visible bool   `json:"visible"`
}

type exposureCheckResult struct {
	Endpoint       string         `json:"endpoint"`
	Repository     string         `json:"repository"`
	Mode           string         `json:"mode"`
	Package        *string        `json:"package"`
	PackageExposed bool           `json:"packageExposed"`
	Rule           *string        `json:"rule"`
	Reason         string         `json:"reason"`
	Versions       []checkVersion `json:"versions"`
	Paths          []checkPath    `json:"paths"`
	DistTags       []checkTag     `json:"distTags"`
	Fingerprint    string         `json:"exposureFingerprint"`
	Warnings       []string       `json:"warnings"`
}

// maxCheckItems bounds the versions and paths of one check.
const maxCheckItems = 1000

// CheckExposure implements POST /api/v1/endpoints/{endpoint}/check-exposure: the
// dry run of R-EP-016. endpoints:read is checked by the declaration; read on the
// repository here, because the repository is in the body (R-API-008). A
// repository the caller cannot read answers 404 like a missing one.
func (s *Server) CheckExposure(w http.ResponseWriter, r *http.Request, endpoint string) {
	svc, err := s.exposure()
	var in exposureCheckRequest
	if err == nil {
		err = DecodeJSON(w, r, &in, int64(s.o.API.Config.MaxRequestBodyBytes))
	}
	if err == nil {
		var fields []errs.FieldError
		if in.Repository == "" {
			fields = append(fields, errs.FieldError{Pointer: "/repository", Code: "required", Detail: "names the repository to check"})
		}
		if len(in.Versions) > maxCheckItems {
			fields = append(fields, errs.FieldError{Pointer: "/versions", Code: "max_items", Detail: fmt.Sprintf("at most %d", maxCheckItems)})
		}
		if len(in.Paths) > maxCheckItems {
			fields = append(fields, errs.FieldError{Pointer: "/paths", Code: "max_items", Detail: fmt.Sprintf("at most %d", maxCheckItems)})
		}
		if len(fields) > 0 {
			err = errs.E(errs.Invalid, errs.CodeValidation, "The check is invalid.", errs.WithFields(fields...))
		}
	}
	if err == nil {
		err = s.requireRead(r.Context(), in.Repository)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	q := authz.ExposureCheckInput{Repository: in.Repository, Package: in.Package, Versions: in.Versions, Paths: in.Paths}
	if in.Rule != nil {
		draft := in.Rule.input()
		q.Draft = &draft
	}
	res, err := svc.Check(r.Context(), endpoint, q)
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.checkExposure"))
		return
	}
	writeJSON(w, r, http.StatusOK, checkResultOf(res))
}

// requireRead decides read on the repository for the caller, on the request's
// endpoint (R-AUTHZ-015); the refusal is that of R-AUTHZ-016.
func (s *Server) requireRead(ctx context.Context, repo string) error {
	a := s.o.Endpoints.Authorizer
	if a == nil {
		return errs.E(errs.Forbidden, errs.CodeForbidden, "The operation is not permitted.")
	}
	c := authz.Coordinate{Repository: repo, Kind: authz.KindMetadata, Actions: []authz.Action{authz.Read}}
	if ep := httpserver.EndpointFrom(ctx); ep != nil {
		c.Endpoint = ep.Name()
	}
	return a.Decide(ctx, SubjectFrom(httpserver.PrincipalFrom(ctx)), c).Err()
}

func checkResultOf(res authz.ExposureCheckResult) exposureCheckResult {
	out := exposureCheckResult{
		Endpoint: res.Endpoint, Repository: res.Repository, Mode: res.Mode, Package: optString(res.Package),
		PackageExposed: res.Exposed, Rule: optString(res.Rule), Reason: res.Reason,
		Versions: make([]checkVersion, 0, len(res.Versions)), Paths: make([]checkPath, 0, len(res.Paths)),
		DistTags: []checkTag{}, Fingerprint: res.Fingerprint, Warnings: orEmpty(res.Warnings),
	}
	for _, v := range res.Versions {
		out.Versions = append(out.Versions, checkVersion{Version: v.Value, Exposed: v.Exposed, Rule: optString(v.Rule), Reason: v.Reason})
	}
	for _, p := range res.Paths {
		out.Paths = append(out.Paths, checkPath{Path: p.Value, Exposed: p.Exposed, Rule: optString(p.Rule), Reason: p.Reason})
	}
	return out
}
