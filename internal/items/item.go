package items

import (
	"time"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Kinds of an item.
const (
	KindFile   = "file"
	KindFolder = "folder"
)

// Item is a live row of the items table with what the descriptions need of its
// blob.
type Item struct {
	ID         int64
	RepoID     int64
	Repository string
	Path       string
	Kind       string
	Size       int64
	MediaType  string
	// Digest is the SHA-256 of the content; the other digests are those of the
	// blob row. All are zero for a folder.
	Digest blob.Digest
	SHA1   []byte
	MD5    []byte
	SHA512 []byte
	// Declared digests are those the client announced (Repr-Digest), nil if none.
	DeclaredSHA256, DeclaredSHA512, DeclaredSHA1, DeclaredMD5 []byte
	DeclaredMismatch                                          bool
	Explicit                                                  bool
	Generated                                                 bool
	ContentStatus                                             string
	// UpstreamURL is where the content of a remote repository's item came from;
	// "" for an item that was uploaded.
	UpstreamURL                      string
	CreatedAt, ModifiedAt, UpdatedAt time.Time
	// CreatedBy and ModifiedBy are "<kind>:<name>" of the principal, "" when the
	// row has none (a break-glass administrator, an erased principal).
	CreatedBy, ModifiedBy string
}

// itemFrom converts the row of GetLiveItem.
func itemFrom(repoKey string, r q.GetLiveItemRow) Item {
	it := Item{
		ID: r.ID, RepoID: r.RepoID, Repository: repoKey, Path: r.Path, Kind: r.Kind,
		DeclaredSHA256: r.DeclaredSha256, DeclaredSHA512: r.DeclaredSha512, DeclaredSHA1: r.DeclaredSha1, DeclaredMD5: r.DeclaredMd5,
		DeclaredMismatch: r.DeclaredMismatch, Explicit: r.ExplicitFolder, Generated: r.Generated, UpstreamURL: r.UpstreamUrl.String,
		CreatedAt: r.CreatedAt.Time.UTC(), ModifiedAt: r.ModifiedAt.Time.UTC(), UpdatedAt: r.UpdatedAt.Time.UTC(),
		CreatedBy: r.CreatedByName, ModifiedBy: r.ModifiedByName,
	}
	if r.Kind != KindFile {
		return it
	}
	it.Size = r.Size.Int64
	it.MediaType = r.MediaType.String
	copy(it.Digest[:], r.BlobSha256)
	it.SHA1, it.MD5, it.SHA512 = r.BlobSha1, r.BlobMd5, r.BlobSha512
	it.ContentStatus = r.ContentStatus.String
	return it
}

// ETag is the strong entity tag of a file: the content address (R-STOR-012).
func (it Item) ETag() string { return `"` + it.Digest.Hex() + `"` }

// MediaTypeOrDefault is the media type served for the item (R-GENERIC-005): the
// one recorded at deploy time, or application/octet-stream.
func (it Item) MediaTypeOrDefault() string {
	if it.MediaType == "" {
		return "application/octet-stream"
	}
	return it.MediaType
}

// Root is the synthetic folder of a repository root.
func Root(key string, created, updated time.Time) Item {
	return Item{
		Repository: key, Kind: KindFolder, Explicit: true,
		CreatedAt: created.UTC(), ModifiedAt: updated.UTC(), UpdatedAt: updated.UTC(),
	}
}
