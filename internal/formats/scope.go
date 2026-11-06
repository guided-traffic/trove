package formats

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/repository"
)

// repositoryScope is the set of repositories one request may name through the
// stores of the Host: the repository the request acts on and the members below it
// (Q-ARCH-260). It is immutable once built.
type repositoryScope struct{ ids []int64 }

type scopeKey struct{}

// withRepositories returns ctx carrying the repositories rq may name. A context that
// already carries a scope is returned as it is: ServeRoute may run on a derived
// request, and the scope of the outer one is the wider.
func withRepositories(ctx context.Context, rq *Request) context.Context {
	if rq == nil || rq.Repo == nil {
		return ctx
	}
	if _, ok := ctx.Value(scopeKey{}).(*repositoryScope); ok {
		return ctx
	}
	sc := &repositoryScope{}
	collect(sc, *rq.Repo, 0)
	return context.WithValue(ctx, scopeKey{}, sc)
}

// maxScopeDepth bounds the nesting of virtual repositories the scope follows; the
// repository model refuses cycles, the bound only keeps a defect finite.
const maxScopeDepth = 16

func collect(sc *repositoryScope, r repository.Repository, depth int) {
	id, err := strconv.ParseInt(r.Name, 10, 64)
	if err == nil && !slices.Contains(sc.ids, id) {
		sc.ids = append(sc.ids, id)
	}
	if depth >= maxScopeDepth {
		return
	}
	for _, m := range r.Members() {
		collect(sc, m, depth+1)
	}
}

// RepositoryAllowed is the check the stores and the package writer of a Host make on
// every repository a format names (Q-ARCH-260): a repository that is neither the
// request's nor a member below it is refused with an internal error, which no
// client can cause. A context that carries no scope - a hook, a job, a fill, which
// act on the repository of their event - is not checked here; the writer of such
// an event is bound to the event's repository instead.
func RepositoryAllowed(ctx context.Context, repo RepoRef) error {
	sc, ok := ctx.Value(scopeKey{}).(*repositoryScope)
	if !ok || slices.Contains(sc.ids, repo.ID) {
		return nil
	}
	return errs.E(errs.Internal, errs.CodeInternal, "A format named a repository that is not the request's or one of its members.",
		errs.WithCause(fmt.Errorf("repository %q (id %d) is outside the request's repositories", repo.Name, repo.ID)))
}
