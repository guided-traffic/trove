package restoreversions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/blob/verify"
)

// fakeDB is a verify.Source over a slice of rows.
type fakeDB struct{ rows []verify.Row }

func (f *fakeDB) Page(_ context.Context, after blob.Digest, first bool, _ time.Time, limit int) ([]verify.Row, error) {
	var out []verify.Row
	for _, r := range f.rows {
		if !first && bytes.Compare(r.Digest[:], after[:]) <= 0 {
			continue
		}
		out = append(out, r)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (f *fakeDB) Items(context.Context, blob.Digest, int) ([]verify.Item, int64, error) {
	return []verify.Item{{Project: "p", Repository: "npm-local", Path: "a"}}, 1, nil
}

// fakeStore is a versioned bucket: the versions of a key, oldest first; a plain read sees the newest.
type fakeStore struct {
	versions map[string][]s3.ObjectVersion
	sizes    map[string]int64
	deleted  []string
	denyList error
	seq      int
}

func (s *fakeStore) put(key string, size int64, at time.Time) {
	s.seq++
	s.versions[key] = append(s.versions[key], s3.ObjectVersion{ID: "v" + string(rune('0'+s.seq)), Modified: at})
	s.sizes[key] = size
}

func (s *fakeStore) mark(key string, at time.Time) {
	s.seq++
	s.versions[key] = append(s.versions[key], s3.ObjectVersion{ID: "m" + string(rune('0'+s.seq)), Marker: true, Modified: at})
}

func (s *fakeStore) Head(_ context.Context, key string) (blob.ObjectInfo, error) {
	vs := s.versions[key]
	if len(vs) == 0 || vs[len(vs)-1].Marker {
		return blob.ObjectInfo{}, blob.ErrNotFound
	}
	return blob.ObjectInfo{Size: s.sizes[key]}, nil
}

func (s *fakeStore) ListVersions(_ context.Context, key string) ([]s3.ObjectVersion, error) {
	if s.denyList != nil {
		return nil, s.denyList
	}
	vs := s.versions[key]
	out := make([]s3.ObjectVersion, 0, len(vs))
	for i := len(vs) - 1; i >= 0; i-- {
		out = append(out, vs[i])
	}
	return out, nil
}

func (s *fakeStore) DeleteVersion(_ context.Context, key, id string) error {
	vs := s.versions[key]
	for i, v := range vs {
		if v.ID == id {
			if !v.Marker {
				return errors.New("a version of an object must never be deleted")
			}
			s.versions[key] = append(vs[:i:i], vs[i+1:]...)
			s.deleted = append(s.deleted, key+"@"+id)
			return nil
		}
	}
	return errors.New("no such version")
}

type fixture struct {
	db    *fakeDB
	store *fakeStore
	t0    time.Time
	keys  map[string]string // name -> object key
}

// newFixture holds four blobs, all with rows in the database: "restorable" (object at t0-2h, delete marker at t0+1h),
// "intact" (object only), "gone-before" (object at t0-3h, delete marker at t0-1h, so deleted before t) and "never" (no version at
// all). t is t0.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{db: &fakeDB{}, store: &fakeStore{versions: map[string][]s3.ObjectVersion{}, sizes: map[string]int64{}}, t0: time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC), keys: map[string]string{}}
	add := func(name string) string {
		d := blob.Digest(sha256.Sum256([]byte(name)))
		f.db.rows = append(f.db.rows, verify.Row{Digest: d, Size: 3})
		key := blob.Key("trove", d)
		f.keys[name] = key
		return key
	}
	k := add("restorable")
	f.store.put(k, 3, f.t0.Add(-2*time.Hour))
	f.store.mark(k, f.t0.Add(time.Hour))
	f.store.mark(k, f.t0.Add(2*time.Hour))
	f.store.put(add("intact"), 3, f.t0.Add(-time.Hour))
	k = add("gone-before")
	f.store.put(k, 3, f.t0.Add(-3*time.Hour))
	f.store.mark(k, f.t0.Add(-time.Hour))
	add("never")
	sort.Slice(f.db.rows, func(i, j int) bool { return bytes.Compare(f.db.rows[i].Digest[:], f.db.rows[j].Digest[:]) < 0 })
	return f
}

func (f *fixture) entry(rep *Report, name string) Entry {
	for _, e := range rep.Entries {
		if e.Key == f.keys[name] {
			return e
		}
	}
	return Entry{}
}

// TestR_HA_025_RestoreVersionsRemovesTheDeleteMarkersYoungerThanTOfMissingBlobsOnly: of four blobs, the one whose object a
// delete marker younger than t hides is current again and loses both markers; the blob whose marker is older than t, the blob
// without any version and the intact blob are left alone, and no object version is deleted.
func TestR_HA_025_RestoreVersionsRemovesTheDeleteMarkersYoungerThanTOfMissingBlobsOnly(t *testing.T) {
	f := newFixture(t)
	rep, err := Run(context.Background(), Options{DB: f.db, Store: f.store, Prefix: "trove", Before: f.t0, PageSize: 2, Concurrency: 2})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Checked != 4 || rep.Missing != 3 || rep.Restored != 1 || rep.MarkersRemoved != 2 || rep.Remaining != 2 || rep.OK() {
		t.Fatalf("report = %+v", rep)
	}
	if e := f.entry(rep, "restorable"); e.Outcome != OutcomeRestored || e.MarkersRemoved != 2 {
		t.Errorf("restorable = %+v", e)
	}
	if e := f.entry(rep, "gone-before"); e.Outcome != OutcomeNoDeleteMarker || e.MarkersRemoved != 0 {
		t.Errorf("gone-before = %+v, want its marker (older than t) left", e)
	}
	if e := f.entry(rep, "never"); e.Outcome != OutcomeNoDeleteMarker {
		t.Errorf("never = %+v", e)
	}
	if got := len(f.store.versions[f.keys["gone-before"]]); got != 2 {
		t.Errorf("gone-before has %d versions, want 2: its marker is older than t and stays", got)
	}
	if _, err := f.store.Head(context.Background(), f.keys["restorable"]); err != nil {
		t.Errorf("the object of restorable is not current: %v", err)
	}
	if len(f.store.deleted) != 2 {
		t.Errorf("deleted = %v, want the two markers of restorable", f.store.deleted)
	}
}

// TestR_HA_025_RestoreVersionsDryRunRemovesNothing: a dry run reports what a run would restore and leaves the bucket as it is.
func TestR_HA_025_RestoreVersionsDryRunRemovesNothing(t *testing.T) {
	f := newFixture(t)
	rep, err := Run(context.Background(), Options{DB: f.db, Store: f.store, Prefix: "trove", Before: f.t0, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if e := f.entry(rep, "restorable"); e.Outcome != OutcomeWouldRestore || e.MarkersRemoved != 2 {
		t.Errorf("restorable = %+v", e)
	}
	if len(f.store.deleted) != 0 || rep.Restored != 1 || rep.MarkersRemoved != 0 {
		t.Errorf("deleted = %v, restored = %d: a dry run changes nothing", f.store.deleted, rep.Restored)
	}
	if _, err := f.store.Head(context.Background(), f.keys["restorable"]); !errors.Is(err, blob.ErrNotFound) {
		t.Errorf("the object is current after a dry run: %v", err)
	}
}

// TestR_HA_025_RestoreVersionsKeepsAMarkerWhenNoVersionIsBelow: a key whose versions are only delete markers is reported and
// nothing is removed from it.
func TestR_HA_025_RestoreVersionsKeepsAMarkerWhenNoVersionIsBelow(t *testing.T) {
	f := newFixture(t)
	k := f.keys["never"]
	f.store.mark(k, f.t0.Add(time.Minute))
	rep, err := Run(context.Background(), Options{DB: f.db, Store: f.store, Prefix: "trove", Before: f.t0})
	if err != nil {
		t.Fatal(err)
	}
	if e := f.entry(rep, "never"); e.Outcome != OutcomeNoVersion || len(f.store.versions[k]) != 1 {
		t.Errorf("never = %+v, versions %v", e, f.store.versions[k])
	}
}

// TestR_HA_025_RestoreVersionsStopsAtAPermissionError: a store that refuses the listing of versions ends the run with that error
// and a report of what was done until then.
func TestR_HA_025_RestoreVersionsStopsAtAPermissionError(t *testing.T) {
	f := newFixture(t)
	f.store.denyList = &blob.StoreError{Op: "ListObjectVersions", Status: 403, Code: "AccessDenied", Kind: blob.KindConfig, Err: errors.New("denied")}
	rep, err := Run(context.Background(), Options{DB: f.db, Store: f.store, Prefix: "trove", Before: f.t0})
	if err == nil || !blob.IsConfig(err) {
		t.Fatalf("err = %v, want a configuration error", err)
	}
	if rep == nil || rep.Missing != 3 || len(f.store.deleted) != 0 {
		t.Errorf("report = %+v deleted %v", rep, f.store.deleted)
	}
}

// TestR_HA_025_RestoreVersionsNeedsATime: without t the run is refused before it asks the store anything.
func TestR_HA_025_RestoreVersionsNeedsATime(t *testing.T) {
	f := newFixture(t)
	if _, err := Run(context.Background(), Options{DB: f.db, Store: f.store}); err == nil {
		t.Fatal("a run without a time was accepted")
	}
}
