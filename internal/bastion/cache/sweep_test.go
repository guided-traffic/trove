package cache

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/bastion/shared"
	"github.com/guided-traffic/trove/internal/config"
)

// fillTwelve stores twelve objects of 100 bytes (1200 bytes against an artifact limit of 1000) with entries, and
// sets their own times to base+i minutes. It returns their entries.
func (r *rig) fillTwelve() []Entry {
	r.t.Helper()
	base := r.clock.Now().Add(-24 * time.Hour)
	var out []Entry
	for i := range 12 {
		data := payload(uint64(100+i), 100)
		r.fillObject(EntryArtifact, data)
		e := r.entry(EntryArtifact, data)
		if err := r.StoreEntry(context.Background(), fmt.Sprintf("/obj/%d", i), e); err != nil {
			r.t.Fatal(err)
		}
		r.setTime(r.objectPath(EntryArtifact, e.SHA256), base.Add(time.Duration(i)*time.Minute))
		out = append(out, e)
	}
	return out
}

func (r *rig) exists(e Entry) bool {
	_, err := r.Backend().HeadObject(WithArea(context.Background(), e.Kind), e.SHA256)
	return err == nil
}

func TestR_BAS_031_ASweepAboveNinetyFivePercentDeletesByLastAccessDownToEightyFive(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	es := r.fillTwelve()
	// The three oldest are read again: their markers are the newest times.
	for _, i := range []int{0, 1, 2} {
		r.clock.Advance(time.Minute)
		r.Touch(ctx, es[i])
	}
	if err := r.SweepOnce(ctx, nil); err != nil {
		t.Fatal(err)
	}
	// 1200 > 950: delete oldest by last access until at most 850 remain, which is eight objects.
	var kept []int
	for i, e := range es {
		if r.exists(e) {
			kept = append(kept, i)
		}
	}
	want := []int{0, 1, 2, 7, 8, 9, 10, 11}
	if fmt.Sprint(kept) != fmt.Sprint(want) {
		t.Fatalf("kept %v, want %v", kept, want)
	}
	if got := evictions(t, r.m, metrics.EvictSize); got != 4 {
		t.Errorf("size evictions %v, want 4", got)
	}
	if got := val(t, r.m.CacheBytes.WithLabelValues(metrics.AreaArtifacts)); got != 800 {
		t.Errorf("trove_bastion_cache_bytes{artifacts} = %v, want 800", got)
	}
	if got := val(t, r.m.CacheCapacity.WithLabelValues(metrics.AreaArtifacts)); got != 1000 {
		t.Errorf("capacity of artifacts = %v, want 1000", got)
	}
	if got := val(t, r.m.CacheCapacity.WithLabelValues(metrics.AreaDocuments)); got != 200 {
		t.Errorf("capacity of documents = %v, want 200", got)
	}
	// The markers of the deleted objects went with them; the three touched ones stay.
	marks, _ := os.ReadDir(filepath.Join(r.dir, "access"))
	if len(marks) != 3 {
		t.Errorf("%d markers left, want 3", len(marks))
	}
}

func TestR_BAS_031_NothingIsDeletedBelowNinetyFivePercent(t *testing.T) {
	r := newRig(t)
	es := r.fillTwelve()
	// 9 objects are 900 bytes: below 950.
	for _, e := range es[:3] {
		if err := r.Backend().DeleteObject(context.Background(), e.SHA256); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.SweepOnce(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range es {
		if r.exists(e) {
			n++
		}
	}
	if n != 9 || evictions(t, r.m, metrics.EvictSize) != 0 {
		t.Fatalf("%d objects left, %v evictions", n, evictions(t, r.m, metrics.EvictSize))
	}
}

func TestR_BAS_031_DocumentsAreSweptByTheirOwnLimitAndNeverEvictArtifacts(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	art := payload(1, 100)
	r.fillObject(EntryArtifact, art)
	var docs []Entry
	for i := range 3 { // 3 x 70 = 210 > 190 (95 % of 200)
		d := payload(uint64(50+i), 70)
		r.fillObject(EntryDocument, d)
		e := r.entry(EntryDocument, d)
		r.setTime(r.objectPath(EntryDocument, e.SHA256), r.clock.Now().Add(time.Duration(i-10)*time.Hour))
		docs = append(docs, e)
	}
	if err := r.SweepOnce(ctx, nil); err != nil {
		t.Fatal(err)
	}
	// 210 > 190: delete down to 170, which is two documents; the oldest goes.
	if r.exists(docs[0]) || !r.exists(docs[1]) || !r.exists(docs[2]) {
		t.Errorf("documents kept: %v %v %v", r.exists(docs[0]), r.exists(docs[1]), r.exists(docs[2]))
	}
	if _, err := r.Backend().HeadObject(ctx, digest(art)); err != nil {
		t.Errorf("the artifact was touched by a document sweep: %v", err)
	}
}

func TestR_BAS_031_AnEntryWhoseBytesAreGoneIsAMissAndIsRemovedWhenRead(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	es := r.fillTwelve()
	if err := r.Backend().DeleteObject(ctx, es[4].SHA256); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.LookupEntry(ctx, "/obj/4"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an entry without bytes: %v", err)
	}
	if evictions(t, r.m, metrics.EvictMissing) != 1 {
		t.Error("the removal was not counted as missing")
	}
	if _, err := r.Backend().GetEntry(ctx, KeyHash("/obj/4")); !errors.Is(err, ErrNotFound) {
		t.Errorf("the entry is still there: %v", err)
	}
	if _, _, err := r.LookupEntry(ctx, "/obj/5"); err != nil {
		t.Fatalf("the neighbour: %v", err)
	}
}

func TestR_BAS_031_AccessMarkersAreWrittenOncePerResolutionPerReplica(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	data := payload(9, 50)
	r.fillObject(EntryArtifact, data)
	e := r.entry(EntryArtifact, data)
	marker := filepath.Join(r.dir, "access", e.SHA256)
	mtime := func() time.Time {
		fi, err := os.Stat(marker)
		if err != nil {
			t.Fatal(err)
		}
		return fi.ModTime()
	}
	t0 := r.clock.Now()
	r.Touch(ctx, e)
	if !mtime().Equal(t0) {
		t.Fatalf("first touch: %v", mtime())
	}
	r.clock.Advance(59 * time.Minute)
	r.Touch(ctx, e)
	if !mtime().Equal(t0) {
		t.Fatal("a marker was written again inside access_resolution")
	}
	r.clock.Advance(2 * time.Minute)
	r.Touch(ctx, e)
	if !mtime().Equal(t0.Add(61 * time.Minute)) {
		t.Fatalf("no marker after access_resolution: %v", mtime())
	}
}

func TestR_BAS_031_ARefillCountsAsAnAccess(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	es := r.fillTwelve()
	// Object 0 has the oldest own time; a marker older than a later refill does not hide the refill.
	old := r.clock.Now().Add(-48 * time.Hour)
	if err := r.Backend().Touch(ctx, es[0].SHA256); err != nil {
		t.Fatal(err)
	}
	r.setTime(filepath.Join(r.dir, "access", es[0].SHA256), old)
	r.setTime(r.objectPath(EntryArtifact, es[0].SHA256), r.clock.Now()) // refilled just now
	if err := r.SweepOnce(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if !r.exists(es[0]) {
		t.Fatal("a freshly refilled object was evicted on the strength of an old marker")
	}
}

func TestR_BAS_031_OnlyTheReplicaWithTheSweepLeaseSweepsABucket(t *testing.T) {
	r := newRig(t, func(c *config.BastionCache) { c.Backend = "bucket" }) // the lease is the bucket's; the store is a volume
	ctx := context.Background()
	es := r.fillTwelve()
	st := shared.NewLocal(1, nil)
	lease := &fakeLease{}
	st.Sweep, st.Shared = lease, func() bool { return true }

	if err := r.SweepOnce(ctx, st); err != nil {
		t.Fatal(err)
	}
	if lease.calls != 1 || lease.ttl != 2*time.Minute {
		t.Fatalf("lease: %d calls, ttl %v (want 2 x sweep_interval)", lease.calls, lease.ttl)
	}
	for _, e := range es {
		if !r.exists(e) {
			t.Fatal("a replica without the lease swept")
		}
	}
	lease.held = true
	if err := r.SweepOnce(ctx, st); err != nil {
		t.Fatal(err)
	}
	if !lease.released || r.exists(es[0]) {
		t.Fatalf("the lease holder did not sweep (released %v)", lease.released)
	}

	// Without Valkey no lease is asked for.
	lease2 := &fakeLease{}
	st2 := shared.NewLocal(2, nil)
	st2.Sweep = lease2
	if err := r.SweepOnce(ctx, st2); err != nil || lease2.calls != 0 {
		t.Fatalf("lease asked for without Valkey: %d %v", lease2.calls, err)
	}
}

func TestR_BAS_031_AVolumeIsSweptByItsOwnReplicaWithoutALease(t *testing.T) {
	r := newRig(t)
	es := r.fillTwelve()
	lease := &fakeLease{}
	st := shared.NewLocal(3, nil)
	st.Sweep, st.Shared = lease, func() bool { return true }
	if err := r.SweepOnce(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if lease.calls != 0 || r.exists(es[0]) {
		t.Fatalf("volume sweep: lease calls %d, oldest kept %v", lease.calls, r.exists(es[0]))
	}
}

func TestR_BAS_031_TheIntervalOfABucketIsRandomizedAroundTheConfiguredOne(t *testing.T) {
	r := newRig(t, func(c *config.BastionCache) { c.Backend = "bucket"; c.SweepInterval = 10 * time.Minute })
	seen := map[time.Duration]bool{}
	for range 200 {
		d := r.interval()
		if d < 8*time.Minute || d > 12*time.Minute {
			t.Fatalf("interval %v outside +-20 %%", d)
		}
		seen[d] = true
	}
	if len(seen) < 100 {
		t.Errorf("only %d distinct intervals in 200 draws", len(seen))
	}
	v := newRig(t, func(c *config.BastionCache) { c.SweepInterval = 10 * time.Minute })
	if v.interval() != 10*time.Minute {
		t.Errorf("a volume sweeps at the configured interval, got %v", v.interval())
	}
}

type fakeLease struct {
	held     bool
	calls    int
	ttl      time.Duration
	released bool
}

func (l *fakeLease) TryAcquire(_ context.Context, ttl time.Duration) (bool, func(), error) {
	l.calls++
	l.ttl = ttl
	return l.held, func() { l.released = true }, nil
}
