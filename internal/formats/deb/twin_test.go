package deb

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strconv"
	"sync"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
)

// restStore carries the groups of the data port that the local class does not use. A call panics on the nil interface, so a test that
// reaches a group of another work package fails loudly. It sits one level deeper than the twin, whose methods win.
type restStore struct{ Store }

// twinStore is the in-memory twin of the local class's part of the data port: the stored files, their coordinates, the marks and
// the journaled events. It keeps what a transaction wrote even when the test never ends it, which is enough for the conformance kit and
// the handler tests; the guarantees of the SQL (the unique key, the row locks) are tested against PostgreSQL.
type twinStore struct {
	mu       sync.Mutex
	binaries map[int64]*rows.Binary
	seqs     map[string]int64 // "<repo>/<suite>" -> change_seq
	tombs    map[string]rows.Tombstone
	arrival  []int64
	// Events are the journaled events in the order they were written: the audit event type and its path.
	Events []string
}

func newTwin() *twinStore {
	return &twinStore{binaries: map[int64]*rows.Binary{}, seqs: map[string]int64{}, tombs: map[string]rows.Tombstone{}}
}

type twinPort struct {
	*twinStore
	restStore
}

func (t *twinStore) raise(repo int64, suites ...string) {
	for _, s := range suites {
		t.seqs[key(repo, s)]++
	}
}

func key(repo int64, s string) string { return strconv.FormatInt(repo, 10) + "/" + s }

var errNoFile = errors.New("no such package file")

func (t *twinStore) seqsOf(repo int64, cs []rows.Coordinate) []rows.SuiteSeq {
	seen := map[string]bool{}
	var out []rows.SuiteSeq
	for _, c := range cs {
		if !seen[c.Suite] {
			seen[c.Suite] = true
			n := t.seqs[key(repo, c.Suite)]
			out = append(out, rows.SuiteSeq{Suite: c.Suite, ChangeSeq: n, PublishedSeq: 0})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Suite < out[j].Suite })
	return out
}

func (t *twinStore) RecordUpload(_ context.Context, _ formats.Tx, in rows.UploadInput) (rows.UploadResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var res rows.UploadResult
	b, ok := t.binaries[in.ItemID]
	if !ok {
		c := in.Binary
		c.Coordinates = nil
		b = &c
		t.binaries[in.ItemID] = b
		t.arrival = append(t.arrival, in.ItemID)
		res.Created = true
	}
	touched := map[string]bool{}
	for _, c := range in.Coordinates {
		if !slices.Contains(b.Coordinates, c) {
			b.Coordinates = append(b.Coordinates, c)
			res.Added = append(res.Added, c)
			touched[c.Suite] = true
		}
	}
	for s := range touched {
		t.raise(in.RepoID, s)
	}
	res.Coordinates = slices.Clone(b.Coordinates)
	res.Suites = t.seqsOf(in.RepoID, b.Coordinates)
	return res, nil
}

func (t *twinStore) SetCoordinates(_ context.Context, _ formats.Tx, in rows.CoordinatesChange) (rows.CoordinatesChanged, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out rows.CoordinatesChanged
	for _, b := range t.binaries {
		if b.RepoID != in.RepoID || b.Path != in.Path {
			continue
		}
		out.ItemID, out.Package, out.Version, out.SHA256 = b.ItemID, b.Package, b.Version, b.SHA256
		for _, c := range b.Coordinates {
			if !slices.Contains(in.Coordinates, c) {
				out.Removed = append(out.Removed, c)
				t.raise(in.RepoID, c.Suite)
			}
		}
		for _, c := range in.Coordinates {
			if !slices.Contains(b.Coordinates, c) {
				out.Added = append(out.Added, c)
				t.raise(in.RepoID, c.Suite)
			}
		}
		b.Coordinates = slices.Clone(in.Coordinates)
		out.Suites = t.seqsOf(in.RepoID, in.Coordinates)
		return out, nil
	}
	return out, errNoFile
}

func (t *twinStore) leave(repo, item int64, purge bool, trashed bool) (rows.RemovedFile, bool) {
	b, ok := t.binaries[item]
	if !ok || b.RepoID != repo {
		return rows.RemovedFile{}, false
	}
	var suites []string
	for _, c := range b.Coordinates {
		if !slices.Contains(suites, c.Suite) {
			suites = append(suites, c.Suite)
		}
	}
	t.raise(repo, suites...)
	out := rows.RemovedFile{ItemID: item, Path: b.Path, SHA256: b.SHA256, Package: b.Package, Version: b.Version, Architecture: b.Architecture, Suites: suites}
	if purge {
		delete(t.binaries, item)
	} else {
		b.Trashed = trashed
	}
	for _, o := range t.binaries {
		if o.RepoID == repo && o.Package == b.Package && o.Version == b.Version && !o.Trashed && o.ItemID != item {
			out.VersionFiles++
		}
	}
	return out, true
}

func (t *twinStore) Removed(_ context.Context, _ formats.Tx, repoID, itemID int64, _ string, _ [32]byte, purge bool) (rows.RemovedFile, bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	out, ok := t.leave(repoID, itemID, purge, true)
	return out, ok, nil
}

func (t *twinStore) Restored(_ context.Context, _ formats.Tx, repoID, itemID int64) (rows.RemovedFile, bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	out, ok := t.leave(repoID, itemID, false, false)
	return out, ok, nil
}

func (t *twinStore) Binary(_ context.Context, repoID int64, path string) (rows.Binary, bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, b := range t.binaries {
		if b.RepoID == repoID && b.Path == path {
			return *b, true, nil
		}
	}
	return rows.Binary{}, false, nil
}

func (t *twinStore) BinaryOf(_ context.Context, _ formats.Tx, repoID int64, pkg, version, architecture string) (rows.Binary, bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, b := range t.binaries {
		if b.RepoID == repoID && b.Package == pkg && b.Version == version && b.Architecture == architecture && !b.Trashed {
			return *b, true, nil
		}
	}
	return rows.Binary{}, false, nil
}

func (t *twinStore) RecordPackageDeleted(_ context.Context, _ formats.Tx, in rows.PackageDeleted) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Events = append(t.Events, "deb.package.deleted "+in.Path)
	return nil
}

func (t *twinStore) RecordCoordinatesRemoved(_ context.Context, _ formats.Tx, in rows.CoordinatesRemoved) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Events = append(t.Events, "deb.coordinates.removed "+in.Path)
	return nil
}

func (t *twinStore) RecordCoordinatesChanged(_ context.Context, _ formats.Tx, in rows.CoordinatesChangedEvent) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Events = append(t.Events, "deb.coordinates.changed "+in.Path)
	return nil
}

func (t *twinStore) Tombstone(_ context.Context, repoID int64, path string) (rows.Tombstone, bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	m, ok := t.tombs[key(repoID, path)]
	return m, ok, nil
}

func (t *twinStore) Stored(_ context.Context, repoID int64, pkg string, limit int) ([]string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []string
	for i := len(t.arrival) - 1; i >= 0; i-- {
		b, ok := t.binaries[t.arrival[i]]
		if !ok || b.RepoID != repoID || b.Package != pkg || b.Trashed || slices.Contains(out, b.Version) {
			continue
		}
		out = append(out, b.Version)
	}
	if limit <= 0 {
		return nil, nil
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (t *twinStore) Describe(context.Context, []int64) (map[int64]rows.VersionView, error) {
	return map[int64]rows.VersionView{}, nil
}

func (t *twinStore) Find(context.Context, []string, string, int) ([]int64, error) { return nil, nil }

func (t *twinStore) Withheld(context.Context, []string) ([]int64, error) { return nil, nil }

var _ Store = twinPort{}
