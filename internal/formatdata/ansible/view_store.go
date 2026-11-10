package ansible

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/formats/ansible/collection"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// viewStore is the group of the data port that the package screens read (R-ANS-026, R-EP-016). The statements are in
// queries/ansible_local.sql; the generic version id is the key the screens use, so the rows are reached through the package and
// version rows.
type viewStore struct{ *group }

// maxReadme is the README text a screen reads (R-ANS-031).
const maxReadme = 64 << 10

// readmeTimeout bounds the inflation of an archive up to its README.
const readmeTimeout = 20 * time.Second

func descriptionOf(info json.RawMessage) (desc string, tags []string) {
	var m struct {
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
	}
	_ = json.Unmarshal(info, &m) //nolint:errcheck // a column this package wrote; an unreadable one shows nothing
	return m.Description, m.Tags
}

// ViewSummaries reads the description and the tags of the given generic versions.
func (s viewStore) ViewSummaries(ctx context.Context, ids []int64) ([]rows.ViewSummary, error) {
	ds, err := s.viewRows(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]rows.ViewSummary, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.ViewSummary)
	}
	return out, nil
}

func (s viewStore) viewRows(ctx context.Context, ids []int64) ([]rows.ViewDetail, error) {
	var out []rows.ViewDetail
	err := s.read(ctx, nil, func(qs *q.Queries) error {
		list, err := qs.AnsibleViewRows(ctx, ids)
		if err != nil {
			return err
		}
		for _, r := range list {
			d := rows.ViewDetail{}
			d.VersionID = r.VersionID
			d.Name, d.Version, d.RequiresAnsible, d.CollectionInfo, d.SHA256, d.Size, d.PublishedAt =
				r.Name, r.Version, r.RequiresAnsible, r.CollectionInfo, digestOf(r.Sha256), r.Size, r.PublishedAt.UTC()
			d.Description, d.Tags = descriptionOf(r.CollectionInfo)
			_ = json.Unmarshal(r.Dependencies, &d.Dependencies) //nolint:errcheck // a column this package wrote
			out = append(out, d)
		}
		return nil
	})
	return out, err
}

// ViewDetails reads the stored metadata of the given generic versions and, with readme, the text of their README; a version without a
// row is absent.
func (s viewStore) ViewDetails(ctx context.Context, ids []int64, readme bool) ([]rows.ViewDetail, error) {
	ds, err := s.viewRows(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]rows.ViewDetail, 0, len(ds))
	for _, d := range ds {
		if readme {
			d.Readme, d.ReadmeFile = s.readme(ctx, d)
		}
		out = append(out, d)
	}
	return out, nil
}

// readme reads the README of an archive by streaming it up to the member (a gzip stream has no offsets); an archive without one, a blob
// that cannot be read and a README that is not text give none.
func (s viewStore) readme(ctx context.Context, d rows.ViewDetail) (text, file string) {
	if s.o.Blobs == nil {
		return "", ""
	}
	var info struct {
		Readme string `json:"readme"`
	}
	if json.Unmarshal(d.CollectionInfo, &info) != nil || info.Readme == "" || len(info.Readme) > 512 {
		return "", ""
	}
	body, err := s.o.Blobs.Open(ctx, d.SHA256, d.Size)
	if err != nil {
		return "", ""
	}
	defer func() { _ = body.Close() }()
	lim := untrusted.DefaultLimits()
	lim.Deadline = readmeTimeout
	data, found, err := collection.ReadFile(ctx, body, lim, strings.TrimPrefix(info.Readme, "./"), maxReadme)
	if err != nil || !found {
		return "", ""
	}
	return strings.ToValidUTF8(string(data), "�"), info.Readme
}

// ViewFind returns the ids of at most limit generic versions whose description contains text or whose tags contain keyword.
func (s viewStore) ViewFind(ctx context.Context, repos []string, text, keyword string, limit int) ([]int64, error) {
	var out []int64
	err := s.read(ctx, nil, func(qs *q.Queries) (err error) {
		out, err = qs.AnsibleViewFind(ctx, q.AnsibleViewFindParams{Repos: repos, Text: text, Keyword: keyword, RowLimit: int32(min(limit, 1<<20))}) //nolint:gosec // bounded above
		return err
	})
	return out, err
}

// ViewStored lists the versions a local repository serves of a collection, newest first.
func (s viewStore) ViewStored(ctx context.Context, repoID int64, name string, limit int) ([]string, error) {
	var out []string
	err := s.read(ctx, nil, func(qs *q.Queries) (err error) {
		out, err = qs.AnsibleViewStored(ctx, q.AnsibleViewStoredParams{RepoID: repoID, Name: name, RowLimit: int32(min(limit, 1<<20))}) //nolint:gosec // bounded above
		return err
	})
	return out, err
}

// ViewWithheld lists the generic versions whose number a tombstone withholds.
func (s viewStore) ViewWithheld(ctx context.Context, repos []string) ([]int64, error) {
	var out []int64
	err := s.read(ctx, nil, func(qs *q.Queries) (err error) {
		out, err = qs.AnsibleViewWithheld(ctx, repos)
		return err
	})
	return out, err
}
