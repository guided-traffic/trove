package files

import (
	"context"
	"encoding/hex"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/repository"
)

// ItemInfo implements R-STOR-016: the description of a file or folder. An empty
// path is the root of the repository. The management API reads a repository that
// is disabled as well; /files does not (Stat).
func (s *Service) ItemInfo(ctx context.Context, c Caller, repoKey, path string) (Item, error) {
	if _, err := s.decide(ctx, c, repoKey, path, authz.KindMetadata, authz.Read); err != nil {
		return Item{}, err
	}
	repo, err := s.resolve(ctx, repoKey, false)
	if err != nil {
		return Item{}, err
	}
	if path == "" {
		return rootItem(repoKey, repo), nil
	}
	it, found, err := s.liveItem(ctx, repoKey, repoID(repo), path)
	if err != nil {
		return Item{}, err
	}
	if !found {
		return Item{}, notFound()
	}
	return it, nil
}

// StatsInfo is the download statistics of a file (R-STOR-015).
type StatsInfo struct {
	Downloads      int64
	LastDownloadAt *time.Time
	LastDownloader string
}

// Stats implements GET /repositories/{repoKey}/stats: the counts of PostgreSQL,
// never the buffer of a replica (R-ARCH-032). Only a file has statistics.
func (s *Service) Stats(ctx context.Context, c Caller, repoKey, path string) (StatsInfo, error) {
	it, err := s.ItemInfo(ctx, c, repoKey, path)
	if err != nil {
		return StatsInfo{}, err
	}
	if it.Kind != KindFile {
		return StatsInfo{}, notFound()
	}
	var out StatsInfo
	err = s.read(ctx, func(qs *q.Queries) error {
		rows, err := qs.GetItemStats(ctx, it.ID)
		if err != nil || len(rows) == 0 {
			return err
		}
		out.Downloads, out.LastDownloader = rows[0].Downloads, rows[0].LastDownloaderName
		if rows[0].LastDownloadAt.Valid {
			t := rows[0].LastDownloadAt.Time.UTC()
			out.LastDownloadAt = &t
		}
		return nil
	})
	return out, err
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

// Child is an entry of a listing. Path is relative to the listed folder.
type Child struct {
	Path       string     `json:"path"`
	Kind       string     `json:"kind"`
	Size       *int64     `json:"size,omitempty"`
	ModifiedAt *time.Time `json:"modifiedAt,omitempty"`
	SHA256     string     `json:"sha256,omitempty"`
	SHA1       string     `json:"sha1,omitempty"`

	// abs is the path in the repository, the key of the cursor.
	abs string
}

// ChildrenPage is one page of a listing.
type ChildrenPage struct {
	Items []Child
	// Next is the cursor of the next page, "" on the last page: the path of the
	// last row the page scanned, which may lie beyond its last visible entry when
	// the caller may not read the rows in between.
	Next string
}

// scanLimit bounds the rows one page reads while entries the caller may not read
// are skipped, so a listing of a huge folder of which the caller sees little
// answers a short page with a cursor instead of scanning it all.
const scanLimit = 100_000

// Children implements R-STOR-017: the untrashed children of a folder in byte
// order of their paths, with a cursor, `read` decided per entry and unreadable
// entries omitted (R-AUTHZ-016). The folder itself needs `read`, and a missing or
// hidden folder is the same 404.
func (s *Service) Children(ctx context.Context, c Caller, repoKey string, cq ChildrenQuery) (ChildrenPage, error) {
	if _, err := s.decide(ctx, c, repoKey, cq.Path, authz.KindMetadata, authz.Read); err != nil {
		return ChildrenPage{}, err
	}
	repo, err := s.resolve(ctx, repoKey, cq.Strict)
	if err != nil {
		return ChildrenPage{}, err
	}
	rid := repoID(repo)
	if cq.Path != "" {
		if cq.Strict && !repo.Admits(cq.Path, true) {
			return ChildrenPage{}, notFound()
		}
		it, found, err := s.liveItem(ctx, repoKey, rid, cq.Path)
		if err != nil {
			return ChildrenPage{}, err
		}
		if !found || it.Kind != KindFolder {
			return ChildrenPage{}, notFound()
		}
	}
	prefix := ""
	if cq.Path != "" {
		prefix = cq.Path + "/"
	}
	var (
		page    ChildrenPage
		cursor  = cq.After
		scanned int
		want    = cq.Limit + 1
	)
	chunk := int32(min(want, 5000)) //nolint:gosec // bounded by the limit of the caller, itself bounded by the API
	for len(page.Items) < want && scanned < scanLimit {
		rows, err := s.childRows(ctx, rid, cq, cursor, chunk)
		if err != nil {
			return ChildrenPage{}, err
		}
		if len(rows) == 0 {
			break
		}
		scanned += len(rows)
		cursor = rows[len(rows)-1].abs
		visible, err := s.visibleRows(ctx, c, repo, repoKey, cq, rows)
		if err != nil {
			return ChildrenPage{}, err
		}
		for _, r := range visible {
			r.Path = strings.TrimPrefix(r.abs, prefix)
			page.Items = append(page.Items, r)
			if len(page.Items) == want {
				break
			}
		}
		if int32(len(rows)) < chunk { //nolint:gosec // bounded above
			cursor = ""
			break
		}
		if len(page.Items) == want {
			break
		}
	}
	switch {
	case len(page.Items) > cq.Limit:
		page.Items = page.Items[:cq.Limit]
		page.Next = page.Items[len(page.Items)-1].abs
	case cursor != "" && scanned >= scanLimit:
		page.Next = cursor
	}
	if page.Items == nil {
		page.Items = []Child{}
	}
	return page, nil
}

func (s *Service) childRows(ctx context.Context, rid int64, cq ChildrenQuery, after string, limit int32) ([]Child, error) {
	var out []Child
	err := s.read(ctx, func(qs *q.Queries) error {
		out = out[:0]
		if !cq.Recursive {
			rows, err := qs.ListChildrenDirect(ctx, q.ListChildrenDirectParams{
				RepoID: rid, Path: cq.Path, After: after, Folders: cq.Folders, RowLimit: limit,
			})
			for _, r := range rows {
				out = append(out, childOf(r.Path, r.Kind, r.Size.Int64, r.Size.Valid, r.ModifiedAt.Time, r.BlobSha256, r.BlobSha1))
			}
			return err
		}
		lo, hi := subtreeBounds(cq.Path)
		slashes := int32(-1)
		if cq.MaxDepth > 0 {
			// An entry D levels below a folder with k slashes has k+D slashes; below the
			// root, D-1.
			slashes = int32(cq.MaxDepth) - 1 //nolint:gosec // bounded by the API
			if cq.Path != "" {
				slashes = int32(strings.Count(cq.Path, "/")) + int32(cq.MaxDepth) //nolint:gosec // a path has at most 4096 bytes
			}
		}
		rows, err := qs.ListChildrenRecursive(ctx, q.ListChildrenRecursiveParams{
			RepoID: rid, Lo: lo, Hi: hi, After: after, Folders: cq.Folders, MaxSlashes: slashes, RowLimit: limit,
		})
		for _, r := range rows {
			out = append(out, childOf(r.Path, r.Kind, r.Size.Int64, r.Size.Valid, r.ModifiedAt.Time, r.BlobSha256, r.BlobSha1))
		}
		return err
	})
	return out, err
}

func childOf(path, kind string, size int64, hasSize bool, modified time.Time, sha256, sha1 []byte) Child {
	c := Child{abs: path, Kind: kind}
	if kind == KindFile && hasSize {
		m := modified.UTC()
		c.Size, c.ModifiedAt = &size, &m
		c.SHA256, c.SHA1 = hex.EncodeToString(sha256), hex.EncodeToString(sha1)
	} else {
		m := modified.UTC()
		c.ModifiedAt = &m
	}
	return c
}

// visibleRows keeps the entries the caller may read, and for /files those the
// repository's patterns admit.
func (s *Service) visibleRows(ctx context.Context, c Caller, repo repository.Repository, repoKey string, cq ChildrenQuery, rows []Child) ([]Child, error) {
	paths := make([]string, len(rows))
	for i, r := range rows {
		paths[i] = r.abs
	}
	allowed, err := s.allowedPaths(ctx, c, repoKey, authz.Read, paths)
	if err != nil {
		return nil, err
	}
	out := rows[:0:0]
	for i, r := range rows {
		if !allowed[i] || (cq.Strict && !repo.Admits(r.abs, r.Kind == KindFolder)) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// allowedPaths decides one action for many paths of a repository: through the
// batch form of the decision point when it has one, else one Decide per path.
func (s *Service) allowedPaths(ctx context.Context, c Caller, repoKey string, a authz.Action, paths []string) ([]bool, error) {
	kind := authz.KindMetadata
	if a != authz.Read {
		kind = authz.KindWrite
	}
	if s.paths != nil {
		return s.paths.PathsAllowed(ctx, c.Subject, authz.Coordinate{Repository: repoKey, Endpoint: c.Endpoint, Kind: kind}, a, paths)
	}
	out := make([]bool, len(paths))
	for i, p := range paths {
		d := s.authz.Decide(ctx, c.Subject, s.coordinate(c, repoKey, p, kind, a))
		switch d.Outcome {
		case authz.Allow:
			out[i] = true
		case authz.DenyStale, authz.DenyEpochUnknown, authz.DenyUnauthenticated:
			return nil, d.Err()
		}
	}
	return out, nil
}

// errForbidden is the 403 of an action the caller does not hold on a path.
func errForbidden(a authz.Action, repo, path string) error {
	target := repo
	if path != "" {
		target += "/" + path
	}
	return errs.E(errs.Forbidden, errs.CodeForbidden, "The action "+string(a)+" is not permitted on "+target+".")
}
