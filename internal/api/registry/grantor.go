package registry

import (
	"context"

	"github.com/guided-traffic/trove/internal/auth/registrytoken"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
)

// ActionOf maps the action of a route's coarse check (R-ARCH-008) onto the token action that must be in a registry token's
// grant (R-OCI-006): read is pull, deploy and overwrite are push (push covers overwrite as a ceiling; the decision point still
// requires overwrite for a moved tag), delete is delete. ok is false for an action no registry route needs: the enforcement
// point refuses such a route for a registry token (fail closed, Q-OCI-56).
func ActionOf(a authz.Action) (registrytoken.Actions, bool) {
	switch a {
	case authz.Read:
		return registrytoken.Pull, true
	case authz.Deploy, authz.Overwrite:
		return registrytoken.Push, true
	case authz.Delete:
		return registrytoken.Delete, true
	}
	return 0, false
}

// authzActions are the actions of the decision point the coarse check asks for each token action (R-OCI-006).
func authzActions(a registrytoken.Actions) []authz.Action {
	var out []authz.Action
	if a&registrytoken.Pull != 0 {
		out = append(out, authz.Read)
	}
	if a&registrytoken.Push != 0 {
		out = append(out, authz.Deploy)
	}
	if a&registrytoken.Delete != 0 {
		out = append(out, authz.Delete)
	}
	return out
}

// tokenAction is the inverse of authzActions for one action.
func tokenAction(a authz.Action) registrytoken.Actions {
	switch a {
	case authz.Read:
		return registrytoken.Pull
	case authz.Deploy:
		return registrytoken.Push
	case authz.Delete:
		return registrytoken.Delete
	}
	return 0
}

// Asker is the question the grant decision asks the decision point on the index of the token request. *edge.Access
// implements it. PathsAllowed decides like the coarse check of a request - endpoint ceiling, grants, token scope, the
// anonymous switch - and writes no audit event: a grant that leaves an entry out is no refusal of an operation
// (R-AUTHZ-029), and the request that later needs the action is decided and audited on its own (R-OCI-005, Q-OCI-55).
type Asker interface {
	PathsAllowed(ctx context.Context, base authz.Coordinate, act authz.Action, paths []string, classify authz.Classifier) ([]bool, error)
}

// Grantor decides what a token request is granted (R-OCI-006, Q-OCI-6).
type Grantor struct {
	// Repos looks a repository up after the decision point allowed an action on it, to keep only repositories of the format
	// that are served (R-AUTHZ-016: authorization runs before the existence lookup).
	Repos repository.Reader
	// Format is the ID of the format whose repositories the registry serves ("oci").
	Format string
	// Documents gives the path a name is decided on: its package document (Q-OCI-140).
	Documents Documents
}

// Grant runs the coarse check of the route kind for each scope entry (R-OCI-006): pull asks read, push asks deploy,
// delete asks delete, each on the path of the image name's package document (Q-OCI-140), so a grant scoped to a package or path
// pattern covers exactly the names it matches. A repository that does not exist, is not of the format, is disabled or being deleted, or is hidden
// from the caller grants nothing and is not reported (Q-OCI-6). The catalog is granted to every caller that reached the
// token service, the anonymous one included where the endpoint lets it in (R-OCI-006); what the catalog lists is decided
// per name (R-OCI-017).
//
// The error is a refusal to decide (503 authz_stale) or a failed lookup; a caller the decision point will not answer for
// (anonymous access switched off since the stage let the request in) gets the empty grant.
func (g *Grantor) Grant(ctx context.Context, ask Asker, scopes []registrytoken.Scope) (registrytoken.Grant, error) {
	var out registrytoken.Grant
	served := map[qname.Repo]bool{}
	for _, s := range scopes {
		if s.Catalog {
			out.Catalog = true
			continue
		}
		var allowed registrytoken.Actions
		doc, named := g.Documents.Path(s.Name)
		if !named {
			continue
		}
		for _, act := range authzActions(s.Actions) {
			ok, err := ask.PathsAllowed(ctx, authz.Coordinate{Repository: s.Repo}, act, []string{doc}, g.Documents.Classify)
			if err != nil {
				if e, isErr := errs.As(err); isErr && e.Kind == errs.Unauthenticated {
					return registrytoken.Grant{}, nil
				}
				return registrytoken.Grant{}, err
			}
			if len(ok) == 1 && ok[0] {
				allowed |= tokenAction(act)
			}
		}
		if allowed == 0 {
			continue
		}
		ok, known := served[s.Repo]
		if !known {
			var err error
			if ok, err = g.serves(ctx, s.Repo); err != nil {
				return registrytoken.Grant{}, err
			}
			served[s.Repo] = ok
		}
		if ok {
			out.Add(registrytoken.Entry{Repo: s.Repo, Name: s.Name, Actions: allowed})
		}
	}
	return out, nil
}

// serves reports whether the repository exists, is of the format and is served: the cases in which the dispatcher would
// not answer 404 for it.
func (g *Grantor) serves(ctx context.Context, name qname.Repo) (bool, error) {
	repo, err := g.Repos.ByName(ctx, name)
	if err != nil {
		if e, ok := errs.As(err); ok && e.Kind == errs.NotFound {
			return false, nil
		}
		return false, err
	}
	return repo.Format == g.Format && repo.Lifecycle != repository.Deleting && !repo.Disabled, nil
}
