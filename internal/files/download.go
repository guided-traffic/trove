package files

import (
	"context"
	"io"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/repository"
)

// Resolved is the answer of Stat: what the request path resolved to, from
// PostgreSQL alone (R-STOR-012: HEAD and conditional requests never reach the
// object store).
type Resolved struct {
	Repo repository.Repository
	// Item is the file, or the folder when IsFolder.
	Item     items.Item
	IsFolder bool
}

// Stat resolves a GET or HEAD of /files/ (R-ARCH-011 steps 4 to 6): `read` on the
// path is decided before the repository or the item is looked up, a path that
// the repository's patterns do not admit is a missing path (R-GENERIC-007), and a
// blob whose object is missing or damaged is refused (R-STOR-012). A folder is
// returned as a folder; the caller renders its index. The root of the repository
// is a folder.
func (s *Service) Stat(ctx context.Context, c Caller, repoKey string, p repository.Cleaned) (Resolved, error) {
	kind := authz.KindArtifact
	if p.Folder {
		kind = authz.KindMetadata
	}
	if _, err := c.decide(ctx, repoKey, p.Path, kind, authz.Read); err != nil {
		return Resolved{}, err
	}
	repo, err := s.resolve(ctx, repoKey, true)
	if err != nil {
		return Resolved{}, err
	}
	if !repo.Admits(p.Path, p.Folder) {
		return Resolved{}, notFound()
	}
	if p.Path == "" {
		return Resolved{Repo: repo, Item: items.Root(repoKey, repo.CreatedAt, repo.UpdatedAt), IsFolder: true}, nil
	}
	it, found, err := s.items.Live(ctx, items.RepoOf(repo), p.Path)
	if err != nil {
		return Resolved{}, err
	}
	if !found || (p.Folder && it.Kind != items.KindFolder) {
		return Resolved{}, notFound()
	}
	if it.Kind == items.KindFolder {
		return Resolved{Repo: repo, Item: it, IsFolder: true}, nil
	}
	if err := items.ContentError(it); err != nil {
		return Resolved{}, err
	}
	return Resolved{Repo: repo, Item: it}, nil
}

// Open opens the content of a resolved file for streaming ([items.Store.Open]).
func (s *Service) Open(ctx context.Context, it items.Item, rng *items.Range) (io.ReadCloser, error) {
	return s.items.Open(ctx, it, rng)
}

// Served records a download that was answered with its content ([items.Store.Served]).
func (s *Service) Served(ctx context.Context, c Caller, res Resolved, countStats bool) {
	s.items.Served(ctx, items.ServedEvent{
		Item: res.Item, Actor: c.Actor, Source: c.Source, Endpoint: c.Subject.Endpoint, CountStats: countStats,
	})
}
