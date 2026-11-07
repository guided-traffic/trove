package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/gauges"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// The read-only API of the backup job (R-API-050, R-BAK-014): the runs from backup_run, the summary, one run. The replicas
// read only backup_run, system_state and their configuration; they never hold target credentials. The three operations are
// declared in api/openapi.yaml under "M5 backup" with the permission settings:read, which the authorization step checks
// before a handler runs. No operation writes: a manual run is a Job the operator creates (Q-API-10).

// BackupServices are the sources of the backup operations and of the backup block of GET /api/v1/system/info. A zero value
// answers the operations with 503 and the block with null.
type BackupServices struct {
	// Runs reads backup_run (backup.PGStore).
	Runs backup.RunReader
	// ExpectedInterval is backup.expected_interval: the threshold of the overdue mark.
	ExpectedInterval time.Duration
	// Namespace and CronJob are backup.manual_run, which the chart renders; "" is unset and shown as null.
	Namespace, CronJob string
	// RestoredFrom reads system_state restored_from; nil, or a nil answer, is an installation that was never restored.
	RestoredFrom func(ctx context.Context) (*gen.BackupRestoredFrom, error)
}

// listedRunsLookback is how many of the newest runs of one operation the summary inspects for the run that last listed the
// target (storedBackups, retention). A create with --no-prune records neither (R-BAK-016), so the newest run is not always
// the one; more than this many such runs in a row leave both null until the next listing run.
const listedRunsLookback = 25

func (s *Server) backupRuns() (backup.RunReader, error) {
	if s.o.Backup.Runs == nil {
		return nil, errs.E(errs.Unavailable, errs.CodeUnavailable, "The backup runs are not available on this replica.")
	}
	return s.o.Backup.Runs, nil
}

// ListBackupRuns implements GET /api/v1/system/backups (R-API-050).
func (s *Server) ListBackupRuns(w http.ResponseWriter, r *http.Request, params gen.ListBackupRunsParams) {
	reader, err := s.backupRuns()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	lq, err := ParseList(r, ListSpec{
		Filters: []string{"operation", "state", "trigger", "startedFrom", "startedTo"}, Total: true,
	}, s.o.API.Config)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	filter, err := backupFilter(lq, params)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	if len(lq.After) > 0 {
		cur, ok := decodeRunCursor(lq.After)
		if !ok {
			httpserver.Problem(w, r, invalidCursor())
			return
		}
		filter.After = &cur
	}
	filter.Limit = lq.FetchLimit()
	ctx := r.Context()
	runs, err := reader.List(ctx, filter)
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.listBackupRuns"))
		return
	}
	page := NewPage(lq, runs, func(run backup.Run) []string {
		return []string{run.StartedAt.UTC().Format(time.RFC3339Nano), run.ID}
	})
	now, err := backupNow(ctx, reader, page.Items)
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.listBackupRuns"))
		return
	}
	out := MapPage(page, func(run backup.Run) gen.BackupRun { return backupRunBody(run, now) })
	if lq.IncludeTotal {
		total, err := reader.Count(ctx, filter)
		if err != nil {
			httpserver.Problem(w, r, errs.Wrap(err, "api.listBackupRuns"))
			return
		}
		out = out.WithTotal(total)
	}
	WritePage(w, r, out)
}

// GetBackupRun implements GET /api/v1/system/backups/{runId} (R-API-050).
func (s *Server) GetBackupRun(w http.ResponseWriter, r *http.Request, runID openapi_types.UUID) {
	reader, err := s.backupRuns()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	run, ok, err := reader.Get(r.Context(), runID.String())
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.getBackupRun"))
		return
	}
	if !ok {
		httpserver.Problem(w, r, errs.E(errs.NotFound, errs.CodeNotFound, "No such backup run."))
		return
	}
	now, err := backupNow(r.Context(), reader, []backup.Run{run})
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.getBackupRun"))
		return
	}
	writeJSON(w, r, http.StatusOK, backupRunBody(run, now))
}

// GetBackupSummary implements GET /api/v1/system/backups/summary (R-API-050).
func (s *Server) GetBackupSummary(w http.ResponseWriter, r *http.Request) {
	reader, err := s.backupRuns()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	ctx := r.Context()
	jobs, err := gauges.ReadJobs(ctx, reader)
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.getBackupSummary"))
		return
	}
	sv := s.o.Backup
	out := gen.BackupSummary{
		ExpectedIntervalSeconds: int64(sv.ExpectedInterval / time.Second),
		LastSuccessAt:           jobs.LastSuccessAt(),
		Overdue:                 jobs.Overdue(sv.ExpectedInterval),
		ManualRun:               gen.BackupManualRun{Namespace: nonEmpty(sv.Namespace), CronJob: nonEmpty(sv.CronJob)},
	}
	if f := jobs.LastFailure(); f != nil {
		out.LastFailure = &gen.BackupLastFailure{RunId: parseUUID(f.RunID), At: f.At, Code: nonEmpty(f.Code)}
	}
	if a := jobs.ActiveRun(); a != nil {
		out.Active = &gen.BackupActiveRun{
			Id: parseUUID(a.ID), Operation: string(a.Operation), State: string(a.State), StartedAt: a.StartedAt,
			LeaseExpired: a.LeaseExpired(jobs.Now),
		}
	}
	newest, err := reader.List(ctx, backup.ListFilter{Limit: 1})
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.getBackupSummary"))
		return
	}
	if len(newest) > 0 {
		out.Target = nonEmpty(newest[0].Target)
	}
	out.Retention, out.StoredBackups, err = listedTarget(ctx, reader)
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.getBackupSummary"))
		return
	}
	if sv.RestoredFrom != nil {
		if out.RestoredFrom, err = sv.RestoredFrom(ctx); err != nil {
			httpserver.Problem(w, r, errs.Wrap(err, "api.getBackupSummary"))
			return
		}
	}
	writeJSON(w, r, http.StatusOK, out)
}

// backupInfo is the backup block of GET /api/v1/system/info (R-OPS-004): the short form of the summary under the same overdue
// rule. It is nil where the runs are not wired and, because the block is best effort like the other database facts of the
// answer, when they cannot be read.
func (s *Server) backupInfo(ctx context.Context) *gen.BackupInfo {
	reader := s.o.Backup.Runs
	if reader == nil {
		return nil
	}
	jobs, err := gauges.ReadJobs(ctx, reader)
	if err != nil {
		return nil
	}
	out := &gen.BackupInfo{
		ExpectedIntervalSeconds: int64(s.o.Backup.ExpectedInterval / time.Second),
		LastSuccessAt:           jobs.LastSuccessAt(),
		Overdue:                 jobs.Overdue(s.o.Backup.ExpectedInterval),
	}
	newest, err := reader.List(ctx, backup.ListFilter{Limit: 1})
	if err != nil {
		return nil
	}
	if len(newest) > 0 {
		state := string(newest[0].State)
		out.LastRunState = &state
	}
	return out
}

// listedTarget finds the run that last listed the target, a create or delete run whose result records the retention it
// applied, and returns what it recorded (R-BAK-016, Q-API-11): both are nil until such a run exists.
func listedTarget(ctx context.Context, reader backup.RunReader) (*gen.BackupRetention, *gen.StoredBackups, error) {
	var newest *backup.Run
	var result backup.Result
	for _, op := range []backup.Operation{backup.OpCreate, backup.OpDelete} {
		runs, err := reader.List(ctx, backup.ListFilter{Operation: string(op), Limit: listedRunsLookback})
		if err != nil {
			return nil, nil, err
		}
		for i := range runs {
			run := runs[i]
			if !run.State.Succeeded() || run.FinishedAt == nil || (newest != nil && !run.FinishedAt.After(*newest.FinishedAt)) {
				continue
			}
			var res backup.Result
			if json.Unmarshal(run.Result, &res) != nil || res.Retention == nil {
				continue
			}
			newest, result = &run, res
		}
	}
	if newest == nil {
		return nil, nil, nil
	}
	items := make([]gen.StoredBackup, 0, len(result.Retained))
	for _, b := range result.Retained {
		items = append(items, gen.StoredBackup{
			BackupId: b.BackupID, SnapshotAt: b.SnapshotAt, TroveVersion: b.TroveVersion, SchemaStep: b.SchemaStep,
			DatabaseBytes: b.DatabaseBytes, BlobCount: b.BlobCount, BlobBytes: b.BlobBytes, Damaged: b.Damaged,
		})
	}
	return &gen.BackupRetention{KeepLast: result.Retention.KeepLast, KeepWithinSeconds: result.Retention.KeepWithinSeconds},
		&gen.StoredBackups{AsOf: *newest.FinishedAt, RunId: parseUUID(newest.ID), Items: items}, nil
}

// backupNow is the database's clock when a run in the answer is in an active state, the only case in which a lease is
// compared; otherwise it costs no query. The comparison never uses the replica's clock where the database's is at hand.
func backupNow(ctx context.Context, reader backup.RunReader, runs []backup.Run) (time.Time, error) {
	for _, run := range runs {
		if run.State.Active() {
			_, now, err := reader.ActiveRuns(ctx)
			return now, err
		}
	}
	return time.Time{}, nil
}

// backupFilter reads the filters of the list. An unknown operation, state or trigger is a malformed request, not an empty list.
func backupFilter(lq ListQuery, params gen.ListBackupRunsParams) (backup.ListFilter, error) {
	f := backup.ListFilter{Operation: lq.First("operation"), State: lq.First("state"), Trigger: lq.First("trigger")}
	if f.Operation != "" && !inSet(f.Operation, "create", "delete", "verify") {
		return f, errs.E(errs.Invalid, errs.CodeInvalid, "The query parameter \"operation\" must be create, delete or verify.")
	}
	if f.Trigger != "" && !inSet(f.Trigger, "schedule", "manual") {
		return f, errs.E(errs.Invalid, errs.CodeInvalid, "The query parameter \"trigger\" must be schedule or manual.")
	}
	if f.State != "" && !inSet(f.State, "starting", "snapshot", "copying", "finalizing", "pruning", "completed",
		"completed_with_warnings", "failed", "interrupted", "abandoned") {
		return f, errs.E(errs.Invalid, errs.CodeInvalid, "The query parameter \"state\" is not a state of a backup run.")
	}
	if params.StartedFrom != nil {
		f.StartedFrom = *params.StartedFrom
	}
	if params.StartedTo != nil {
		f.StartedTo = *params.StartedTo
	}
	return f, nil
}

func inSet(v string, set ...string) bool {
	for _, s := range set {
		if v == s {
			return true
		}
	}
	return false
}

func decodeRunCursor(key []string) (backup.RunCursor, bool) {
	if len(key) != 2 {
		return backup.RunCursor{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, key[0])
	if err != nil {
		return backup.RunCursor{}, false
	}
	var id openapi_types.UUID
	if id.UnmarshalText([]byte(key[1])) != nil {
		return backup.RunCursor{}, false
	}
	return backup.RunCursor{StartedAt: at, ID: id.String()}, true
}

// backupRunBody renders a run in the names of R-API-003. now is the database's clock for the lease; it is the zero time when
// no run of the answer is active, which leaves leaseExpired false for the runs that are not.
func backupRunBody(run backup.Run, now time.Time) gen.BackupRun {
	out := gen.BackupRun{
		Id: parseUUID(run.ID), Operation: string(run.Operation), BackupId: nonEmpty(run.BackupID), State: string(run.State),
		Trigger: string(run.Trigger), Pod: run.Pod, TroveVersion: run.TroveVersion, Target: run.Target,
		StartedAt: run.StartedAt, HeartbeatAt: run.HeartbeatAt, LeaseExpired: run.LeaseExpired(now),
		FinishedAt: run.FinishedAt, SnapshotAt: run.SnapshotAt, Progress: backupProgressBody(run.Progress),
		Result: jsonObject(run.Result), Error: jsonObject(run.Error),
	}
	return out
}

func backupProgressBody(p backup.Progress) gen.BackupProgress {
	out := gen.BackupProgress{SnapshotOpenSince: p.SnapshotOpenSince}
	if p.Phase != "" {
		out.Phase = &p.Phase
	}
	if p.TablesDone != 0 {
		out.TablesDone = &p.TablesDone
	}
	if p.TablesTotal != 0 {
		out.TablesTotal = &p.TablesTotal
	}
	if p.BlobsToCopy != 0 {
		out.BlobsToCopy = &p.BlobsToCopy
	}
	if p.BlobsCopied != 0 {
		out.BlobsCopied = &p.BlobsCopied
	}
	if p.BytesCopied != 0 {
		out.BytesCopied = &p.BytesCopied
	}
	if p.PinsLeft != 0 {
		out.PinsLeft = &p.PinsLeft
	}
	return out
}
