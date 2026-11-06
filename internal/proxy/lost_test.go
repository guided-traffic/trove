package proxy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/items"
)

// lostBlobs is a BlobStore whose objects are all gone or all damaged.
type lostBlobs struct {
	err   error
	opens int
}

func (b *lostBlobs) Commit(context.Context, *blob.Spooled, blob.LinkFunc) (blob.CommitResult, error) {
	return blob.CommitResult{}, errors.New("not used")
}

func (b *lostBlobs) Open(context.Context, blob.Digest, int64, blob.OpenOptions) (io.ReadCloser, error) {
	b.opens++
	return nil, b.err
}

func (b *lostBlobs) RecheckMissing(context.Context, blob.Digest, int64) bool { return false }

// recheckItems is an item store whose recheck of a missing object finds it back or not (items.Store.Usable).
type recheckItems struct {
	ItemStore
	back  bool
	asked int
}

func (r *recheckItems) Usable(_ context.Context, it items.Item) (items.Item, error) {
	if it.ContentStatus != "missing" {
		return it, items.ContentError(it)
	}
	r.asked++
	if !r.back {
		return it, items.ContentError(it)
	}
	it.ContentStatus = "ok"
	return it, nil
}

func TestR_STOR_027_ALostItemIsAMissAndItsAnswerIs503(t *testing.T) {
	t.Run("Q-REPO-202 an entry or an item whose blob is not ok is damaged; an unread status is ok", func(t *testing.T) {
		for status, want := range map[string]bool{"": false, "ok": false, "missing": true, "corrupt": true} {
			if got := (entry{ContentStatus: status}).Damaged(); got != want {
				t.Errorf("entry %q: Damaged = %v", status, got)
			}
			wantHit := hitStored
			if want {
				wantHit = hitDamaged
			}
			if got := hitOf(items.Item{ContentStatus: status}); got != wantHit {
				t.Errorf("item %q: %v", status, got)
			}
		}
	})

	t.Run("Q-STOR-153 an item marked missing whose object is back is a hit again; a View asks no store", func(t *testing.T) {
		back := &recheckItems{back: true}
		e := &Engine{o: Options{Items: back}}
		if it, state := e.classify(context.Background(), items.Item{ContentStatus: "missing"}); state != hitStored || it.ContentStatus != "ok" || back.asked != 1 {
			t.Fatalf("back: %v %q, %d rechecks", state, it.ContentStatus, back.asked)
		}
		gone := &recheckItems{}
		e = &Engine{o: Options{Items: gone}}
		if _, state := e.classify(context.Background(), items.Item{ContentStatus: "missing"}); state != hitDamaged {
			t.Fatalf("gone: %v", state)
		}
		if _, state := e.classify(context.Background(), items.Item{ContentStatus: "ok"}); state != hitStored || gone.asked != 1 {
			t.Fatalf("an ok item was rechecked: %d", gone.asked)
		}
		view := &recheckItems{back: true}
		e = &Engine{o: Options{Items: view}, readOnly: true}
		if _, state := e.classify(context.Background(), items.Item{ContentStatus: "missing"}); state != hitDamaged || view.asked != 0 {
			t.Fatalf("a View: %v, %d rechecks", state, view.asked)
		}
	})

	t.Run("Q-REPO-202 a client that cannot fill a lost item gets 503 content_unavailable with the cause's Retry-After, or 30 s", func(t *testing.T) {
		e := lostContent("the upstream is assumed to be offline", assumedOffline(42*time.Second))
		if e.Status() != 503 || e.Code != errs.CodeContentUnavailable || e.RetryAfter != 42*time.Second {
			t.Fatalf("%d %s %s", e.Status(), e.Code, e.RetryAfter)
		}
		if e = lostContent("x is offline", nil); e.RetryAfter != lostRetry || e.Err != nil {
			t.Fatalf("Retry-After %s, cause %v", e.RetryAfter, e.Err)
		}
		if !items.ContentLost(e) {
			t.Fatal("the answer is not one that the item store's callers recognise")
		}
	})

	t.Run("Q-REPO-202 the answer of a miss stays for an item that is not lost and for a mirror's pass", func(t *testing.T) {
		miss := offlineMiss(Remote{})
		if got := lostOr(Request{}, false, "offline", miss); !errors.Is(got, miss) {
			t.Fatalf("not lost: %v", got)
		}
		if got := lostOr(Request{Priority: PriorityBackground}, true, "offline", miss); !errors.Is(got, miss) {
			t.Fatalf("a pass: %v", got)
		}
		got := lostOr(Request{}, true, "offline", miss)
		if ee, ok := errs.As(got); !ok || ee.Code != errs.CodeContentUnavailable || !errors.Is(got, miss) {
			t.Fatalf("a client: %v", got)
		}
	})

	t.Run("Q-REPO-202 a document's missing object is 503 and a corrupt one 500, both content_unavailable; nothing else changes", func(t *testing.T) {
		if ee, _ := errs.As(storedObjectError(blob.ErrNotFound)); ee == nil || ee.Status() != 503 || ee.Code != errs.CodeContentUnavailable || ee.RetryAfter != lostRetry {
			t.Fatalf("missing: %v", ee)
		}
		if ee, _ := errs.As(storedObjectError(blob.ErrCorrupt)); ee == nil || ee.Status() != 500 || ee.Code != errs.CodeContentUnavailable {
			t.Fatalf("corrupt: %v", ee)
		}
		other := errs.E(errs.Unavailable, errs.CodeObjectStoreUnavail, "outage")
		if got := storedObjectError(other); !errors.Is(got, other) {
			t.Fatalf("an outage became %v", got)
		}
	})

	t.Run("Q-REPO-203 a stored document's lost bytes are filled again only on the request path, once per digest", func(t *testing.T) {
		bs := &lostBlobs{err: blob.ErrNotFound}
		e := &Engine{o: Options{Blobs: bs}, log: slog.New(slog.DiscardHandler), throttled: map[string]time.Time{}}
		e.o.Now = time.Now
		en := entry{Digest: [32]byte{7}, Size: 10}
		for _, rq := range []Request{{Mode: CacheView}, {Mode: OnlyIfCached}} {
			_, err := e.storedDocument(rq, time.Time{}, en, CacheStatus{}).Bytes(context.Background())
			if ee, ok := errs.As(err); !ok || ee.Status() != 503 || ee.Code != errs.CodeContentUnavailable {
				t.Fatalf("mode %d: %v", rq.Mode, err)
			}
		}
		// Under the refill of the same digest the bytes are not filled again; another digest's still are (a discovery
		// document the flight reads), which this engine without a database cannot do, so it is not called here.
		ctx := context.WithValue(context.Background(), refillKey{}, en.Digest)
		if !refilling(ctx, en.Digest) || refilling(ctx, [32]byte{8}) || refilling(context.Background(), en.Digest) {
			t.Fatal("the refill mark is not the digest's")
		}
		if _, err := e.storedDocument(Request{}, time.Time{}, en, CacheStatus{}).Bytes(ctx); !items.ContentLost(err) {
			t.Fatalf("under its own refill: %v", err)
		}
		// An outage of the store is no lost object: it is answered as it is, and nothing is filled.
		bs.err = errs.E(errs.Unavailable, errs.CodeObjectStoreUnavail, "outage")
		if _, err := e.storedDocument(Request{}, time.Time{}, en, CacheStatus{}).Bytes(context.Background()); items.ContentLost(err) {
			t.Fatalf("an outage: %v", err)
		}
	})
}
