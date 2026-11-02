package virtual_test

import (
	"context"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/virtual"
	vt "github.com/guided-traffic/trove/internal/virtual/virtualtest"
)

// readsMore is a format whose merge reads more of a member than the document the engine handed
// it, as NuGet reads the registration pages a remote member's index lists by address: the merged
// bytes change with what it read, while every part the engine knows stays the same.
type readsMore struct {
	*vt.Merger
	page string
}

func (m readsMore) Merge(ctx context.Context, d virtual.Descriptor, parts []virtual.Part) ([]byte, error) {
	b, err := m.Merger.Merge(ctx, d, parts)
	return append(b, m.page...), err
}

// TestR_REPO_020_TheMergeFingerprintFollowsTheMergedBytes (Q-NUGET-82): the validator of a merged
// document is an input of its ETag and of the key of its rendering, so it has to change whenever
// the merged bytes do. A page of a remote member can change while its index does not; the merge
// fingerprint covered the members' revisions alone, so a client kept a stale document under an
// unchanged ETag and the rendition cache served the old rendering.
func TestR_REPO_020_TheMergeFingerprintFollowsTheMergedBytes(t *testing.T) {
	w := newWorld(t)
	upstream(w, "p", time.Hour, "1.0.0")
	c, p := w.plan(alice, metadataOf("p"))
	m1, err := w.r.Metadata(context.Background(), c, p, readsMore{Merger: w.merger, page: " page v1"})
	if err != nil {
		t.Fatal(err)
	}
	w.caches.FlushAll("admin")
	m2, err := w.r.Metadata(context.Background(), c, p, readsMore{Merger: w.merger, page: " page v2"})
	if err != nil {
		t.Fatal(err)
	}
	if m1.MergeFingerprint == m2.MergeFingerprint {
		t.Fatal("two merges of the same parts into different bytes have one fingerprint")
	}
	w.caches.FlushAll("admin")
	m3, err := w.r.Metadata(context.Background(), c, p, readsMore{Merger: w.merger, page: " page v2"})
	if err != nil || m3.MergeFingerprint != m2.MergeFingerprint {
		t.Fatalf("the same bytes have another fingerprint (%v)", err)
	}
}
