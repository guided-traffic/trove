package v1

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"html"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/qname"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/internal/vuln/report"
	"github.com/guided-traffic/trove/internal/vuln/store"
	"github.com/guided-traffic/trove/internal/vuln/triage"
)

// The findings operations of the vulnerability scanning (R-VULN-013, R-VULN-019, R-VULN-020): the list, the detail, the triage
// of one and of many findings, the advisory view and the CSV export. The services are internal/vuln/report (what the caller
// can read, narrowed in SQL) and internal/vuln/triage (the status changes); this file turns their answers into the contract.
// The summary, the trend, the sources and the package views are in vulnerabilities_views_api.go, the rescan in
// vulnerabilities_rescan_api.go.

// VulnServices are what the vulnerability operations need. The composition root fills it (internal/app/wire_vuln.go); a
// replica that has not wired the scanning answers them 503, never an empty list, because an empty list says "no finding".
type VulnServices struct {
	// Report reads, Triage changes.
	Report *report.Service
	Triage *triage.Service
	// Decider begins the request's index; Scoper classifies the repositories for what the caller can read (R-VULN-019).
	Decider authz.Decider
	Scoper  authz.ReadScoper
}

func (s *Server) vulnReport() (*report.Service, error) {
	v := s.o.Vuln
	if v.Report == nil || v.Decider == nil || v.Scoper == nil {
		return nil, unavailable("The vulnerability report")
	}
	return v.Report, nil
}

// vulnRequest opens the request's index and builds what the caller can read: the snapshot of the repositories, the
// classification of the physical ones and the virtual ones the caller may read (R-VULN-019).
func (s *Server) vulnRequest(r *http.Request, rep *report.Service) (context.Context, *report.Snapshot, report.Caller, error) {
	ctx := r.Context()
	sn, err := rep.Snapshot(ctx)
	if err != nil {
		return ctx, nil, report.Caller{}, err
	}
	ctx, acc, err := requestHolder(ctx, s.o.Vuln.Decider, sn.Topo.Names(sn.IDs())...)
	if err != nil {
		return ctx, nil, report.Caller{}, err
	}
	if acc == nil {
		return ctx, nil, report.Caller{}, unavailable("The decision point")
	}
	rs, err := s.o.Vuln.Scoper.ReadScopeOn(ctx, acc.Index(), acc.Subject, qname.Repo{}, sn.Topo.Names(sn.Topo.Physical()))
	if err != nil {
		return ctx, nil, report.Caller{}, err
	}
	virtuals, err := acc.Readable(ctx, sn.Topo.Names(sn.Topo.Virtual()))
	if err != nil {
		return ctx, nil, report.Caller{}, err
	}
	return ctx, sn, report.Caller{Reads: rs, VirtualReadable: sn.Topo.IDs(virtuals)}, nil
}

// findingFilters are the query parameters of the list and of the export (R-VULN-020).
func findingFilters() []string {
	return []string{
		"project", "repository", "format", "package", "packagePrefix", "version", "advisory", "severity", "status", "kind",
		"knownExploited", "fixAvailable", "blocked", "blockReason", "detectedFrom", "detectedTo", "q",
	}
}

func findingListSpec() ListSpec {
	return ListSpec{
		Filters:     findingFilters(),
		Sorts:       []string{"severity", "detectedAt", "package", "advisory", "status"},
		DefaultSort: "-severity",
	}
}

func triBool(lq ListQuery, name string) (*bool, error) {
	raw := lq.First(name)
	if raw == "" {
		return nil, nil
	}
	b, err := strconv.ParseBool(raw)
	if err != nil || (raw != "true" && raw != "false") {
		return nil, badParam(name, "must be true or false")
	}
	return &b, nil
}

func queryOneOf(lq ListQuery, name string, allowed []string) (string, error) {
	vals := lq.Filter[name]
	if len(vals) > 1 {
		return "", badParam(name, "is given once")
	}
	if len(vals) == 0 || vals[0] == "" {
		return "", nil
	}
	if !slices.Contains(allowed, vals[0]) {
		return "", badParam(name, "must be one of "+strings.Join(allowed, ", "))
	}
	return vals[0], nil
}

func queryManyOf(lq ListQuery, name string, allowed []string) ([]string, error) {
	var out []string
	for _, v := range splitValues(lq.Filter[name]) {
		if !slices.Contains(allowed, v) {
			return nil, badParam(name, "must be one of "+strings.Join(allowed, ", "))
		}
		out = append(out, v)
	}
	return out, nil
}

func severityNames() []string {
	var out []string
	for _, s := range advisory.Severities() {
		out = append(out, string(s))
	}
	return out
}

// findingFilter reads the filters of the list and the export (R-VULN-020). A virtual repository in the repository filter
// stands for its members; the scope then narrows them to what the caller can read.
func findingFilter(lq ListQuery, sn *report.Snapshot, caller report.Caller) (store.Filter, error) {
	f := store.Filter{
		Formats: splitValues(lq.Filter["format"]), Packages: splitValues(lq.Filter["package"]), PackagePrefix: lq.First("packagePrefix"),
		Versions: splitValues(lq.Filter["version"]), Advisory: lq.First("advisory"), Q: lq.First("q"),
	}
	var err error
	if f.Repos, f.HasRepos, err = findingRepos(lq, sn, caller); err != nil {
		return f, err
	}
	if f.Severities, err = queryManyOf(lq, "severity", severityNames()); err != nil {
		return f, err
	}
	if f.Statuses, err = queryManyOf(lq, "status", report.AllStatuses()); err != nil {
		return f, err
	}
	if f.Kind, err = queryOneOf(lq, "kind", []string{string(advisory.KindVulnerability), string(advisory.KindMalicious)}); err != nil {
		return f, err
	}
	if f.BlockReason, err = queryOneOf(lq, "blockReason", []string{string(vuln.BlockMalicious), string(vuln.BlockTriage)}); err != nil {
		return f, err
	}
	for _, p := range []struct {
		name string
		to   **bool
	}{{"knownExploited", &f.KnownExploited}, {"fixAvailable", &f.FixAvailable}, {"blocked", &f.Blocked}} {
		if *p.to, err = triBool(lq, p.name); err != nil {
			return f, err
		}
	}
	for _, p := range []struct {
		name string
		to   **time.Time
	}{{"detectedFrom", &f.DetectedFrom}, {"detectedTo", &f.DetectedTo}} {
		if raw := lq.First(p.name); raw != "" {
			t, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				return f, badParam(p.name, "must be an RFC 3339 date and time")
			}
			*p.to = &t
		}
	}
	return f, nil
}

// findingRepos resolves the filters repository and project to the physical repositories a statement is
// narrowed to (R-VULN-020, R-VULN-021): a repository is a qualified name (a bare key is 400), a project stands for
// its physical repositories, and the two together intersect. A project that does not exist, or that the caller
// cannot read, gives an empty set, never a 404: the answer is that of a project without findings.
func findingRepos(lq ListQuery, sn *report.Snapshot, caller report.Caller) (ids []int64, has bool, err error) {
	reps, hasRepo := lq.Filter["repository"]
	projects := splitValues(lq.Filter["project"])
	var names []qname.Repo
	for _, text := range splitValues(reps) {
		n, perr := qname.ParseRepo(text)
		if perr != nil {
			return nil, false, badParam("repository", "must be a repository name <project>/<key>")
		}
		names = append(names, n)
	}
	for _, key := range projects {
		if !qname.ValidProjectKey(key) {
			return nil, false, badParam("project", "must be a project key")
		}
	}
	if hasRepo {
		ids, has = sn.ExpandFor(caller, sn.Topo.IDs(names)), true
	}
	if len(projects) == 0 {
		return ids, has, nil
	}
	var inProject []int64
	for _, id := range sn.Topo.Physical() {
		if slices.Contains(projects, sn.Topo.Name(id).Project) && (!has || slices.Contains(ids, id)) {
			inProject = append(inProject, id)
		}
	}
	return inProject, true, nil
}

func findingOrder(lq ListQuery) store.Order { return store.Order{Sort: lq.Sort, Desc: lq.Desc} }

func toFinding(f report.Finding) gen.VulnerabilityFinding {
	out := gen.VulnerabilityFinding{
		Id: parseUUID(f.ID), Project: projectOf(f.Repository), Repository: f.Repository, Format: f.Format, Package: f.Package, Version: f.Version, Advisory: f.Advisory,
		Aliases: orEmpty(f.Aliases), Kind: gen.VulnerabilityKind(f.Kind), Summary: f.Summary, Severity: gen.VulnerabilitySeverity(f.Severity),
		Cvss: toCvss(f.CVSSVersion, f.CVSSVector, f.CVSSScore), KnownExploited: f.KnownExploited, FixAvailable: f.FixAvailable,
		FixedVersions: orEmpty(f.FixedVersions), Status: gen.VulnerabilityStatus(f.Status), Blocked: f.Blocked,
		DetectedAt: f.DetectedAt, ResolvedAt: f.ResolvedAt,
	}
	out.KnownExploitedAddedAt = toDate(f.KnownExploitedAddedAt)
	if len(f.Components) > 0 {
		var comps []gen.VulnerabilityImageComponent
		if err := json.Unmarshal(f.Components, &comps); err == nil {
			out.Components = &comps
		}
	}
	if f.DismissalReason != "" {
		d := gen.VulnerabilityDismissalReason(f.DismissalReason)
		out.DismissalReason = &d
	}
	if f.ResolvedReason != "" {
		d := gen.VulnerabilityResolvedReason(f.ResolvedReason)
		out.ResolvedReason = &d
	}
	if f.Blocked && f.BlockReason != "" {
		b := gen.VulnerabilityBlockReason(f.BlockReason)
		out.BlockReason = &b
	}
	return out
}

// projectOf is the project key of a qualified repository name.
func projectOf(repository string) string {
	project, _, _ := strings.Cut(repository, "/")
	return project
}

// toCvss is the vector a severity was scored from; a negative score and an empty vector are an advisory without one.
func toCvss(version, vector string, score float64) *gen.VulnerabilityCvss {
	if version == "" || vector == "" {
		return nil
	}
	return &gen.VulnerabilityCvss{Version: gen.VulnerabilityCvssVersion(version), Vector: vector, Score: float32(max(score, 0))}
}

func toDate(s string) *openapi_types.Date {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return nil
	}
	return &openapi_types.Date{Time: t}
}

func findingKey(f report.Finding) []string { return report.CursorKey(f) }

// ListVulnerabilityFindings implements GET /api/v1/vulnerabilities/findings (R-VULN-020).
func (s *Server) ListVulnerabilityFindings(w http.ResponseWriter, r *http.Request, _ gen.ListVulnerabilityFindingsParams) {
	rep, err := s.vulnReport()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	lq, err := ParseList(r, findingListSpec(), s.o.API.Config)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	ctx, sn, caller, err := s.vulnRequest(r, rep)
	var f store.Filter
	if err == nil {
		f, err = findingFilter(lq, sn, caller)
	}
	var after *store.After
	if err == nil && lq.After != nil {
		var ok bool
		if after, ok = report.ParseAfter(lq.After); !ok {
			err = invalidCursor()
		}
	}
	var sc report.Scope
	if err == nil {
		sc, err = rep.Scope(ctx, sn, caller, f)
	}
	var rows []report.Finding
	if err == nil {
		rows, err = rep.List(ctx, sc, f, findingOrder(lq), after, lq.FetchLimit())
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	WritePage(w, r, MapPage(NewPage(lq, rows, findingKey), toFinding))
}

// GetVulnerabilityFinding implements GET /api/v1/vulnerabilities/findings/{id}.
func (s *Server) GetVulnerabilityFinding(w http.ResponseWriter, r *http.Request, id string, _ gen.GetVulnerabilityFindingParams) {
	rep, err := s.vulnReport()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	_, d, err := s.findingDetail(r, rep, id)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	if NotModified(w, r, ETag(d.Finding.Revision, "")) {
		return
	}
	SetETag(w, d.Finding.Revision, "")
	writeJSON(w, r, http.StatusOK, s.toDetail(d))
}

func findingNotFound() error {
	return errs.E(errs.NotFound, errs.CodeNotFound, "The finding does not exist.")
}

// findingDetail builds the scope of one finding and reads it; an id the caller cannot read is 404, whatever its shape.
func (s *Server) findingDetail(r *http.Request, rep *report.Service, id string) (context.Context, report.Detail, error) {
	ctx, sn, caller, err := s.vulnRequest(r, rep)
	if err != nil {
		return ctx, report.Detail{}, err
	}
	sc, err := rep.Scope(ctx, sn, caller, report.IDFilter(id))
	if err != nil {
		return ctx, report.Detail{}, err
	}
	d, found, err := rep.Detail(ctx, sc, id)
	if err == nil && !found {
		err = findingNotFound()
	}
	return ctx, d, err
}

func (s *Server) toDetail(d report.Detail) gen.VulnerabilityFindingDetail {
	f := toFinding(d.Finding)
	out := gen.VulnerabilityFindingDetail{
		Finding: f, Advisory: s.toAdvisory(d.Advisory), VirtualRepositories: virtualTexts(d.VirtualRepositories),
		History: make([]gen.VulnerabilityFindingEvent, 0, len(d.History)),
	}
	changed := d.Finding.StatusChangedAt
	out.StatusChangedAt = &changed
	if d.Finding.Comment != "" {
		c := d.Finding.Comment
		out.Comment = &c
	}
	if d.Finding.Blocked && d.Finding.BlockReason == vuln.BlockTriage {
		by := &struct {
			At        time.Time `json:"at"`
			Comment   *string   `json:"comment"`
			Principal *struct {
				Id   string  `json:"id"`
				Name *string `json:"name"`
			} `json:"principal"`
		}{At: changed, Comment: out.Comment}
		if d.Finding.StatusChangedBy != "" {
			by.Principal = &struct {
				Id   string  `json:"id"`
				Name *string `json:"name"`
			}{Id: d.Finding.StatusChangedBy, Name: nonEmpty(d.Finding.StatusChangedByName)}
		}
		out.BlockedBy = by
	}
	for _, e := range d.History {
		ev := gen.VulnerabilityFindingEvent{
			At: e.At, Change: gen.VulnerabilityFindingEventChange(e.Change), FromStatus: nonEmpty(e.From), ToStatus: nonEmpty(e.To),
			Reason: nonEmpty(e.Reason), Comment: nonEmpty(e.Comment),
		}
		if e.ActorID != "" {
			ev.Actor = &struct {
				Id   string  `json:"id"`
				Name *string `json:"name"`
			}{Id: e.ActorID, Name: nonEmpty(e.ActorName)}
		}
		if len(e.Details) > 0 {
			var m map[string]any
			if json.Unmarshal(e.Details, &m) == nil {
				ev.Details = &m
			}
		}
		out.History = append(out.History, ev)
	}
	return out
}

// detailsHTML renders the Markdown of an advisory's details through the sanitizer of the package README (R-UI-008): the text is
// untrusted, and a client shows this member and never renders `details` itself. Without a renderer the text is escaped.
func (s *Server) detailsHTML(text string) string {
	if s.o.Packages.Readme == nil {
		return html.EscapeString(text)
	}
	return s.o.Packages.Readme.Render(text).HTML
}

func (s *Server) toAdvisory(a report.Advisory) gen.VulnerabilityAdvisory {
	out := gen.VulnerabilityAdvisory{
		Ecosystem: a.Ecosystem, Id: a.ID, Aliases: orEmpty(a.Aliases), Kind: gen.VulnerabilityKind(a.Kind), Summary: a.Summary,
		Details: a.Details, DetailsHtml: s.detailsHTML(a.Details), Severity: gen.VulnerabilitySeverity(a.Severity), Cvss: toCvss(a.CVSS.Version, a.CVSS.Vector, a.CVSSScore),
		KnownExploited: a.KnownExploited, KnownExploitedAddedAt: toDate(a.KnownExploitedAddedAt), Cwe: orEmpty(a.CWEIDs),
		FixedVersions: orEmpty(a.FixedVersions), Modified: a.Modified, Affected: make([]gen.VulnerabilityAffected, 0, len(a.Affected)),
		References: make([]struct {
			Type *string `json:"type,omitempty"`
			Url  string  `json:"url"`
		}, 0, len(a.References)),
		Source: gen.VulnerabilitySource{Name: a.Source.Name, Url: a.Source.URL, License: nonEmpty(a.Source.License)},
	}
	if !a.Published.IsZero() {
		t := a.Published
		out.Published = &t
	}
	if !a.Withdrawn.IsZero() {
		t := a.Withdrawn
		out.Withdrawn = &t
	}
	for _, ref := range a.References {
		out.References = append(out.References, struct {
			Type *string `json:"type,omitempty"`
			Url  string  `json:"url"`
		}{Type: nonEmpty(ref.Type), Url: ref.URL})
	}
	for _, af := range a.Affected {
		raw, err := json.Marshal(map[string]any{"name": af.Name, "ranges": nilToEmptyRanges(af.Ranges), "versions": orEmpty(af.Versions), "fixed": orEmpty(af.Fixed)})
		if err != nil {
			continue
		}
		var g gen.VulnerabilityAffected
		if json.Unmarshal(raw, &g) == nil {
			out.Affected = append(out.Affected, g)
		}
	}
	return out
}

func nilToEmptyRanges(r []advisory.Range) []advisory.Range {
	if r == nil {
		return []advisory.Range{}
	}
	return r
}

var uuidText = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// findingPatch reads the merge patch of a triage (R-VULN-013): status, dismissalReason and comment; any other member is a 400.
func findingPatch(raw []byte) (triage.Request, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return triage.Request{}, malformed("", "invalid_json", "The patch is a JSON object.", err)
	}
	var req triage.Request
	for k, v := range m {
		isNull := string(v) == "null"
		switch k {
		case "status":
			var st string
			if isNull || json.Unmarshal(v, &st) != nil {
				return req, malformed("/status", "invalid_type", "The status is a string.", nil)
			}
			req.Status = vuln.Status(st)
		case "dismissalReason":
			req.HasReason = true
			if !isNull && json.Unmarshal(v, &req.DismissalReason) != nil {
				return req, malformed("/dismissalReason", "invalid_type", "The dismissal reason is a string.", nil)
			}
		case "comment":
			req.HasComment = true
			if !isNull && json.Unmarshal(v, &req.Comment) != nil {
				return req, malformed("/comment", "invalid_type", "The comment is a string.", nil)
			}
		default:
			return req, malformed("/"+pointerEscape(k), "unknown", "The member is not known.", nil)
		}
	}
	return req, nil
}

func (s *Server) triageActor(ctx context.Context, r *http.Request) triage.Actor {
	return triage.Actor{ID: callerID(ctx), Audit: ActorFrom(httpserver.PrincipalFrom(ctx)), Source: auditSource(r)}
}

func (s *Server) vulnTriage() (*triage.Service, error) {
	if s.o.Vuln.Triage == nil || s.o.API.Store == nil {
		return nil, unavailable("The triage of findings")
	}
	return s.o.Vuln.Triage, nil
}

// UpdateVulnerabilityFinding implements PATCH /api/v1/vulnerabilities/findings/{id} (R-VULN-013): the capability was checked
// by the authorization step; the read on the finding is the scope, and a finding outside it is 404.
func (s *Server) UpdateVulnerabilityFinding(w http.ResponseWriter, r *http.Request, id string, _ gen.UpdateVulnerabilityFindingParams) {
	rep, err := s.vulnReport()
	var tri *triage.Service
	if err == nil {
		tri, err = s.vulnTriage()
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	pre, err := ParseIfMatch(r)
	var raw []byte
	if err == nil {
		raw, err = ReadMergePatch(w, r, int64(s.o.API.Config.MaxRequestBodyBytes))
	}
	var req triage.Request
	if err == nil {
		req, err = findingPatch(raw)
	}
	if err == nil {
		err = req.Validate(false)
	}
	var (
		ctx context.Context
		sn  *report.Snapshot
		c   report.Caller
		sc  report.Scope
	)
	if err == nil {
		ctx, sn, c, err = s.vulnRequest(r, rep)
	}
	if err == nil {
		sc, err = rep.Scope(ctx, sn, c, report.IDFilter(id))
	}
	var res []triage.Result
	if err == nil {
		err = s.o.API.Store.InTx(ctx, func(tx meta.Tx) (err error) {
			res, err = tri.Apply(ctx, tx, s.triageActor(ctx, r), sc.Scope, []string{id}, req, expectedRevision(pre), "")
			return err
		})
	}
	if err == nil && len(res) == 1 && res[0].Err != nil {
		err = res[0].Err
	}
	var d report.Detail
	if err == nil {
		var found bool
		if d, found, err = rep.Detail(ctx, sc, id); err == nil && !found {
			err = findingNotFound()
		}
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	SetETag(w, d.Finding.Revision, "")
	writeJSON(w, r, http.StatusOK, s.toDetail(d))
}

// setStatusBody is the body of POST /api/v1/vulnerabilities/findings/set-status (R-VULN-013).
type setStatusBody struct {
	IDs             []string `json:"ids"`
	Status          string   `json:"status"`
	DismissalReason *string  `json:"dismissalReason"`
	Comment         *string  `json:"comment"`
}

// SetVulnerabilityFindingsStatus implements POST /api/v1/vulnerabilities/findings/set-status (R-VULN-013): up to 500
// findings in one transaction, one result per id, one bulk id in every event, an idempotency key accepted.
func (s *Server) SetVulnerabilityFindingsStatus(w http.ResponseWriter, r *http.Request, _ gen.SetVulnerabilityFindingsStatusParams) {
	rep, err := s.vulnReport()
	var tri *triage.Service
	if err == nil {
		tri, err = s.vulnTriage()
	}
	if err == nil && s.idem == nil {
		err = unavailable("The triage of findings")
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	body, err := ReadBody(w, r, MediaJSON, int64(s.o.API.Config.MaxRequestBodyBytes))
	var in setStatusBody
	if err == nil {
		err = StrictUnmarshal(body, &in)
	}
	var req triage.Request
	if err == nil {
		req, err = bulkRequest(in)
	}
	var (
		ctx context.Context
		sn  *report.Snapshot
		c   report.Caller
		sc  report.Scope
	)
	if err == nil {
		ctx, sn, c, err = s.vulnRequest(r, rep)
	}
	if err == nil {
		sc, err = rep.Scope(ctx, sn, c, report.IDFilter(in.IDs...))
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	err = s.idem.Do(w, r, body, IdempotentOp{Run: func(ctx context.Context, tx meta.Tx) (Outcome, error) {
		bulk := triage.NewBulkID()
		res, err := tri.Apply(ctx, tx, s.triageActor(ctx, r), sc.Scope, in.IDs, req, errs.AnyRevision, bulk)
		if err != nil {
			return Outcome{}, err
		}
		return Outcome{Status: http.StatusOK, Body: toSetStatusResult(bulk, res)}, nil
	}})
	if err != nil {
		httpserver.Problem(w, r, err)
	}
}

func bulkRequest(in setStatusBody) (triage.Request, error) {
	if len(in.IDs) == 0 || len(in.IDs) > triage.MaxBulk {
		return triage.Request{}, errs.E(errs.Invalid, errs.CodeValidation, "1 field is invalid.", errs.WithFields(errs.FieldError{
			Pointer: "/ids", Code: "invalid_count", Detail: "A change takes 1 to 500 finding ids.",
		}))
	}
	for i, id := range in.IDs {
		if !uuidText.MatchString(id) {
			return triage.Request{}, errs.E(errs.Invalid, errs.CodeValidation, "1 field is invalid.", errs.WithFields(errs.FieldError{
				Pointer: "/ids/" + strconv.Itoa(i), Code: "invalid_value", Detail: "A finding id is a UUID.",
			}))
		}
	}
	req := triage.Request{Status: vuln.Status(in.Status)}
	if in.DismissalReason != nil {
		req.HasReason, req.DismissalReason = true, *in.DismissalReason
	}
	if in.Comment != nil {
		req.HasComment, req.Comment = true, *in.Comment
	}
	return req, req.ValidateBulk()
}

func toSetStatusResult(bulk string, res []triage.Result) gen.VulnerabilitySetStatusResult {
	out := gen.VulnerabilitySetStatusResult{BulkId: bulk, Results: make([]struct {
		Code    *string                                        `json:"code"`
		Id      openapi_types.UUID                             `json:"id"`
		Outcome gen.VulnerabilitySetStatusResultResultsOutcome `json:"outcome"`
		Status  int                                            `json:"status"`
	}, 0, len(res))}
	for _, x := range res {
		out.Results = append(out.Results, struct {
			Code    *string                                        `json:"code"`
			Id      openapi_types.UUID                             `json:"id"`
			Outcome gen.VulnerabilitySetStatusResultResultsOutcome `json:"outcome"`
			Status  int                                            `json:"status"`
		}{Code: nonEmpty(x.Code), Id: parseUUID(x.ID), Outcome: gen.VulnerabilitySetStatusResultResultsOutcome(x.Outcome), Status: x.Status})
	}
	return out
}

// GetVulnerabilityAdvisory implements GET /api/v1/vulnerabilities/advisories/{ecosystem}/{id} (R-VULN-020): the advisory is
// public data and visible to every authenticated caller, the findings of it are the ones the caller can read.
func (s *Server) GetVulnerabilityAdvisory(w http.ResponseWriter, r *http.Request, ecosystem, id string) {
	rep, err := s.vulnReport()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	adv, found, err := rep.ResolveAdvisory(r.Context(), ecosystem, id)
	if err == nil && !found {
		err = errs.E(errs.NotFound, errs.CodeNotFound, "The advisory does not exist.")
	}
	var (
		ctx context.Context
		sn  *report.Snapshot
		c   report.Caller
		sc  report.Scope
		det report.AdvisoryDetail
	)
	if err == nil {
		ctx, sn, c, err = s.vulnRequest(r, rep)
	}
	if err == nil {
		sc, err = rep.Scope(ctx, sn, c, report.AdvisoryFilter(adv.Ecosystem, adv.ID))
	}
	if err == nil {
		det, err = rep.AdvisoryFindings(ctx, sc, adv)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	out := gen.VulnerabilityAdvisoryDetail{Advisory: s.toAdvisory(det.Advisory), Findings: make([]gen.VulnerabilityFinding, 0, len(det.Findings))}
	for _, f := range det.Findings {
		out.Findings = append(out.Findings, toFinding(f))
	}
	if det.Truncated {
		t := true
		out.Truncated = &t
	}
	writeJSON(w, r, http.StatusOK, out)
}

// exportHeader are the columns of the CSV export (R-VULN-020).
func exportHeader() []string {
	return []string{
		"findingId", "status", "severity", "cvssScore", "cvssVersion", "kind", "knownExploited", "advisory", "aliases",
		"summary", "format", "project", "repository", "package", "version", "fixAvailable", "fixedVersions", "blocked", "blockReason", "detectedAt",
		"resolvedAt", "dismissalReason",
	}
}

func exportRow(f report.Finding) []string {
	score := ""
	if f.CVSSScore >= 0 && f.CVSSVersion != "" {
		score = strconv.FormatFloat(f.CVSSScore, 'f', 1, 64)
	}
	resolved := ""
	if f.ResolvedAt != nil {
		resolved = f.ResolvedAt.UTC().Format(time.RFC3339)
	}
	reason := ""
	if f.Blocked {
		reason = string(f.BlockReason)
	}
	row := []string{
		f.ID, string(f.Status), string(f.Severity), score, f.CVSSVersion, string(f.Kind), strconv.FormatBool(f.KnownExploited),
		f.Advisory, strings.Join(f.Aliases, " "), f.Summary, f.Format, projectOf(f.Repository), f.Repository, f.Package, f.Version, strconv.FormatBool(f.FixAvailable),
		strings.Join(f.FixedVersions, " "), strconv.FormatBool(f.Blocked), reason, f.DetectedAt.UTC().Format(time.RFC3339), resolved, f.DismissalReason,
	}
	for i, c := range row {
		row[i] = csvSafe(c)
	}
	return row
}

// ExportVulnerabilityFindings implements GET /api/v1/vulnerabilities/findings/export (R-VULN-020): the list's filters, the
// answer CSV only, streamed. An export above vulnerabilities.export.max_rows is 422 before anything is sent; one that is cut
// by a failing batch ends without the terminating chunk, which the client sees as incomplete.
func (s *Server) ExportVulnerabilityFindings(w http.ResponseWriter, r *http.Request, _ gen.ExportVulnerabilityFindingsParams) {
	rep, err := s.vulnReport()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	for _, n := range []string{"limit", "cursor", "includeTotal"} {
		if r.URL.Query().Has(n) {
			httpserver.Problem(w, r, badParam(n, "is not known to this endpoint"))
			return
		}
	}
	lq, err := ParseList(r, findingListSpec(), s.o.API.Config)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	ctx, sn, caller, err := s.vulnRequest(r, rep)
	var f store.Filter
	if err == nil {
		f, err = findingFilter(lq, sn, caller)
	}
	var sc report.Scope
	if err == nil {
		sc, err = rep.Scope(ctx, sn, caller, f)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	cw := csv.NewWriter(w)
	rc := http.NewResponseController(w)
	started := false
	begin := func() error {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Disposition", `attachment; filename="vulnerability-findings.csv"`)
		w.WriteHeader(http.StatusOK)
		return cw.Write(exportHeader())
	}
	n := 0
	err = rep.Export(ctx, sc, f, findingOrder(lq), func() error { started = true; return begin() }, func(fd report.Finding) error {
		if err := cw.Write(exportRow(fd)); err != nil {
			return err
		}
		if n++; n%500 == 0 {
			cw.Flush()
			if err := cw.Error(); err != nil {
				return err
			}
			_ = rc.Flush()
		}
		return nil
	})
	if err != nil {
		if !started {
			httpserver.Problem(w, r, err)
			return
		}
		httpserver.Logger(r.Context()).Error("vulnerability export: a batch failed, the export is cut", "error", err)
		panic(http.ErrAbortHandler)
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return // the client went away
	}
}

// virtualTexts renders the names of the virtual repositories that show a finding; the answer holds an array, never null.
func virtualTexts(names []qname.Repo) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = n.String()
	}
	return out
}
