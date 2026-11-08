package v1

import (
	"context"
	"net/http"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/qname"
)

// Reindexer is what a format with a job-built index offers the reindex endpoint (R-REPO-016, R-ARCH-012 item 2, Q-ARCH-384). The
// composition root offers every data port that implements it, by format ID (Options.Reindexers); a format that does not has no
// reindex and answers 405 not_applicable. The Debian data port implements it (formatdata/deb/enqueue.go) and the index builder's job
// does the work.
type Reindexer interface {
	// Reindex enqueues, in tx, the full rebuild of the repository's index and returns its job. A build that waits automatically is
	// upgraded to the full one and moved to now (the job it returns is that one); a reindex that meets a waiting or running reindex
	// enqueues nothing and returns that job with conflict true. by is the principal's id or "".
	Reindex(ctx context.Context, tx meta.Tx, repoID int64, by string) (job jobs.Job, conflict bool, err error)
}

// ReindexRepository implements POST /api/v1/projects/{projectKey}/repositories/{repoKey}/reindex (R-REPO-016) for the formats that have
// a job-built index: 202 with the job, 409 reindex_running naming the job of a reindex that waits or runs, and 405 not_applicable for
// a repository without one (another format, or a remote repository, whose index is the upstream's). The permission, `manage`, is
// declared in api/openapi.yaml and enforced before the handler runs.
//
// The rebuild of the derived rows of npm and NuGet (R-REPO-016's first rule) is not built: those formats answer 405 here.
func (s *Server) ReindexRepository(w http.ResponseWriter, r *http.Request, projectKey gen.ProjectKey, repoKey gen.RepoKey, _ gen.ReindexRepositoryParams) {
	if s.idem == nil || s.o.Repositories.Reader == nil {
		httpserver.Problem(w, r, unavailable("The reindex"))
		return
	}
	name := qname.Repo{Project: projectKey, Key: repoKey}
	repo, err := s.o.Repositories.Reader.ByName(r.Context(), name)
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.reindexRepository"))
		return
	}
	rx := s.o.Reindexers[repo.Format]
	if rx == nil || string(repo.Class) == "remote" {
		httpserver.Problem(w, r, errs.E(errs.MethodNotAllowed, errs.CodeNotApplicable,
			"The "+string(repo.Class)+" repository "+name.String()+" has no job-built index to rebuild (R-REPO-016)."))
		return
	}
	body, err := readOptionalBody(w, r, int64(s.o.API.Config.MaxRequestBodyBytes))
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	err = s.idem.Do(w, r, body, IdempotentOp{Run: func(ctx context.Context, tx meta.Tx) (Outcome, error) {
		job, conflict, err := rx.Reindex(ctx, tx, repo.NumericID(), callerID(ctx))
		if err != nil {
			return Outcome{}, err
		}
		ref := jobReference(job.ID, job.Kind, job.State)
		if conflict {
			return Outcome{}, errs.E(errs.Conflict, errs.CodeReindexRunning,
				"A reindex of "+name.String()+" is queued or running; the job is "+job.ID+".", errs.WithExtension("job", ref))
		}
		return Outcome{
			Status: http.StatusAccepted,
			Header: map[string]string{"Location": jobLocation(r, job.ID)},
			Body:   gen.JobReferenceEnvelope{Job: ref},
		}, nil
	}})
	if err != nil {
		httpserver.Problem(w, r, err)
	}
}
