package nuget

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// The bounds of NuGet's mirror discovery on what an upstream sends (R-REPO-051, Q-NUGET-107 to Q-NUGET-109; Q-NUGET-83 is the
// client path's budget, which the plan shares).

// allocated runs fn and returns the bytes the process allocated meanwhile. Tests of this file do not run in parallel.
func allocated(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// A version list of millions of entries: the plan refuses it before it builds the entries, in a bounded allocation.
func TestR_REPO_051_AHostileVersionListIsRefusedWithinABoundedAllocation(t *testing.T) {
	const entries = 3_000_000 // 24 MB of `"1.0.0",`, under the bound of a document
	var doc bytes.Buffer
	doc.WriteString(`{"versions":[`)
	for range entries {
		doc.WriteString(`"1.0.0",`)
	}
	doc.WriteString(`"2.0.0"]}`)
	f := newFakeUpstream(true, true)
	f.docs["acme.big/index.json"] = doc.Bytes()

	var err error
	used := allocated(func() { _, err = lister(t).MirrorPlan(context.Background(), f, "acme.big") })
	if e, ok := errs.As(err); !ok || e.Code != errs.CodeNuGetInvalidUpstreamDocument {
		t.Fatalf("error %v: want nuget_invalid_upstream_document", err)
	}
	// Materializing the entries costs 16 bytes of header and the string each, over 72 MB for 3 million; 12 MiB is half of the document.
	limit := uint64(12 << 20)
	if raceEnabled {
		limit = 64 << 20 // 32,768 strings at about 1 KiB each; a decode of every entry is gigabytes
	}
	if used > limit {
		t.Errorf("the plan allocated %d bytes for a list of %d entries", used, entries)
	}
}

// A list at the limit is planned: the bound is the leaf limit of the client path, not less.
func TestR_REPO_051_AVersionListAtTheLeafLimitIsPlanned(t *testing.T) {
	var doc strings.Builder
	doc.WriteString(`{"versions":[`)
	for i := range 32768 {
		if i > 0 {
			doc.WriteByte(',')
		}
		fmt.Fprintf(&doc, `"1.0.%d"`, i)
	}
	doc.WriteString(`]}`)
	f := newFakeUpstream(true, true)
	f.docs["acme.big/index.json"] = []byte(doc.String())
	plan, err := lister(t).MirrorPlan(context.Background(), f, "acme.big")
	if err != nil || len(plan.Items) != 32768 {
		t.Fatalf("%d items, %v", len(plan.Items), err)
	}
	f.docs["acme.big/index.json"] = []byte(strings.Replace(doc.String(), `"1.0.0"`, `"1.0.0","2.0.0"`, 1))
	if _, err := lister(t).MirrorPlan(context.Background(), f, "acme.big"); err == nil {
		t.Fatal("a list over the limit was planned")
	}
}

// The registration pages of one plan share one budget, as the pages of a rendering do: three pages of 20,000 leaves are more than
// the 32,768 versions trove reads of one ID.
func TestR_REPO_051_ThePlanReadsTheRegistrationWithinOneBudget(t *testing.T) {
	leaves := func(from int) string {
		var b strings.Builder
		b.WriteString(`{"items":[`)
		for i := range 20000 {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"catalogEntry":{"version":"%d.0.%d","packageSize":5}}`, from, i)
		}
		b.WriteString(`]}`)
		return b.String()
	}
	f := newFakeUpstream(true, true)
	f.docs["acme.reg/index.json"] = []byte(`{"versions":["1.0.0"]}`)
	f.docs["acme.reg/.registration/index.json"] = []byte(`{"items":[
	 {"@id":"https://feed.test/p1","lower":"1.0.0","upper":"1.0.19999"},
	 {"@id":"https://feed.test/p2","lower":"2.0.0","upper":"2.0.19999"},
	 {"@id":"https://feed.test/p3","lower":"3.0.0","upper":"3.0.19999"}]}`)
	for i := 1; i <= 3; i++ {
		f.docs[fmt.Sprintf("acme.reg/.registration/page/%d.0.0/%d.0.19999.json", i, i)] = []byte(leaves(i))
	}
	if _, err := lister(t).MirrorPlan(context.Background(), f, "acme.reg"); err == nil {
		t.Fatal("60,000 leaves over three pages were read")
	}
	// Two pages are within the budget.
	f.docs["acme.reg/.registration/index.json"] = []byte(`{"items":[
	 {"@id":"https://feed.test/p1","lower":"1.0.0","upper":"1.0.19999"}]}`)
	if _, err := lister(t).MirrorPlan(context.Background(), f, "acme.reg"); err != nil {
		t.Fatalf("one page of 20,000 leaves: %v", err)
	}
}

func gzipOfZeros(n int) []byte {
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	chunk := make([]byte, 1<<20)
	for written := 0; written < n; written += len(chunk) {
		_, _ = zw.Write(chunk)
	}
	_ = zw.Close()
	return out.Bytes()
}

// A discovery body is inflated within the bound of the engine's discovery documents (32 MiB, proxy.DiscoveryDocumentMaxBytes),
// whatever the Accept-Encoding of the request was: a gzip bomb stops there, and every request says so to Get.
func TestR_REPO_051_ADiscoveryBodyIsInflatedWithinTheDiscoveryBound(t *testing.T) {
	f := newFakeUpstream(true, true)
	f.pages[fakeCatLog] = gzipOfZeros(300 << 20)
	var err error
	used := allocated(func() {
		_, err = lister(t).MirrorNames(context.Background(), f, discover.PageRequest{Method: discover.MethodFeed, Scope: admit("acme.")})
	})
	if e, ok := errs.As(err); !ok || e.Code != errs.CodeUpstreamItemTooLarge {
		t.Fatalf("error %v: want upstream_item_too_large", err)
	}
	if used > 160<<20 {
		t.Errorf("a 300 MiB bomb allocated %d bytes", used)
	}
	if len(f.maxes) != 1 || f.maxes[0] != maxDiscoveryBytes {
		t.Errorf("the body bounds of the requests: %v, want %d", f.maxes, int64(maxDiscoveryBytes))
	}
}
