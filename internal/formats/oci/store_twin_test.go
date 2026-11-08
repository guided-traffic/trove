package oci

import (
	"bytes"
	"context"
	"slices"
	"sort"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/oci/rows"
)

// The list and delete side of the in-memory twin of the data port (twin_test.go has the push and pull side): the tags list, the referrers,
// the reads of the removal hook and the record of a deletion. The tests of this side seed the twin directly (putManifest, putTag), so the
// methods ignore the repository id: one repository is under test. memStore names the twin where a test speaks of this side.
type memStore = twin

// lists is the state of the list and delete side; it lives in the twin and is made on first use.
type lists struct {
	tags    map[string]map[string][32]byte // by name, then tag
	blobs   map[string]bool                // names that hold a blob item
	deleted []rows.ManifestDeleted
}

func newMemStore() *memStore { return newTwin() }

func (t *twin) l() *lists {
	if t.lists == nil {
		t.lists = &lists{tags: map[string]map[string][32]byte{}, blobs: map[string]bool{}}
	}
	return t.lists
}

// putManifest records a manifest row with its references.
func (t *twin) putManifest(m rows.Manifest, refs ...rows.Reference) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if m.MediaType == "" {
		m.MediaType = "application/vnd.oci.image.manifest.v1+json"
	}
	k := mkey{0, m.Name, m.Digest}
	t.manifests[k] = m
	t.refs[k] = refs
}

func (t *twin) putTag(name, tag string, d [32]byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	l := t.l()
	if l.tags[name] == nil {
		l.tags[name] = map[string][32]byte{}
	}
	l.tags[name][tag] = d
}

func (t *twin) Tags(_ context.Context, _ int64, name, after string, limit int) (rows.TagPage, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var all []string
	for tag := range t.l().tags[name] {
		if tag > after {
			all = append(all, tag)
		}
	}
	sort.Strings(all)
	page := rows.TagPage{Tags: all}
	if len(all) > limit {
		page.Tags, page.Next = all[:limit], all[limit-1]
	}
	return page, nil
}

func (t *twin) Holds(_ context.Context, _ int64, name string) (bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.l().tags[name]) > 0 || t.l().blobs[name] {
		return true, nil
	}
	for k := range t.manifests {
		if k.name == name {
			return true, nil
		}
	}
	return false, nil
}

func (t *twin) Referrers(_ context.Context, _ int64, name string, subject [32]byte, artifactType string, after *[32]byte, limit int) ([]rows.Manifest, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []rows.Manifest
	for k, m := range t.manifests {
		if k.name != name || m.Subject == nil || *m.Subject != subject {
			continue
		}
		if artifactType != "" && m.ArtifactType != artifactType {
			continue
		}
		if after != nil && bytes.Compare(m.Digest[:], after[:]) <= 0 {
			continue
		}
		out = append(out, m)
	}
	slices.SortFunc(out, func(a, b rows.Manifest) int { return bytes.Compare(a.Digest[:], b.Digest[:]) })
	return out[:min(len(out), limit)], nil
}

func (t *twin) ReferencedBy(_ context.Context, _ formats.Tx, _ int64, name string, target [32]byte) ([][32]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out [][32]byte
	for k, refs := range t.refs {
		if k.name != name {
			continue
		}
		for _, r := range refs {
			if r.Target == target {
				out = append(out, k.digest)
				break
			}
		}
	}
	return out, nil
}

func (t *twin) RemoveManifest(_ context.Context, _ formats.Tx, _ int64, name string, d [32]byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k := range t.manifests {
		if k.name == name && k.digest == d {
			delete(t.manifests, k)
			delete(t.refs, k)
		}
	}
	return nil
}

func (t *twin) TagsPointingAt(_ context.Context, _ formats.Tx, _ int64, name string, d [32]byte) ([]string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []string
	for tag, got := range t.l().tags[name] {
		if got == d {
			out = append(out, tag)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (t *twin) SubjectManifests(ctx context.Context, _ formats.Tx, repoID int64, name string, subject [32]byte) ([][32]byte, error) {
	ms, _ := t.Referrers(ctx, repoID, name, subject, "", nil, 1<<20)
	var out [][32]byte
	for _, m := range ms {
		out = append(out, m.Digest)
	}
	return out, nil
}

func (t *twin) Children(_ context.Context, _ formats.Tx, _ int64, name string, manifest [32]byte) ([][32]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out [][32]byte
	for k, refs := range t.refs {
		if k.name != name || k.digest != manifest {
			continue
		}
		for _, r := range refs {
			if r.Kind == rows.KindManifest {
				out = append(out, r.Target)
			}
		}
	}
	return out, nil
}

func (t *twin) RecordManifestDeleted(_ context.Context, _ formats.Tx, in rows.ManifestDeleted) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.l().deleted = append(t.l().deleted, in)
	return nil
}

func (t *twin) CatalogNames(_ context.Context, _ int64, after string, limit int) ([]string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	seen := map[string]bool{}
	for k := range t.manifests {
		if k.name > after {
			seen[k.name] = true
		}
	}
	var out []string
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out[:min(len(out), limit)], nil
}

// manifestKey names a manifest row of the single repository under test.
func manifestKey(name string, d [32]byte) mkey { return mkey{0, name, d} }

// digestN is a digest whose first byte is n, so digests sort in the order of their numbers.
func digestN(n byte) [32]byte {
	var d [32]byte
	d[0] = n
	d[31] = 1
	return d
}
