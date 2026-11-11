package authz

import (
	"context"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// replayProjectVisibility replays authz.project.visibility_changed (R-AUTHZ-036). The comparator is the order of the levels,
// private before internal before public: the entry applies only when its after-level comes no later than the project's level in
// the restored database, and then sets that level and takes every repository of the project whose artifacts or wiki level lies
// above it down to it (R-AUTHZ-033 rule 3). A level that is the current one is already in effect; a later one would open
// content, so it is skipped, and an entry that names no project of the restored database is reported unknown.
func replayProjectVisibility(ctx context.Context, qs *q.Queries, env journal.Env, tx meta.Tx, o journal.Object) (journal.Result, error) {
	var e struct {
		ProjectID string `json:"projectId"`
		After     string `json:"after"`
	}
	if !o.Into(&e) || !journal.IsUUID(e.ProjectID) {
		return o.Malformed("projectId is not an ID"), nil
	}
	after, ok := ParseLevel(e.After)
	if !ok {
		return o.Malformed("after is not a level"), nil
	}
	rows, err := qs.JournalProjectLevelLock(ctx, e.ProjectID)
	if err != nil {
		return journal.Result{}, err
	}
	if len(rows) == 0 {
		return o.Result(journal.SubjectUnknown, e.ProjectID), nil
	}
	current, ok := ParseLevel(rows[0].Visibility)
	switch {
	case !ok || after > current:
		return o.Result(journal.Skipped, rows[0].Key), nil
	case after == current:
		return o.Result(journal.AlreadyInEffect, rows[0].Key), nil
	}
	if err := qs.JournalProjectSetLevel(ctx, q.JournalProjectSetLevelParams{ID: e.ProjectID, Level: after.String()}); err != nil {
		return journal.Result{}, err
	}
	lowered, err := qs.JournalProjectLowerRepositories(ctx, q.JournalProjectLowerRepositoriesParams{ProjectID: e.ProjectID, Level: after.String()})
	if err != nil {
		return journal.Result{}, err
	}
	for _, r := range lowered {
		if err := env.Notify(tx, cluster.AreaRepoConfig, 1, map[string]any{"id": r.ID, "key": rows[0].Key + "/" + r.Key, "op": "updated", "version": r.ConfigVersion}); err != nil {
			return journal.Result{}, err
		}
	}
	return o.Result(journal.Applied, rows[0].Key), env.Notify(tx, cluster.AreaAuthz, payloadVersion, changePayload{Type: "project", ID: e.ProjectID})
}
