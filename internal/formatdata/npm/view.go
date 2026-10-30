package npm

import (
	"context"
	"encoding/json"
	"math"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/npm/rows"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The reads of the package view (R-API-029): what the screens show of an npm version
// beyond the generic rows, and the two searches that need the manifest. The
// statements are in internal/meta/postgres/queries/npm_view.sql.

// ViewSummaries reads the description and the keywords of the manifests of the
// versions. A version without an npm row is absent.
func (s *Store) ViewSummaries(ctx context.Context, ids []int64) ([]rows.ViewSummary, error) {
	var out []rows.ViewSummary
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		out = out[:0]
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rs, err := qs.NPMViewSummaries(ctx, ids)
		if err != nil {
			return err
		}
		for _, r := range rs {
			var kw []string
			if err := json.Unmarshal([]byte(r.Keywords), &kw); err != nil {
				kw = nil
			}
			out = append(out, rows.ViewSummary{VersionID: r.VersionID, Description: r.Description, Keywords: nonEmpty(kw)})
		}
		return nil
	})
	return out, err
}

// nonEmpty drops the empty entries of a keyword list.
func nonEmpty(in []string) []string {
	out := in[:0:0]
	for _, k := range in {
		if k != "" {
			out = append(out, k)
		}
	}
	return out
}

// ViewDetails reads the manifest, the deprecation and the dist-tags of the versions,
// and their README text when asked.
func (s *Store) ViewDetails(ctx context.Context, ids []int64, withReadme bool) ([]rows.ViewDetail, error) {
	var out []rows.ViewDetail
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		out = out[:0]
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rs, err := qs.NPMViewDetails(ctx, q.NPMViewDetailsParams{Ids: ids, WithReadme: withReadme})
		if err != nil {
			return err
		}
		for _, r := range rs {
			tags, err := parseTags(r.DistTags)
			if err != nil {
				return err
			}
			out = append(out, rows.ViewDetail{
				VersionID: r.VersionID, Manifest: []byte(r.Manifest), Deprecated: r.Deprecated,
				HasReadme: r.HasReadme, Readme: r.Readme, ReadmeFilename: r.ReadmeFilename, DistTags: tags,
			})
		}
		return nil
	})
	return out, err
}

// ViewFind returns the ids of at most limit versions of the repositories (by key)
// whose description contains text or whose keywords contain keyword.
func (s *Store) ViewFind(ctx context.Context, repos []string, text, keyword string, limit int) ([]int64, error) {
	if (text == "") == (keyword == "") {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "npm: a view search sets exactly one of text and keyword")
	}
	var out []int64
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		out, err = qs.NPMViewFind(ctx, q.NPMViewFindParams{Repos: repos, Text: text, Keyword: keyword, MaxRows: int32(min(max(limit, 0), math.MaxInt32))})
		return err
	})
	return out, err
}

// ViewWithheld returns the stored versions of the repositories that the format does
// not serve (R-NPM-047).
func (s *Store) ViewWithheld(ctx context.Context, repos []string) ([]int64, error) {
	var out []int64
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		out, err = qs.NPMViewWithheld(ctx, repos)
		return err
	})
	return out, err
}
