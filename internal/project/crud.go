package project

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/qname"
)

// The field codes of a project's validation errors (R-AUTHZ-026), in errors[] of
// the 422 validation_failed answer.
const (
	fieldInvalidKey         = "invalid_key"
	fieldInvalidDisplayName = "invalid_display_name"
	fieldInvalidDescription = "invalid_description"
)

// problems checks the free-text fields of an input; the key only on create (it is
// immutable, and a replace compares it in the handler).
func problems(in Input, create bool) error {
	var fields []errs.FieldError
	if create && !qname.ValidProjectKey(in.Key) {
		fields = append(fields, errs.FieldError{
			Pointer: "/key", Code: fieldInvalidKey,
			Detail: "The key has " + qname.ProjectKeyRule + ".",
		})
	}
	if n := utf8.RuneCountInString(in.DisplayName); strings.TrimSpace(in.DisplayName) == "" || n > MaxDisplayName {
		fields = append(fields, errs.FieldError{
			Pointer: "/displayName", Code: fieldInvalidDisplayName,
			Detail: fmt.Sprintf("The display name has 1 to %d characters and is not blank.", MaxDisplayName),
		})
	}
	if utf8.RuneCountInString(in.Description) > MaxDescription {
		fields = append(fields, errs.FieldError{
			Pointer: "/description", Code: fieldInvalidDescription,
			Detail: fmt.Sprintf("The description has at most %d characters.", MaxDescription),
		})
	}
	if len(fields) == 0 {
		return nil
	}
	detail := fields[0].Detail
	if len(fields) > 1 {
		detail = fmt.Sprintf("%d fields are invalid.", len(fields))
	}
	return errs.E(errs.Invalid, errs.CodeValidation, detail, errs.WithFields(fields...))
}

// List returns every project, ordered by key (bytewise: the cursor of the API
// compares keys the same way, whatever the database collation says).
func (s *service) List(ctx context.Context) ([]Project, error) {
	var out []Project
	err := s.inTx(ctx, func(_ meta.Tx, qs *q.Queries) error {
		rows, err := qs.ProjectList(ctx)
		if err != nil {
			return err
		}
		mem, err := qs.ProjectMembersOfAll(ctx)
		if err != nil {
			return err
		}
		byKey := map[string][]Member{}
		for _, m := range mem {
			if member, ok := memberOf(m.ProjectKey, m.PrincipalKind, m.PrincipalName, m.PrincipalID, m.Role); ok {
				byKey[m.ProjectKey] = append(byKey[m.ProjectKey], member)
			}
		}
		out = make([]Project, 0, len(rows))
		for _, r := range rows {
			members := byKey[r.Key]
			if members == nil {
				members = []Member{}
			}
			out = append(out, toProject(head(r), members))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// Get returns one project with its members.
func (s *service) Get(ctx context.Context, key string) (Project, error) {
	var p Project
	err := s.inTx(ctx, func(_ meta.Tx, qs *q.Queries) (err error) {
		p, err = load(ctx, qs, key)
		return err
	})
	return p, err
}

// CreateIn creates a project on the caller's transaction (R-AUTHZ-026). The users and groups of
// in.Owners become its first Owners in the same transaction, each recorded as
// authz.project.member_added, so that a project an administrator creates for a team is administered
// by it from the first epoch on (Q-AUTHZ-208).
func (s *service) CreateIn(ctx context.Context, tx meta.Tx, actor authz.Subject, in Input, epoch *int64) (Project, error) {
	if err := problems(in, true); err != nil {
		return Project{}, err
	}
	qs, err := queriesOf(tx)
	if err != nil {
		return Project{}, err
	}
	id := newID()
	rows, err := qs.ProjectInsert(ctx, q.ProjectInsertParams{
		ID: id, Key: in.Key, DisplayName: in.DisplayName, Description: in.Description, Actor: actorID(actor),
	})
	if err != nil {
		return Project{}, err
	}
	if len(rows) == 0 {
		return Project{}, errs.E(errs.Conflict, errs.CodeAlreadyExists, fmt.Sprintf("A project with the key %q exists.", in.Key))
	}
	owners, err := firstOwners(ctx, qs, id, in.Owners)
	if err != nil {
		return Project{}, err
	}
	if err := s.notifyAuthz(tx, "project", id); err != nil {
		return Project{}, err
	}
	out, err := load(ctx, qs, in.Key)
	if err != nil {
		return Project{}, err
	}
	afterEpoch(tx, epoch, func(ctx context.Context, tx meta.Tx, epoch int64) error {
		if err := s.o.Recorder.RecordTx(ctx, tx, projectEvent(audit.EventProjectCreated, actor, out, nil, stateOf(out), epoch, nil)); err != nil {
			return err
		}
		for _, pr := range owners {
			ev := memberEvent(audit.EventProjectMemberAdded, actor, out.Key, id, pr, epoch, authz.ProjectRoleNone, RoleOwner)
			if err := s.o.Recorder.RecordTx(ctx, tx, ev); err != nil {
				return err
			}
		}
		return nil
	})
	return out, nil
}

// firstOwners adds the users and groups of a create's owners as members with the role owner. A reference
// that names no member kind, an auto-join group or a principal that does not exist refuses the create, as
// it refuses the PUT of a member; a reference given twice is added once.
func firstOwners(ctx context.Context, qs *q.Queries, projectID string, refs []string) ([]PrincipalRef, error) {
	var out []PrincipalRef
	seen := map[string]bool{}
	for _, ref := range refs {
		pr, err := memberPrincipal(ctx, qs, ref)
		if err != nil {
			return nil, err
		}
		if seen[pr.ID] {
			continue
		}
		seen[pr.ID] = true
		if _, err := qs.ProjectMemberAdd(ctx, q.ProjectMemberAddParams{ProjectID: projectID, PrincipalID: pr.ID, Role: RoleOwner.String()}); err != nil {
			return nil, err
		}
		out = append(out, pr)
	}
	return out, nil
}

// Replace changes display name and description under the version check
// (R-API-012). The version moves with these two fields only (Q-AUTHZ-185).
func (s *service) Replace(ctx context.Context, actor authz.Subject, key string, expected int64, in Input) (Project, int64, error) {
	if err := problems(in, false); err != nil {
		return Project{}, 0, err
	}
	var out Project
	var epoch int64
	err := s.inTx(ctx, func(tx meta.Tx, qs *q.Queries) error {
		if _, err := lockProject(ctx, qs, key); err != nil {
			return err
		}
		before, err := load(ctx, qs, key)
		if err != nil {
			return err
		}
		rows, err := qs.ProjectUpdate(ctx, q.ProjectUpdateParams{
			DisplayName: in.DisplayName, Description: in.Description,
			Actor: actorID(actor), Key: key, Expected: expected,
		})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return errs.Stale("The project", before.Version)
		}
		if err := s.notifyAuthz(tx, "project", before.ID); err != nil {
			return err
		}
		if out, err = load(ctx, qs, key); err != nil {
			return err
		}
		afterEpoch(tx, &epoch, func(ctx context.Context, tx meta.Tx, epoch int64) error {
			return s.o.Recorder.RecordTx(ctx, tx, projectEvent(audit.EventProjectUpdated, actor, out, stateOf(before), stateOf(out), epoch, nil))
		})
		return nil
	})
	if err != nil {
		return Project{}, 0, err
	}
	return out, epoch, nil
}

// maxDeleteRounds bounds the retries of a delete that finds new policies of the
// project between deleting the old ones and deleting the project.
const maxDeleteRounds = 3

// maxJournaledPolicies is the limit of the audit schema of events_projects.go.
const maxJournaledPolicies = 1000

// Delete deletes an empty project (Q-AUTHZ-185): a first read refuses a project
// with a repository (in any lifecycle), a service account or a CI mapping of its
// tier, the policies go one by one through the policy store (each audited and
// journaled in its own transaction as any policy deletion), then one transaction
// takes the project's lock, counts again and deletes the project with its member
// entries. A policy created in the meantime sends the delete round again; the
// foreign keys of repositories, service accounts and CI mappings are the safety
// net behind the count.
func (s *service) Delete(ctx context.Context, actor authz.Subject, key string, expected int64) (int64, error) {
	var deleted []string
	for range maxDeleteRounds {
		policies, err := s.precheckDelete(ctx, key, expected)
		if err != nil {
			return 0, err
		}
		changed := false
		for _, pol := range policies {
			if s.o.Policies == nil {
				return 0, errs.E(errs.Unavailable, errs.CodeUnavailable, "The policy store is not available on this replica.")
			}
			// The version read in the precheck is the If-Match of the deletion, and the
			// project is checked under the policy lock: a policy that was edited since, or
			// replaced under the same name by a global one or another project's, is not
			// removed, whatever its version (Q-AUTHZ-204, Q-AUTHZ-199). A stale answer
			// sends the round again; a policy of another owner answers 404 and is skipped.
			_, err := s.o.Policies.Delete(ctx, actor, pol.Name, pol.Version, authz.ExpectProject(&key))
			switch {
			case err == nil:
				deleted = append(deleted, pol.Name)
			case errs.KindOf(err) == errs.NotFound:
			case errs.KindOf(err) == errs.PreconditionFailed:
				changed = true
			default:
				return 0, err
			}
		}
		if changed {
			continue
		}
		epoch, again, err := s.deleteEmpty(ctx, actor, key, expected, deleted)
		if err != nil || !again {
			return epoch, err
		}
	}
	return 0, errs.E(errs.Conflict, errs.CodeConflict, "The policies of the project keep changing; try again.")
}

// precheckDelete reads the project and refuses what Delete refuses before it
// touches a policy: an unknown project, a stale version, a project that is not
// empty. It returns the project's policies with the version each has now.
func (s *service) precheckDelete(ctx context.Context, key string, expected int64) ([]q.ProjectPolicyNamesRow, error) {
	var names []q.ProjectPolicyNamesRow
	err := s.inTx(ctx, func(_ meta.Tx, qs *q.Queries) error {
		p, err := load(ctx, qs, key)
		if err != nil {
			return err
		}
		if err := checkDeletable(p, expected); err != nil {
			return err
		}
		names, err = qs.ProjectPolicyNames(ctx, p.ID)
		return err
	})
	return names, err
}

func checkDeletable(p Project, expected int64) error {
	if expected >= 0 && expected != p.Version {
		return errs.Stale("The project", p.Version)
	}
	if !p.Empty() {
		return errs.E(errs.Conflict, errs.CodeProjectNotEmpty,
			fmt.Sprintf("The project %q still has %d repositories, %d service accounts and %d CI mappings; delete the repositories, the accounts and the CI mappings first.",
				p.Key, p.Repositories, p.ServiceAccounts, p.CIMappings))
	}
	return nil
}

// deleteEmpty is the closing transaction of a delete. again is true when policies
// of the project exist after all: the caller deletes them and comes back.
func (s *service) deleteEmpty(ctx context.Context, actor authz.Subject, key string, expected int64, policies []string) (epoch int64, again bool, err error) {
	err = s.inTx(ctx, func(tx meta.Tx, qs *q.Queries) error {
		if _, err := lockProject(ctx, qs, key); err != nil {
			return err
		}
		p, err := load(ctx, qs, key)
		if err != nil {
			return err
		}
		if err := checkDeletable(p, expected); err != nil {
			return err
		}
		left, err := qs.ProjectPolicyNames(ctx, p.ID)
		if err != nil {
			return err
		}
		if len(left) > 0 {
			again = true
			return nil
		}
		rows, err := qs.ProjectDelete(ctx, q.ProjectDeleteParams{Key: key, Expected: expected})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return errs.Stale("The project", p.Version)
		}
		if err := s.notifyAuthz(tx, "project", p.ID); err != nil {
			return err
		}
		listed := policies
		if len(listed) > maxJournaledPolicies {
			listed = listed[:maxJournaledPolicies]
		}
		afterEpoch(tx, &epoch, func(ctx context.Context, tx meta.Tx, epoch int64) error {
			return s.o.Recorder.RecordTx(ctx, tx, projectEvent(audit.EventProjectDeleted, actor, p, stateOf(p), nil, epoch, listed))
		})
		return nil
	})
	if again {
		return 0, true, nil
	}
	return epoch, false, err
}
