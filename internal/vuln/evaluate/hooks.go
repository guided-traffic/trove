package evaluate

import (
	"context"

	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/vuln"
)

// Hooks wraps the storage hooks of the item store with the evaluation (R-VULN-011): after the format's OnCommit linked the
// item to its version, the version is evaluated in the commit's transaction; before the format's OnItemsRemoved removes the
// version rows, the findings of the removed versions resolve in the removing transaction. A nil evaluator returns inner.
func Hooks(inner items.Hooks, ev vuln.Evaluator) items.Hooks {
	if ev == nil {
		return inner
	}
	return &hooks{inner: inner, ev: ev}
}

type hooks struct {
	inner items.Hooks
	ev    vuln.Evaluator
}

var _ items.Hooks = (*hooks)(nil)

// OnCommit implements [items.Hooks]. An error of the evaluation rolls the commit back, as the format's own does.
func (h *hooks) OnCommit(ctx context.Context, tx meta.Tx, e items.CommitEvent) error {
	if err := h.inner.OnCommit(ctx, tx, e); err != nil {
		return err
	}
	trigger := vuln.TriggerPublish
	if e.Origin == items.OriginFill {
		trigger = vuln.TriggerFill
	}
	return h.ev.OnVersionCommitted(ctx, tx, vuln.CommittedVersion{
		RepoID: e.Repo.ID, RepoKey: e.Repo.Name, Format: e.Repo.Format, ItemID: e.Item.ID, Trigger: trigger,
	})
}

// OnItemsRemoved implements [items.Hooks].
func (h *hooks) OnItemsRemoved(ctx context.Context, tx meta.Tx, e items.RemovedEvent) error {
	var ids []int64
	seen := map[int64]bool{}
	for _, r := range e.Items {
		if r.Version != 0 && !seen[r.Version] {
			seen[r.Version] = true
			ids = append(ids, r.Version)
		}
	}
	if len(ids) > 0 {
		if err := h.ev.OnVersionsRemoved(ctx, tx, ids); err != nil {
			return err
		}
	}
	return h.inner.OnItemsRemoved(ctx, tx, e)
}

// JobEnqueuer enqueues a job in the caller's transaction; *jobs.Queue implements it.
type JobEnqueuer interface {
	Enqueue(ctx context.Context, tx meta.Tx, e jobs.EnqueueSpec) (jobs.Result, error)
}

// RepositoryHook is the repository.Hooks of the scanning: a change of a repository's scanning inputs (vulnerabilities.enabled,
// matchPublicAdvisories, the upstream URL) enqueues a vuln-rescan of the repository in the same transaction (R-VULN-002).
// blockMalicious and onMissingData act without a job.
type RepositoryHook struct{ jobs JobEnqueuer }

var _ repository.Hooks = (*RepositoryHook)(nil)

// NewRepositoryHook creates the hook over the job queue.
func NewRepositoryHook(q JobEnqueuer) *RepositoryHook { return &RepositoryHook{jobs: q} }

// OnUpdated implements [repository.Hooks].
func (h *RepositoryHook) OnUpdated(ctx context.Context, tx meta.Tx, _ repository.Caller, before, after repository.Repository) error {
	if h.jobs == nil || after.VulnerabilityEcosystem() == "" {
		return nil
	}
	b, a := before.VulnerabilitySettings(), after.VulnerabilitySettings()
	if !b.RescanInputsDiffer(a) && upstreamOf(before) == upstreamOf(after) {
		return nil
	}
	id, ok := repoID(after)
	if !ok {
		return nil
	}
	_, err := h.jobs.Enqueue(ctx, tx, jobs.EnqueueSpec{
		Kind: vuln.KindRescan, Args: vuln.RescanArgs{Repository: after.Name, RepositoryID: id}, DedupKey: vuln.RescanKey(id), TargetKind: "repository", TargetKey: after.Name,
	})
	return err
}

// OnDelete implements [repository.Hooks]: the findings of a deleted repository go with its row (foreign key).
func (*RepositoryHook) OnDelete(context.Context, meta.Tx, repository.Caller, repository.Repository) error {
	return nil
}

func upstreamOf(r repository.Repository) string {
	if rs, ok := r.Remote(); ok {
		return rs.URL
	}
	return ""
}

func repoID(r repository.Repository) (int64, bool) {
	rv, ok := viewOf(r)
	return rv.ID, ok
}
