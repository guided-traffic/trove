package nuget

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The search read of local repositories (R-NUGET-010): the statements are in
// internal/meta/postgres/queries/nuget_search.sql. The format decides what a caller may
// read; this file answers which packages match and in which order.

// searchQueries binds the generated statements to the transaction of a read.
func searchQueries(tx meta.Tx) (*q.Queries, error) {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "nuget: the transaction is not the database's")
	}
	return q.New(ptx), nil
}

// searchTerm is a term as the statements read it: the folded text for the ID tests and the
// tsquery texts for search_doc and the metadata fields.
type searchTerm struct {
	Field string `json:"field"`
	Value string `json:"value"`
	TSQ   string `json:"tsq"`
	TSQID string `json:"tsq_id"`
}

// termsJSON renders the terms of a query as the JSON array the statements take.
func termsJSON(terms []rows.SearchTerm) (string, error) {
	out := make([]searchTerm, 0, len(terms))
	for _, t := range terms {
		out = append(out, searchTerm{Field: t.Field, Value: t.Value, TSQ: tsQuery(t.Value, ""), TSQID: tsQuery(t.Value, "A")})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", errs.E(errs.Internal, errs.CodeInternal, "", errs.WithCause(err))
	}
	return string(b), nil
}

// tsQuery turns the text of a term into a tsquery text: its words, as prefixes, one after
// the other (the phrase operator), limited to a weight when one is given. The words are
// the runs of letters and digits, folded to lower case like the 'simple' configuration
// folds the document; no other character reaches the text, so the query cannot be
// malformed or carry an operator of the user's. "" means the term holds no word.
func tsQuery(value, weight string) string {
	words := strings.FieldsFunc(strings.ToLower(value), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if len(words) == 0 {
		return ""
	}
	var b strings.Builder
	for i, w := range words {
		if i > 0 {
			b.WriteString(" <-> ")
		}
		b.WriteString("'" + w + "':*" + weight)
	}
	return b.String()
}

// SearchCount implements the count of the matches (R-NUGET-010).
func (s *Store) SearchCount(ctx context.Context, query rows.SearchQuery) (int, error) {
	terms, err := termsJSON(query.Terms)
	if err != nil {
		return 0, err
	}
	var total int64
	err = s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := searchQueries(tx)
		if err != nil {
			return err
		}
		total, err = qs.NuGetSearchCount(ctx, q.NuGetSearchCountParams{
			RepositoryID: query.RepoID, Prerelease: query.Prerelease, Semver2: query.SemVer2,
			Terms: terms, PackageType: query.PackageType,
		})
		return err
	})
	return int(total), err
}

// Search implements the search read (R-NUGET-010): a page of the matches in rank order, or
// the packages holding the versions Only names.
func (s *Store) Search(ctx context.Context, query rows.SearchQuery) ([]rows.SearchHit, error) {
	if len(query.Only) > 0 {
		return s.searchVersions(ctx, query)
	}
	if query.Limit <= 0 {
		return nil, nil
	}
	terms, err := termsJSON(query.Terms)
	if err != nil {
		return nil, err
	}
	var out []rows.SearchHit
	err = s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		out = nil
		qs, err := searchQueries(tx)
		if err != nil {
			return err
		}
		rs, err := qs.NuGetSearchPage(ctx, q.NuGetSearchPageParams{
			RepositoryID: query.RepoID, Prerelease: query.Prerelease, Semver2: query.SemVer2,
			Terms: terms, PackageType: query.PackageType, WithMetadata: !query.KeysOnly,
			RowOffset: int64(max(query.Offset, 0)), RowLimit: int64(query.Limit),
		})
		if err != nil {
			return err
		}
		for _, r := range rs {
			h, err := hitOf(hitRow(r))
			if err != nil {
				return err
			}
			out = append(out, h)
		}
		return nil
	})
	return out, err
}

func (s *Store) searchVersions(ctx context.Context, query rows.SearchQuery) ([]rows.SearchHit, error) {
	type ref struct {
		LowerID      string `json:"lower_id"`
		LowerVersion string `json:"lower_version"`
	}
	refs := make([]ref, 0, len(query.Only))
	for _, v := range query.Only {
		refs = append(refs, ref{LowerID: v.LowerID, LowerVersion: v.LowerVersion})
	}
	b, err := json.Marshal(refs)
	if err != nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "", errs.WithCause(err))
	}
	var out []rows.SearchHit
	err = s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		out = nil
		qs, err := searchQueries(tx)
		if err != nil {
			return err
		}
		rs, err := qs.NuGetSearchVersions(ctx, q.NuGetSearchVersionsParams{RepositoryID: query.RepoID, Refs: string(b)})
		if err != nil {
			return err
		}
		for _, r := range rs {
			h, err := hitOf(hitRow(r))
			if err != nil {
				return err
			}
			out = append(out, h)
		}
		return nil
	})
	return out, err
}

// hitRow is the shape both search statements return a package in.
type hitRow struct {
	LowerID, DisplayName, DeclaredID string
	VersionID                        int64
	LowerVersion                     string
	DisplayVersion                   string
	IsPrerelease, IsSemver2          bool
	PublishedAt                      string
	PackageTypes                     []string
	Metadata, Versions               string
}

// versionDoc is one element of the versions array of a row.
type versionDoc struct {
	ID       int64    `json:"id"`
	Lower    string   `json:"lower"`
	Full     string   `json:"full"`
	Declared string   `json:"declared"`
	Pre      bool     `json:"pre"`
	SV2      bool     `json:"sv2"`
	At       string   `json:"at"`
	Types    []string `json:"types"`
}

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, errs.E(errs.Internal, errs.CodeInternal, "", errs.WithCause(err))
	}
	return t.UTC(), nil
}

// hitOf builds the value that crosses the port from a row.
func hitOf(r hitRow) (rows.SearchHit, error) {
	at, err := parseTime(r.PublishedAt)
	if err != nil {
		return rows.SearchHit{}, err
	}
	h := rows.SearchHit{LowerID: r.LowerID, DisplayName: r.DisplayName}
	h.Latest = rows.RegistrationVersion{
		VersionEntry: rows.VersionEntry{
			VersionID: r.VersionID, LowerVersion: r.LowerVersion, FullVersion: r.DisplayVersion, DeclaredID: r.DeclaredID,
			Listed: true, IsPrerelease: r.IsPrerelease, IsSemver2: r.IsSemver2, PublishedAt: at,
		},
		PackageTypes: r.PackageTypes,
	}
	if r.Metadata != "" {
		h.Latest.Metadata = []byte(r.Metadata)
	}
	var vs []versionDoc
	if err := json.Unmarshal([]byte(r.Versions), &vs); err != nil {
		return rows.SearchHit{}, errs.E(errs.Internal, errs.CodeInternal, "", errs.WithCause(err))
	}
	for _, v := range vs {
		vat, err := parseTime(v.At)
		if err != nil {
			return rows.SearchHit{}, err
		}
		h.Listed = append(h.Listed, rows.VersionEntry{
			VersionID: v.ID, LowerVersion: v.Lower, FullVersion: v.Full, DeclaredID: v.Declared,
			Listed: true, IsPrerelease: v.Pre, IsSemver2: v.SV2, PublishedAt: vat,
		})
		h.ListedTypes = append(h.ListedTypes, v.Types)
	}
	return h, nil
}
