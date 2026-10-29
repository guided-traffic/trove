package files

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/repository"
)

const retryAfter = 2 * time.Second

// Resolved is the answer of Stat: what the request path resolved to, from
// PostgreSQL alone (R-STOR-012: HEAD and conditional requests never reach the
// object store).
type Resolved struct {
	Repo repository.Repository
	// Item is the file, or the folder when IsFolder.
	Item     Item
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
	if _, err := s.decide(ctx, c, repoKey, p.Path, kind, authz.Read); err != nil {
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
		return Resolved{Repo: repo, Item: rootItem(repoKey, repo), IsFolder: true}, nil
	}
	it, found, err := s.liveItem(ctx, repoKey, repoID(repo), p.Path)
	if err != nil {
		return Resolved{}, err
	}
	if !found || (p.Folder && it.Kind != KindFolder) {
		return Resolved{}, notFound()
	}
	if it.Kind == KindFolder {
		return Resolved{Repo: repo, Item: it, IsFolder: true}, nil
	}
	if err := contentStatus(it); err != nil {
		return Resolved{}, err
	}
	return Resolved{Repo: repo, Item: it}, nil
}

// rootItem is the synthetic folder of the repository root.
func rootItem(key string, repo repository.Repository) Item {
	return Item{
		Repository: key, Kind: KindFolder, Explicit: true,
		CreatedAt: repo.CreatedAt.UTC(), ModifiedAt: repo.UpdatedAt.UTC(), UpdatedAt: repo.UpdatedAt.UTC(),
	}
}

// contentStatus refuses a file whose blob is not ok: 503 with Retry-After for a
// missing object, which can come back through a restore, and 500 for a corrupt
// one (R-STOR-012).
func contentStatus(it Item) error {
	switch it.ContentStatus {
	case "", "ok":
		return nil
	case "missing":
		return errs.E(errs.Unavailable, errs.CodeContentUnavailable, "The stored object of this file is missing; retry later.", errs.WithRetryAfter(30*time.Second))
	}
	return errs.E(errs.Internal, errs.CodeContentUnavailable, "The stored object of this file failed verification.")
}

// Range is a byte range of a download; Length < 0 reads to the end.
type Range struct{ Offset, Length int64 }

// Open opens the content of a resolved file for streaming: the whole file through
// the verifying reader of R-STOR-013, or the byte range as stored (ranged reads
// cannot be verified, H-11). An object that is missing is 503 content_unavailable.
func (s *Service) Open(ctx context.Context, it Item, rng *Range) (io.ReadCloser, error) {
	var o blob.OpenOptions
	if rng != nil {
		o.Range = &blob.ByteRange{Offset: rng.Offset, Length: rng.Length}
	}
	body, err := s.blobs.Open(ctx, it.Digest, it.Size, o)
	switch {
	case err == nil:
		return body, nil
	case errors.Is(err, blob.ErrNotFound):
		return nil, errs.E(errs.Unavailable, errs.CodeContentUnavailable, "The stored object of this file is missing; retry later.",
			errs.WithRetryAfter(30*time.Second), errs.WithCause(err))
	case errors.Is(err, blob.ErrCorrupt):
		return nil, errs.E(errs.Internal, errs.CodeContentUnavailable, "The stored object of this file failed verification.", errs.WithCause(err))
	}
	return nil, err
}

// Served records a download that was answered with its content: 200 with the
// whole file, or 206 for a range that starts at byte 0 (R-STOR-015). HEAD, 304
// and `?stats=false` are not counted. It counts the download statistics, the
// repository activity and, with audit.record_downloads, the asynchronous event
// (R-OPS-015). It never blocks and never fails the request.
func (s *Service) Served(ctx context.Context, c Caller, res Resolved, countStats bool) {
	if s.activity != nil {
		s.activity.Touch(res.Repo.ID)
	}
	if countStats && s.stats != nil {
		s.stats.Add(res.Item.ID, c.actorID())
	}
	if s.download {
		raw, _ := json.Marshal(map[string]any{"path": res.Item.Path, "sha256": res.Item.Digest.Hex()})
		s.rec.Record(ctx, audit.Event{
			Type: audit.EventArtifactDownloaded, Actor: c.Actor, Outcome: audit.Success, Source: c.Source, Endpoint: c.Endpoint,
			Subject: audit.Subject{Kind: "repository", Name: res.Item.Repository}, Repository: res.Item.Repository,
			Path: res.Item.Path, Details: raw,
		})
	}
}
