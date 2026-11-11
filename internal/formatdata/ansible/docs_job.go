package ansible

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/guided-traffic/trove/internal/formats/ansible/galaxydoc"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// Background gives a job the upstream of one remote repository for one run: the egress, credential, share and throttling rules of a
// background request, without a mirror pass (Host.BackgroundUpstream, Q-REPO-272). The composition root implements it over the format host.
type Background interface {
	Upstream(ctx context.Context, repoID int64, jobID string) (discover.Upstream, error)
}

// DocsFillHandler is the handler of the job ansible-docs-fill (R-ANS-042): it fetches the docs-blob document of one version of a remote
// repository through the engine as a background request. The engine runs PrepareFill and OnFill of the format, so the route and the job
// share one code path and the rows are written by the fill. A version that is gone, a repository that is not a remote Ansible Galaxy
// repository and one that does not fetch documentation end the job at once.
func DocsFillHandler(st *Store, bg Background) jobs.Handler {
	return func(ctx context.Context, run *jobs.Run) error {
		var a DocsFillArgs
		if err := json.Unmarshal(run.Args, &a); err != nil {
			return jobs.Permanent(fmt.Errorf("ansible-docs-fill: the arguments do not read: %w", err))
		}
		qs := q.New(run.DB())
		repos, err := qs.AnsibleDocsRemoteRepository(ctx, a.RepoID)
		if err != nil {
			return err
		}
		if len(repos) == 0 {
			return nil // the repository is gone, or is none of a remote Ansible Galaxy
		}
		if _, found, err := st.Announced(ctx, a.RepoID, a.Name, a.Version); err != nil || !found {
			return err // the version is gone: the job ends, and its fetch row goes with the version's removal
		}
		up, err := bg.Upstream(ctx, a.RepoID, run.ID)
		if err != nil {
			return err
		}
		if !fetchesDocumentation(up.Settings()) {
			return nil
		}
		ns, name, _ := splitName(a.Name)
		_, found, err := up.Document(ctx, "collections/"+ns+"/"+name+"/docs/"+a.Version+".json", "doc")
		switch {
		case err != nil:
			reason := galaxydoc.FetchFailure(err)
			if serr := st.SetDocFetch(ctx, nil, a.RepoID, a.Name, a.Version, rows.DocFetchUnavailable, reason); serr != nil {
				return serr
			}
			if reason != rows.DocReasonUpstreamError {
				return jobs.Permanent(err) // a retry would repeat it
			}
			return err
		case !found:
			return st.SetDocFetch(ctx, nil, a.RepoID, a.Name, a.Version, rows.DocFetchNone, "")
		}
		return nil
	}
}

// fetchesDocumentation reads ansible.fetchDocumentation from the format's member of the repository document (default true).
func fetchesDocumentation(member json.RawMessage) bool {
	var m struct {
		FetchDocumentation *bool `json:"fetchDocumentation"`
	}
	if json.Unmarshal(member, &m) != nil || m.FetchDocumentation == nil {
		return true
	}
	return *m.FetchDocumentation
}

// splitName splits "<ns>.<name>".
func splitName(n string) (ns, name string, ok bool) {
	for i := 0; i < len(n); i++ {
		if n[i] == '.' {
			return n[:i], n[i+1:], true
		}
	}
	return "", "", false
}
