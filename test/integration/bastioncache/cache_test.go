//go:build integration

package bastioncache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/cache"
	"github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/bastion/shared"
	"github.com/guided-traffic/trove/internal/config"
)

func TestR_BAS_030_TwoReplicasSharingABucketSeeEachOthersBytes(t *testing.T) {
	e := newEnv(t, "shared")
	ctx := context.Background()
	a, b := e.replica(), e.replica()
	if err := a.Ready(ctx); err != nil {
		t.Fatalf("replica A: %v", err)
	}
	if err := b.Ready(ctx); err != nil {
		t.Fatalf("replica B: %v", err)
	}

	data := payload(1, 300*1024)
	key := "/api/npm/it/npm-all/lodash/-/lodash-4.17.21.tgz"
	want := a.put(t, cache.EntryArtifact, key, data)

	// B finds the entry and the bytes A filled, without having seen the request.
	got, err := b.read(key)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("replica B did not read A's bytes: %d bytes, %v", len(got), err)
	}
	entry, info, err := b.LookupEntry(ctx, key)
	if err != nil || entry.SHA256 != want.SHA256 || entry.Repository != "it/npm-all" || info.Size != int64(len(data)) {
		t.Fatalf("entry as B sees it: %+v %+v %v", entry, info, err)
	}
	// A shared document has its own area and is shared too.
	doc := payload(2, 4096)
	a.put(t, cache.EntryDocument, "/api/npm/it/npm-all/lodash", doc)
	if got, err := b.read("/api/npm/it/npm-all/lodash"); err != nil || !bytes.Equal(got, doc) {
		t.Fatalf("document: %v", err)
	}
	// The bytes are kept once per content address, however many keys point at them.
	a.put(t, cache.EntryArtifact, key+"?other=1", data)
	n := 0
	for _, o := range e.listAll() {
		if strings.HasPrefix(o.Key, "sha256/") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d objects below sha256/ for one content address", n)
	}
	// The audit spool is shared the same way (R-BAS-039): A writes, B lists, reads and deletes.
	if err := a.Backend().SpoolWrite(ctx, "bastion-a", []byte(`{"e":1}`+"\n")); err != nil {
		t.Fatal(err)
	}
	files, err := b.Backend().SpoolList(ctx)
	if err != nil || len(files) != 1 || files[0].Replica != "bastion-a" {
		t.Fatalf("spool as B sees it: %+v %v", files, err)
	}
	if body, err := b.Backend().SpoolRead(ctx, files[0]); err != nil || string(body) != `{"e":1}`+"\n" {
		t.Fatalf("spool read: %q %v", body, err)
	}
	if err := b.Backend().SpoolDelete(ctx, files[0]); err != nil {
		t.Fatal(err)
	}
	if files, _ := a.Backend().SpoolList(ctx); len(files) != 0 {
		t.Errorf("spool after delete: %+v", files)
	}
}

func TestR_BAS_030_OnlyTheFiveOperationsAreUsedAndTheLayoutIsTheSpecs(t *testing.T) {
	e := newEnv(t, "ops")
	ctx := context.Background()
	r := e.replica(func(c *config.BastionCache) { c.MaxBytes, c.DocumentsMaxBytes = 30*1024, 10*1024 })
	if err := r.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	art, doc := payload(3, 10*1024), payload(4, 2048)
	ea := r.put(t, cache.EntryArtifact, "/a", art)
	r.put(t, cache.EntryDocument, "/d", doc)
	if _, err := r.read("/a"); err != nil {
		t.Fatal(err)
	}
	rc, err := r.OpenRange(ctx, ea, 100, 50)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, rc)
	_ = rc.Close()
	r.Touch(ctx, ea)
	eb := r.put(t, cache.EntryArtifact, "/b", payload(5, 10*1024))
	r.Touch(ctx, eb)
	// 20 KiB of an artifact limit of 20 KiB: the sweep lists, and deletes the older of the two.
	if err := r.SweepOnce(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := r.Backend().SpoolWrite(ctx, "bastion-0", []byte("{}\n")); err != nil {
		t.Fatal(err)
	}
	files, _ := r.Backend().SpoolList(ctx)
	if _, err := r.Backend().SpoolRead(ctx, files[0]); err != nil {
		t.Fatal(err)
	}
	_ = r.Backend().SpoolDelete(ctx, files[0])
	if err := r.DeleteEntry(ctx, "/d"); err != nil {
		t.Fatal(err)
	}

	// The layout of what is left.
	seen := map[string]int{}
	for _, o := range e.listAll() {
		if !layout.MatchString(o.Key) {
			t.Errorf("object %q is not in the layout of R-BAS-030", o.Key)
		}
		seen[strings.SplitN(o.Key, "/", 2)[0]]++
	}
	for _, dir := range []string{"entries", "access"} {
		if seen[dir] == 0 {
			t.Errorf("nothing below %s/ (saw %v)", dir, seen)
		}
	}
	if seen["sha256"]+seen["documents"] == 0 {
		t.Errorf("no bytes (saw %v)", seen)
	}

	ops := e.count.counts()
	for _, op := range []string{"PutObject", "GetObject", "HeadObject", "ListObjectsV2", "DeleteObject"} {
		if ops[op] == 0 {
			t.Errorf("%s was never used; the test does not cover it (counts %v)", op, ops)
		}
	}
	if len(ops) != 5 {
		t.Errorf("operations used: %v", sortedKeys(ops))
	}
	e.count.mu.Lock()
	defer e.count.mu.Unlock()
	if len(e.count.bad) != 0 {
		t.Errorf("requests outside the five operations, or with versioning or a conditional write:\n%s", strings.Join(e.count.bad, "\n"))
	}
}

func TestR_BAS_030_ABucketWithTheWrongCredentialIsNotReady(t *testing.T) {
	e := newEnv(t, "creds")
	if err := os.WriteFile(e.creds.SecretAccessKeyFile, []byte("not-the-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := e.replica()
	if err := r.Ready(context.Background()); err == nil {
		t.Fatal("a bucket the replica cannot write is ready")
	}
}

func TestR_BAS_030_AFailingBucketAfterReadinessTurnsHitsIntoCountedMisses(t *testing.T) {
	e := newEnv(t, "down")
	ctx := context.Background()
	r := e.replica()
	if err := r.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	data := payload(6, 10*1024)
	r.put(t, cache.EntryArtifact, "/k", data)
	if _, err := r.read("/k"); err != nil {
		t.Fatal(err)
	}

	e.count.down.Store(true)
	if err := r.Ready(ctx); err != nil {
		t.Fatalf("readiness is gated at the first check only: %v", err)
	}
	if _, err := r.read("/k"); err == nil || errors.Is(err, cache.ErrNotFound) {
		t.Fatalf("an unreachable bucket must be an error the caller passes through as a miss: %v", err)
	}
	if value(t, r.m.CacheErrors.WithLabelValues(metrics.OpGet)) == 0 {
		t.Error("trove_bastion_cache_errors_total{op=get} was not counted")
	}
	e.count.down.Store(false)
	if got, err := r.read("/k"); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("the bucket is back: %v", err)
	}
}

func TestR_BAS_030_KillingTheEphemeralVolumeOfAReplicaLosesOnlyItsCache(t *testing.T) {
	// The volume backend: the pod's volume is gone, the new pod starts empty and works.
	ctx := context.Background()
	dir := t.TempDir() + "/cache"
	cfg := config.BastionCache{
		Backend: "volume", MaxBytes: 100 * mib, MaxObjectBytes: mib, DocumentsMaxBytes: 10 * mib,
		SweepInterval: time.Minute, AccessResolution: time.Hour, Volume: config.BastionCacheVolume{Dir: dir},
	}
	signer := newEnvSigner(t)
	open := func() *cache.Cache {
		c, err := cache.New(ctx, cfg, signer, cache.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Ready(ctx); err != nil {
			t.Fatal(err)
		}
		return c
	}
	v := open()
	data := payload(7, 50*1024)
	fillAndStore(t, v, "/k", data)
	if got, err := readVia(v, "/k"); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("before: %v", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	v2 := open()
	if _, err := readVia(v2, "/k"); !errors.Is(err, cache.ErrNotFound) {
		t.Fatalf("the cache outlived its volume: %v", err)
	}
	fillAndStore(t, v2, "/k", data)
	if got, err := readVia(v2, "/k"); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("after the refill: %v", err)
	}

	// The bucket backend: the pod's temporary volume is the only thing a pod owns; the cache is in the bucket.
	e := newEnv(t, "pod")
	a := e.replica()
	a.put(t, cache.EntryArtifact, "/k", data)
	fl, err := a.NewFill(cache.EntryArtifact) // a fill in flight when the pod dies
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fl.Write(data[:1000])
	if err := os.RemoveAll(a.tmp); err != nil {
		t.Fatal(err)
	}
	b := e.replica() // the replacement pod, a new tmp
	if err := b.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := b.read("/k"); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("the bucket's cache did not survive the pod: %v", err)
	}
	if err := fl.Commit(ctx, digestOf(data)); err == nil {
		t.Error("a fill whose spool was killed was committed")
	}
}

func TestR_BAS_031_EvictionKeepsTheRecentlyReadTwentyMiBOfThreeHundred(t *testing.T) {
	e := newEnv(t, "evict")
	ctx := context.Background()
	r := e.replica(func(c *config.BastionCache) {
		c.MaxBytes, c.DocumentsMaxBytes, c.MaxObjectBytes = 100*mib, 10*mib, 2*mib
	})
	if err := r.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	const objects = 300
	keys := make([]string, objects)
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i := range objects {
		keys[i] = fmt.Sprintf("/tarball/%03d.tgz", i)
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			r.put(t, cache.EntryArtifact, keys[i], payload(uint64(1000+i), mib))
		}()
	}
	wg.Wait()
	if t.Failed() {
		t.FailNow()
	}
	// The store dates objects to the second: the reads come after the last fill by more than that.
	time.Sleep(2500 * time.Millisecond)
	for i := range 20 {
		if _, err := r.read(keys[i]); err != nil {
			t.Fatalf("re-read %d: %v", i, err)
		}
		e, _, _ := r.LookupEntry(ctx, keys[i])
		r.Touch(ctx, e)
	}

	if err := r.SweepOnce(ctx, nil); err != nil {
		t.Fatal(err)
	}
	u, err := r.Backend().Usage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if u.Artifacts >= 100*mib || u.Artifacts > 90*mib*85/100 {
		t.Fatalf("after the sweep %d MiB are cached, want at most 85 %% of the 90 MiB artifact limit", u.Artifacts/mib)
	}
	for i := range 20 {
		if _, err := r.read(keys[i]); err != nil {
			t.Errorf("tarball %d, read again before the sweep, was evicted: %v", i, err)
		}
	}
	gone := 0
	for i := 20; i < objects; i++ {
		if _, _, err := r.LookupEntry(ctx, keys[i]); errors.Is(err, cache.ErrNotFound) {
			gone++
		}
	}
	if gone < 200 {
		t.Errorf("only %d of the 280 untouched tarballs were evicted", gone)
	}
	if got := value(t, r.m.CacheEvictions.WithLabelValues(metrics.EvictSize)); got < 200 {
		t.Errorf("trove_bastion_cache_evictions_total{size} = %v", got)
	}
	if got := value(t, r.m.CacheBytes.WithLabelValues(metrics.AreaArtifacts)); got != float64(u.Artifacts) {
		t.Errorf("trove_bastion_cache_bytes{artifacts} = %v, backend holds %d", got, u.Artifacts)
	}
}

func TestR_BAS_031_TwoSweepersWithoutValkeyNeverDeleteWhatWasAccessedWithinTheResolutionWhileBelowEightyFivePercent(t *testing.T) {
	e := newEnv(t, "sweepers-low")
	ctx := context.Background()
	a, b := e.replica(), e.replica()
	var es []cache.Entry
	for i := range 12 { // 12 MiB of a 90 MiB artifact limit
		es = append(es, a.put(t, cache.EntryArtifact, fmt.Sprintf("/k/%d", i), payload(uint64(2000+i), mib)))
	}
	time.Sleep(2200 * time.Millisecond)
	for _, en := range es {
		a.Touch(ctx, en)
	}
	st := shared.NewLocal(2, nil)
	for range 3 {
		var wg sync.WaitGroup
		for _, r := range []*replica{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := r.SweepOnce(ctx, st); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
	}
	for i := range es {
		if _, _, err := b.LookupEntry(ctx, fmt.Sprintf("/k/%d", i)); err != nil {
			t.Errorf("entry %d, accessed within access_resolution, is gone: %v", i, err)
		}
	}
	for _, r := range []*replica{a, b} {
		if got := value(t, r.m.CacheEvictions.WithLabelValues(metrics.EvictSize)); got != 0 {
			t.Errorf("a sweeper evicted %v objects below 85 %%", got)
		}
	}
}

func TestR_BAS_031_TwoSweepersWithoutValkeyThatBothEvictKeepWhatWasRecentlyAccessed(t *testing.T) {
	e := newEnv(t, "sweepers-high")
	ctx := context.Background()
	cfg := func(c *config.BastionCache) { c.MaxBytes, c.DocumentsMaxBytes, c.MaxObjectBytes = 20*mib, 2*mib, 2*mib }
	a, b := e.replica(cfg), e.replica(cfg)
	var es []cache.Entry
	for i := range 20 { // 20 MiB of an 18 MiB artifact limit: above 95 %
		es = append(es, a.put(t, cache.EntryArtifact, fmt.Sprintf("/k/%d", i), payload(uint64(3000+i), mib)))
	}
	time.Sleep(2200 * time.Millisecond)
	for _, en := range es[:5] { // the oldest fills, read again
		a.Touch(ctx, en)
	}
	st := shared.NewLocal(2, nil)
	var wg sync.WaitGroup
	for _, r := range []*replica{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := r.SweepOnce(ctx, st); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	for i := range 5 {
		if _, _, err := b.LookupEntry(ctx, fmt.Sprintf("/k/%d", i)); err != nil {
			t.Errorf("entry %d, accessed within access_resolution, was evicted: %v", i, err)
		}
	}
	u, _ := a.Backend().Usage(ctx)
	if u.Artifacts > 15*mib+mib/4 {
		t.Errorf("%d bytes cached after two sweeps, want at most 85 %% of 18 MiB", u.Artifacts)
	}
}

func TestR_BAS_032_AReplacedObjectAbortsBeforeItsLastHoldBackAndTheNextReadIsAMiss(t *testing.T) {
	e := newEnv(t, "replaced")
	ctx := context.Background()
	r := e.replica()
	data := payload(8, 1536*1024)
	en := r.put(t, cache.EntryArtifact, "/big.tgz", data)
	if got, err := r.read("/big.tgz"); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("intact: %v", err)
	}

	// Someone with write access to the bucket puts other bytes of the same length under the content address.
	evil := payload(9, len(data))
	objectKey := e.prefix + "/sha256/" + en.SHA256[0:2] + "/" + en.SHA256[2:4] + "/" + en.SHA256
	if err := e.raw.PutPlain(ctx, objectKey, bytes.NewReader(evil), int64(len(evil)), nil); err != nil {
		t.Fatal(err)
	}

	got, _, err := r.LookupEntry(ctx, "/big.tgz")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	rc, err := r.Open(ctx, "/big.tgz", got)
	if err != nil {
		t.Fatal(err)
	}
	n, err := io.Copy(io.Discard, rc)
	_ = rc.Close()
	if !errors.Is(err, cache.ErrCorrupt) {
		t.Fatalf("a replaced object was served: %d bytes, %v", n, err)
	}
	if n > int64(len(data)-cache.DefaultHoldBack) {
		t.Fatalf("%d of %d bytes were released, the last %d must be withheld", n, len(data), cache.DefaultHoldBack)
	}
	if value(t, r.m.CacheCorrupt) != 1 {
		t.Errorf("trove_bastion_cache_corrupt_total = %v", value(t, r.m.CacheCorrupt))
	}
	// The next read is a miss, and the object and the entry are gone from the bucket.
	if _, _, err := r.LookupEntry(ctx, "/big.tgz"); !errors.Is(err, cache.ErrNotFound) {
		t.Fatalf("the next read: %v", err)
	}
	for _, o := range e.listAll() {
		if strings.HasPrefix(o.Key, "sha256/") || strings.HasPrefix(o.Key, "entries/") {
			t.Errorf("%s was kept", o.Key)
		}
	}
	// A refill from the main installation restores it.
	r.put(t, cache.EntryArtifact, "/big.tgz", data)
	if got, err := r.read("/big.tgz"); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("after the refill: %v", err)
	}
}

func TestR_BAS_032_AnEntryRewrittenInTheBucketToPointAtOtherBytesIsIgnored(t *testing.T) {
	e := newEnv(t, "forged")
	ctx := context.Background()
	r := e.replica()
	good, other := payload(10, 8192), payload(11, 8192)
	en := r.put(t, cache.EntryArtifact, "/good.tgz", good)
	r.put(t, cache.EntryArtifact, "/other.tgz", other)

	entryKey := func(cacheKey string) string {
		h := cache.KeyHash(cacheKey)
		return e.prefix + "/entries/" + h[0:2] + "/" + h + ".json"
	}
	rc, _, err := e.raw.OpenPlain(ctx, entryKey("/good.tgz"), 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := io.ReadAll(rc)
	_ = rc.Close()
	forged := bytes.Replace(rec, []byte(en.SHA256), []byte(digestOf(other)), 1)
	if bytes.Equal(forged, rec) {
		t.Fatal("setup: the record does not hold the address")
	}
	if err := e.raw.PutPlain(ctx, entryKey("/good.tgz"), bytes.NewReader(forged), int64(len(forged)), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.read("/good.tgz"); !errors.Is(err, cache.ErrNotFound) {
		t.Fatalf("an entry rewritten to point at another address was served: %v", err)
	}
	// The record of another key, copied into this key's place, is refused as well.
	rc, _, _ = e.raw.OpenPlain(ctx, entryKey("/other.tgz"), 0, -1)
	rec2, _ := io.ReadAll(rc)
	_ = rc.Close()
	if err := e.raw.PutPlain(ctx, entryKey("/good.tgz"), bytes.NewReader(rec2), int64(len(rec2)), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.read("/good.tgz"); !errors.Is(err, cache.ErrNotFound) {
		t.Fatalf("an entry moved to another key was served: %v", err)
	}
	// What was ignored was deleted; the untouched neighbour is intact.
	if _, err := r.Backend().GetEntry(ctx, cache.KeyHash("/good.tgz")); !errors.Is(err, cache.ErrNotFound) {
		t.Errorf("the forged entry was kept: %v", err)
	}
	if got, err := r.read("/other.tgz"); err != nil || !bytes.Equal(got, other) {
		t.Errorf("the neighbour: %v", err)
	}
}

func newEnvSigner(t *testing.T) *cache.Signer {
	t.Helper()
	s, err := cache.NewSigner(bytes.Repeat([]byte{4}, cache.MinKeyBytes))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func digestOf(b []byte) string { return digest(b) }

func fillAndStore(t *testing.T, c *cache.Cache, key string, data []byte) {
	t.Helper()
	ctx := context.Background()
	f, err := c.NewFill(cache.EntryArtifact)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write(data)
	if err := f.Commit(ctx, digest(data)); err != nil {
		t.Fatal(err)
	}
	en := cache.Entry{Kind: cache.EntryArtifact, SHA256: digest(data), Size: int64(len(data)), Repository: "r", Status: 200}
	if err := c.StoreEntry(ctx, key, en); err != nil {
		t.Fatal(err)
	}
}

func readVia(c *cache.Cache, key string) ([]byte, error) {
	ctx := context.Background()
	en, _, err := c.LookupEntry(ctx, key)
	if err != nil {
		return nil, err
	}
	rc, err := c.Open(ctx, key, en)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	return io.ReadAll(rc)
}
