//go:build integration

package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/target"
	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/outbound"
)

// A restore whose target is on another endpoint than the blob store cannot copy server-side and streams every blob through the
// pod, hashed (R-BAK-006 rules, R-BAK-021 step 3).
func TestR_BAK_021_RestoreStreamsWhenTheStoreCannotCopy(t *testing.T) {
	src := newSource(t, 12)
	dst := src.destination()
	r, _ := dst.restorer(restorerOptions{targetEndpoint: "https://another-store.invalid"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if _, err := r.Restore(ctx, backup.RestoreOptions{ID: src.id, SkipRevocationJournal: true}); err != nil {
		t.Fatal(err)
	}
	for d, content := range src.seed.blobs {
		if got := dst.blobObject(d); !bytes.Equal(got, content) {
			t.Fatalf("object %s = %q", d.Hex(), got)
		}
	}
}

func sinkFor(t *testing.T, dst *destination, threshold, part int64, opts ...s3.SinkOption) *s3.BlobSink {
	t.Helper()
	cfg := dst.cfg.Storage.S3
	cfg.Multipart.Threshold, cfg.Multipart.PartSize = config.ByteSize(threshold), config.ByteSize(part)
	c, err := s3.NewFromFactory(context.Background(), cfg, outbound.New(dst.cfg.Proxy.Outbound))
	if err != nil {
		t.Fatal(err)
	}
	return s3.NewBlobSink(c, cfg, 5<<20, opts...)
}

func TestR_BAK_021_BlobSink(t *testing.T) {
	src := newSource(t, 1)
	dst := src.destination()
	ctx := context.Background()
	sink := sinkFor(t, dst, 5<<20, 5<<20)
	big := make([]byte, 11<<20)
	if _, err := rand.Read(big); err != nil {
		t.Fatal(err)
	}
	d := format.Digest(sha256.Sum256(big))

	t.Run("R-BAK-021 a stream larger than the threshold goes up in parts and is confirmed by size", func(t *testing.T) {
		if err := sink.Put(ctx, d, int64(len(big)), bytes.NewReader(big)); err != nil {
			t.Fatal(err)
		}
		if got := dst.blobObject(d); !bytes.Equal(got, big) {
			t.Fatalf("object has %d bytes, want %d", len(got), len(big))
		}
		if ok, err := sink.Has(ctx, d, int64(len(big))); err != nil || !ok {
			t.Fatalf("Has = %v, %v", ok, err)
		}
		if ok, _ := sink.Has(ctx, d, int64(len(big))+1); ok {
			t.Error("Has accepts another size")
		}
	})

	t.Run("R-BAK-021 a stream whose hash differs from the digest is never made visible, small or large", func(t *testing.T) {
		for _, size := range []int{100, 11 << 20} {
			content := make([]byte, size)
			if _, err := rand.Read(content); err != nil {
				t.Fatal(err)
			}
			wrong := format.Digest(sha256.Sum256([]byte("another content")))
			err := sink.Put(ctx, wrong, int64(size), bytes.NewReader(content))
			if err == nil || !strings.Contains(err.Error(), "hashes to") {
				t.Fatalf("size %d: err = %v", size, err)
			}
			if dst.blobObject(wrong) != nil {
				t.Fatalf("size %d: the object exists", size)
			}
		}
	})

	t.Run("R-BAK-021 a stream of the wrong length is refused", func(t *testing.T) {
		content := []byte("short")
		dg := format.Digest(sha256.Sum256(content))
		if err := sink.Put(ctx, dg, int64(len(content))+3, bytes.NewReader(content)); err == nil {
			t.Fatal("no error")
		}
		if dst.blobObject(dg) != nil {
			t.Fatal("the object exists")
		}
	})

	t.Run("R-BAK-021 a server-side copy of a missing pool object is reported as not found; a foreign endpoint as unavailable", func(t *testing.T) {
		var missing format.Digest
		missing[0] = 9
		ref := target.ObjectRef{Endpoint: src.tgtCfg.Target.Endpoint, Bucket: src.tgtCfg.Target.Bucket, Key: src.layout.Pool(missing), Size: 10}
		if err := sink.CopyFrom(ctx, ref, missing, 10); err == nil || strings.Contains(err.Error(), "unavailable") {
			t.Errorf("missing source: err = %v", err)
		}
		ref.Endpoint = "https://another-store.invalid"
		if err := sink.CopyFrom(ctx, ref, missing, 10); err == nil || !strings.Contains(err.Error(), "not available") {
			t.Errorf("foreign endpoint: err = %v", err)
		}
	})

	t.Run("R-BAK-020 the listing finds a foreign key, skips probe objects, and takes the blobs after an anchor", func(t *testing.T) {
		if key, err := sink.FirstForeign(ctx, false); err != nil || key == "" {
			t.Fatalf("with blobs present and no anchor: %q, %v", key, err)
		}
		if key, err := sink.FirstForeign(ctx, true); err != nil || key != "" {
			t.Fatalf("with an anchor: %q, %v", key, err)
		}
		if err := sink.Probe(ctx, "pod-x"); err != nil {
			t.Fatal(err)
		}
		if key, err := sink.FirstForeign(ctx, true); err != nil || key != "" {
			t.Fatalf("after a probe: %q, %v", key, err)
		}
	})
}

// The streaming path of a restore runs with many workers; the bytes it holds in memory stay bounded whatever the multipart
// threshold of the configuration says (R-BAK-021 step 3).
func TestR_BAK_021_BlobSinkMemoryIsBounded(t *testing.T) {
	src := newSource(t, 1)
	dst := src.destination()
	ctx := context.Background()
	content := func(n int) ([]byte, format.Digest) {
		b := make([]byte, n)
		if _, err := rand.Read(b); err != nil {
			t.Fatal(err)
		}
		return b, format.Digest(sha256.Sum256(b))
	}

	t.Run("R-BAK-021 concurrent streams share one budget", func(t *testing.T) {
		const budget = 12 << 20
		sink := sinkFor(t, dst, 100<<20, 5<<20, s3.WithMemoryBudget(budget))
		var wg sync.WaitGroup
		for range 8 {
			b, d := content(6 << 20)
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := sink.Put(ctx, d, int64(len(b)), bytes.NewReader(b)); err != nil {
					t.Error(err)
				}
			}()
			t.Cleanup(func() {
				if got := dst.blobObject(d); !bytes.Equal(got, b) {
					t.Errorf("object %s has %d bytes", d.Hex(), len(got))
				}
			})
		}
		wg.Wait()
		if peak := sink.PeakBuffered(); peak == 0 || peak > budget {
			t.Fatalf("peak %d bytes, budget %d", peak, budget)
		}
	})

	t.Run("R-BAK-021 an object above the cap goes up in parts although the threshold is 100 MiB", func(t *testing.T) {
		sink := sinkFor(t, dst, 100<<20, 5<<20)
		b, d := content(20 << 20)
		if err := sink.Put(ctx, d, int64(len(b)), bytes.NewReader(b)); err != nil {
			t.Fatal(err)
		}
		if peak := sink.PeakBuffered(); peak > 5<<20 {
			t.Fatalf("peak %d bytes: the object was buffered whole", peak)
		}
		if got := dst.blobObject(d); !bytes.Equal(got, b) {
			t.Fatalf("object has %d bytes", len(got))
		}
	})
}
