package virtualtest

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/virtual"
)

// Rows are the package rows of the local repositories: per repository and package the live
// versions and the rev_seq, which every change bumps (R-ARCH-012). It implements
// virtual.Locals.
type Rows struct {
	mu    sync.Mutex
	pkgs  map[int64]map[string]*row
	items map[int64]map[string]bool
	// RevQueries counts the calls of RevSeqs: one per request that finds an entry.
	RevQueries atomic.Int64
}

type row struct {
	rev      int64
	versions []string
}

var _ virtual.Locals = (*Rows)(nil)

// NewRows returns an empty store.
func NewRows() *Rows {
	return &Rows{pkgs: map[int64]map[string]*row{}, items: map[int64]map[string]bool{}}
}

func (r *Rows) get(repo int64, pkg string) *row {
	if r.pkgs[repo] == nil {
		r.pkgs[repo] = map[string]*row{}
	}
	if r.pkgs[repo][pkg] == nil {
		r.pkgs[repo][pkg] = &row{}
	}
	return r.pkgs[repo][pkg]
}

// Publish adds a live version and bumps the revision.
func (r *Rows) Publish(repo int64, pkg, version string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.get(repo, pkg)
	p.versions = append(p.versions, version)
	p.rev++
}

// Unpublish removes a version and bumps the revision; the package row stays.
func (r *Rows) Unpublish(repo int64, pkg, version string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.get(repo, pkg)
	p.versions = slices.DeleteFunc(p.versions, func(v string) bool { return v == version })
	p.rev++
}

// AddItem stores an artifact path in a repository.
func (r *Rows) AddItem(repo int64, path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.items[repo] == nil {
		r.items[repo] = map[string]bool{}
	}
	r.items[repo][path] = true
}

// Document implements DocSource: the live versions of a local member and the revision they were
// read at.
func (r *Rows) Document(_ context.Context, repo int64, pkg string) ([]byte, int64, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.pkgs[repo][pkg]
	if p == nil {
		return nil, 0, false, nil
	}
	return Doc(slices.Clone(p.versions)), p.rev, len(p.versions) > 0, nil
}

// Doc renders the document of a local member from its versions.
func Doc(versions []string) []byte {
	sort.Strings(versions)
	b, _ := json.Marshal(struct {
		Versions []string `json:"versions"`
	}{versions})
	return b
}

// DocSource renders the document of a local member: the test format's LocalDocument. Rows
// implements it in memory; the integration tests implement it over the package rows.
type DocSource interface {
	Document(ctx context.Context, repo int64, pkg string) (doc []byte, rev int64, found bool, err error)
}

// RevSeqs implements virtual.Locals.
func (r *Rows) RevSeqs(_ context.Context, ids []int64, name string) (map[int64]int64, error) {
	r.RevQueries.Add(1)
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[int64]int64, len(ids))
	for _, id := range ids {
		out[id] = 0
		if p := r.pkgs[id][name]; p != nil {
			out[id] = p.rev
		}
	}
	return out, nil
}

// Holds implements virtual.Locals.
func (r *Rows) Holds(_ context.Context, m repository.Repository, d virtual.Descriptor) (bool, error) {
	id := idOf(m)
	if d.Package != "" && d.Kind == "metadata" {
		return r.HoldsVersion(context.Background(), m, d.Package)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.items[id][d.Path], nil
}

// HoldsVersion implements virtual.Locals.
func (r *Rows) HoldsVersion(_ context.Context, m repository.Repository, name string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.pkgs[idOf(m)][name]
	return p != nil && len(p.versions) > 0, nil
}

func idOf(m repository.Repository) int64 {
	var n int64
	for _, c := range m.ID {
		n = n*10 + int64(c-'0')
	}
	return n
}

// Merger is the test format as the engine meets it. A member document is
// {"versions":["1.0.0",...]}; the merge is {"versions":{"1.0.0":"<member key>",...}}, each version
// taken from the member the source rule of R-REPO-020 picks: local members in order, then remote
// members with the version's artifact cached, then remote members in order. A restricted part lists
// only its cached versions (R-AUTHZ-008). Artifacts live at <package>/-/<version>.tgz.
type Merger struct {
	Docs DocSource
	Mode virtual.Strategy
	// Merges counts the calls of Merge.
	Merges atomic.Int64
}

var _ virtual.Merger = (*Merger)(nil)

// NewMerger returns the test format over the documents of the local members, resolving by Merge.
func NewMerger(docs DocSource) *Merger { return &Merger{Docs: docs, Mode: virtual.Merge} }

// Strategy implements virtual.Merger.
func (m *Merger) Strategy(virtual.Descriptor) virtual.Strategy { return m.Mode }

// LocalDocument implements virtual.Merger.
func (m *Merger) LocalDocument(ctx context.Context, mem virtual.Member, d virtual.Descriptor) ([]byte, int64, bool, error) {
	return m.Docs.Document(ctx, mem.ID, d.Package)
}

// Merge implements virtual.Merger.
func (m *Merger) Merge(_ context.Context, _ virtual.Descriptor, parts []virtual.Part) ([]byte, error) {
	m.Merges.Add(1)
	type pick struct {
		rank, pos int
		key       string
	}
	best := map[string]pick{}
	for _, p := range parts {
		var doc struct {
			Versions []string `json:"versions"`
		}
		if err := json.Unmarshal(p.Doc, &doc); err != nil {
			return nil, err
		}
		for _, v := range doc.Versions {
			rank := 0
			if p.Tier != virtual.TierLocal {
				rank = 2
				if p.CachedVersions[v] {
					rank = 1
				} else if p.Restricted {
					continue
				}
			}
			c := pick{rank, p.Member.Position, p.Member.Repo.Key}
			if b, ok := best[v]; !ok || c.rank < b.rank || (c.rank == b.rank && c.pos < b.pos) {
				best[v] = c
			}
		}
	}
	out := map[string]string{}
	for v, b := range best {
		out[v] = b.key
	}
	return json.Marshal(struct {
		Versions map[string]string `json:"versions"`
	}{out})
}

// ArtifactPrefix implements virtual.Merger.
func (m *Merger) ArtifactPrefix(d virtual.Descriptor) string { return d.Package + "/-/" }

// CachedVersion implements virtual.Merger.
func (m *Merger) CachedVersion(path string) string {
	_, file, ok := strings.Cut(path, "/-/")
	if !ok {
		return ""
	}
	return strings.TrimSuffix(file, ".tgz")
}

// Versions decodes a merged document into version -> member key.
func Versions(doc []byte) map[string]string {
	var out struct {
		Versions map[string]string `json:"versions"`
	}
	if err := json.Unmarshal(doc, &out); err != nil {
		return nil
	}
	return out.Versions
}
