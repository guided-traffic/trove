package files

import (
	"context"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
)

// DeleteRequest is a delete of a file or of a folder with its subtree.
type DeleteRequest struct {
	Repo string
	Path string
	// Strict is the form of /files and of the management API for every caller
	// without content:bypass: a path outside the repository's patterns is a missing
	// path, and a disabled repository is refused (R-GENERIC-007, R-REPO-005). Only
	// the administrator's cleanup through the management API deletes such items
	// (Q-AUTHZ-162).
	Strict bool
}

// Delete implements R-STOR-018: `delete` on the path, then on every item of the
// subtree, each on the coordinate its format gives it (R-EP-014), decided
// completely before anything is removed (403 and nothing changes otherwise). The
// format is then asked about the files the delete removes, in the transaction
// that removes them or queues their job: npm's package ownership (R-AUTHZ-018,
// Q-AUTHZ-161). The item store removes a small subtree at once and queues the job
// `items-delete` for a larger one; both are its business ([items.Store.Delete]).
// The decision on every item of the subtree is the store's callback, so it sees
// the rows that are removed.
func (s *Service) Delete(ctx context.Context, c Caller, req DeleteRequest) (items.DeleteResult, error) {
	if req.Path == "" {
		return items.DeleteResult{}, errs.E(errs.Invalid, errs.CodeInvalidPath,
			"The repository root cannot be deleted as an item; delete the repository instead.")
	}
	t := s.locate(ctx, req.Repo, req.Path, false)
	dec, err := c.decide(ctx, req.Repo, req.Path, t.class, authz.KindWrite, authz.Delete)
	if err != nil {
		return items.DeleteResult{}, err
	}
	repo, err := t.resolved(req.Strict)
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
	if !found || (req.Strict && !admits(repo, req.Path, it.Kind == items.KindFolder, t.class)) {
		return items.DeleteResult{}, notFound()
	}
	classify := s.classifier(repo)
	record := c.recorder(req.Repo, req.Path, t.class, dec, authz.Delete)
	return s.items.Delete(ctx, items.DeleteSpec{
		Repo: repo, Path: req.Path, Actor: c.Actor, Source: c.Source, Endpoint: c.Subject.Endpoint,
		Check: func(ctx context.Context, paths []string) error {
			ok, err := c.allowedPaths(ctx, req.Repo, authz.Delete, paths, classify)
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
		Extra: func(ctx context.Context, tx meta.Tx, files []string) error {
			if err := record(ctx, tx); err != nil {
				return err
			}
			return s.checkRemoval(ctx, tx, c, repo, files)
		},
	})
}
