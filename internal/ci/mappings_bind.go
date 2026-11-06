package ci

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/qname"
)

// What the policies, the groups and the repositories of a mapping's grants resolve to: the existence
// rules, the project's bounds (Q-CI-26) and the binding of the mapping's principal in the policies'
// principal lists (R-CI-013, Q-CI-35).

// newBindingActions are the actions of the entry a save adds to a policy's principal list: `read`, the
// least a policy can give (Q-CI-35). The entry is the policy's: widening it is an edit of the policy, which
// the write rules of the mapping do not see (H-412). A function, not a package variable, so no caller can
// change the list another one reads (R-ARCH-032).
func newBindingActions() []string { return []string{"read"} }

// lockedPolicy is a policy a save names, read under its row lock.
type lockedPolicy struct {
	ID        string
	Name      string
	Project   string // the key of the policy's project, "" for a global policy
	ProjectID string
}

// binding is the entry of the mapping's principal in one policy.
type binding struct {
	PolicyID string
	Policy   string
	Actions  []string
}

// resolvedGrants is what a save's policies and groups come to.
type resolvedGrants struct {
	// locked are the policies of the save and of the current bindings, by name.
	locked map[string]lockedPolicy
	// current are the mapping's bindings before the write.
	current []binding
	// actions are the actions the listed policies and groups give the mapping, for claims.Check.
	actions []claims.Action
	// listed are the stored names of the policies the save lists, in the order of the grants: the name
	// within the project qualified by the project for a project's mapping (a project policy is stored as
	// <project>/<name>, Q-AUTHZ-209), as written for a system mapping.
	listed []string
}

// storedPolicyName is the stored name of the policy a mapping of the project lists by name: a project's mapping
// names the policies of its own project, which the table keeps as <project>/<name>.
func storedPolicyName(project, name string) string {
	if project == "" {
		return name
	}
	return project + "/" + name
}

// resolveGrants reads and locks what the grants name: it answers the unknown policies and groups, a
// project mapping's policy that is not the project's, and the actions the policies' entries and the groups
// give. project is the key of the mapping's project, "" for the system tier; principalID is the mapping's
// principal, "" for a mapping that is created.
func (s *MappingService) resolveGrants(ctx context.Context, qs *q.Queries, project, principalID string, spec claims.Spec) (resolvedGrants, []errs.FieldError, error) {
	rg := resolvedGrants{locked: map[string]lockedPolicy{}}
	if principalID != "" {
		rows, err := qs.CiMappingPolicyBindings(ctx, []string{principalID})
		if err != nil {
			return rg, nil, err
		}
		for _, r := range rows {
			if project != "" && r.Project != project {
				continue // a project's save neither reads nor removes a binding of a policy that is not the project's
			}
			rg.current = append(rg.current, binding{PolicyID: r.PolicyID, Policy: r.Policy, Actions: r.Actions})
		}
	}
	for _, name := range spec.Policies {
		rg.listed = append(rg.listed, storedPolicyName(project, name))
	}
	names := slices.Clone(rg.listed)
	for _, b := range rg.current {
		names = append(names, b.Policy)
	}
	slices.Sort(names)
	names = slices.Compact(names)
	if len(names) > 0 {
		rows, err := qs.CiPoliciesLock(ctx, names)
		if err != nil {
			return rg, nil, err
		}
		for _, r := range rows {
			rg.locked[r.Name] = lockedPolicy{ID: r.ID, Name: r.Name, Project: r.Project, ProjectID: r.ProjectID}
		}
	}
	var fields []errs.FieldError
	for i, name := range spec.Policies {
		ptr := "/grants/policies/" + strconv.Itoa(i)
		p, ok := rg.locked[rg.listed[i]]
		switch {
		case !ok && project != "":
			// A project's administrator learns nothing about the policies of others: a name that does not exist
			// answers as one that is not the project's (Q-CI-37, Q-AUTHZ-197).
			fields = append(fields, field(ptr, errs.CodePolicyOutsideProject, fmt.Sprintf("The project %q has no policy named %q; a project's mapping names the project's own policies.", project, name)))
		case !ok:
			fields = append(fields, field(ptr, errs.CodeUnknownPolicy, fmt.Sprintf("No permission policy %q exists.", name)))
		case project != "" && p.Project != project:
			fields = append(fields, field(ptr, errs.CodePolicyOutsideProject, fmt.Sprintf("The project %q has no policy named %q; a project's mapping names the project's own policies.", project, name)))
		}
	}
	if project == "" && len(spec.Groups) > 0 {
		found, err := qs.CiGroupsFound(ctx, spec.Groups)
		if err != nil {
			return rg, nil, err
		}
		for i, name := range spec.Groups {
			if !slices.Contains(found, name) {
				fields = append(fields, field("/grants/groups/"+strconv.Itoa(i), errs.CodeUnknownGroup, fmt.Sprintf("No group %q exists.", name)))
			}
		}
		acts, err := qs.CiGroupActions(ctx, spec.Groups)
		if err != nil {
			return rg, nil, err
		}
		for _, a := range acts {
			rg.actions = append(rg.actions, claims.Action(a))
		}
	}
	for _, name := range rg.listed {
		if _, ok := rg.locked[name]; !ok {
			continue
		}
		acts := newBindingActions()
		if i := slices.IndexFunc(rg.current, func(b binding) bool { return b.Policy == name }); i >= 0 {
			acts = rg.current[i].Actions
		}
		for _, a := range acts {
			rg.actions = append(rg.actions, claims.Action(a))
		}
	}
	return rg, fields, nil
}

// applyBindings brings the policies' principal lists in step with the policies the mapping lists
// (R-CI-013): an entry with the actions of newBindingActions is added where the mapping is not yet a
// principal of a listed policy, and the entry of a policy the mapping no longer lists is removed. An
// entry that stays keeps its actions. A policy whose list changed moves its version and notifies the
// change area authz, so the authorization index rebuilds (R-AUTHZ-028). It returns the names of the
// policies added and removed, for the event.
func (s *MappingService) applyBindings(ctx context.Context, tx meta.Tx, qs *q.Queries, principalID string, rg resolvedGrants) (added, removed []string, err error) {
	listed := rg.listed
	for _, name := range listed {
		p := rg.locked[name]
		if slices.ContainsFunc(rg.current, func(b binding) bool { return b.PolicyID == p.ID }) {
			continue
		}
		if p.ProjectID == "" {
			err = qs.AuthzInsertEntry(ctx, q.AuthzInsertEntryParams{PolicyID: p.ID, PrincipalID: principalID, Actions: newBindingActions()})
		} else {
			err = qs.AuthzInsertProjectEntry(ctx, q.AuthzInsertProjectEntryParams{PolicyID: p.ID, ProjectID: p.ProjectID, PrincipalID: principalID, Actions: newBindingActions()})
		}
		if err != nil {
			return nil, nil, err
		}
		if err := s.touchPolicy(ctx, tx, qs, p.ID); err != nil {
			return nil, nil, err
		}
		added = append(added, name)
	}
	for _, b := range rg.current {
		if slices.Contains(listed, b.Policy) {
			continue
		}
		if err := qs.CiPolicyEntryDelete(ctx, q.CiPolicyEntryDeleteParams{PolicyID: b.PolicyID, PrincipalID: principalID}); err != nil {
			return nil, nil, err
		}
		if err := s.touchPolicy(ctx, tx, qs, b.PolicyID); err != nil {
			return nil, nil, err
		}
		removed = append(removed, b.Policy)
	}
	return added, removed, nil
}

func (s *MappingService) touchPolicy(ctx context.Context, tx meta.Tx, qs *q.Queries, id string) error {
	if err := qs.CiPolicyTouch(ctx, id); err != nil {
		return err
	}
	return s.notifyAuthz(tx, id)
}

// releaseBindings moves the version of every policy that names the principal and notifies the change area
// authz, before the principal row (and with it its entries) is deleted (R-AUTHZ-002, R-AUTHZ-028).
func (s *MappingService) releaseBindings(ctx context.Context, tx meta.Tx, qs *q.Queries, principalID string) error {
	ids, err := qs.AuthzTouchPoliciesOfPrincipal(ctx, principalID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.notifyAuthz(tx, id); err != nil {
			return err
		}
	}
	return nil
}

// outsideFields are the key selectors of a project mapping's inline grants that name no repository of the
// project (R-CI-019, Q-CI-26): 422 repository_outside_project, and a key that does not exist answers the
// same, so the answer never tells whether a repository of another project exists. A pattern or a class
// selector means the project's repositories, and a template is bounded by the evaluator (the token's
// contribution names the project), so neither is checked here.
func outsideFields(ctx context.Context, qs *q.Queries, project, projectID string, spec claims.Spec) ([]errs.FieldError, error) {
	type use struct{ ptr, key string }
	var uses []use
	var keys []string
	for i, g := range spec.Inline {
		for j, sel := range g.Repositories {
			if strings.Contains(sel, "{{") || !qname.ValidKey(sel) {
				continue
			}
			uses = append(uses, use{"/grants/inline/" + strconv.Itoa(i) + "/repositories/" + strconv.Itoa(j), sel})
			keys = append(keys, sel)
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	own, err := qs.CiProjectRepositories(ctx, q.CiProjectRepositoriesParams{ProjectID: projectID, Keys: keys})
	if err != nil {
		return nil, err
	}
	var fields []errs.FieldError
	for _, u := range uses {
		if !slices.Contains(own, u.key) {
			fields = append(fields, field(u.ptr, errs.CodeRepositoryOutsideProject, fmt.Sprintf("The repository %q is not a repository of the project %q.", u.key, project)))
		}
	}
	return fields, nil
}
