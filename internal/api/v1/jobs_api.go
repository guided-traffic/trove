package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The job operations of R-OPS-019 and R-API-015: list, read, cancel. Jobs are
// rows of the job table, so every replica sees every job at once; the replica
// that answers only reads or marks the row, and a worker does the work.

// pollSeconds is the Retry-After of a job that has not ended (R-API-015).
const pollSeconds = 2

func jobHref(id string) string { return "/api/v1/system/jobs/" + id }

// jobFields is the part of the list and read queries both return; the three
// generated row types have the same fields, so they convert to it.
type jobFields = q.GetJobViewRow

func toJob(r jobFields) gen.Job {
	id := r.ID.String()
	j := gen.Job{
		Id: openapi_types.UUID(r.ID.Bytes), Kind: r.Kind, State: gen.JobState(r.State),
		EnqueuedAt: r.CreatedAt.Time.UTC(), Attempt: int(r.Attempt), MaxAttempts: int(r.MaxAttempts),
		Href: jobHref(id),
	}
	if r.StartedAt.Valid {
		t := r.StartedAt.Time.UTC()
		j.StartedAt = &t
	}
	if r.FinishedAt.Valid {
		t := r.FinishedAt.Time.UTC()
		j.FinishedAt = &t
	}
	if r.State != string(gen.JobStateQueued) {
		switch {
		case r.ReplicaName.Valid:
			j.Replica = &r.ReplicaName.String
		case r.RanBy.Valid:
			u := r.RanBy.String()
			j.Replica = &u
		}
	}
	if r.ProgressDone.Valid {
		p := gen.JobProgress{Done: r.ProgressDone.Int64}
		if r.ProgressTotal.Valid {
			p.Total = &r.ProgressTotal.Int64
		}
		if r.ProgressUnit.Valid {
			p.Unit = &r.ProgressUnit.String
		}
		j.Progress = &p
	}
	j.Result, j.Error = jsonObject(r.Result), jsonObject(r.Error)
	if r.CreatedBy.Valid {
		by := r.CreatedBy.String()
		j.TriggeredBy = &by
	}
	if r.TargetKind.Valid && r.TargetKey.Valid {
		j.Target = &gen.JobTarget{Kind: r.TargetKind.String, Key: r.TargetKey.String}
	}
	return j
}

// jsonObject decodes a jsonb object column; SQL NULL and JSON null are nil.
func jsonObject(raw []byte) *map[string]interface{} {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]interface{}
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return nil
	}
	return &m
}

func jobReference(id, kind, state string) gen.JobReference {
	return gen.JobReference{Id: parseUUID(id), Kind: kind, State: gen.JobState(state), Href: jobHref(id)}
}

func parseUUID(s string) openapi_types.UUID {
	var u openapi_types.UUID
	_ = u.UnmarshalText([]byte(s))
	return u
}

// jobSortKey is the sort key of the job list: the creation time with
// microseconds, as PostgreSQL stores it, and the ID.
func jobSortKey(j gen.Job) []string {
	return []string{j.EnqueuedAt.UTC().Format(time.RFC3339Nano), j.Id.String()}
}

// visibleJobs says which jobs the caller may see: all with settings:read, else
// the ones it started (R-API-015). ok is false for a caller that can see none.
func (s *Server) visibleJobs(ctx context.Context) (createdBy string, ok bool, err error) {
	all, err := s.holds(ctx, string(authz.SettingsRead))
	if err != nil || all {
		return "", err == nil, err
	}
	id := callerID(ctx)
	return id, id != "", nil
}

// ListJobs implements GET /api/v1/system/jobs (R-OPS-019).
func (s *Server) ListJobs(w http.ResponseWriter, r *http.Request, _ gen.ListJobsParams) {
	if s.o.API.Store == nil {
		httpserver.Problem(w, r, unavailable("The job list"))
		return
	}
	lq, err := ParseList(r, ListSpec{
		Filters: []string{"kind", "state"}, Sorts: []string{"enqueuedAt"}, DefaultSort: "-enqueuedAt",
	}, s.o.API.Config)
	if err == nil {
		err = validJobStates(lq.Filter["state"])
	}
	if err == nil && lq.After != nil && len(lq.After) != 2 {
		err = invalidCursor()
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	createdBy, visible, err := s.visibleJobs(r.Context())
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	var items []gen.Job
	if visible {
		items, err = s.queryJobs(r.Context(), lq, createdBy)
		if err != nil {
			httpserver.Problem(w, r, err)
			return
		}
	}
	WritePage(w, r, NewPage(lq, items, jobSortKey))
}

func validJobStates(states []string) error {
	for _, st := range states {
		switch gen.JobState(st) {
		case gen.JobStateQueued, gen.JobStateRunning, gen.JobStateSucceeded, gen.JobStateFailed, gen.JobStateCancelling, gen.JobStateCancelled:
		default:
			return errs.E(errs.Invalid, errs.CodeInvalid, "The query parameter \"state\" has an unknown value: "+st+".")
		}
	}
	return nil
}

// queryJobs runs the keyset query of one page: one statement on the request
// pool, no state between pages.
func (s *Server) queryJobs(ctx context.Context, lq ListQuery, createdBy string) ([]gen.Job, error) {
	// A nil slice would be SQL NULL and match nothing: "no filter" is an empty array.
	kinds, states := append([]string{}, lq.Filter["kind"]...), append([]string{}, lq.Filter["state"]...)
	afterAt, afterID := "", ""
	if len(lq.After) == 2 {
		afterAt, afterID = lq.After[0], lq.After[1]
	}
	var out []gen.Job
	err := s.o.API.Store.Read(ctx, func(db q.DBTX) error {
		out = out[:0]
		qs := q.New(db)
		limit := int32(lq.FetchLimit()) //nolint:gosec // bounded by api.page_size_max
		if lq.Desc {
			rows, err := qs.ListJobsNewestFirst(ctx, q.ListJobsNewestFirstParams{
				Kinds: kinds, States: states, CreatedBy: createdBy, AfterAt: afterAt, AfterID: afterID, RowLimit: limit,
			})
			for _, row := range rows {
				out = append(out, toJob(jobFields(row)))
			}
			return err
		}
		rows, err := qs.ListJobsOldestFirst(ctx, q.ListJobsOldestFirstParams{
			Kinds: kinds, States: states, CreatedBy: createdBy, AfterAt: afterAt, AfterID: afterID, RowLimit: limit,
		})
		for _, row := range rows {
			out = append(out, toJob(jobFields(row)))
		}
		return err
	})
	return out, err
}

// loadJob reads one job; found is false for a missing one.
func (s *Server) loadJob(ctx context.Context, id string) (j gen.Job, createdBy string, found bool, err error) {
	err = s.o.API.Store.Read(ctx, func(db q.DBTX) error {
		rows, err := q.New(db).GetJobView(ctx, id)
		if err != nil || len(rows) == 0 {
			found = false
			return err
		}
		found = true
		j = toJob(rows[0])
		createdBy = ""
		if rows[0].CreatedBy.Valid {
			createdBy = rows[0].CreatedBy.String()
		}
		return nil
	})
	return j, createdBy, found, err
}

func errJobNotFound() error { return errs.E(errs.NotFound, errs.CodeNotFound, "No job has this ID.") }

// readableJob loads a job and applies the visibility rule of R-API-015: a caller
// without settings:read sees only the jobs it started, and any other job
// answers like a missing one.
func (s *Server) readableJob(ctx context.Context, id string) (gen.Job, string, bool, error) {
	if s.o.API.Store == nil {
		return gen.Job{}, "", false, unavailable("The job list")
	}
	j, createdBy, found, err := s.loadJob(ctx, id)
	if err != nil || !found {
		return gen.Job{}, "", false, err
	}
	all, err := s.holds(ctx, string(authz.SettingsRead))
	if err != nil {
		return gen.Job{}, "", false, err
	}
	if !all && (createdBy == "" || createdBy != callerID(ctx)) {
		return gen.Job{}, "", false, nil
	}
	return j, createdBy, true, nil
}

// GetJob implements GET /api/v1/system/jobs/{id} (R-OPS-019).
func (s *Server) GetJob(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	j, _, ok, err := s.readableJob(r.Context(), id.String())
	switch {
	case err != nil:
		httpserver.Problem(w, r, err)
	case !ok:
		httpserver.Problem(w, r, errJobNotFound())
	default:
		if !jobEnded(j.State) {
			w.Header().Set("Retry-After", strconv.Itoa(pollSeconds))
		}
		writeJSON(w, r, http.StatusOK, j)
	}
}

func jobEnded(st gen.JobState) bool {
	return st == gen.JobStateSucceeded || st == gen.JobStateFailed || st == gen.JobStateCancelled
}

// CancelJob implements POST /api/v1/system/jobs/{id}/cancel (R-OPS-019).
func (s *Server) CancelJob(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	ctx := r.Context()
	j, createdBy, ok, err := s.readableJob(ctx, id.String())
	if err == nil && !ok {
		err = errJobNotFound()
	}
	if err == nil {
		err = s.mayCancel(ctx, createdBy)
	}
	if err == nil && jobEnded(j.State) {
		err = errs.E(errs.Conflict, errs.CodeConflict, "The job has ended and cannot be cancelled.", errs.WithExtension("job", jobReference(j.Id.String(), j.Kind, string(j.State))))
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	if j.State != gen.JobStateCancelling {
		if err := s.cancel(ctx, r, j); err != nil {
			httpserver.Problem(w, r, err)
			return
		}
	}
	after, _, _, err := s.loadJob(ctx, id.String())
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	w.Header().Set("Location", jobLocation(r, after.Id.String()))
	writeJSON(w, r, http.StatusAccepted, after)
}

// mayCancel is the rule of R-API-015: a caller with settings:write, or the one
// who started the job. A reader who may see the job but not stop it gets 403.
func (s *Server) mayCancel(ctx context.Context, createdBy string) error {
	admin, err := s.holds(ctx, string(authz.SettingsWrite))
	if err != nil || admin {
		return err
	}
	if createdBy != "" && createdBy == callerID(ctx) {
		return nil
	}
	return errs.E(errs.Forbidden, errs.CodeForbidden, "Only an administrator or the caller who started the job may cancel it.")
}

// cancel marks the job in a transaction together with its audit event.
func (s *Server) cancel(ctx context.Context, r *http.Request, j gen.Job) error {
	details, _ := json.Marshal(map[string]string{"jobId": j.Id.String(), "kind": j.Kind})
	var raced bool
	err := s.o.API.Store.InTx(ctx, func(tx meta.Tx) error {
		raced = false
		ptx, ok := postgres.Unwrap(tx)
		if !ok {
			return errs.E(errs.Internal, errs.CodeInternal, "")
		}
		rows, err := q.New(ptx).CancelJob(ctx, j.Id.String())
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			raced = true // ended or already cancelling since the read
			return nil
		}
		return s.recorder().RecordTx(ctx, tx, audit.Event{
			Type: "system.job.cancelled", Actor: ActorFrom(httpserver.PrincipalFrom(ctx)), Outcome: audit.Success, Source: auditSource(r),
			Subject: audit.Subject{Kind: "job", ID: j.Id.String(), Name: j.Kind}, Details: details,
		})
	})
	if err != nil {
		return err
	}
	if raced {
		cur, _, found, lerr := s.loadJob(ctx, j.Id.String())
		if lerr == nil && found && jobEnded(cur.State) {
			return errs.E(errs.Conflict, errs.CodeConflict, "The job has ended and cannot be cancelled.")
		}
	}
	return nil
}

// jobLocation is the absolute URL of a job for Location headers.
func jobLocation(r *http.Request, id string) string {
	return strings.TrimSuffix(httpserver.InfoFrom(r.Context()).BaseURL, "/") + jobHref(id)
}
