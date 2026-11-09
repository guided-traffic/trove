package v1

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
)

// DebSuiteState is the state of one suite of a Debian repository as the index state read reports it (R-DEB-014).
type DebSuiteState struct {
	Suite        string
	ChangeSeq    int64
	PublishedSeq int64
	Generation   int64
	ReleaseDate  *time.Time
	State        string
	Reason       string
	SignedBy     []string
}

// DebIndexService reads the index state of a local Debian repository. The composition root implements it over the data port.
type DebIndexService interface {
	// State lists the suites of the repository.
	State(ctx context.Context, repoID int64) ([]DebSuiteState, error)
	// Wait returns the suite once its published number has reached seq, or after max with the state it has reached; found is false
	// for a suite the repository does not have. It holds no transaction (Q-DEB-25).
	Wait(ctx context.Context, repoID int64, suite string, seq int64, max time.Duration) (s DebSuiteState, found bool, err error)
	// MaxWait is deb.index_wait_max.
	MaxWait() time.Duration
}

// maxWaitFor is the number of suites one waitFor may name.
const maxWaitFor = 64

type waitTarget struct {
	suite string
	seq   int64
}

// parseWaitFor reads `<suite>:<changeSeq>[,…]` (R-DEB-014).
func parseWaitFor(raw string) ([]waitTarget, error) {
	var out []waitTarget
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		suite, num, ok := strings.Cut(part, ":")
		seq, err := strconv.ParseInt(num, 10, 64)
		if !ok || suite == "" || err != nil || seq < 0 {
			return nil, errs.E(errs.Invalid, errs.CodeValidation, "waitFor is a list of <suite>:<changeSeq>.",
				errs.WithFields(errs.FieldError{Pointer: "/waitFor", Code: "invalid_value", Detail: "must be <suite>:<changeSeq>, separated by commas"}))
		}
		out = append(out, waitTarget{suite, seq})
	}
	if len(out) > maxWaitFor {
		return nil, errs.E(errs.Invalid, errs.CodeValidation, "waitFor names too many suites.",
			errs.WithFields(errs.FieldError{Pointer: "/waitFor", Code: "out_of_range", Detail: "at most 64 suites"}))
	}
	return out, nil
}

type suiteJSON struct {
	Distribution string     `json:"distribution"`
	ChangeSeq    int64      `json:"changeSeq"`
	PublishedSeq int64      `json:"publishedSeq"`
	Generation   int64      `json:"generation"`
	ReleaseDate  *time.Time `json:"releaseDate,omitempty"`
	State        string     `json:"state"`
	Reason       *string    `json:"reason,omitempty"`
	SignedBy     *[]string  `json:"signedBy,omitempty"`
}

func suiteBody(s DebSuiteState) suiteJSON {
	out := suiteJSON{Distribution: s.Suite, ChangeSeq: s.ChangeSeq, PublishedSeq: s.PublishedSeq, Generation: s.Generation, ReleaseDate: s.ReleaseDate, State: s.State}
	if s.State == string(gen.DebIndexStateSuitesStateFailed) && s.Reason != "" {
		out.Reason = &s.Reason
	}
	if len(s.SignedBy) > 0 {
		out.SignedBy = &s.SignedBy
	}
	return out
}

// GetDebIndexState implements GET /api/v1/projects/{projectKey}/repositories/{repoKey}/deb/index (R-DEB-014): the state of each suite,
// and with waitFor the wait until every named suite has published the change number, or deb.index_wait_max has passed. The wait
// holds no transaction and no upload waits for it.
func (s *Server) GetDebIndexState(w http.ResponseWriter, r *http.Request, projectKey gen.ProjectKey, repoKeyText gen.RepoKey, params gen.GetDebIndexStateParams) {
	svc := s.o.Deb.Index
	var targets []waitTarget
	var repoID int64
	var err error
	if svc == nil || s.o.Packages.Reader == nil {
		err = unavailable("The Debian index state")
	}
	if err == nil && params.WaitFor != nil {
		targets, err = parseWaitFor(*params.WaitFor)
	}
	var repo repository.Repository
	if err == nil {
		repo, err = s.o.Packages.Reader.ByName(r.Context(), qname.Repo{Project: projectKey, Key: repoKeyText})
	}
	if err == nil {
		repoID, err = parseRepositoryID(repo.ID)
	}
	if err == nil && (repo.Format != "deb" || repo.Class == repository.Remote) {
		err = errs.E(errs.Invalid, errs.CodeUnsupportedClass, "The index state is read from a local or a virtual Debian repository.")
	}
	if err == nil && repo.Class == repository.Virtual && len(targets) > 0 {
		// A virtual repository has no change number of its own to wait for: its index follows the members (R-DEB-026).
		err = errs.E(errs.Invalid, errs.CodeValidation, "A virtual repository has no change number to wait for.",
			errs.WithFields(errs.FieldError{Pointer: "/waitFor", Code: "invalid_value", Detail: "not supported for a virtual repository"}))
	}
	var list []DebSuiteState
	if err == nil {
		deadline := time.Now().Add(svc.MaxWait())
		for _, t := range targets {
			left := time.Until(deadline)
			if left <= 0 {
				break
			}
			if _, _, err = svc.Wait(r.Context(), repoID, t.suite, t.seq, left); err != nil {
				break
			}
		}
	}
	if err == nil {
		list, err = svc.State(r.Context(), repoID)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	body := struct {
		Suites []suiteJSON `json:"suites"`
	}{Suites: make([]suiteJSON, 0, len(list))}
	for _, st := range list {
		body.Suites = append(body.Suites, suiteBody(st))
	}
	writeJSON(w, r, http.StatusOK, body)
}
