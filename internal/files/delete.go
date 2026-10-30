package files

import (
	"context"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/repository"
)

// DeleteRequest is a delete of a file or of a folder with its subtree.
type DeleteRequest struct {
	Repo string
	Path string
	// Strict is the /files form: a path outside the repository's patterns is a
	// missing path, and a disabled repository is refused (R-GENERIC-007). The
	// management API deletes such items, so an administrator can clean up.
	Strict bool
}

// Delete implements R-STOR-018: `delete` on the path, then on every item of the
// subtree, decided completely before anything is removed (403 and nothing changes
// otherwise). The item store removes a small subtree at once and queues the job
// `items-delete` for a larger one; both are its business ([items.Store.Delete]).
// The decision on every item of the subtree is the store's callback, so it sees
// the rows that are removed.
func (s *Service) Delete(ctx context.Context, c Caller, req DeleteRequest) (items.DeleteResult, error) {
	if req.Path == "" {
		return items.DeleteResult{}, errs.E(errs.Invalid, errs.CodeInvalidPath,
			"The repository root cannot be deleted as an item; delete the repository instead.")
	}
	dec, err := c.decide(ctx, req.Repo, req.Path, authz.KindWrite, authz.Delete)
	if err != nil {
		return items.DeleteResult{}, err
	}
	repo, err := s.resolve(ctx, req.Repo, req.Strict)
	if err != nil {
		return items.DeleteResult{}, err
	}
	if repo.Class == repository.Virtual {
		return items.DeleteResult{}, errs.E(errs.MethodNotAllowed, errs.CodeMethodNotAllowed,
			"A virtual repository holds no items; delete them in a member repository.", errs.WithAllow("GET", "HEAD"))
	}
	it, found, err := s.items.Live(ctx, items.RepoOf(repo), req.Path)
	if err != nil {
		return items.DeleteResult{}, err
	}
	if !found || (req.Strict && !repo.Admits(req.Path, it.Kind == items.KindFolder)) {
		return items.DeleteResult{}, notFound()
	}
	return s.items.Delete(ctx, items.DeleteSpec{
		Repo: repo, Path: req.Path, Actor: c.Actor, Source: c.Source, Endpoint: c.Subject.Endpoint,
		Check: func(ctx context.Context, paths []string) error {
			ok, err := c.allowedPaths(ctx, req.Repo, authz.Delete, paths)
			if err != nil {
				return err
			}
			for _, allowed := range ok {
				if !allowed {
					return errForbidden(authz.Delete, req.Repo, req.Path)
				}
			}
			return nil
		},
		Extra: c.recorder(req.Repo, req.Path, dec, authz.Delete),
	})
}
