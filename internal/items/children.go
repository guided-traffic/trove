package items

import (
	"context"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

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
}

// Child is an entry of a listing. Path is relative to the listed folder.
type Child struct {
	Path       string     `json:"path"`
	Kind       string     `json:"kind"`
	Size       *int64     `json:"size,omitempty"`
	ModifiedAt *time.Time `json:"modifiedAt,omitempty"`
	SHA256     string     `json:"sha256,omitempty"`
	SHA1       string     `json:"sha1,omitempty"`

	// Abs is the path in the repository, the key of the cursor and the path a
	// [Visibility] decides on.
	Abs string `json:"-"`
	// ID is the item's id, the position of a cursor that must not name the entry.
	ID int64 `json:"-"`
}

// ChildrenPage is one page of a listing.
type ChildrenPage struct {
	Items []Child
	// Next is the cursor of the next page, "" on the last page: the path of the
	// last entry of the page, or, when the page ended at the scan limit beyond
	// its last visible entry, the position of the last row it scanned in the
	// form of [hiddenPosition], which does not name that row.
	Next string
}

// Visibility says which of the rows a page scanned the caller may see: one bool
// per row, in order. The store reads rows in batches and asks for each batch; it
// knows nothing of why a row is hidden (a grant, the repository's patterns).
// An error stops the listing.
type Visibility func(ctx context.Context, rows []Child) ([]bool, error)

// scanLimit bounds the rows one page reads while entries the caller may not read
// are skipped, so a listing of a huge folder of which the caller sees little
// answers a short page with a cursor instead of scanning it all.
const scanLimit = 100_000

// maxChunk is the most rows one query of a listing reads. A query reads one
// more row than the page holds and doubles its size after each query that
// left the page short, so a page costs a few dozen queries at most whatever its
// size: 2 + 4 + … + 4096 rows, then the rest of scanLimit in chunks of maxChunk.
const maxChunk = 5000

// hiddenPosition marks a cursor that holds the id of the last row a page
// scanned instead of its path. The row may be one the caller may not read, and
// the client can decode a cursor, so it must not carry the name (R-AUTHZ-016).
// A canonical path never holds a control character, so the mark is no path.
const hiddenPosition = "\x00"

// Children lists the untrashed children of a folder in byte order of their
// paths, with a cursor (R-STOR-017). Rows for which visible says false are
// omitted. The caller has checked that the folder exists and may be read.
func (s *Store) Children(ctx context.Context, repo RepoRef, cq ChildrenQuery, visible Visibility) (ChildrenPage, error) {
	prefix := ""
	if cq.Path != "" {
		prefix = cq.Path + "/"
	}
	after, err := s.cursorPath(ctx, repo.ID, prefix, cq.After)
	if err != nil {
		return ChildrenPage{}, err
	}
	var (
		page    ChildrenPage
		cursor  = after
		lastID  int64
		scanned int
		want    = cq.Limit + 1
	)
	chunk := int32(min(want, maxChunk)) //nolint:gosec // bounded by the limit of the caller, itself bounded by the API
	for len(page.Items) < want && scanned < scanLimit {
		rows, err := s.childRows(ctx, repo.ID, cq, cursor, chunk)
		if err != nil {
			return ChildrenPage{}, err
		}
		if len(rows) == 0 {
			break
		}
		scanned += len(rows)
		cursor, lastID = rows[len(rows)-1].Abs, rows[len(rows)-1].ID
		ok, err := visible(ctx, rows)
		if err != nil {
			return ChildrenPage{}, err
		}
		for i, r := range rows {
			if i >= len(ok) || !ok[i] {
				continue
			}
			r.Path = strings.TrimPrefix(r.Abs, prefix)
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
		// The caller sees little of what this folder holds: read more per query.
		chunk = min(chunk*2, maxChunk)
	}
	switch {
	case len(page.Items) > cq.Limit:
		page.Items = page.Items[:cq.Limit]
		page.Next = page.Items[len(page.Items)-1].Abs
	case cursor != "" && scanned >= scanLimit:
		page.Next = hiddenPosition + strconv.FormatInt(lastID, 10)
	}
	if page.Items == nil {
		page.Items = []Child{}
	}
	return page, nil
}

// cursorPath turns the cursor of a listing into the path to continue after: a
// path as it is, a [hiddenPosition] through the item it names, which must lie
// in this repository below the listed folder. A position whose item was purged
// meanwhile answers 400 invalid_cursor, and the client starts again.
func (s *Store) cursorPath(ctx context.Context, rid int64, prefix, after string) (string, error) {
	invalid := errs.E(errs.Invalid, errs.CodeInvalidCursor,
		"The cursor is not valid for this request. Start the traversal again without a cursor.")
	pos, ok := strings.CutPrefix(after, hiddenPosition)
	if !ok {
		if strings.IndexByte(after, 0) >= 0 { // no path holds one; PostgreSQL text cannot either
			return "", invalid
		}
		return after, nil
	}
	id, err := strconv.ParseInt(pos, 10, 64)
	if err != nil {
		return "", invalid
	}
	var path string
	err = s.read(ctx, func(qs *q.Queries) error {
		rows, err := qs.ItemPathByID(ctx, q.ItemPathByIDParams{ID: id, RepoID: rid})
		if len(rows) == 1 {
			path = rows[0]
		}
		return err
	})
	if err != nil {
		return "", err
	}
	if path == "" || !strings.HasPrefix(path, prefix) {
		return "", invalid
	}
	return path, nil
}

func (s *Store) childRows(ctx context.Context, rid int64, cq ChildrenQuery, after string, limit int32) ([]Child, error) {
	var out []Child
	err := s.read(ctx, func(qs *q.Queries) error {
		out = out[:0]
		if !cq.Recursive {
			rows, err := qs.ListChildrenDirect(ctx, q.ListChildrenDirectParams{
				RepoID: rid, Path: cq.Path, After: after, Folders: cq.Folders, RowLimit: limit,
			})
			for _, r := range rows {
				out = append(out, childOf(r.ID, r.Path, r.Kind, r.Size.Int64, r.Size.Valid, r.ModifiedAt.Time, r.BlobSha256, r.BlobSha1))
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
			out = append(out, childOf(r.ID, r.Path, r.Kind, r.Size.Int64, r.Size.Valid, r.ModifiedAt.Time, r.BlobSha256, r.BlobSha1))
		}
		return err
	})
	return out, err
}

func childOf(id int64, path, kind string, size int64, hasSize bool, modified time.Time, sha256, sha1 []byte) Child {
	c := Child{Abs: path, ID: id, Kind: kind}
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
