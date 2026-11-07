package project

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Replay of the project events of the revocation journal (R-HA-054, R-AUTHZ-031). Memberships are removed or lowered, an
// empty project is deleted, and a moved repository is disabled rather than moved back: moving it again would give the target
// project's members what the restored database does not give them, and leaving it would give the source project's members
// back what the move took, while the disabled repository grants nothing to either. An administrator moves it again
// (R-REPO-060) and enables it. Every entry names its objects by ID.
func init() {
	journal.Register(journal.Handler{
		Domain: "project",
		Events: []string{audit.EventProjectDeleted, audit.EventProjectMemberRemoved, audit.EventProjectMemberChanged, audit.EventRepositoryMoved},
		Apply:  replayProject,
	})
}

var errNotPostgres = errors.New("project: the transaction does not come from the PostgreSQL store")

func replayProject(ctx context.Context, tx meta.Tx, env journal.Env, o journal.Object) (journal.Result, error) {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return journal.Result{}, errNotPostgres
	}
	qs := q.New(ptx)
	switch o.Event {
	case audit.EventProjectDeleted:
		return replayProjectDeleted(ctx, qs, env, tx, o)
	case audit.EventProjectMemberRemoved:
		return replayMemberRemoved(ctx, qs, env, tx, o)
	case audit.EventProjectMemberChanged:
		return replayMemberChanged(ctx, qs, env, tx, o)
	default:
		return replayMoved(ctx, qs, env, tx, o)
	}
}

func notifyProject(env journal.Env, tx meta.Tx, typ, id string) error {
	return env.Notify(tx, authz.AreaAuthz, payloadVersion, authzChange{Type: typ, ID: id})
}

// replayProjectDeleted deletes the project when the restored database holds no repository, no service account and no CI
// mapping of it, and removes all its members otherwise, so the project grants nothing while an administrator decides about
// what it still holds. The kept project is reported with the counts.
func replayProjectDeleted(ctx context.Context, qs *q.Queries, env journal.Env, tx meta.Tx, o journal.Object) (journal.Result, error) {
	var e struct {
		ProjectID string `json:"projectId"`
	}
	if !o.Into(&e) || !journal.IsUUID(e.ProjectID) {
		return o.Malformed("projectId is not an ID"), nil
	}
	pr, err := qs.JournalProjectLock(ctx, e.ProjectID)
	if err != nil {
		return journal.Result{}, err
	}
	if len(pr) == 0 {
		return o.Result(journal.SubjectUnknown, e.ProjectID), nil
	}
	key := pr[0].Key
	holds, err := qs.JournalProjectHolds(ctx, e.ProjectID)
	if err != nil {
		return journal.Result{}, err
	}
	if holds.Repositories+holds.ServiceAccounts+holds.CiMappings == 0 {
		if _, err := qs.JournalProjectDelete(ctx, e.ProjectID); err != nil {
			return journal.Result{}, err
		}
		return o.Result(journal.Applied, key), notifyProject(env, tx, "project", e.ProjectID)
	}
	n, err := qs.JournalProjectMembersRemoveAll(ctx, e.ProjectID)
	if err != nil {
		return journal.Result{}, err
	}
	r := o.Result(journal.Applied, key)
	r.Detail, _ = json.Marshal(map[string]any{
		"kept": true, "membersRemoved": n,
		"repositories": holds.Repositories, "serviceAccounts": holds.ServiceAccounts, "ciMappings": holds.CiMappings,
	})
	if n == 0 {
		r.Outcome = journal.AlreadyInEffect
		return r, nil
	}
	return r, notifyProject(env, tx, "project", e.ProjectID)
}

func replayMemberRemoved(ctx context.Context, qs *q.Queries, env journal.Env, tx meta.Tx, o journal.Object) (journal.Result, error) {
	var e struct {
		ProjectID   string `json:"projectId"`
		PrincipalID string `json:"principalId"`
	}
	if !o.Into(&e) || !journal.IsUUID(e.ProjectID) || !journal.IsUUID(e.PrincipalID) {
		return o.Malformed("projectId or principalId is not an ID"), nil
	}
	pr, err := qs.JournalProjectLock(ctx, e.ProjectID)
	if err != nil {
		return journal.Result{}, err
	}
	if len(pr) == 0 {
		return o.Result(journal.SubjectUnknown, e.ProjectID), nil
	}
	n, err := qs.ProjectMemberRemove(ctx, q.ProjectMemberRemoveParams{ProjectID: e.ProjectID, PrincipalID: e.PrincipalID})
	if err != nil {
		return journal.Result{}, err
	}
	if n == 0 {
		return o.Result(journal.AlreadyInEffect, pr[0].Key), nil
	}
	return o.Result(journal.Applied, pr[0].Key), notifyProject(env, tx, "project", e.ProjectID)
}

// roleOrder is the role comparator of R-AUTHZ-031: Narrows(current, after) is true only when the membership exists and the
// role of after is not higher than the current one, in the order reader, developer, maintainer, owner. A replay never adds a
// member and never raises a role, so a role that is not one of the four, or an absent membership, never narrows.
type roleOrder struct{}

var _ journal.Narrower[authz.ProjectRole] = roleOrder{}

// Narrows implements journal.Narrower.
func (roleOrder) Narrows(current, after authz.ProjectRole) bool {
	return current != authz.ProjectRoleNone && after != authz.ProjectRoleNone && after <= current
}

func replayMemberChanged(ctx context.Context, qs *q.Queries, env journal.Env, tx meta.Tx, o journal.Object) (journal.Result, error) {
	var e struct {
		ProjectID   string `json:"projectId"`
		PrincipalID string `json:"principalId"`
		Before      string `json:"before"`
		After       string `json:"after"`
	}
	if !o.Into(&e) || !journal.IsUUID(e.ProjectID) || !journal.IsUUID(e.PrincipalID) {
		return o.Malformed("projectId or principalId is not an ID"), nil
	}
	before, ok1 := authz.ParseProjectRole(e.Before)
	after, ok2 := authz.ParseProjectRole(e.After)
	if !ok1 || !ok2 {
		return o.Malformed("before or after is not a project role"), nil
	}
	pr, err := qs.JournalProjectLock(ctx, e.ProjectID)
	if err != nil {
		return journal.Result{}, err
	}
	if len(pr) == 0 {
		return o.Result(journal.SubjectUnknown, e.ProjectID), nil
	}
	roles, err := qs.ProjectMemberRole(ctx, q.ProjectMemberRoleParams{ProjectID: e.ProjectID, PrincipalID: e.PrincipalID})
	if err != nil {
		return journal.Result{}, err
	}
	if len(roles) == 0 {
		return o.Result(journal.SubjectUnknown, pr[0].Key), nil
	}
	current, _ := authz.ParseProjectRole(roles[0])
	if current == after {
		return o.Result(journal.AlreadyInEffect, pr[0].Key), nil
	}
	setRole := func() error {
		if _, err := qs.ProjectMemberSetRole(ctx, q.ProjectMemberSetRoleParams{ProjectID: e.ProjectID, PrincipalID: e.PrincipalID, Role: e.After}); err != nil {
			return err
		}
		return notifyProject(env, tx, "project", e.ProjectID)
	}
	act := journal.UpdateActions{
		Apply: setRole,
		// The most restrictive state of a membership is none.
		Suspend: func() (json.RawMessage, error) {
			if _, err := qs.ProjectMemberRemove(ctx, q.ProjectMemberRemoveParams{ProjectID: e.ProjectID, PrincipalID: e.PrincipalID}); err != nil {
				return nil, err
			}
			detail, err := json.Marshal(map[string]string{"principalId": e.PrincipalID, "after": e.After})
			if err != nil {
				return nil, err
			}
			return detail, notifyProject(env, tx, "project", e.ProjectID)
		},
	}
	out, detail, err := journal.ApplyUpdate[authz.ProjectRole](roleOrder{}, current, before, after, act)
	r := o.Result(out, pr[0].Key)
	r.Detail = detail
	return r, err
}

func replayMoved(ctx context.Context, qs *q.Queries, env journal.Env, tx meta.Tx, o journal.Object) (journal.Result, error) {
	var e struct {
		RepositoryID string `json:"repositoryId"`
		From         struct {
			ID string `json:"id"`
		} `json:"from"`
		To struct {
			ID string `json:"id"`
		} `json:"to"`
	}
	if !o.Into(&e) || !journal.IsUUID(e.From.ID) || !journal.IsUUID(e.To.ID) {
		return o.Malformed("the projects are not named by ID"), nil
	}
	id, perr := strconv.ParseInt(e.RepositoryID, 10, 64)
	if perr != nil {
		return o.Malformed("repositoryId is not an ID"), nil //nolint:nilerr // an unreadable ID in an untrusted entry is a skipped entry, not a failed replay
	}
	rows, err := qs.JournalRepositoryLock(ctx, id)
	if err != nil {
		return journal.Result{}, err
	}
	if len(rows) == 0 || rows[0].Lifecycle != "active" {
		return o.Result(journal.SubjectUnknown, e.RepositoryID), nil
	}
	repo := rows[0]
	subject := repo.ProjectKey + "/" + repo.Key
	switch repo.ProjectID {
	case e.To.ID:
		return o.Result(journal.AlreadyInEffect, subject), nil
	case e.From.ID:
	default:
		return o.Result(journal.SubjectUnknown, subject), nil
	}
	doc := map[string]json.RawMessage{}
	if len(repo.Config) > 0 {
		if err := json.Unmarshal(repo.Config, &doc); err != nil {
			return journal.Result{}, err
		}
	}
	doc["disabled"] = json.RawMessage("true")
	raw, err := json.Marshal(doc)
	if err != nil {
		return journal.Result{}, err
	}
	ver, err := qs.JournalRepositorySetConfig(ctx, q.JournalRepositorySetConfigParams{ID: id, Config: string(raw)})
	if err != nil {
		return journal.Result{}, err
	}
	if len(ver) > 0 {
		if err := env.Notify(tx, cluster.AreaRepoConfig, payloadVersion, map[string]any{"id": e.RepositoryID, "key": subject, "op": "updated", "version": ver[0]}); err != nil {
			return journal.Result{}, err
		}
		if err := notifyProject(env, tx, "repository", e.RepositoryID); err != nil {
			return journal.Result{}, err
		}
	}
	r := o.Result(journal.Suspended, subject)
	r.Detail, _ = json.Marshal(map[string]string{"disabled": subject, "movedFromProjectId": e.From.ID, "movedToProjectId": e.To.ID})
	return r, nil
}
