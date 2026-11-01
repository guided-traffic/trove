package nugettest

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"

	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
	"github.com/guided-traffic/trove/internal/formats/nuget/version"
)

// The package view of the twin (R-API-029, R-EP-016): the reads internal/formatdata/nuget
// answers with SQL, over the versions InsertVersion recorded. The statements and the twin
// agree on four rules, and a test in each keeps them so: the description falls back to the
// summary, a version is "stored" whether it is listed or not, a hosted repository lists its
// versions in arrival order (the version id grows with every insert) and a remote one by
// publish time, and a README is read only when asked for.

// viewRow is a version as the view sees it.
type viewRow struct {
	in     rows.VersionInput
	url    string
	listed bool
}

// viewState is the part of the twin the view reads.
type viewState struct {
	mu      sync.Mutex
	rows    []viewRow
	keys    map[string]int64
	readmes map[int64]string
}

// record keeps a version the twin was handed; a version that is stored again replaces its row.
func (s *Store) record(in rows.VersionInput) {
	v, err := version.Parse(in.OriginalVersion)
	if err != nil {
		return
	}
	s.view.mu.Lock()
	defer s.view.mu.Unlock()
	row := viewRow{in: in, url: v.URLForm(), listed: true}
	for i := range s.view.rows {
		if s.view.rows[i].in.VersionID == in.VersionID {
			s.view.rows[i] = row
			return
		}
	}
	s.view.rows = append(s.view.rows, row)
}

// setListed records the listing state of a version.
func (s *Store) setListed(versionID int64, listed bool) bool {
	s.view.mu.Lock()
	defer s.view.mu.Unlock()
	for i := range s.view.rows {
		if s.view.rows[i].in.VersionID == versionID {
			s.view.rows[i].listed = listed
			return true
		}
	}
	return false
}

// drop forgets a version the twin removed.
func (s *Store) drop(versionID int64) {
	s.view.mu.Lock()
	defer s.view.mu.Unlock()
	s.view.rows = slices.DeleteFunc(s.view.rows, func(r viewRow) bool { return r.in.VersionID == versionID })
}

// BindRepo tells the twin the key of a repository, so that ViewFind can select by key.
func (s *Store) BindRepo(key string, repoID int64) {
	s.view.mu.Lock()
	defer s.view.mu.Unlock()
	if s.view.keys == nil {
		s.view.keys = map[string]int64{}
	}
	s.view.keys[key] = repoID
}

// SetReadme gives a version the text of its README file.
func (s *Store) SetReadme(versionID int64, text string) {
	s.view.mu.Lock()
	defer s.view.mu.Unlock()
	if s.view.readmes == nil {
		s.view.readmes = map[int64]string{}
	}
	s.view.readmes[versionID] = text
}

func (s *Store) rowsOf(ids []int64) []viewRow {
	s.view.mu.Lock()
	defer s.view.mu.Unlock()
	var out []viewRow
	for _, r := range s.view.rows {
		if slices.Contains(ids, r.in.VersionID) {
			out = append(out, r)
		}
	}
	return out
}

// metadataOf reads the members of the stored metadata the view needs.
func metadataOf(raw []byte) (description, summary string, tags []string, readme string) {
	var m struct {
		Description string          `json:"description"`
		Summary     string          `json:"summary"`
		Tags        json.RawMessage `json:"tags"`
		Readme      string          `json:"readme"`
	}
	_ = json.Unmarshal(raw, &m)
	var list []string
	if json.Unmarshal(m.Tags, &list) != nil {
		list = strings.Fields(strings.Trim(string(m.Tags), `"`))
	}
	return strings.TrimSpace(m.Description), strings.TrimSpace(m.Summary), list, strings.TrimSpace(m.Readme)
}

// ViewSummaries reads the description and the tags.
func (s *Store) ViewSummaries(_ context.Context, ids []int64) ([]rows.ViewSummary, error) {
	var out []rows.ViewSummary
	for _, r := range s.rowsOf(ids) {
		d, sum, tags, _ := metadataOf(r.in.Metadata)
		if d == "" {
			d = sum
		}
		out = append(out, rows.ViewSummary{VersionID: r.in.VersionID, Description: d, Tags: tags})
	}
	return out, nil
}

// ViewDetails reads the metadata, the listing state and, when asked, the README.
func (s *Store) ViewDetails(_ context.Context, ids []int64, withReadme bool) ([]rows.ViewDetail, error) {
	var out []rows.ViewDetail
	for _, r := range s.rowsOf(ids) {
		d := rows.ViewDetail{VersionID: r.in.VersionID, Metadata: r.in.Metadata, Listed: r.listed, DeclaredID: r.in.DeclaredID}
		if _, _, _, file := metadataOf(r.in.Metadata); withReadme && file != "" {
			s.view.mu.Lock()
			d.Readme, d.ReadmeFile = s.view.readmes[r.in.VersionID], file
			s.view.mu.Unlock()
		}
		out = append(out, d)
	}
	return out, nil
}

// ViewFind returns at most limit ids whose description contains text or whose tags contain
// keyword, in version id order.
func (s *Store) ViewFind(_ context.Context, repos []string, text, keyword string, limit int) ([]int64, error) {
	s.view.mu.Lock()
	defer s.view.mu.Unlock()
	var out []int64
	for _, r := range s.view.rows {
		if len(out) >= max(limit, 0) {
			break
		}
		if !s.inRepos(repos, r.in.RepoID) {
			continue
		}
		d, sum, tags, _ := metadataOf(r.in.Metadata)
		if d == "" {
			d = sum
		}
		if (text != "" && strings.Contains(strings.ToLower(d), strings.ToLower(text))) || (keyword != "" && slices.Contains(tags, keyword)) {
			out = append(out, r.in.VersionID)
		}
	}
	return out, nil
}

// inRepos reports whether a repository id is one of the keys; the caller holds the lock.
func (s *Store) inRepos(keys []string, repoID int64) bool {
	for _, k := range keys {
		if id, ok := s.view.keys[k]; ok && id == repoID {
			return true
		}
	}
	return false
}

// ViewStored lists the versions of an ID in a repository, newest first.
func (s *Store) ViewStored(_ context.Context, repoID int64, lowerID string, remote bool, limit int) ([]string, error) {
	s.view.mu.Lock()
	var held []viewRow
	for _, r := range s.view.rows {
		if r.in.RepoID == repoID && r.in.LowerID == lowerID {
			held = append(held, r)
		}
	}
	s.view.mu.Unlock()
	slices.SortFunc(held, func(a, b viewRow) int {
		if remote {
			if c := b.in.PublishedAt.Compare(a.in.PublishedAt); c != 0 {
				return c
			}
		}
		return int(b.in.VersionID - a.in.VersionID)
	})
	out := make([]string, 0, len(held))
	for _, r := range held[:min(len(held), max(limit, 0))] {
		out = append(out, r.url)
	}
	return out, nil
}
