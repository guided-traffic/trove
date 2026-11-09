package verify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/blob/blobtest"
)

// fakeDB is a Source over a slice of rows; it records the page sizes it was asked for.
type fakeDB struct {
	rows  []Row
	items map[blob.Digest][]Item
	since []time.Time
	pages []int
}

func (f *fakeDB) Page(_ context.Context, after blob.Digest, first bool, since time.Time, limit int) ([]Row, error) {
	f.pages = append(f.pages, limit)
	f.since = append(f.since, since)
	var out []Row
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

func (f *fakeDB) Items(_ context.Context, d blob.Digest, limit int) ([]Item, int64, error) {
	all := f.items[d]
	n := int64(len(all))
	if len(all) > limit {
		all = all[:limit]
	}
	return all, n, nil
}

func fixture(t *testing.T, n int) (*fakeDB, *blobtest.Store) {
	t.Helper()
	store := blobtest.New()
	db := &fakeDB{items: map[blob.Digest][]Item{}}
	for i := range n {
		data := []byte{byte(i), byte(i >> 8), 'x'}
		d := blob.Digest(sha256.Sum256(data))
		store.Set(blob.Key("trove", d), data, time.Now())
		db.rows = append(db.rows, Row{Digest: d, Size: int64(len(data))})
		db.items[d] = []Item{{Project: "p", Repository: "npm-local", Path: "pkg/" + d.Hex()[:6]}}
	}
	sort.Slice(db.rows, func(i, j int) bool { return bytes.Compare(db.rows[i].Digest[:], db.rows[j].Digest[:]) < 0 })
	return db, store
}

func noSleep(context.Context, time.Duration) error { return nil }

// TestR_HA_024_VerifyReportsEveryItemWithoutItsObject: a run over a table of several pages finds exactly the blobs whose
// object was removed or has another size, names their items, and reads the table in pages of the configured size.
func TestR_HA_024_VerifyReportsEveryItemWithoutItsObject(t *testing.T) {
	db, store := fixture(t, 25)
	gone := db.rows[3].Digest
	short := db.rows[17].Digest
	if err := store.Delete(context.Background(), blob.Key("trove", gone)); err != nil {
		t.Fatal(err)
	}
	store.Set(blob.Key("trove", short), []byte("z"), time.Now())
	db.items[gone] = append(db.items[gone], Item{Project: "p", Repository: "npm-local", Path: "other", Trashed: true})

	rep, err := Run(context.Background(), Options{DB: db, Objects: store, Prefix: "trove", PageSize: 10, Concurrency: 4, MaxItems: 1, Sleep: noSleep})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Checked != 25 || rep.Missing != 1 || rep.SizeMismatch != 1 || len(rep.Findings) != 2 || rep.OK() {
		t.Fatalf("report = %+v", rep)
	}
	if rep.ItemsAffected != 3 {
		t.Errorf("ItemsAffected = %d, want 3", rep.ItemsAffected)
	}
	f := rep.Findings[0]
	if f.Kind != KindMissing || f.SHA256 != gone.Hex() || len(f.Items) != 1 || f.ItemCount != 2 {
		t.Errorf("first finding = %+v, want the missing blob with one listed item of two", f)
	}
	if g := rep.Findings[1]; g.Kind != KindSizeMismatch || g.ObjectSize != 1 || g.Size != 3 {
		t.Errorf("second finding = %+v", g)
	}
	if len(db.pages) != 4 { // 10 + 10 + 5 + the empty page that ends the run
		t.Errorf("pages = %v, want 4 queries", db.pages)
	}
	for _, p := range db.pages {
		if p != 10 {
			t.Errorf("a query asked for %d rows, want the page size 10", p)
		}
	}
}

// TestR_HA_024_VerifyOfAnIntactStoreIsOK: no finding, nothing unchecked.
func TestR_HA_024_VerifyOfAnIntactStoreIsOK(t *testing.T) {
	db, store := fixture(t, 7)
	rep, err := Run(context.Background(), Options{DB: db, Objects: store, Prefix: "trove", Sleep: noSleep})
	if err != nil || !rep.OK() || rep.Checked != 7 {
		t.Fatalf("rep = %+v, err = %v", rep, err)
	}
}

// TestR_HA_025_VerifyPassesTheSinceCutOffToTheTable: the cut-off reaches the query, so that a restore drill can narrow a run.
func TestR_HA_025_VerifyPassesTheSinceCutOffToTheTable(t *testing.T) {
	db, store := fixture(t, 2)
	cut := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	rep, err := Run(context.Background(), Options{DB: db, Objects: store, Prefix: "trove", Since: cut, Sleep: noSleep})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Since.Equal(cut) || !db.since[0].Equal(cut) {
		t.Errorf("since = %v / %v, want %v", rep.Since, db.since[0], cut)
	}
}

// TestR_STOR_027_VerifyTreatsAStoreErrorAsUncheckedNotMissing: a 5xx that outlasts the attempts is not a lost object; a
// permission error ends the run.
func TestR_STOR_027_VerifyTreatsAStoreErrorAsUncheckedNotMissing(t *testing.T) {
	db, store := fixture(t, 3)
	flaky := db.rows[1].Digest
	var calls atomic.Int32
	store.SetFault(func(op, key string) error {
		if op == "head" && strings.HasSuffix(key, flaky.Hex()) {
			calls.Add(1)
			return &blob.StoreError{Op: "HeadObject", Kind: blob.KindTransient, Status: 503}
		}
		return nil
	})
	rep, err := Run(context.Background(), Options{DB: db, Objects: store, Prefix: "trove", Sleep: noSleep})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Unchecked) != 1 || rep.Unchecked[0].SHA256 != flaky.Hex() || rep.Missing != 0 || calls.Load() != headAttempts || rep.OK() {
		t.Errorf("rep = %+v, attempts = %d", rep, calls.Load())
	}

	store.SetFault(func(op, _ string) error {
		if op == "head" {
			return &blob.StoreError{Op: "HeadObject", Kind: blob.KindConfig, Code: "AccessDenied", Status: 403}
		}
		return nil
	})
	_, err = Run(context.Background(), Options{DB: db, Objects: store, Prefix: "trove", Sleep: noSleep})
	if !blob.IsConfig(err) {
		t.Errorf("err = %v, want a configuration error of the store", err)
	}
}

// TestR_HA_024_VerifyRetriesATransientErrorBeforeItGivesUp: one failure and then an answer is a checked blob.
func TestR_HA_024_VerifyRetriesATransientErrorBeforeItGivesUp(t *testing.T) {
	db, store := fixture(t, 1)
	var n atomic.Int32
	store.SetFault(func(op, _ string) error {
		if op == "head" && n.Add(1) == 1 {
			return errors.New("connection reset")
		}
		return nil
	})
	rep, err := Run(context.Background(), Options{DB: db, Objects: store, Prefix: "trove", Sleep: noSleep})
	if err != nil || !rep.OK() || rep.Checked != 1 {
		t.Fatalf("rep = %+v, err = %v", rep, err)
	}
}
