package ansible

import (
	"bytes"
	"context"
	"net/http"
	"strconv"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/ansible/galaxydoc"
	"github.com/guided-traffic/trove/internal/formats/ansible/version"
)

// versionValid reports whether a string is a semantic version (R-ANS-002).
func versionValid(v string) bool { return version.Valid(v) }

// What a client does to a virtual repository when it publishes (R-ANS-024). The publish route serves the classes local and virtual: the
// dispatcher keeps the repository the client addressed, and the handler resolves the deployment target itself, so that the answer's
// task can name the repository the client was given. Everything that is decided or committed (the checks of R-ANS-008, the ownership,
// the shadowing check of R-REPO-048, the audit event) acts on a request made for the target ([formats.Request.WriteTarget]) by the
// handler of the local class; the shadowing check asks the remote members of every virtual repository that has the target as a member.

// deployTarget resolves the local repository a write to the virtual repository goes to (R-REPO-021): 405 no_deploy_target without one.
func deployTarget(ctx context.Context, rq *formats.Request) (*formats.Request, error) {
	target, err := rq.Host.Resolver().DeployTarget(ctx, rq)
	if err != nil {
		return nil, err
	}
	if target == nil || target.Class != formats.Local {
		return nil, errs.E(errs.MethodNotAllowed, errs.CodeNoDeployTarget, "The deployment target of the virtual repository is not a local repository.")
	}
	return rq.WriteTarget(target), nil
}

// virtualPublish hands a publish to the handler of the local class on a request made for the deployment target (R-ANS-024) and
// rewrites the address of the task in its answer to the virtual repository.
func (f *Format) virtualPublish(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	trq, err := deployTarget(ctx, rq)
	if err != nil {
		return err
	}
	handlerRegistry.mu.RLock()
	build := handlerRegistry.by[formats.Local][routePublish]
	handlerRegistry.mu.RUnlock()
	if build == nil {
		return notImplemented(routePublish, formats.Local)
	}
	tw := &taskWriter{
		ResponseWriter: w,
		from:           trq.Host.Links(trq).Registry(trq.Addressed()),
		to:             rq.Host.Links(rq).Registry(rq.Addressed()),
	}
	if err := build(f)(ctx, trq, tw); err != nil {
		return err
	}
	return tw.flush()
}

// taskWriter holds the 202 answer of a publish, which carries the address of the import task, and writes it with the address of the
// repository the client was given in place of the deployment target's. Every other answer passes through.
type taskWriter struct {
	http.ResponseWriter
	from, to string
	held     bool
	buf      bytes.Buffer
}

func (t *taskWriter) WriteHeader(code int) {
	if code == http.StatusAccepted {
		t.held = true
		return
	}
	t.ResponseWriter.WriteHeader(code)
}

func (t *taskWriter) Write(b []byte) (int, error) {
	if t.held {
		return t.buf.Write(b)
	}
	return t.ResponseWriter.Write(b)
}

func (t *taskWriter) flush() error {
	if !t.held {
		return nil
	}
	body := bytes.ReplaceAll(t.buf.Bytes(), []byte(t.from), []byte(t.to))
	h := t.Header()
	h.Set("Content-Length", strconv.Itoa(len(body)))
	t.ResponseWriter.WriteHeader(http.StatusAccepted)
	_, err := t.ResponseWriter.Write(body)
	return err
}

// virtualImport answers the import task of a publish through the virtual repository: the task is looked up in the deployment target at
// the time of the read, and a task of a target the repository no longer has is a missing one (R-ANS-009, R-ANS-024).
func (f *Format) virtualImport(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	id := rq.Params.Get(paramID)
	if !taskID(id) {
		return errNoSuchTask()
	}
	trq, err := deployTarget(ctx, rq)
	if err != nil {
		if errs.KindOf(err) == errs.MethodNotAllowed {
			return errNoSuchTask()
		}
		return err
	}
	st, err := storeOf(rq.Host.FormatData(ID))
	if err != nil {
		return err
	}
	task, found, err := st.Task(ctx, trq.RepoRef().ID, id)
	if err != nil {
		return err
	}
	if !found {
		return errNoSuchTask()
	}
	return writeDocument(rq, w, galaxydoc.RenderTask(task))
}

func errNoSuchTask() error {
	return errs.E(errs.NotFound, errs.CodeNotFound, "No import task has this id in the deployment target of this repository.")
}

// taskID reports whether s has the form of the id of a task, a UUID (R-ANS-009); anything else is a missing task without a lookup.
func taskID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F'):
			return false
		}
	}
	return true
}

// plannedRemote is a remote member that takes part in a request, with the request derived for it.
type plannedRemote struct {
	planned formats.PlannedMember
	rq      *formats.Request
}

// remoteMembers plans a request for the path (R-REPO-019 steps 1 to 5: the repository's patterns, the pin rules and claims, the
// members the caller may read) and returns the remote members of the plan, in resolution order. A repository whose patterns refuse the
// path has none.
func (f *Format) remoteMembers(ctx context.Context, rq *formats.Request, path string) ([]plannedRemote, error) {
	p := formats.Path{Path: path}
	plan, err := rq.Host.Resolver().Plan(ctx, rq, p, f.Classify(p))
	if err != nil {
		return nil, err
	}
	if plan == nil || !plan.Admitted {
		return nil, nil
	}
	var out []plannedRemote
	for _, m := range plan.Members {
		if m.Tier != formats.TierRemote {
			continue
		}
		mrq, err := memberRequest(rq, m.Member.Name)
		if err != nil {
			return nil, err
		}
		out = append(out, plannedRemote{planned: m, rq: mrq})
	}
	return out, nil
}
