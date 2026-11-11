package v1

import (
	"net/http"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
	"github.com/guided-traffic/trove/internal/httpserver"
)

type ansibleTaskMessage struct {
	Level   string    `json:"level"`
	Message string    `json:"message"`
	Time    time.Time `json:"time"`
}

type ansibleTaskError struct {
	Code        string `json:"code"`
	Description string `json:"description"`
}

type ansibleTask struct {
	ID         string               `json:"id"`
	State      string               `json:"state"`
	Namespace  string               `json:"namespace"`
	Name       string               `json:"name"`
	Version    string               `json:"version"`
	CreatedAt  time.Time            `json:"createdAt"`
	FinishedAt time.Time            `json:"finishedAt"`
	CreatedBy  string               `json:"createdBy,omitempty"`
	Error      *ansibleTaskError    `json:"error"`
	Messages   []ansibleTaskMessage `json:"messages"`
}

func ansibleTaskRep(t rows.Task) ansibleTask {
	out := ansibleTask{
		ID: t.ID, State: t.State, Namespace: t.Namespace, Name: t.Name, Version: t.Version, CreatedAt: t.CreatedAt, FinishedAt: t.FinishedAt,
		CreatedBy: t.CreatedBy, Messages: make([]ansibleTaskMessage, 0, len(t.Messages)),
	}
	if t.ErrorCode != "" {
		out.Error = &ansibleTaskError{Code: t.ErrorCode, Description: t.ErrorDescription}
	}
	for _, m := range t.Messages {
		out.Messages = append(out.Messages, ansibleTaskMessage{Level: m.Level, Message: m.Message, Time: m.Time})
	}
	return out
}

// ListAnsibleImportTasks implements GET /api/v1/projects/{projectKey}/repositories/{repoKey}/ansible/imports (R-ANS-009): the import
// tasks of a local Ansible Galaxy repository, newest first, optionally of one state; a virtual repository lists its deployment target's.
func (s *Server) ListAnsibleImportTasks(w http.ResponseWriter, r *http.Request, projectKey gen.ProjectKey, repoKey gen.RepoKey, _ gen.ListAnsibleImportTasksParams) {
	store, repo, ok := s.ansibleTarget(w, r, projectKey, repoKey, true)
	if !ok {
		return
	}
	lq, err := ParseList(r, ListSpec{Filters: []string{"state", "package", "version"}, Sorts: []string{"createdAt"}, DefaultSort: "-createdAt"}, s.o.API.Config)
	if err == nil && lq.After != nil && len(lq.After) != 1 {
		err = invalidCursor()
	}
	state := lq.First("state")
	if err == nil && state != "" && state != rows.TaskCompleted && state != rows.TaskFailed {
		err = errs.E(errs.Invalid, errs.CodeInvalid, "The filter state is completed or failed.")
	}
	var list []rows.Task
	if err == nil {
		after := ""
		if lq.After != nil {
			after = lq.After[0]
		}
		list, err = store.Tasks(r.Context(), repo.NumericID(), rows.TaskFilter{State: state, Name: strings.ToLower(lq.First("package")), Version: lq.First("version")}, after, lq.FetchLimit())
	}
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.listAnsibleImportTasks"))
		return
	}
	WritePage(w, r, MapPage(NewPage(lq, list, func(t rows.Task) []string { return []string{t.ID} }), ansibleTaskRep))
}
