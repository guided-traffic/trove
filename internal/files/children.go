package files

import (
	"context"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/repository"
)

// ItemInfo implements R-STOR-016: the description of a file or folder, decided on
// the coordinate the format gives the path (R-EP-014). An empty path is the root
// of the repository. The management API reads a repository that is disabled as
// well; /files does not (Stat).
func (s *Service) ItemInfo(ctx context.Context, c Caller, repoKey, path string) (items.Item, error) {
	t := s.locate(ctx, repoKey, path, false)
	if _, err := c.decide(ctx, repoKey, path, t.class, authz.KindMetadata, authz.Read); err != nil {
		return items.Item{}, err
	}
	repo, err := t.resolved(false)
	if err != nil {
		return items.Item{}, err
	}
	if path == "" {
		return items.Root(repoKey, repo.CreatedAt, repo.UpdatedAt), nil
	}
	it, found, err := s.items.Live(ctx, items.RepoOf(repo), path)
	if err != nil {
		return items.Item{}, err
	}
	if !found {
		return items.Item{}, notFound()
	}
	return it, nil
}

// Stats implements GET /repositories/{repoKey}/stats: the counts of PostgreSQL,
// never the buffer of a replica (R-ARCH-032). Only a file has statistics.
func (s *Service) Stats(ctx context.Context, c Caller, repoKey, path string) (items.DownloadStats, error) {
	it, err := s.ItemInfo(ctx, c, repoKey, path)
	if err != nil {
		return items.DownloadStats{}, err
	}
	if it.Kind != items.KindFile {
		return items.DownloadStats{}, notFound()
	}
	return s.items.DownloadStats(ctx, it.ID)
}

// ChildrenQuery selects a page of the children of a folder (R-STOR-017).
type ChildrenQuery struct {
	// Path is the canonical path of the folder; "" is the repository root.
	Path      string
	Recursive bool
	// MaxDepth bounds a recursive listing: 1 is the children, 2 their children,
	// and so on; 0 is no bound.
	MaxDepth int
	// Folders includes folder entries.
	Folders bool
	// After is the cursor: the path of the last entry the previous page scanned.
	After string
	// Limit is the page size.
	Limit int
	// Strict applies the include and exclude patterns of the repository and
	// refuses a disabled repository, as /files does (R-GENERIC-007).
	Strict bool
}

// Children implements R-STOR-017: the untrashed children of a folder in byte
// order of their paths, with a cursor, `read` decided per entry on the coordinate
// the format gives it (R-EP-014) and unreadable entries omitted (R-AUTHZ-016).
// The folder itself needs `read`, and a missing or hidden folder is the same 404.
func (s *Service) Children(ctx context.Context, c Caller, repoKey string, cq ChildrenQuery) (items.ChildrenPage, error) {
	t := s.locate(ctx, repoKey, cq.Path, true)
	if _, err := c.decide(ctx, repoKey, cq.Path, t.class, authz.KindMetadata, authz.Read); err != nil {
		return items.ChildrenPage{}, err
	}
	repo, err := t.resolved(cq.Strict)
	if err != nil {
		return items.ChildrenPage{}, err
	}
	ref := items.RepoOf(repo)
	if cq.Path != "" {
		if cq.Strict && !admits(repo, cq.Path, true, t.class) {
			return items.ChildrenPage{}, notFound()
		}
		it, found, err := s.items.Live(ctx, ref, cq.Path)
		if err != nil {
			return items.ChildrenPage{}, err
		}
		if !found || it.Kind != items.KindFolder {
			return items.ChildrenPage{}, notFound()
		}
	}
	return s.items.Children(ctx, ref, items.ChildrenQuery{
		Path: cq.Path, Recursive: cq.Recursive, MaxDepth: cq.MaxDepth, Folders: cq.Folders, After: cq.After, Limit: cq.Limit,
	}, func(ctx context.Context, rows []items.Child) ([]bool, error) {
		return s.visibleRows(ctx, c, repo, repoKey, cq.Strict, rows)
	})
}

// visibleRows says which entries the caller may read, each on the coordinate its
// format gives it, and for /files which the repository's patterns admit.
func (s *Service) visibleRows(ctx context.Context, c Caller, repo repository.Repository, repoKey string, strict bool, rows []items.Child) ([]bool, error) {
	paths := make([]string, len(rows))
	classes := make(map[string]PathClass, len(rows))
	for i, r := range rows {
		paths[i] = r.Abs
		classes[r.Abs] = s.classify(repo, r.Abs, r.Kind == items.KindFolder)
	}
	allowed, err := c.allowedPaths(ctx, repoKey, authz.Read, paths, func(p string) (string, string) {
		pc := classes[p]
		return pc.Package, pc.Version
	})
	if err != nil {
		return nil, err
	}
	out := make([]bool, len(rows))
	for i, r := range rows {
		out[i] = allowed[i] && (!strict || admits(repo, r.Abs, r.Kind == items.KindFolder, classes[r.Abs]))
	}
	return out, nil
}

// allowedPaths decides one action for many paths of a repository on the
// request's index, each path on the coordinate classify gives it.
func (c Caller) allowedPaths(ctx context.Context, repoKey string, a authz.Action, paths []string, classify authz.Classifier) ([]bool, error) {
	kind := authz.KindMetadata
	if a != authz.Read {
		kind = authz.KindWrite
	}
	return c.Access.PathsAllowed(ctx, authz.Coordinate{Repository: repoKey, Kind: kind}, a, paths, classify)
}
