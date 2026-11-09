package files

import (
	"context"
	"encoding/json"
	"time"

	"github.com/guided-traffic/trove/internal/qname"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
)

// DeleteRequest is a delete of a file or of a folder with its subtree.
type DeleteRequest struct {
	Repo qname.Repo
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
	if err := s.refuseWrite(ctx); err != nil {
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
	subject, err := json.Marshal(c.Access.JobSubject())
	if err != nil {
		return items.DeleteResult{}, err
	}
	classify := s.classifier(repo)
	record := c.recorder(req.Repo, req.Path, t.class, dec, authz.Delete)
	// What a storage hook cascades to is held to the grants of the caller as the subtree is (the cascade of an OCI manifest takes its tags).
	ctx = items.WithCascadeGuard(ctx, func(ctx context.Context, paths []string) error {
		ok, err := c.allowedPaths(ctx, req.Repo, authz.Delete, paths, classify)
		if err != nil {
			return err
		}
		for i, allowed := range ok {
			if !allowed {
				return errForbidden(authz.Delete, req.Repo, paths[i])
			}
		}
		return nil
	})
	return s.items.Delete(ctx, items.DeleteSpec{
		Repo: repo, Path: req.Path, Actor: c.Actor, Source: c.Source, Endpoint: c.Subject.Endpoint, Subject: subject,
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
			return s.checkRemoval(ctx, tx, repo, files, c.Access.Index(), c.Subject)
		},
	})
}

// JobDecider is the decision point as a batch of a delete job uses it: an index taken once per batch, and every question of the batch
// asked of it. *authz.Engine implements it.
type JobDecider interface {
	Begin(ctx context.Context, sub authz.Subject, names ...qname.Repo) (*authz.Index, error)
	PathsAllowedOn(ctx context.Context, idx *authz.Index, sub authz.Subject, base authz.Coordinate, a authz.Action, paths []string, classify authz.Classifier) ([]bool, error)
	HoldsOn(idx *authz.Index, sub authz.Subject, permission string) bool
	RepositoryIDOn(idx *authz.Index, name qname.Repo) (int64, bool)
}

// Standings reads what the principal and the credential of a job's subject hold now (Q-AUTHZ-260): the kind, the name, the groups, the
// token's scope and a CI token's bundle. It refuses with an error of kind Unauthenticated when the principal is no longer active or
// the credential was withdrawn (Q-AUTHZ-261, Q-AUTHZ-262). The composition root binds the authentication layer's reader.
type Standings interface {
	Standing(ctx context.Context, j authz.JobSubject) (authz.Standing, error)
}

// DeleteJobGuard returns the guard of the delete job (Q-STOR-190, Q-STOR-191): every batch reads the standing of the subject that
// asked, finds the job's repository by its ID - a moved repository is decided where it is now, one that is gone or being deleted ends
// the job as done ([items.ErrRepositoryGone]) - takes the index installed now, and decides on that subject `delete` on the batch's own
// items, the format's question about its files (the package ownership of R-AUTHZ-018) and every path a storage hook cascades to. A
// disabled repository stops the job unless the subject holds content:bypass, as a request's delete does (Q-AUTHZ-162). A refusal
// fails the job with a 403 that names the path the request named, never the refused item; a withdrawn principal or credential with
// the 401 the request would get now.
func (s *Service) DeleteJobGuard(d JobDecider, st Standings) items.JobGuard {
	return func(ctx context.Context, a items.DeleteArgs) (items.BatchGuard, error) {
		var j authz.JobSubject
		if err := json.Unmarshal(a.Subject, &j); err != nil {
			return items.BatchGuard{}, errs.E(errs.Forbidden, errs.CodeForbidden, "The delete job carries no subject that can be decided for.", errs.WithCause(err))
		}
		repo, err := s.repos.ByID(ctx, a.RepoID)
		switch {
		case errs.KindOf(err) == errs.NotFound:
			return items.BatchGuard{}, items.ErrRepositoryGone
		case err != nil:
			return items.BatchGuard{}, err
		case repo.Lifecycle == repository.Deleting:
			return items.BatchGuard{}, items.ErrRepositoryGone
		}
		now, err := st.Standing(ctx, j)
		if err != nil {
			return items.BatchGuard{}, err
		}
		sub := j.Subject(now)
		idx, err := d.Begin(ctx, sub, repo.Name)
		if err != nil {
			return items.BatchGuard{}, err
		}
		if id, ok := d.RepositoryIDOn(idx, repo.Name); !ok || id != a.RepoID {
			// The repository snapshot and the index follow the change log each on its own; for a moment after a move one can name
			// another repository than the other. The batch waits for the next attempt rather than decide on that one.
			return items.BatchGuard{}, errs.E(errs.Unavailable, errs.CodeUnavailable,
				"The authorization index does not hold the job's repository under its current name yet; the batch is tried again.",
				errs.WithRetryAfter(time.Second))
		}
		if repo.Disabled && !d.HoldsOn(idx, sub, string(authz.ContentBypass)) {
			return items.BatchGuard{}, errs.E(errs.Forbidden, errs.CodeForbidden,
				"The repository "+repo.Name.String()+" is disabled, and the subject that asked for this delete does not hold content:bypass; the job stopped.")
		}
		classify := s.classifier(repo)
		return items.BatchGuard{
			Name: repo.Name,
			Check: func(ctx context.Context, paths []string) error {
				ok, err := d.PathsAllowedOn(ctx, idx, sub, authz.Coordinate{Repository: repo.Name, Kind: authz.KindWrite}, authz.Delete, paths, classify)
				if err != nil {
					return err
				}
				for _, allowed := range ok {
					if !allowed {
						// The path the request named, as the request's own check names it: the refused item may be one the subject
						// cannot read, and the job's error is read by others than the subject (R-AUTHZ-016).
						return errForbidden(authz.Delete, repo.Name, a.Path)
					}
				}
				return nil
			},
			Removal: func(ctx context.Context, tx meta.Tx, files []string) error {
				return s.checkRemoval(ctx, tx, repo, files, idx, sub)
			},
		}, nil
	}
}
