package deb

import (
	"context"
	"strings"

	"github.com/guided-traffic/trove/internal/formats/deb/control"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// viewStore is the group of the data port that the package screens read (R-DEB-030).
type viewStore struct{ *group }

// Describe reads, for the versions with the given ids, what a package screen shows: the architectures, the coordinates, the first line
// of the description and the control fields of R-DEB-030. The control data of the version is that of its first file by architecture;
// the packages of one version differ in the architecture and rarely in the rest.
func (s viewStore) Describe(ctx context.Context, versionIDs []int64) (map[int64]rows.VersionView, error) {
	out := map[int64]rows.VersionView{}
	if len(versionIDs) == 0 {
		return out, nil
	}
	err := s.read(ctx, nil, func(qs *q.Queries) error {
		files, err := qs.DebViewRows(ctx, versionIDs)
		if err != nil {
			return err
		}
		for _, f := range files {
			if !f.VersionID.Valid {
				continue
			}
			v, ok := out[f.VersionID.Int64]
			if !ok {
				v = rows.VersionView{VersionID: f.VersionID.Int64, Fields: map[string]string{}}
				if p, err := control.Parse([]byte(f.Control)); err == nil {
					v.Description, _, _ = strings.Cut(p.Value("Description"), "\n")
					for _, name := range []string{"Maintainer", "Section", "Priority", "Installed-Size", "Homepage", "Depends", "Source"} {
						if val, ok := p.Get(name); ok {
							v.Fields[name] = val
						}
					}
				}
			}
			v.Architectures = append(v.Architectures, f.Architecture)
			out[f.VersionID.Int64] = v
		}
		cs, err := qs.DebViewCoordinates(ctx, versionIDs)
		if err != nil {
			return err
		}
		for _, c := range cs {
			if v, ok := out[c.VersionID.Int64]; ok {
				v.Coordinates = append(v.Coordinates, rows.Coordinate{Suite: c.Suite, Component: c.Component})
				out[c.VersionID.Int64] = v
			}
		}
		return nil
	})
	return out, err
}

// Stored lists the versions of a package a repository serves, newest arrival first: the versions with a live file that the replay did
// not withhold.
func (s viewStore) Stored(ctx context.Context, repoID int64, pkg string, limit int) ([]string, error) {
	var out []string
	err := s.read(ctx, nil, func(qs *q.Queries) error {
		var err error
		out, err = qs.DebStoredVersions(ctx, q.DebStoredVersionsParams{RepoID: repoID, Package: pkg, MaxRows: int64(limit)})
		return err
	})
	return out, err
}

// Find returns the ids of at most limit versions whose control data contains the text.
func (s viewStore) Find(ctx context.Context, repos []string, text string, limit int) ([]int64, error) {
	var out []int64
	if text == "" || len(repos) == 0 {
		return nil, nil
	}
	err := s.read(ctx, nil, func(qs *q.Queries) error {
		ids, err := qs.DebFindVersions(ctx, q.DebFindVersionsParams{Repos: repos, Needle: text, MaxRows: int64(limit)})
		for _, id := range ids {
			if id.Valid {
				out = append(out, id.Int64)
			}
		}
		return err
	})
	return out, err
}

// Withheld returns the ids of the live versions all of whose live files the journal replay withheld (R-DEB-032).
func (s viewStore) Withheld(ctx context.Context, repos []string) ([]int64, error) {
	var out []int64
	if len(repos) == 0 {
		return nil, nil
	}
	err := s.read(ctx, nil, func(qs *q.Queries) error {
		var err error
		out, err = qs.DebWithheldVersions(ctx, repos)
		return err
	})
	return out, err
}
