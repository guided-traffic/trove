package project

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/qname"
)

// The move of a repository to another project (R-REPO-060, Q-REPO-217). The repository keeps its ID, its
// content and everything held by the ID (items, packages, cached entries, claims, package owners, findings,
// mirror state); its address changes, so the roles and policies of the source project stop reaching it and
// those of the target project reach it. Nothing that names it inside the source project is rewritten: such
// a reference refuses the move.

// Reference kinds of a 409 repository_in_use, the member `references` of the problem: what of the source
// project names the repository (R-REPO-060).
const (
	RefVirtualMember           = "virtual_member"            // a virtual repository lists it as a member
	RefPinRule                 = "pin_rule"                  // a pin rule of a virtual repository names it
	RefVirtualDeploymentTarget = "virtual_deployment_target" // a virtual repository deploys into it
	RefPolicy                  = "policy"                    // a key selector of a policy of the project
	RefCIMapping               = "ci_mapping"                // an inline grant of a CI mapping of the project's tier
	RefMember                  = "member"                    // a member of the virtual repository that moves
	RefDeploymentTarget        = "deployment_target"         // the deployment target of the virtual repository that moves
	RefGlobalPolicy            = "global_policy"             // a key selector of a global policy, for a restricted caller (Q-REPO-232)
)

// Reference is one object that keeps a repository from moving: its kind and its name within the source
// project.
type Reference struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// pgUniqueViolation is the SQLSTATE of a key taken in the target project.
const pgUniqueViolation = "23505"

// stateHeldByProject is the SQLSTATE of the database guard of a move (AI007, migration 00024): something of the source
// project holds the repository by ID. The references refuse the move first; the guard holds for a write that did not
// take the row locks a move waits for.
const stateHeldByProject = "AI007"

// opMoved is the op of the repo_config change of a move: the repository's addresses changed and its
// config_version moved, so a cache keyed by the repository and its version drops what it built before.
const opMoved = "moved"

// MoveRepository implements [Service] (R-REPO-060). One transaction takes the row locks of both projects in
// key order and of the repository, refuses what the move may not do, changes the repository's project,
// raises its config_version, notifies the change areas authz and repo_config with the repository's ID (the
// grant index, the repository snapshot and its negative cache, and every cache keyed by the configuration
// version follow on every replica within the bound of R-AUTHZ-028) and records repository.moved, journaled
// as a narrowing (R-AUTHZ-031). A target equal to the source changes nothing and answers the current epoch
// without an event (Q-AUTHZ-193).
//
// Refusals: 404 for a source repository that does not exist or is being deleted and for a target project
// that does not exist; 409 repository_in_use with the member `references` while an object of the source
// project names the repository, or the repository is virtual and has members or a deployment target, and,
// for a restricted caller, while a key selector of a global policy names it (Q-REPO-232); 409
// already_exists when the target project holds the key, also by a repository being deleted. The caller
// decided before that the actor may move it: repositories:write-config, or Owner of both projects, which
// is restricted.
func (s *service) MoveRepository(ctx context.Context, actor authz.Subject, from qname.Repo, toProject string, restricted bool) (qname.Repo, int64, error) {
	to := qname.Repo{Project: toProject, Key: from.Key}
	var epoch int64
	err := s.inTx(ctx, func(tx meta.Tx, qs *q.Queries) error {
		fromID, toID, err := lockBoth(ctx, qs, from.Project, toProject)
		if err != nil {
			return err
		}
		rows, err := qs.ProjectRepositoryLock(ctx, q.ProjectRepositoryLockParams{Project: from.Project, Key: from.Key})
		if err != nil {
			return err
		}
		if len(rows) == 0 || rows[0].Lifecycle != "active" {
			return errs.E(errs.NotFound, errs.CodeNotFound, fmt.Sprintf("The repository %q does not exist.", from.String()))
		}
		repo := rows[0]
		if fromID == toID {
			epoch, err = epochOf(ctx, qs)
			return err
		}
		refs, err := references(ctx, qs, repo.ID, fromID, from)
		if err != nil {
			return err
		}
		if restricted {
			global, err := globalReferences(ctx, qs, repo.ID)
			if err != nil {
				return err
			}
			refs = append(refs, global...)
		}
		if len(refs) > 0 {
			return inUse(from, refs)
		}
		taken, err := qs.ProjectRepositoryKeyTaken(ctx, q.ProjectRepositoryKeyTakenParams{ProjectID: toID, Key: from.Key})
		if err != nil {
			return err
		}
		if taken {
			return keyTaken(to)
		}
		versions, err := qs.ProjectRepositoryMove(ctx, q.ProjectRepositoryMoveParams{
			ToProjectID: toID, Actor: actorID(actor), ID: repo.ID, FromProjectID: fromID,
		})
		if err != nil {
			if postgres.IsState(err, pgUniqueViolation) {
				return keyTaken(to)
			}
			if postgres.IsState(err, stateHeldByProject) {
				// The transaction is aborted, so the references cannot be read again in it: the answer lists none,
				// and the next attempt lists them.
				return inUse(from, []Reference{})
			}
			return err
		}
		if len(versions) == 0 {
			return errs.E(errs.NotFound, errs.CodeNotFound, fmt.Sprintf("The repository %q does not exist.", from.String()))
		}
		id := strconv.FormatInt(repo.ID, 10)
		if err := s.notifyAuthz(tx, "repository", id); err != nil {
			return err
		}
		if err := s.o.Changes.Notify(tx, authz.AreaRepoConfig, payloadVersion, repoChange{ID: id, Key: to.String(), Op: opMoved, Version: versions[0]}); err != nil {
			return err
		}
		afterEpoch(tx, &epoch, func(ctx context.Context, tx meta.Tx, epoch int64) error {
			out, in := movedEvents(actor, from, to, id, fromID, toID, versions[0], epoch)
			if err := s.o.Recorder.RecordTx(ctx, tx, out); err != nil {
				return err
			}
			return s.o.Recorder.RecordTx(ctx, tx, in)
		})
		return nil
	})
	if err != nil {
		return qname.Repo{}, 0, err
	}
	return to, epoch, nil
}

// lockBoth takes the row locks of the two projects of a move in key order, so that two moves between the
// same projects in opposite directions do not deadlock, and returns their IDs. A project that does not
// exist is 404; the source's answer is the one of its repository, which then does not exist either.
func lockBoth(ctx context.Context, qs *q.Queries, from, to string) (fromID, toID string, err error) {
	first, second := from, to
	if to < from {
		first, second = to, from
	}
	ids := map[string]string{}
	for _, key := range []string{first, second} {
		if _, done := ids[key]; done {
			continue
		}
		id, err := lockProject(ctx, qs, key)
		if err != nil {
			return "", "", err
		}
		ids[key] = id
	}
	return ids[from], ids[to], nil
}

// references lists what of the source project names the repository (ProjectRepositoryReferences) and the CI
// mappings of the project's tier whose inline grants name it by key. A pattern or a class names no
// repository: it matches by address and simply stops covering the moved one. A template that has not been
// rendered names none either. A shared ownership domain of the project (R-AUTHZ-020, P1) would be a reference
// too; no repository can name one today (`ownershipDomain` is 422 not_applicable, Q-AUTHZ-155), so there is
// nothing to read for it.
func references(ctx context.Context, qs *q.Queries, repoID int64, projectID string, from qname.Repo) ([]Reference, error) {
	rows, err := qs.ProjectRepositoryReferences(ctx, q.ProjectRepositoryReferencesParams{ID: repoID, ProjectID: projectID})
	if err != nil {
		return nil, err
	}
	out := make([]Reference, 0, len(rows))
	for _, r := range rows {
		name := r.Name
		if r.Kind == RefPolicy {
			name = strings.TrimPrefix(name, from.Project+"/") // a project's policy is shown within the project
		}
		out = append(out, Reference{Kind: r.Kind, Name: name})
	}
	mappings, err := qs.ProjectCIMappingGrants(ctx, projectID)
	if err != nil {
		return nil, err
	}
	in := qname.Context{Project: from.Project}
	for _, m := range mappings {
		if specNames(m.Grants, in, from.Key) {
			out = append(out, Reference{Kind: RefCIMapping, Name: m.Name})
		}
	}
	return out, nil
}

// globalReferences lists the global policies whose key selector names the repository (Q-REPO-232). Their
// grant is held by the repository's ID and follows it into the target project, whose other Owners cannot
// remove it; an Owner who moves without repositories:write-config is therefore refused while one exists,
// and asks an administrator of trove, who sees the policy by this name. A pattern or a class names no
// repository and is not listed.
func globalReferences(ctx context.Context, qs *q.Queries, repoID int64) ([]Reference, error) {
	names, err := qs.ProjectRepositoryGlobalPolicies(ctx, repoID)
	if err != nil {
		return nil, err
	}
	out := make([]Reference, 0, len(names))
	for _, n := range names {
		out = append(out, Reference{Kind: RefGlobalPolicy, Name: n})
	}
	return out, nil
}

// specNames reports whether an inline grant of a CI mapping's grant document names the key as a key
// selector of the project context. A document that does not parse names nothing it can be held to; the CI
// service validated it when it was written.
func specNames(doc string, in qname.Context, key string) bool {
	var spec claims.Spec
	if json.Unmarshal([]byte(doc), &spec) != nil {
		return false
	}
	for _, g := range spec.Inline {
		for _, text := range g.Repositories {
			sel, err := qname.ParseSelector(text, in)
			if err == nil && sel.Kind == qname.SelectorKey && sel.Key == key {
				return true
			}
		}
	}
	return false
}

func inUse(from qname.Repo, refs []Reference) error {
	if len(refs) == 0 {
		return errs.E(errs.Conflict, errs.CodeRepositoryInUse,
			fmt.Sprintf("The repository %q was named in its project while it moved; move it again to see the references.", from.String()),
			errs.WithExtension("references", refs))
	}
	names := make([]string, 0, len(refs))
	for _, r := range refs {
		names = append(names, r.Kind+" "+r.Name)
	}
	return errs.E(errs.Conflict, errs.CodeRepositoryInUse,
		fmt.Sprintf("The repository %q is named by %s; remove the references, then move it.", from.String(), strings.Join(names, ", ")),
		errs.WithExtension("references", refs))
}

func keyTaken(to qname.Repo) error {
	return errs.E(errs.Conflict, errs.CodeAlreadyExists, fmt.Sprintf("The project %q has a repository %q already.", to.Project, to.Key))
}

// movedEvents are the two records of a move (R-REPO-060, Q-REPO-233), each in the audit of one project:
// repository.moved in the source project under the repository's former address, with the journal entry
// (R-AUTHZ-031: a restore suspends the repository rather than move it back or keep it), and
// repository.moved_in in the target project under its new address, without one, so the restore reads the
// move once. Both carry the repository's ID and key and the source and the target project.
func movedEvents(actor authz.Subject, from, to qname.Repo, id, fromID, toID string, version, epoch int64) (out, in audit.Event) {
	details := mustJSON(map[string]any{
		"epoch": epoch, "repository": from.Key, "repositoryId": id,
		"from": from.Project, "fromId": fromID, "to": to.Project, "toId": toID, "configVersion": version,
	})
	out = audit.Event{
		Type: audit.EventRepositoryMoved, Actor: actorOf(actor), Outcome: audit.Success,
		Subject:    audit.Subject{Kind: "repository", ID: id, Name: from.String()},
		Repository: from.String(), Project: from.Project,
		Details: details,
		Journal: mustJSON(map[string]any{
			"repositoryId": id, "from": map[string]string{"id": fromID, "key": from.Project}, "to": map[string]string{"id": toID, "key": to.Project},
		}),
	}
	in = audit.Event{
		Type: audit.EventRepositoryMovedIn, Actor: actorOf(actor), Outcome: audit.Success,
		Subject:    audit.Subject{Kind: "repository", ID: id, Name: to.String()},
		Repository: to.String(), Project: to.Project,
		Details: details,
	}
	return out, in
}
