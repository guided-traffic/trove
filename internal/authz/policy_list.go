package authz

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/guided-traffic/trove/internal/qname"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
)

// ListFilter selects policies for GET /api/v1/permission-policies (R-AUTHZ-021).
type ListFilter struct {
	// Principal is a principal reference. With ExpandGroups the policies of the
	// groups the principal belongs to match as well.
	Principal    string
	ExpandGroups bool
	// Repository is a repository name <project>/<key>: the policies whose selectors cover it.
	Repository string
	// Project is a project key: the policies of that project (R-AUTHZ-026). An
	// unknown project is 422, like an unknown repository.
	Project string
	// Delegated is set for a caller that holds policies:read only as a project
	// administrator: a principal or a repository that does not exist answers like
	// one that has no policy of the project (an empty list, not 422), so the filter
	// is no test for the existence of principals or repositories (Q-AUTHZ-197).
	Delegated bool
}

// unprocessable is the 422 of a filter value that names nothing: validation_failed
// with the code of the problem in errors[] at the query parameter's name
// (R-API-009).
func unprocessable(param, code, detail string) error {
	return problemError([]ProblemField{field("/"+param, code, detail)})
}

// Select keeps the policies of all that match the filter. A principal or a
// repository that does not exist is 422 (R-AUTHZ-021 "422 unknown filter
// value"). The group membership is resolved now, never from a token
// (R-AUTHZ-002).
func (s *Service) Select(ctx context.Context, all []Policy, f ListFilter) ([]Policy, error) {
	refs, none, err := s.principalFilter(ctx, f)
	if err != nil {
		return nil, err
	}
	if none {
		return []Policy{}, nil
	}
	var repo *repoView
	repoProject := ""
	if f.Repository != "" {
		name, perr := qname.ParseRepo(f.Repository)
		if perr != nil {
			if f.Delegated {
				return []Policy{}, nil
			}
			return nil, unprocessable("repository", errs.CodeUnknownRepository, fmt.Sprintf("No repository %q exists.", f.Repository))
		}
		r, err := s.o.Repositories.ByName(ctx, name)
		if err != nil {
			var ee *errs.Error
			if errors.As(err, &ee) && ee.Kind == errs.NotFound {
				if f.Delegated {
					return []Policy{}, nil
				}
				return nil, unprocessable("repository", errs.CodeUnknownRepository, fmt.Sprintf("No repository %q exists.", f.Repository))
			}
			return nil, err
		}
		id, _ := strconv.ParseInt(r.ID, 10, 64)
		repo = &repoView{ID: id, Name: r.Name, Class: r.Class, Format: r.Format}
		repoProject = r.ProjectID
	}
	projectIDs := map[string]string{} // project key -> ID, for the policies of a project
	if f.Project != "" {
		id, err := s.projectID(ctx, f.Project)
		if err != nil {
			return nil, err
		}
		if id == "" {
			return nil, unprocessable("project", errs.CodeUnknownProject, fmt.Sprintf("No project %q exists.", f.Project))
		}
	}
	out := make([]Policy, 0, len(all))
	for _, p := range all {
		if f.Project != "" && (p.Project == nil || *p.Project != f.Project) {
			continue
		}
		if refs != nil && !grantsTo(p, refs) {
			continue
		}
		if repo != nil {
			if p.Project != nil {
				pid, ok := projectIDs[*p.Project]
				if !ok {
					var err error
					if pid, err = s.projectID(ctx, *p.Project); err != nil {
						return nil, err
					}
					projectIDs[*p.Project] = pid
				}
				if pid == "" || pid != repoProject {
					continue // a project policy covers only its project's repositories
				}
			}
			if !covers(p, *repo) {
				continue
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// principalFilter resolves the principal filter of a list: the references whose entries are kept (nil for no
// filter), and none when the filter selects nothing, a delegated caller's unknown principal and a Bastion
// registration, which no policy names (R-BAS-004).
func (s *Service) principalFilter(ctx context.Context, f ListFilter) (refs map[string]bool, none bool, err error) {
	if f.Principal == "" {
		return nil, false, nil
	}
	ref, err := ParseRef(f.Principal)
	if err != nil {
		return nil, false, unprocessable("principal", errs.CodeUnknownPrincipal, err.Error())
	}
	if ref.Kind == principal.KindBastion {
		return nil, true, nil
	}
	id, err := s.principalID(ctx, ref)
	if err != nil {
		if errors.Is(err, principal.ErrNotFound) {
			if f.Delegated {
				return nil, true, nil
			}
			return nil, false, unprocessable("principal", errs.CodeUnknownPrincipal, fmt.Sprintf("No principal %s exists.", f.Principal))
		}
		return nil, false, err
	}
	refs = map[string]bool{ref.String(): true}
	if f.ExpandGroups && !f.Delegated && ref.Kind != principal.KindAnonymous && ref.Kind != principal.KindAuthenticated {
		groups, err := s.o.Principals.Groups(ctx, id)
		if err != nil {
			return nil, false, err
		}
		for _, g := range groups {
			refs["group:"+g.Name] = true
		}
	}
	return refs, false, nil
}

func grantsTo(p Policy, refs map[string]bool) bool {
	for _, e := range p.Entries {
		if refs[e.Principal] {
			return true
		}
	}
	return false
}

// covers reports whether a selector of the policy covers the repository.
func covers(p Policy, r repoView) bool {
	for _, text := range p.Resources.Repositories {
		sel, err := parseSelector(text, contextOfPolicy(p))
		if err != nil {
			continue
		}
		if sel.matches(r) {
			return true
		}
	}
	return false
}

// Annotate fills the warnings of policies read by List, which skips them (the
// computation resolves principals and the repository list). A policy that does
// not validate any more, because a principal was deleted, keeps an empty list.
func (s *Service) Annotate(ctx context.Context, ps []Policy) error {
	// One read transaction serves the project lookups of the whole page (Q-AUTHZ-199).
	return s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queriesOf(tx)
		if err != nil {
			return err
		}
		for i := range ps {
			rp, problems, err := s.resolve(ctx, ps[i].PolicyInput, true, s.inTx(qs))
			if err != nil {
				return err
			}
			if len(problems) == 0 && rp != nil {
				ps[i].Warnings = rp.warnings
			}
			if ps[i].Warnings == nil {
				ps[i].Warnings = []string{}
			}
		}
		return nil
	})
}

// contextOfPolicy is the context a policy's selector texts are read in.
func contextOfPolicy(p Policy) qname.Context {
	if p.Project != nil {
		return projectContext(*p.Project)
	}
	return instanceContext
}
