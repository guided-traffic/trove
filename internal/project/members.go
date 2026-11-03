package project

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// parseAdminRef reads the reference of an administrator. A malformed reference and a
// kind that cannot administer are 422 (Q-AUTHZ-183); the second is reported by ok.
func parseAdminRef(ref string) (kind PrincipalKind, name string, ok bool, err error) {
	r, perr := authz.ParseRef(ref)
	if perr != nil {
		return "", "", false, errs.E(errs.Invalid, errs.CodeValidation, perr.Error(), errs.WithFields(errs.FieldError{
			Pointer: "/principalRef", Code: "invalid_principal", Detail: perr.Error(),
		}))
	}
	switch r.Kind {
	case principal.KindUser:
		return KindUser, r.Name, true, nil
	case principal.KindGroup:
		return KindGroup, r.Name, true, nil
	}
	return "", r.Name, false, nil
}

func notAdministrator(ref string) error {
	if strings.HasPrefix(ref, "bastion:") {
		return principal.BastionNotAllowed("the administrator of a project") // R-BAS-004
	}
	detail := fmt.Sprintf("%q cannot administer a project: only users and groups do.", ref)
	return errs.E(errs.Invalid, errs.CodeValidation, detail, errs.WithFields(errs.FieldError{
		Pointer: "/principalRef", Code: "principal_kind_not_allowed", Detail: detail,
	}))
}

// principalOf reads the user or group a reference names, in the writer's
// transaction.
func principalOf(ctx context.Context, qs *q.Queries, kind PrincipalKind, name string) (PrincipalRef, bool, error) {
	rows, err := qs.ProjectPrincipalByRef(ctx, q.ProjectPrincipalByRefParams{Kind: string(kind), Name: name})
	if err != nil || len(rows) == 0 {
		return PrincipalRef{}, false, err
	}
	return PrincipalRef{Kind: kind, Name: rows[0].Name, ID: rows[0].ID}, true, nil
}

// AddAdmin makes a user or a group an administrator. The write does not move the
// project's version (Q-AUTHZ-185); an administrator that already was one is not a
// write and records nothing.
func (s *service) AddAdmin(ctx context.Context, actor authz.Subject, key, ref string) (PrincipalRef, bool, int64, error) {
	kind, name, ok, err := parseAdminRef(ref)
	if err != nil {
		return PrincipalRef{}, false, 0, err
	}
	if !ok {
		return PrincipalRef{}, false, 0, notAdministrator(ref)
	}
	var admin PrincipalRef
	var created bool
	var epoch int64
	err = s.inTx(ctx, func(tx meta.Tx, qs *q.Queries) error {
		if err := qs.ProjectLock(ctx, key); err != nil {
			return err
		}
		p, err := load(ctx, qs, key)
		if err != nil {
			return err
		}
		var found bool
		if admin, found, err = principalOf(ctx, qs, kind, name); err != nil {
			return err
		} else if !found {
			return errs.E(errs.NotFound, errs.CodeNotFound, fmt.Sprintf("The %s %q does not exist.", kind, name))
		}
		n, err := qs.ProjectAddAdmin(ctx, q.ProjectAddAdminParams{PrincipalID: admin.ID, Key: key})
		if err != nil {
			return err
		}
		if created = n == 1; !created {
			epoch, err = epochOf(ctx, qs)
			return err
		}
		if err := s.notifyAuthz(tx, "project_admin", p.ID); err != nil {
			return err
		}
		afterEpoch(tx, &epoch, func(ctx context.Context, tx meta.Tx, epoch int64) error {
			return s.o.Recorder.RecordTx(ctx, tx, adminEvent(audit.EventProjectAdminAdded, actor, p, admin, epoch))
		})
		return nil
	})
	if err != nil {
		return PrincipalRef{}, false, 0, err
	}
	return admin, created, epoch, nil
}

// RemoveAdmin removes an administrator at once (a journaled revocation).
func (s *service) RemoveAdmin(ctx context.Context, actor authz.Subject, key, ref string) (int64, error) {
	kind, name, ok, err := parseAdminRef(ref)
	if err != nil {
		return 0, err
	}
	none := errs.E(errs.NotFound, errs.CodeNotFound, fmt.Sprintf("%q is no administrator of the project %q.", ref, key))
	if !ok {
		return 0, none // a service account never was one
	}
	var epoch int64
	err = s.inTx(ctx, func(tx meta.Tx, qs *q.Queries) error {
		if err := qs.ProjectLock(ctx, key); err != nil {
			return err
		}
		p, err := load(ctx, qs, key)
		if err != nil {
			return err
		}
		admin, found, err := principalOf(ctx, qs, kind, name)
		if err != nil {
			return err
		}
		if !found {
			return none
		}
		n, err := qs.ProjectRemoveAdmin(ctx, q.ProjectRemoveAdminParams{Key: key, PrincipalID: admin.ID})
		if err != nil {
			return err
		}
		if n == 0 {
			return none
		}
		if err := s.notifyAuthz(tx, "project_admin", p.ID); err != nil {
			return err
		}
		afterEpoch(tx, &epoch, func(ctx context.Context, tx meta.Tx, epoch int64) error {
			return s.o.Recorder.RecordTx(ctx, tx, adminEvent(audit.EventProjectAdminRemoved, actor, p, admin, epoch))
		})
		return nil
	})
	return epoch, err
}

func repositoryNotFound(key string) error {
	return errs.E(errs.NotFound, errs.CodeRepositoryNotFound, fmt.Sprintf("The repository %q does not exist.", key))
}

// assigned is the project a repository belongs to now, as the statement reads it.
type assigned struct {
	repoID    int64
	projectID string
	key       string
}

func currentProject(ctx context.Context, qs *q.Queries, repo string) (assigned, error) {
	rows, err := qs.ProjectOfRepository(ctx, repo)
	if err != nil {
		return assigned{}, err
	}
	if len(rows) == 0 {
		return assigned{}, repositoryNotFound(repo)
	}
	return assigned{repoID: rows[0].RepositoryID, projectID: rows[0].ProjectID, key: rows[0].ProjectKey}, nil
}

// lockProjects takes the row locks of the projects an assignment touches, in key
// order so that two moves in opposite directions cannot deadlock, and reads the
// repository's project again under them: when it changed between the first read and
// the locks, the write would act on a state it did not decide on and says so.
func lockProjects(ctx context.Context, qs *q.Queries, repo string, first assigned, keys ...string) (assigned, error) {
	if first.key != "" {
		keys = append(keys, first.key)
	}
	sort.Strings(keys)
	for i, k := range keys {
		if i > 0 && k == keys[i-1] {
			continue
		}
		if err := qs.ProjectLock(ctx, k); err != nil {
			return assigned{}, err
		}
	}
	now, err := currentProject(ctx, qs, repo)
	if err != nil {
		return assigned{}, err
	}
	if now.projectID != first.projectID {
		return assigned{}, errs.E(errs.Conflict, errs.CodeRepositoryAssigned,
			fmt.Sprintf("The repository %q was assigned by another request meanwhile; try again.", repo))
	}
	return now, nil
}

// expectOwner compares the project the caller expects (from; nil: no expectation, empty:
// none) with the one read under the locks of the write (Q-AUTHZ-205). The answer names
// the current project, so the caller can ask again without another read.
func expectOwner(repo string, from *string, cur assigned) error {
	if from == nil || *from == cur.key {
		return nil
	}
	var current any
	detail := fmt.Sprintf("The repository %q belongs to no project now.", repo)
	if cur.key != "" {
		current = cur.key
		detail = fmt.Sprintf("The repository %q belongs to the project %q now.", repo, cur.key)
	}
	return errs.E(errs.Conflict, errs.CodeProjectOwnerChanged, detail, errs.WithExtension("currentProject", current))
}

// notifyAssignment tells both areas: authz (the index gives project administrators
// manage on the project's repositories and scopes project policies to them) and
// repo_config (the repository's document carries its project, and a replica's
// snapshot of repositories carries project_id). The repo_config payload is the one
// the repository package writes, with the config_version the assignment left.
func (s *service) notifyAssignment(tx meta.Tx, repoID, repoKey string, version int64) error {
	if err := s.notifyAuthz(tx, "project_repository", repoID); err != nil {
		return err
	}
	return s.o.Changes.Notify(tx, authz.AreaRepoConfig, payloadVersion, repoChange{ID: repoID, Key: repoKey, Op: opUpdated, Version: version})
}

// Assign assigns a repository to the project (R-AUTHZ-026). The repository row
// changes only when it belongs to no project or to the one read under the locks, so
// two assignments cannot both win; a caller that names the owner it expects (from) is
// refused when the owner read under the locks is another one; its config_version moves on because the project
// is a member of its document.
func (s *service) Assign(ctx context.Context, actor authz.Subject, key, repo string, move bool, from *string) (int64, error) {
	var epoch int64
	err := s.inTx(ctx, func(tx meta.Tx, qs *q.Queries) error {
		first, err := currentProject(ctx, qs, repo)
		if err != nil {
			return err
		}
		cur, err := lockProjects(ctx, qs, repo, first, key)
		if err != nil {
			return err
		}
		if err := expectOwner(repo, from, cur); err != nil {
			return err
		}
		target, err := load(ctx, qs, key)
		if err != nil {
			return err
		}
		if cur.projectID == target.ID {
			epoch, err = epochOf(ctx, qs)
			return err
		}
		if cur.projectID != "" && !move {
			return errs.E(errs.Conflict, errs.CodeRepositoryAssigned,
				fmt.Sprintf("The repository %q belongs to the project %q; move=true moves it.", repo, cur.key))
		}
		rows, err := qs.ProjectAssignRepository(ctx, q.ProjectAssignRepositoryParams{
			ProjectID: target.ID, Actor: actorID(actor), RepositoryKey: repo, FromProjectID: cur.projectID,
		})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return errs.E(errs.Conflict, errs.CodeRepositoryAssigned, fmt.Sprintf("The repository %q was assigned by another request meanwhile; try again.", repo))
		}
		id := strconv.FormatInt(rows[0].RepositoryID, 10)
		if err := s.notifyAssignment(tx, id, repo, rows[0].ConfigVersion); err != nil {
			return err
		}
		afterEpoch(tx, &epoch, func(ctx context.Context, tx meta.Tx, epoch int64) error {
			moved := cur.projectID != ""
			if moved {
				old := Project{ID: cur.projectID, Key: cur.key}
				ev := assignmentEvent(audit.EventProjectRepositoryUnassigned, actor, old, repoMove{ID: id, Key: repo, Moved: true, Other: key}, epoch)
				if err := s.o.Recorder.RecordTx(ctx, tx, ev); err != nil {
					return err
				}
			}
			ev := assignmentEvent(audit.EventProjectRepositoryAssigned, actor, target, repoMove{ID: id, Key: repo, Moved: moved, Other: cur.key}, epoch)
			return s.o.Recorder.RecordTx(ctx, tx, ev)
		})
		return nil
	})
	return epoch, err
}

// Unassign removes the repository from the project it belongs to.
func (s *service) Unassign(ctx context.Context, actor authz.Subject, key, repo string) (int64, error) {
	var epoch int64
	err := s.inTx(ctx, func(tx meta.Tx, qs *q.Queries) error {
		first, err := currentProject(ctx, qs, repo)
		if err != nil {
			return err
		}
		if first.key != key {
			return errs.E(errs.NotFound, errs.CodeNotFound, fmt.Sprintf("The repository %q is not assigned to the project %q.", repo, key))
		}
		cur, err := lockProjects(ctx, qs, repo, first)
		if err != nil {
			return err
		}
		rows, err := qs.ProjectAssignRepository(ctx, q.ProjectAssignRepositoryParams{
			ProjectID: "", Actor: actorID(actor), RepositoryKey: repo, FromProjectID: cur.projectID,
		})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return errs.E(errs.Conflict, errs.CodeRepositoryAssigned, fmt.Sprintf("The repository %q was assigned by another request meanwhile; try again.", repo))
		}
		id := strconv.FormatInt(rows[0].RepositoryID, 10)
		if err := s.notifyAssignment(tx, id, repo, rows[0].ConfigVersion); err != nil {
			return err
		}
		afterEpoch(tx, &epoch, func(ctx context.Context, tx meta.Tx, epoch int64) error {
			p := Project{ID: cur.projectID, Key: cur.key}
			return s.o.Recorder.RecordTx(ctx, tx, assignmentEvent(audit.EventProjectRepositoryUnassigned, actor, p, repoMove{ID: id, Key: repo}, epoch))
		})
		return nil
	})
	return epoch, err
}

// LookupPrincipals answers the lookup of Q-AUTHZ-182. The bounds are the service's
// own, so that no caller can widen them: a prefix of at least MinLookupPrefix
// characters, at most MaxLookup results.
func (s *service) LookupPrincipals(ctx context.Context, prefix string, kind PrincipalKind, limit int) ([]PrincipalRef, error) {
	var fields []errs.FieldError
	if utf8.RuneCountInString(prefix) < MinLookupPrefix {
		fields = append(fields, errs.FieldError{
			Pointer: "/q", Code: "prefix_too_short",
			Detail: fmt.Sprintf("The prefix has at least %d characters.", MinLookupPrefix),
		})
	}
	if kind != "" && kind != KindUser && kind != KindGroup {
		fields = append(fields, errs.FieldError{Pointer: "/kind", Code: "invalid_kind", Detail: "The kind is user or group."})
	}
	if limit < 0 {
		fields = append(fields, errs.FieldError{Pointer: "/limit", Code: "invalid_limit", Detail: fmt.Sprintf("The limit is 1 to %d.", MaxLookup)})
	}
	if len(fields) > 0 {
		return nil, errs.E(errs.Invalid, errs.CodeValidation, fields[0].Detail, errs.WithFields(fields...))
	}
	if limit == 0 || limit > MaxLookup {
		limit = MaxLookup
	}
	var out []PrincipalRef
	err := s.inTx(ctx, func(_ meta.Tx, qs *q.Queries) error {
		rows, err := qs.ProjectLookupPrincipals(ctx, q.ProjectLookupPrincipalsParams{Kind: string(kind), Prefix: prefix, MaxRows: int32(limit)}) //nolint:gosec // at most MaxLookup
		if err != nil {
			return err
		}
		out = make([]PrincipalRef, 0, len(rows))
		for _, r := range rows {
			out = append(out, PrincipalRef{Kind: PrincipalKind(r.Kind), Name: r.Name})
		}
		return nil
	})
	return out, err
}
