package v1

import (
	"context"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
)

// The helpers of a delegable operation (R-AUTHZ-026, Q-AUTHZ-180). An operation
// whose declaration names projectAdmin admits two kinds of caller: one that holds
// the capability and so acts on the whole installation, and a project administrator
// who acts on the projects it administers. The shared step lets both in; the
// handler calls one of these to narrow the second kind to the project the request
// addresses, before it reads or writes anything of it.
//
// The questions are answered on the index of the request and audit nothing. The
// refusals follow R-AUTHZ-016: a project the caller does not administer answers 404
// (the answer does not tell whether it exists), a project it administers but whose
// switch is off answers 403 project_switch_off, a right it lacks 403 forbidden.

// DelegatedScope is what the caller of a delegable operation may reach.
type DelegatedScope struct {
	// Global is true when the caller holds the operation's capability: every project
	// and every repository, as before projects existed.
	Global bool
	// Projects are the projects the caller administers, with their switches, empty
	// for a caller without any. A Global caller may administer some as well.
	Projects []authz.AdministeredProject

	// refused records a refusal of the caller as the denied-request audit of the
	// capability does (R-AUTHZ-029, Q-AUTHZ-201); nil records nothing.
	refused func()
}

// noteRefusal records that the caller was refused by its narrowing. The shared step
// admitted it as a project administrator, so no layer above has audited the
// refusal: the capability's own denial is recorded here, once per refusal, under
// the rules of authz.denied (the dedup window and the anonymous switch).
func (d DelegatedScope) noteRefusal() {
	if d.refused != nil {
		d.refused()
	}
}

// May reports whether the caller may act on the project with the right.
func (d DelegatedScope) May(project string, right authz.ProjectRight) bool {
	if d.Global {
		return true
	}
	for _, p := range d.Projects {
		if p.Key == project {
			return p.Holds(right)
		}
	}
	return false
}

// Sees reports whether the caller may read the project: it holds the capability or
// administers it.
func (d DelegatedScope) Sees(project string) bool { return d.May(project, authz.ProjectAny) }

// delegatedScope asks what the caller may reach with the capability: whether it holds
// it and which projects it administers. Without an Access that answers projects the
// caller has none.
func (s *Server) delegatedScope(ctx context.Context, capability string) (DelegatedScope, error) {
	var d DelegatedScope
	var err error
	if d.Global, err = s.holds(ctx, capability); err != nil {
		return DelegatedScope{}, err
	}
	if pa, ok := s.o.Access.(ProjectAccess); ok {
		if d.Projects, err = pa.AdministeredProjects(ctx); err != nil {
			return DelegatedScope{}, err
		}
	}
	if !d.Global && s.o.Access != nil {
		access := s.o.Access
		d.refused = func() { _ = access.Require(ctx, capability) } // refused: the call records authz.denied
	}
	return d, nil
}

// requireProject refuses unless the caller holds the capability or administers the
// project with the right. Call it after the shared step, with the project key the
// request addresses.
func (s *Server) requireProject(ctx context.Context, capability, project string, right authz.ProjectRight) error {
	d, err := s.delegatedScope(ctx, capability)
	if err != nil {
		return err
	}
	return refuseProject(d, project, right)
}

// refuseProject is the refusal of a scope for one project and right, nil when it may.
func refuseProject(d DelegatedScope, project string, right authz.ProjectRight) error {
	if d.May(project, right) {
		return nil
	}
	d.noteRefusal()
	if d.Sees(project) {
		if right == authz.ProjectManagePolicies || right == authz.ProjectManageRepositories {
			return errs.E(errs.Forbidden, errs.CodeProjectSwitchOff, "The project does not allow this: its switch "+string(right)+" is off.")
		}
		return errs.E(errs.Forbidden, errs.CodeForbidden, "The operation is not permitted.")
	}
	return errs.E(errs.NotFound, errs.CodeNotFound, "The project does not exist or is not visible to the caller.")
}

// requireProjectOfRepository is requireProject for the project a repository belongs
// to: it refuses unless the caller holds the capability or administers that project
// with the right. It returns the project's key ("" for a caller that holds the
// capability, whose rights do not depend on it). A repository outside every project,
// and one the caller does not administer, answer 404 as an unknown repository does.
func (s *Server) requireProjectOfRepository(ctx context.Context, capability, repository string, right authz.ProjectRight) (string, error) {
	held, err := s.holds(ctx, capability)
	if err != nil || held {
		return "", err
	}
	pa, ok := s.o.Access.(ProjectAccess)
	if ok {
		if project, admin, err := pa.AdministersRepository(ctx, repository, right); err != nil || admin {
			return project, err
		}
		// Administers the project without the switch: say which, so the UI can explain.
		if project, admin, err := pa.AdministersRepository(ctx, repository, authz.ProjectAny); err != nil {
			return "", err
		} else if admin {
			return "", refuseProject(DelegatedScope{Projects: []authz.AdministeredProject{{Key: project}}, refused: s.refusalOf(ctx, capability)}, project, right)
		}
	}
	s.refusalOf(ctx, capability)()
	return "", errs.E(errs.NotFound, errs.CodeNotFound, "The repository does not exist or is not visible to the caller.")
}

// refusalOf is the recorder of a refusal of the capability for a caller that does
// not hold it (see [DelegatedScope.noteRefusal]).
func (s *Server) refusalOf(ctx context.Context, capability string) func() {
	return func() {
		if s.o.Access != nil {
			_ = s.o.Access.Require(ctx, capability)
		}
	}
}
