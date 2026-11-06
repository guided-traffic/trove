package files

import (
	"context"
	"github.com/guided-traffic/trove/internal/qname"
	"io"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/bastionmain"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/vuln/enforce"
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
// coordinate the format gives the path (R-EP-014) is decided before the item is
// looked up, a path that the repository's patterns do not admit is a missing path
// (R-GENERIC-007, R-REPO-005), and a blob whose object is missing or damaged is
// refused (R-STOR-012). A folder is returned as a folder; the caller renders its
// index. The root of the repository is a folder.
func (s *Service) Stat(ctx context.Context, c Caller, repoKey qname.Repo, p repository.Cleaned) (Resolved, error) {
	kind := authz.KindArtifact
	if p.Folder {
		kind = authz.KindMetadata
	}
	t := s.locate(ctx, repoKey, p.Path, p.Folder)
	if _, err := c.decide(ctx, repoKey, p.Path, t.class, kind, authz.Read); err != nil {
		return Resolved{}, err
	}
	repo, err := t.resolved(true)
	if err != nil {
		return Resolved{}, err
	}
	if !admits(repo, p.Path, p.Folder, t.class) {
		return Resolved{}, notFound()
	}
	if p.Path == "" {
		return Resolved{Repo: repo, Item: items.Root(repoKey, repo.CreatedAt, repo.UpdatedAt), IsFolder: true}, nil
	}
	ref := items.RepoOf(repo)
	ref.Vuln = s.enforce.PolicyOf(repo) // the block of the version is read by the lookup itself (R-VULN-015)
	if s.formats != nil {
		ref.Facts = s.formats.ServeFactsKey(repo, p.Path) // and the rows the format's serve check decides on (R-ARCH-027)
	}
	it, found, err := s.items.Live(ctx, ref, p.Path)
	if err != nil {
		return Resolved{}, err
	}
	if !found || (p.Folder && it.Kind != items.KindFolder) {
		if !found && repo.Class == repository.Local {
			// A local repository that holds nothing at the path holds nothing for any subject (R-BAS-013).
			bastionmain.NoteMissing(ctx)
		}
		return Resolved{}, notFound()
	}
	if it.Kind == items.KindFolder {
		return Resolved{Repo: repo, Item: it, IsFolder: true}, nil
	}
	// A blob marked missing is looked for again once per interval, and served when its object is back (Q-STOR-153).
	if it, err = s.items.Usable(ctx, it); err != nil {
		return Resolved{}, err
	}
	// A format keeps a say about its own items on this raw view too (R-ARCH-008
	// "Serve-time check"): npm's tombstoned tarball is a 404 here as on its route.
	if s.formats != nil {
		if err := s.formats.ServeCheck(ctx, repo, it); err != nil {
			return Resolved{}, err
		}
	}
	// The block of the vulnerability scanning comes last: after authorization and the format's own check, so that a
	// version the caller may not read stays a 404 and a tombstoned one the format's (R-VULN-015).
	if err := s.enforce.Refuse(ctx, enforce.Download{
		Addressed: repoKey.String(), Store: repo.Name.String(), RepoID: repo.ID, Format: repo.Format, Path: it.Path, VersionID: it.VersionID, Stored: true,
		Block: s.enforce.Decide(it.Policy, it.Block, it.VersionID), Actor: c.Actor, Source: c.Source, Endpoint: c.Subject.Endpoint,
		Ecosystem: it.Policy.Ecosystem,
	}); err != nil {
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
