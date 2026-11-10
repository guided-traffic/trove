package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/qname"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/packages/view"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/internal/vuln/report"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// The summary, the trend, the sources, the claimed-name matches and the package views of the vulnerability scanning
// (R-VULN-019, R-VULN-020, R-VULN-021).

func toSeverityCounts(c report.Counts) gen.VulnerabilitySeverityCounts {
	return gen.VulnerabilitySeverityCounts{Critical: c.Critical, High: c.High, Medium: c.Medium, Low: c.Low, None: c.None, Unknown: c.Unknown}
}

// GetVulnerabilitySummary implements GET /api/v1/vulnerabilities/summary (R-VULN-021): per request, over the findings the
// caller can read, never cached across callers.
func (s *Server) GetVulnerabilitySummary(w http.ResponseWriter, r *http.Request, _ gen.GetVulnerabilitySummaryParams) {
	rep, err := s.vulnReport()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	lq, err := ParseList(r, ListSpec{Filters: []string{"project", "repository", "format"}}, s.o.API.Config)
	if err == nil && (r.URL.Query().Has("limit") || r.URL.Query().Has("cursor")) {
		err = badParam("limit", "is not known to this endpoint")
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	ctx, sn, caller, err := s.vulnRequest(r, rep)
	var f store.Filter
	if err == nil {
		var repos []int64
		var has bool
		if repos, has, err = findingRepos(lq, sn, caller); err == nil {
			f = report.SummaryFilter(repos, has, splitValues(lq.Filter["format"]))
		}
	}
	var sc report.Scope
	if err == nil {
		sc, err = rep.Scope(ctx, sn, caller, f)
	}
	var sum report.Summary
	if err == nil {
		sum, err = rep.Summary(ctx, sc, f)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	out := gen.VulnerabilitySummary{
		BySeverity: toSeverityCounts(sum.BySeverity), NeedsTriage: sum.NeedsTriage, BlockedVersions: sum.BlockedVersions, FixAvailable: sum.FixAvailable,
	}
	out.ByKind.Vulnerability, out.ByKind.Malicious = sum.Vulnerability, sum.Malicious
	out.Coverage.Evaluated, out.Coverage.PendingData = int(sum.Coverage.Evaluated), int(sum.Coverage.PendingData)
	out.Coverage.Internal, out.Coverage.NotCovered = int(sum.Coverage.Internal), int(sum.Coverage.NotCovered)
	out.Data = make([]struct {
		Ecosystem string                            `json:"ecosystem"`
		Stale     bool                              `json:"stale"`
		State     gen.VulnerabilitySummaryDataState `json:"state"`
		SyncedAt  *time.Time                        `json:"syncedAt"`
	}, 0, len(sum.Data))
	for _, d := range sum.Data {
		state := gen.VulnerabilitySummaryDataState("empty")
		if d.Complete {
			state = "complete"
		}
		out.Data = append(out.Data, struct {
			Ecosystem string                            `json:"ecosystem"`
			Stale     bool                              `json:"stale"`
			State     gen.VulnerabilitySummaryDataState `json:"state"`
			SyncedAt  *time.Time                        `json:"syncedAt"`
		}{Ecosystem: d.Ecosystem, Stale: d.Stale, State: state, SyncedAt: d.SyncedAt})
	}
	writeJSON(w, r, http.StatusOK, out)
}

// trendDay reads a date parameter of the trend.
func trendDay(raw, name string, def time.Time) (time.Time, error) {
	if raw == "" {
		return def, nil
	}
	t, err := time.Parse(time.DateOnly, raw)
	if err != nil {
		return time.Time{}, badParam(name, "must be a date, YYYY-MM-DD")
	}
	return t, nil
}

// GetVulnerabilityTrend implements GET /api/v1/vulnerabilities/trend (R-VULN-021, Q-VULN-16).
func (s *Server) GetVulnerabilityTrend(w http.ResponseWriter, r *http.Request, _ gen.GetVulnerabilityTrendParams) {
	rep, err := s.vulnReport()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	lq, err := ParseList(r, ListSpec{Filters: []string{"from", "to", "project", "repository", "format", "kind"}}, s.o.API.Config)
	if err == nil && (r.URL.Query().Has("limit") || r.URL.Query().Has("cursor")) {
		err = badParam("limit", "is not known to this endpoint")
	}
	var to, from time.Time
	if err == nil {
		today := time.Now().UTC().Truncate(24 * time.Hour)
		if to, err = trendDay(lq.First("to"), "to", today); err == nil {
			from, err = trendDay(lq.First("from"), "from", to.AddDate(0, 0, -89))
		}
	}
	var kind string
	if err == nil {
		kind, err = queryOneOf(lq, "kind", []string{string(advisory.KindVulnerability), string(advisory.KindMalicious)})
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	ctx, sn, caller, err := s.vulnRequest(r, rep)
	var f store.Filter
	if err == nil {
		var repos []int64
		var has bool
		if repos, has, err = findingRepos(lq, sn, caller); err == nil {
			f = store.Filter{Repos: repos, HasRepos: has, Formats: splitValues(lq.Filter["format"]), Kind: kind}
		}
	}
	var sc report.Scope
	if err == nil {
		sc, err = rep.Scope(ctx, sn, caller, store.Filter{Repos: f.Repos, HasRepos: f.HasRepos, Formats: f.Formats, Kind: f.Kind, Statuses: report.AllStatuses()})
	}
	var points []report.TrendPoint
	if err == nil {
		points, err = rep.Trend(ctx, sc, f, from, to)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	out := gen.VulnerabilityTrend{From: openapi_types.Date{Time: from}, To: openapi_types.Date{Time: to}, Basis: report.TrendBasis}
	out.Points = make([]struct {
		Counts gen.VulnerabilitySeverityCounts `json:"counts"`
		Date   openapi_types.Date              `json:"date"`
	}, 0, len(points))
	for _, p := range points {
		d, _ := time.Parse(time.DateOnly, p.Date)
		out.Points = append(out.Points, struct {
			Counts gen.VulnerabilitySeverityCounts `json:"counts"`
			Date   openapi_types.Date              `json:"date"`
		}{Counts: toSeverityCounts(p.Counts), Date: openapi_types.Date{Time: d}})
	}
	writeJSON(w, r, http.StatusOK, out)
}

// nextRunOf answers the next scheduled run of a task for the sources view, from the resolved schedules of the registry.
func (s *Server) nextRunOf() func(task string) *time.Time {
	tasks, err := s.resolveTasks()
	if err != nil {
		return nil
	}
	now := time.Now()
	return func(name string) *time.Time {
		for _, t := range tasks {
			if t.Name == name && t.Enabled && t.Sched != nil {
				if next, ok := t.Sched.Next(now); ok {
					n := next.UTC()
					return &n
				}
			}
		}
		return nil
	}
}

func toProblem(raw json.RawMessage) *gen.Problem {
	if len(raw) == 0 {
		return nil
	}
	var p gen.Problem
	if json.Unmarshal(raw, &p) != nil {
		return nil
	}
	return &p
}

// ListVulnerabilitySources implements GET /api/v1/vulnerabilities/sources (R-VULN-021); the caller holds settings:read.
func (s *Server) ListVulnerabilitySources(w http.ResponseWriter, r *http.Request) {
	rep, err := s.vulnReport()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	rows, err := rep.Sources(r.Context(), s.nextRunOf())
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	out := gen.VulnerabilitySourceList{Items: make([]gen.VulnerabilitySourceState, 0, len(rows))}
	for _, v := range rows {
		out.Items = append(out.Items, gen.VulnerabilitySourceState{
			Source: gen.VulnerabilitySourceStateSource(v.Source), Ecosystem: v.Ecosystem, Mode: gen.VulnerabilitySourceStateMode(v.Mode),
			State: gen.VulnerabilitySourceStateState(v.State), Stale: v.Stale, SyncedAt: v.SyncedAt, FullSyncedAt: v.FullSyncedAt,
			AttemptedAt: v.AttemptedAt, Failures: v.Failures, LastError: toProblem(v.LastError), Records: v.Records, Rejected: v.Rejected, FullRejected: v.FullRejected,
			Unevaluable: v.Unevaluable, NextRunAt: v.NextRunAt, Repository: nonEmpty(v.Repository),
		})
	}
	writeJSON(w, r, http.StatusOK, out)
}

// ListClaimedNameMatches implements GET /api/v1/vulnerabilities/claimed-name-matches (R-VULN-010); the caller holds
// repositories:read-config.
func (s *Server) ListClaimedNameMatches(w http.ResponseWriter, r *http.Request, _ gen.ListClaimedNameMatchesParams) {
	rep, err := s.vulnReport()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	lq, err := ParseList(r, ListSpec{}, s.o.API.Config)
	var after *store.ClaimedAfter
	if err == nil && lq.After != nil {
		if len(lq.After) != 3 {
			err = invalidCursor()
		} else {
			after = &store.ClaimedAfter{Ecosystem: lq.After[0], Advisory: lq.After[1], Name: lq.After[2]}
		}
	}
	var rows []report.ClaimedMatch
	if err == nil {
		rows, err = rep.ClaimedMatches(r.Context(), after, lq.FetchLimit())
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	page := NewPage(lq, rows, func(m report.ClaimedMatch) []string { return []string{m.Ecosystem, m.Advisory, m.Name} })
	WritePage(w, r, MapPage(page, func(m report.ClaimedMatch) gen.ClaimedNameMatch {
		return gen.ClaimedNameMatch{
			Ecosystem: m.Ecosystem, Advisory: m.Advisory, Name: m.Name, Kind: gen.VulnerabilityKind(m.Kind),
			ClaimRepository: nonEmpty(m.ClaimRepository), FirstSeenAt: m.FirstSeenAt,
		}
	}))
}

// packageIdentity reads the package of a package ID, 404 for one that is none.
func packageIdentity(id string) (format, name string, err error) {
	format, name, ok := view.ParseID(id)
	if !ok {
		return "", "", packageNotFound()
	}
	return format, name, nil
}

// GetPackageVersionVulnerabilities implements GET /api/v1/packages/{packageId}/versions/{version}/vulnerabilities
// (R-VULN-020): per readable repository that holds the version, the evaluation state, the block and the findings.
func (s *Server) GetPackageVersionVulnerabilities(w http.ResponseWriter, r *http.Request, id gen.PackageId, version gen.PackageVersionPath) {
	rep, err := s.vulnReport()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	format, name, err := packageIdentity(id)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	if v, derr := url.PathUnescape(version); derr == nil {
		version = v
	}
	ctx, sn, caller, err := s.vulnRequest(r, rep)
	var sc report.Scope
	if err == nil {
		sc, err = rep.PackageScope(ctx, sn, caller, format, name, version)
	}
	var repos []report.RepositoryVersion
	if err == nil {
		var found bool
		if repos, found, err = rep.Version(ctx, sc, format, name, version); err == nil && !found {
			err = errs.E(errs.NotFound, errs.CodeNotFound, "The version does not exist.")
		}
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	out := gen.VulnerabilityVersionReport{PackageId: id, Version: version}
	out.Repositories = make([]struct {
		BlockReason *gen.VulnerabilityVersionBlockReason `json:"blockReason"`
		Blocked     bool                                 `json:"blocked"`
		Evaluation  gen.VulnerabilityEvaluation          `json:"evaluation"`
		Findings    []gen.VulnerabilityFinding           `json:"findings"`
		Repository  string                               `json:"repository"`
		SyncedAt    *time.Time                           `json:"syncedAt,omitempty"`
	}, 0, len(repos))
	for _, rv := range repos {
		item := struct {
			BlockReason *gen.VulnerabilityVersionBlockReason `json:"blockReason"`
			Blocked     bool                                 `json:"blocked"`
			Evaluation  gen.VulnerabilityEvaluation          `json:"evaluation"`
			Findings    []gen.VulnerabilityFinding           `json:"findings"`
			Repository  string                               `json:"repository"`
			SyncedAt    *time.Time                           `json:"syncedAt,omitempty"`
		}{
			Blocked: rv.Blocked, Evaluation: gen.VulnerabilityEvaluation(rv.Evaluation), Repository: rv.Repository, SyncedAt: rv.SyncedAt,
			Findings: make([]gen.VulnerabilityFinding, 0, len(rv.Findings)),
		}
		if rv.Blocked && rv.BlockReason != "" {
			b := gen.VulnerabilityVersionBlockReason(rv.BlockReason)
			item.BlockReason = &b
		}
		for _, f := range rv.Findings {
			item.Findings = append(item.Findings, toFinding(f))
		}
		out.Repositories = append(out.Repositories, item)
	}
	writeJSON(w, r, http.StatusOK, out)
}

// GetPackageVulnerabilities implements GET /api/v1/packages/{packageId}/vulnerabilities (R-VULN-020): the open findings per
// version, counted by severity, over the findings the caller can read.
// A repository named in `repository` narrows the counts to that repository (a virtual one to its members), as the repository's content
// page counts them (R-UI-052).
func (s *Server) GetPackageVulnerabilities(w http.ResponseWriter, r *http.Request, id gen.PackageId, params gen.GetPackageVulnerabilitiesParams) {
	rep, err := s.vulnReport()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	format, name, err := packageIdentity(id)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	var repoNames []string
	if params.Repository != nil {
		repoNames = *params.Repository
	}
	names := repoFilter(repoNames)
	ctx, sn, caller, err := s.vulnRequest(r, rep)
	var sc report.Scope
	if err == nil {
		sc, err = rep.PackageScope(ctx, sn, caller, format, name, "")
	}
	var versions []report.VersionCounts
	if err == nil {
		f := store.Filter{}
		if params.Repository != nil {
			f.Repos, f.HasRepos = sn.ExpandFor(caller, sn.Topo.IDs(names)), true
		}
		var found bool
		if versions, found, err = rep.Package(ctx, sc, format, name, f); err == nil && !found {
			err = packageNotFound()
		}
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	out := gen.VulnerabilityPackageReport{PackageId: id}
	out.Versions = make([]struct {
		Open    gen.VulnerabilitySeverityCounts `json:"open"`
		Version string                          `json:"version"`
	}, 0, len(versions))
	for _, v := range versions {
		out.Versions = append(out.Versions, struct {
			Open    gen.VulnerabilitySeverityCounts `json:"open"`
			Version string                          `json:"version"`
		}{Open: toSeverityCounts(v.Open), Version: v.Version})
	}
	writeJSON(w, r, http.StatusOK, out)
}

// RescanRepositoryVulnerabilities implements POST /api/v1/projects/{projectKey}/repositories/{repoKey}/vulnerabilities/rescan (R-VULN-011): it enqueues
// a vuln-rescan job of the repository and answers 202 with the job, 409 with the running one. The caller holds manage on the
// repository (declared, Q-VULN-27).
func (s *Server) RescanRepositoryVulnerabilities(w http.ResponseWriter, r *http.Request, projectKey, repoKeyText string, _ gen.RescanRepositoryVulnerabilitiesParams) {
	key := qname.Repo{Project: projectKey, Key: repoKeyText}
	rep, err := s.vulnReport()
	if err == nil && (s.idem == nil || s.o.API.Queue == nil) {
		err = unavailable("The rescan")
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	sn, err := rep.Snapshot(r.Context())
	var id int64
	if err == nil {
		repo, ok := sn.Repository(sn.Topo.ID(key))
		switch {
		case !ok:
			err = errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "No repository has the requested key.")
		case repo.VulnerabilityEcosystem() == "":
			err = errs.E(errs.Invalid, errs.CodeNotApplicable, "The vulnerability scanning does not apply to this repository: it is virtual or of a format without the vulnerability adapter.",
				errs.WithFields(errs.FieldError{Pointer: "/vulnerabilities", Code: errs.CodeNotApplicable, Detail: "does not apply to this repository"}))
		default:
			id, err = parseRepositoryID(repo.ID)
		}
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	err = s.idem.Do(w, r, nil, IdempotentOp{Run: func(ctx context.Context, tx meta.Tx) (Outcome, error) {
		res, err := s.o.API.Queue.Enqueue(ctx, tx, jobs.EnqueueSpec{
			Kind: vuln.KindRescan, Args: vuln.RescanArgs{Repository: key.String(), RepositoryID: id}, DedupKey: vuln.RescanKey(id),
			TargetKind: "repository", TargetKey: key.String(), CreatedBy: callerID(ctx),
		})
		if err != nil {
			return Outcome{}, err
		}
		ref := jobReference(res.Job.ID, res.Job.Kind, res.Job.State)
		if !res.Created {
			return Outcome{}, errs.E(errs.Conflict, errs.CodeConflict, "A rescan of this repository is queued or running.", errs.WithExtension("job", ref))
		}
		return Outcome{Status: http.StatusAccepted, Header: map[string]string{"Location": jobLocation(r, res.Job.ID)}, Body: gen.JobReferenceEnvelope{Job: ref}}, nil
	}})
	if err != nil {
		httpserver.Problem(w, r, err)
	}
}

func parseRepositoryID(s string) (int64, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, errs.E(errs.Internal, errs.CodeInternal, "The repository has no numeric id.", errs.WithCause(err))
	}
	return n, nil
}
