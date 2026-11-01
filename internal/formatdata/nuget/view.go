package nuget

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/nuget/nupkg"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The reads of the package view (R-API-029, R-EP-016, Q-NUGET-48): what the screens show of a
// NuGet version beyond the generic rows, and the two searches that need the metadata. The
// statements are in internal/meta/postgres/queries/nuget_view.sql.

// maxReadme is the size of a README the view reads: R-NUGET-024's bound for the file.
const maxReadme = 1 << 20

// BlobReader gives ranged reads over the content of a stored blob. The composition root
// binds it to the blob store; the view uses it to open the .nupkg of a version.
type BlobReader interface {
	// ReaderAt returns a reader over the blob with the SHA-256 digest, size bytes long. Each
	// ReadAt is one ranged read of the object store; ctx bounds all of them.
	ReaderAt(ctx context.Context, sha256 []byte, size int64) io.ReaderAt
}

// ViewSummaries reads the description and the tags of the given versions. A version
// without a NuGet row, or in the trash, is absent.
func (s *Store) ViewSummaries(ctx context.Context, ids []int64) ([]rows.ViewSummary, error) {
	var out []rows.ViewSummary
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		out = out[:0]
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rs, err := qs.NuGetViewSummaries(ctx, ids)
		if err != nil {
			return err
		}
		for _, r := range rs {
			var tags []string
			if json.Unmarshal([]byte(r.Tags), &tags) != nil {
				tags = nil
			}
			out = append(out, rows.ViewSummary{VersionID: r.VersionID, Description: r.Description, Tags: nonEmpty(tags)})
		}
		return nil
	})
	return out, err
}

// nonEmpty drops the empty entries of a tag list.
func nonEmpty(in []string) []string {
	out := in[:0:0]
	for _, t := range in {
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// ViewDetails reads the metadata of the given versions and, when withReadme is set, the
// README file their .nuspec names, out of the stored package.
func (s *Store) ViewDetails(ctx context.Context, ids []int64, withReadme bool) ([]rows.ViewDetail, error) {
	var found []q.NuGetViewDetailsRow
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		found, err = qs.NuGetViewDetails(ctx, ids)
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make([]rows.ViewDetail, 0, len(found))
	for _, r := range found {
		d := rows.ViewDetail{VersionID: r.VersionID, Metadata: []byte(r.Metadata), Listed: r.Listed, DeclaredID: r.DeclaredID}
		// The object store is read after the transaction ended: a slow range read must not
		// hold a connection.
		if withReadme && s.o.Blobs != nil && len(r.BlobSha256) > 0 {
			if d.ReadmeFile = readmePath(d.Metadata); d.ReadmeFile != "" {
				if d.Readme, err = s.readme(ctx, r.BlobSha256, r.BlobSize, d.ReadmeFile); err != nil {
					return nil, err
				}
			}
		}
		out = append(out, d)
	}
	return out, nil
}

// readmePath is the path of the README file the metadata names, "" for none.
func readmePath(metadata []byte) string {
	var m struct {
		Readme string `json:"readme"`
	}
	if json.Unmarshal(metadata, &m) != nil {
		return ""
	}
	return strings.TrimSpace(m.Readme)
}

// readme reads one file of a stored package. A package that cannot be opened, a file the
// package does not hold, one larger than maxReadme and one that is not UTF-8 text are a
// version without a README (""); an object store that fails is an error, so that the
// failure is not cached as an absent README.
func (s *Store) readme(ctx context.Context, sha256 []byte, size int64, path string) (string, error) {
	a, err := nupkg.Open(ctx, s.o.Blobs.ReaderAt(ctx, sha256, size), size, nupkg.Limits{})
	if err != nil {
		if nupkg.IsRefusal(err) {
			return "", nil
		}
		return "", err
	}
	e, ok := a.Lookup(path)
	if !ok || e.Dir || e.Size > maxReadme {
		return "", nil
	}
	b, err := a.Read(ctx, e, maxReadme)
	if err != nil {
		if nupkg.IsRefusal(err) {
			return "", nil
		}
		return "", err
	}
	if !utf8.Valid(b) {
		return "", nil
	}
	return string(b), nil
}

// ViewFind returns the ids of at most limit versions of the repositories (by key) whose
// description contains text or whose tags contain keyword.
func (s *Store) ViewFind(ctx context.Context, repos []string, text, keyword string, limit int) ([]int64, error) {
	if (text == "") == (keyword == "") {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "nuget: a view search sets exactly one of text and keyword")
	}
	var out []int64
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		out, err = qs.NuGetViewFind(ctx, q.NuGetViewFindParams{Repos: repos, Text: text, Keyword: keyword, MaxRows: int32(min(max(limit, 0), math.MaxInt32))})
		return err
	})
	return out, err
}

// ViewStored lists the versions a repository holds of an ID, listed or not: a hosted
// repository newest arrival first, a remote one newest published first.
func (s *Store) ViewStored(ctx context.Context, repoID int64, lowerID string, remote bool, limit int) ([]string, error) {
	maxRows := int32(min(max(limit, 0), math.MaxInt32))
	var out []string
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		if remote {
			out, err = qs.NuGetViewStoredRemote(ctx, q.NuGetViewStoredRemoteParams{RepoID: repoID, Name: lowerID, MaxRows: maxRows})
			return err
		}
		out, err = qs.NuGetViewStoredLocal(ctx, q.NuGetViewStoredLocalParams{RepoID: repoID, Name: lowerID, MaxRows: maxRows})
		return err
	})
	return out, err
}
