package npmtest

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/guided-traffic/trove/internal/formats/npm/rows"
)

// The package view of the twin (R-API-029, R-EP-016): the reads internal/formatdata/npm
// answers from its tables, answered from the twin's maps. The twin knows repositories by
// id only, so a read by repository key (ViewFind, ViewWithheld) covers every repository
// the twin holds when the key list is not empty.

func (s *Store) versionByID(id int64) (*pkgRec, *verRec) {
	for _, p := range s.byID {
		for _, v := range p.versions {
			if v.id == id {
				return p, v
			}
		}
	}
	return nil, nil
}

// ViewSummaries reads the description and the keywords of the manifests.
func (s *Store) ViewSummaries(_ context.Context, ids []int64) ([]rows.ViewSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []rows.ViewSummary
	for _, id := range ids {
		if _, v := s.versionByID(id); v != nil {
			desc, kw := summaryOf(v.manifest)
			out = append(out, rows.ViewSummary{VersionID: id, Description: desc, Keywords: kw})
		}
	}
	return out, nil
}

func summaryOf(manifest []byte) (string, []string) {
	var m struct {
		Description string   `json:"description"`
		Keywords    []string `json:"keywords"`
	}
	_ = json.Unmarshal(manifest, &m)
	return m.Description, m.Keywords
}

// ViewDetails reads the stored facts of the versions; the README only when asked.
func (s *Store) ViewDetails(_ context.Context, ids []int64, withReadme bool) ([]rows.ViewDetail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []rows.ViewDetail
	for _, id := range ids {
		p, v := s.versionByID(id)
		if v == nil {
			continue
		}
		d := rows.ViewDetail{
			VersionID: id, Manifest: append([]byte(nil), v.manifest...), Deprecated: v.deprecated,
			HasReadme: v.readme != "", DistTags: copyTags(p.tags),
		}
		if withReadme {
			d.Readme, d.ReadmeFilename = v.readme, v.readmeFile
		}
		out = append(out, d)
	}
	return out, nil
}

// ViewFind finds at most limit+1 versions whose description contains text
// (case-insensitively) or whose keywords contain keyword exactly.
func (s *Store) ViewFind(_ context.Context, repos []string, text, keyword string, limit int) ([]int64, error) {
	if len(repos) == 0 {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []int64
	for _, p := range s.byID {
		for _, v := range p.versions {
			desc, kw := summaryOf(v.manifest)
			hit := text != "" && strings.Contains(strings.ToLower(desc), strings.ToLower(text))
			for _, k := range kw {
				hit = hit || (keyword != "" && k == keyword)
			}
			if hit {
				out = append(out, v.id)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out[:min(len(out), max(limit, 0)+1)], nil
}

// ViewWithheld lists the versions that carry a tombstone: they are stored and not served.
func (s *Store) ViewWithheld(_ context.Context, repos []string) ([]int64, error) {
	if len(repos) == 0 {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []int64
	for _, p := range s.byID {
		for _, v := range p.versions {
			if _, gone := s.tombstones[key(p.repo, p.name, v.version)]; gone {
				out = append(out, v.id)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// ViewStored lists the versions a repository serves of a package, newest first, as the statements
// of the database do (R-EP-016): a hosted repository its live versions, newest arrival first, with
// the dist-tags when asked; a remote one the versions of its cached document, newest published
// first and unknown times last, an unlisted one too when the repository serves removed versions.
func (s *Store) ViewStored(_ context.Context, repoID int64, name string, remote bool, limit int, withTags bool) ([]string, map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit = max(limit, 0)
	if remote {
		rec := s.remote[pkgKey(repoID, name)]
		if rec == nil {
			return []string{}, nil, nil
		}
		var vs []*remoteVersion
		for _, v := range rec.versions {
			if v.listed || s.remoteState.ServeRemoved {
				vs = append(vs, v)
			}
		}
		sort.Slice(vs, func(i, j int) bool {
			a, b := vs[i], vs[j]
			switch {
			case a.PublishedAt.IsZero() != b.PublishedAt.IsZero():
				return !a.PublishedAt.IsZero()
			case !a.PublishedAt.Equal(b.PublishedAt):
				return a.PublishedAt.After(b.PublishedAt)
			}
			return a.Version > b.Version
		})
		out := make([]string, 0, min(len(vs), limit))
		for _, v := range vs[:min(len(vs), limit)] {
			out = append(out, v.Version)
		}
		return out, nil, nil
	}
	p := s.pkgs[pkgKey(repoID, name)]
	if p == nil {
		return []string{}, nil, nil
	}
	var live []*verRec
	for _, v := range p.versions {
		if _, gone := s.tombstones[key(p.repo, p.name, v.version)]; !gone {
			live = append(live, v)
		}
	}
	sort.Slice(live, func(i, j int) bool { return live[i].id > live[j].id })
	out := make([]string, 0, min(len(live), limit))
	for _, v := range live[:min(len(live), limit)] {
		out = append(out, v.version)
	}
	if !withTags {
		return out, nil, nil
	}
	return out, copyTags(p.tags), nil
}
