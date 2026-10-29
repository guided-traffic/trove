package authz

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/errs"
)

// ListFilter selects policies for GET /api/v1/permission-policies (R-AUTHZ-021).
type ListFilter struct {
	// Principal is a principal reference. With ExpandGroups the policies of the
	// groups the principal belongs to match as well.
	Principal    string
	ExpandGroups bool
	// Repository is a repository key: the policies whose selectors cover it.
	Repository string
}

func unprocessable(code, detail string) error {
	return errs.E(errs.Unprocessable, code, detail)
}

// Select keeps the policies of all that match the filter. A principal or a
// repository that does not exist is 422 (R-AUTHZ-021 "422 unknown filter
// value"). The group membership is resolved now, never from a token
// (R-AUTHZ-002).
func (s *Service) Select(ctx context.Context, all []Policy, f ListFilter) ([]Policy, error) {
	var refs map[string]bool
	if f.Principal != "" {
		ref, err := ParseRef(f.Principal)
		if err != nil {
			return nil, unprocessable(errs.CodeUnknownPrincipal, err.Error())
		}
		id, err := s.principalID(ctx, ref)
		if err != nil {
			if errors.Is(err, principal.ErrNotFound) {
				return nil, unprocessable(errs.CodeUnknownPrincipal, fmt.Sprintf("No principal %s exists.", f.Principal))
			}
			return nil, err
		}
		refs = map[string]bool{ref.String(): true}
		if f.ExpandGroups && ref.Kind != principal.KindAnonymous && ref.Kind != principal.KindAuthenticated {
			groups, err := s.o.Principals.Groups(ctx, id)
			if err != nil {
				return nil, err
			}
			for _, g := range groups {
				refs["group:"+g.Name] = true
			}
		}
	}
	var repo *repoView
	if f.Repository != "" {
		r, err := s.o.Repositories.ByKey(ctx, f.Repository)
		if err != nil {
			var ee *errs.Error
			if errors.As(err, &ee) && ee.Kind == errs.NotFound {
				return nil, unprocessable(errs.CodeUnknownRepository, fmt.Sprintf("No repository %q exists.", f.Repository))
			}
			return nil, err
		}
		id, _ := strconv.ParseInt(r.ID, 10, 64)
		repo = &repoView{ID: id, Key: r.Key, Class: r.Class, Format: r.Format}
	}
	out := make([]Policy, 0, len(all))
	for _, p := range all {
		if refs != nil && !grantsTo(p, refs) {
			continue
		}
		if repo != nil && !covers(p, *repo) {
			continue
		}
		out = append(out, p)
	}
	return out, nil
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
		sel, err := parseSelector(text)
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
	for i := range ps {
		rp, problems, err := s.resolve(ctx, ps[i].PolicyInput)
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
}
