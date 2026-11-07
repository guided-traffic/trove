package v1

import (
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// The read-only API of the backup job (R-API-050, R-BAK-014): the runs from backup_run, the summary, one run. The replicas
// read only backup_run, system_state and their configuration; they never hold target credentials. The three operations are
// declared in api/openapi.yaml under "M5 backup" with the permission settings:read, which the authorization step checks
// before a handler runs.
//
// The bodies below answer as an installation that has never run a backup: no runs, a summary of nulls, an unknown run. The
// API work package replaces them with reads through backup.RunReader (internal/backup) and adds the reader and
// backup.expected_interval to the server options.

// ListBackupRuns implements GET /api/v1/system/backups (R-API-050).
func (s *Server) ListBackupRuns(w http.ResponseWriter, r *http.Request, _ gen.ListBackupRunsParams) {
	writeJSON(w, r, http.StatusOK, gen.BackupRunList{Items: []gen.BackupRun{}})
}

// GetBackupSummary implements GET /api/v1/system/backups/summary (R-API-050).
func (s *Server) GetBackupSummary(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, http.StatusOK, gen.BackupSummary{})
}

// GetBackupRun implements GET /api/v1/system/backups/{runId} (R-API-050).
func (s *Server) GetBackupRun(w http.ResponseWriter, r *http.Request, _ openapi_types.UUID) {
	httpserver.Problem(w, r, errs.E(errs.NotFound, errs.CodeNotFound, "No such backup run."))
}
