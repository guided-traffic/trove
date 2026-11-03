package v1

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// The operations of the vulnerability scanning (R-VULN-011, R-VULN-020, R-VULN-021, docs/developer/vulnerabilities.md).
// The contract wave declares them in api/openapi.yaml, with the permission of R-VULN-019 that runs before the handler
// (R-API-008), and answers each with 503 until the services behind them exist, as a replica without a CI service does
// (the response conformance test of R-API-002 allows no other 5xx for a GET): work package E replaces every method of this
// file, and this notice, with a handler over the services of internal/vuln/triage and internal/vuln/report. The
// `vulnerabilityData` block of GetSystemInfo is work package G's.

// vulnUnavailable answers an operation whose services are not built or not wired on this replica.
func vulnUnavailable(w http.ResponseWriter, r *http.Request) {
	httpserver.Problem(w, r, errs.E(errs.Unavailable, errs.CodeUnavailable,
		"The vulnerability scanning is not available on this replica."))
}

// ListVulnerabilityFindings implements GET /api/v1/vulnerabilities/findings.
func (s *Server) ListVulnerabilityFindings(w http.ResponseWriter, r *http.Request, _ gen.ListVulnerabilityFindingsParams) {
	vulnUnavailable(w, r)
}

// ExportVulnerabilityFindings implements GET /api/v1/vulnerabilities/findings/export.
func (s *Server) ExportVulnerabilityFindings(w http.ResponseWriter, r *http.Request, _ gen.ExportVulnerabilityFindingsParams) {
	vulnUnavailable(w, r)
}

// SetVulnerabilityFindingsStatus implements POST /api/v1/vulnerabilities/findings/set-status.
func (s *Server) SetVulnerabilityFindingsStatus(w http.ResponseWriter, r *http.Request, _ gen.SetVulnerabilityFindingsStatusParams) {
	vulnUnavailable(w, r)
}

// GetVulnerabilityFinding implements GET /api/v1/vulnerabilities/findings/{id}.
func (s *Server) GetVulnerabilityFinding(w http.ResponseWriter, r *http.Request, _ string, _ gen.GetVulnerabilityFindingParams) {
	vulnUnavailable(w, r)
}

// UpdateVulnerabilityFinding implements PATCH /api/v1/vulnerabilities/findings/{id}.
func (s *Server) UpdateVulnerabilityFinding(w http.ResponseWriter, r *http.Request, _ string, _ gen.UpdateVulnerabilityFindingParams) {
	vulnUnavailable(w, r)
}

// GetVulnerabilityAdvisory implements GET /api/v1/vulnerabilities/advisories/{ecosystem}/{id}.
func (s *Server) GetVulnerabilityAdvisory(w http.ResponseWriter, r *http.Request, _, _ string) {
	vulnUnavailable(w, r)
}

// GetVulnerabilitySummary implements GET /api/v1/vulnerabilities/summary.
func (s *Server) GetVulnerabilitySummary(w http.ResponseWriter, r *http.Request, _ gen.GetVulnerabilitySummaryParams) {
	vulnUnavailable(w, r)
}

// GetVulnerabilityTrend implements GET /api/v1/vulnerabilities/trend.
func (s *Server) GetVulnerabilityTrend(w http.ResponseWriter, r *http.Request, _ gen.GetVulnerabilityTrendParams) {
	vulnUnavailable(w, r)
}

// ListVulnerabilitySources implements GET /api/v1/vulnerabilities/sources.
func (s *Server) ListVulnerabilitySources(w http.ResponseWriter, r *http.Request) {
	vulnUnavailable(w, r)
}

// ListClaimedNameMatches implements GET /api/v1/vulnerabilities/claimed-name-matches.
func (s *Server) ListClaimedNameMatches(w http.ResponseWriter, r *http.Request, _ gen.ListClaimedNameMatchesParams) {
	vulnUnavailable(w, r)
}

// GetPackageVersionVulnerabilities implements GET /api/v1/packages/{packageId}/versions/{version}/vulnerabilities.
func (s *Server) GetPackageVersionVulnerabilities(w http.ResponseWriter, r *http.Request, _ gen.PackageId, _ gen.PackageVersionPath) {
	vulnUnavailable(w, r)
}

// GetPackageVulnerabilities implements GET /api/v1/packages/{packageId}/vulnerabilities.
func (s *Server) GetPackageVulnerabilities(w http.ResponseWriter, r *http.Request, _ gen.PackageId) {
	vulnUnavailable(w, r)
}

// RescanRepositoryVulnerabilities implements POST /api/v1/repositories/{key}/vulnerabilities/rescan.
func (s *Server) RescanRepositoryVulnerabilities(w http.ResponseWriter, r *http.Request, _ string, _ gen.RescanRepositoryVulnerabilitiesParams) {
	vulnUnavailable(w, r)
}
