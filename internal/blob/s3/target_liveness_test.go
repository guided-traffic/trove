package s3_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/backup/target"
	"github.com/guided-traffic/trove/internal/blob/s3"
)

func xml(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/xml")
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>` + body))
}

// copyServer answers the requests of a server-side copy; delay is how long each CopyObject or UploadPartCopy takes to
// answer, with no byte sent meanwhile (the store answers when it has finished). It records the multipart aborts.
type copyServer struct {
	delay   time.Duration
	denyAt  int32 // the part number answered with AccessDenied; 0 for none
	aborted atomic.Int32
	parts   atomic.Int32
}

func (c *copyServer) handle(w http.ResponseWriter, r *http.Request) bool {
	q := r.URL.Query()
	switch {
	case r.Method == http.MethodPut && r.Header.Get("X-Amz-Copy-Source") != "" && q.Get("partNumber") == "":
		time.Sleep(c.delay)
		xml(w, `<CopyObjectResult><ETag>"e"</ETag></CopyObjectResult>`)
	case r.Method == http.MethodPut && q.Get("partNumber") != "" && r.Header.Get("X-Amz-Copy-Source") != "":
		n, _ := strconv.Atoi(q.Get("partNumber"))
		c.parts.Add(1)
		if int32(n) == c.denyAt {
			s3Error(w, r, http.StatusForbidden, "AccessDenied")
			return true
		}
		time.Sleep(c.delay)
		xml(w, `<CopyPartResult><ETag>"p"</ETag></CopyPartResult>`)
	case r.Method == http.MethodPost && q.Has("uploads"):
		xml(w, `<InitiateMultipartUploadResult><Bucket>b</Bucket><Key>k</Key><UploadId>u1</UploadId></InitiateMultipartUploadResult>`)
	case r.Method == http.MethodPost && q.Get("uploadId") != "":
		xml(w, `<CompleteMultipartUploadResult><Location>l</Location><Bucket>b</Bucket><Key>k</Key><ETag>"c"</ETag></CompleteMultipartUploadResult>`)
	case r.Method == http.MethodDelete && q.Get("uploadId") != "":
		c.aborted.Add(1)
		w.WriteHeader(http.StatusNoContent)
	default:
		return false
	}
	return true
}

// progress records the values of ObjectRef.Progress.
type progress struct {
	mu   sync.Mutex
	seen []int64
}

func (p *progress) fn(n int64) { p.mu.Lock(); p.seen = append(p.seen, n); p.mu.Unlock() }

func (p *progress) values() []int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]int64(nil), p.seen...)
}

func TestR_BAK_006_CopyObjectAnsweredAfterTheIdleTimeout(t *testing.T) {
	// The store sends nothing until the copy is done, longer than the idle timeout: a copy has no body whose bytes could show
	// progress, so the idle watchdog must not judge it. The size-derived bound does.
	cs := &copyServer{delay: 900 * time.Millisecond}
	e := newTargetEnv(t, "none", s3.WithIdleTimeout(200*time.Millisecond), s3.WithProgressInterval(100*time.Millisecond))
	e.fake.fail = cs.handle
	var p progress
	if err := e.t.Copy(context.Background(), target.ObjectRef{Bucket: "src", Key: "k", Size: 1000, Progress: p.fn}, "pool/x"); err != nil {
		t.Fatalf("a copy that takes longer than the idle timeout failed: %v", err)
	}
	if n := len(p.values()); n < 3 {
		t.Fatalf("progress called %d times while the request was outstanding, want at least 3", n)
	}
}

func TestR_BAK_006_CopyThatOutlastsItsBoundFallsBackToStreaming(t *testing.T) {
	cs := &copyServer{delay: 3 * time.Second}
	e := newTargetEnv(t, "none", s3.WithCopyBound(300*time.Millisecond, 1<<20))
	e.fake.fail = cs.handle
	start := time.Now()
	err := e.t.Copy(context.Background(), target.ObjectRef{Bucket: "src", Key: "k", Size: 1000}, "pool/x")
	if !errors.Is(err, target.ErrServerCopyUnavailable) {
		t.Fatalf("err = %v, want ErrServerCopyUnavailable", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("the bound did not hold: %s", time.Since(start))
	}
	// The caller's own cancellation stays a cancellation.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	e2 := newTargetEnv(t, "none")
	e2.fake.fail = cs.handle
	if err := e2.t.Copy(ctx, target.ObjectRef{Bucket: "src", Key: "k", Size: 1000}, "pool/x"); errors.Is(err, target.ErrServerCopyUnavailable) {
		t.Fatalf("a cancelled caller was told to stream: %v", err)
	}
}

func TestR_BAK_006_UploadPartCopyReportsProgressPerPart(t *testing.T) {
	cs := &copyServer{delay: 50 * time.Millisecond}
	e := newTargetEnv(t, "none", s3.WithSingleCopyLimit(6<<20), s3.WithCopyPartSize(5<<20))
	e.fake.fail = cs.handle
	var p progress
	const size = 12<<20 + 5
	if err := e.t.Copy(context.Background(), target.ObjectRef{Bucket: "src", Key: "k", Size: size, Progress: p.fn}, "pool/x"); err != nil {
		t.Fatal(err)
	}
	got := p.values()
	want := []int64{5 << 20, 10 << 20, size}
	if len(got) != len(want) {
		t.Fatalf("progress = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("progress = %v, want %v", got, want)
		}
	}
}

func TestR_BAK_006_AFailedPartCopyAbortsTheUploadAndFallsBack(t *testing.T) {
	cs := &copyServer{denyAt: 2}
	e := newTargetEnv(t, "none", s3.WithSingleCopyLimit(6<<20), s3.WithCopyPartSize(5<<20))
	e.fake.fail = cs.handle
	err := e.t.Copy(context.Background(), target.ObjectRef{Bucket: "src", Key: "k", Size: 12 << 20}, "pool/x")
	if !errors.Is(err, target.ErrServerCopyUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if cs.aborted.Load() != 1 {
		t.Fatalf("%d aborts, want 1", cs.aborted.Load())
	}
}

func TestR_BAK_006_AbortAfterCancellationStillGoesOutWithinTheGrace(t *testing.T) {
	cs := &copyServer{delay: 300 * time.Millisecond}
	e := newTargetEnv(t, "none", s3.WithSingleCopyLimit(6<<20), s3.WithCopyPartSize(5<<20))
	e.fake.fail = cs.handle
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if err := e.t.Copy(ctx, target.ObjectRef{Bucket: "src", Key: "k", Size: 12 << 20}, "pool/x"); err == nil {
		t.Fatal("a cancelled copy succeeded")
	}
	if cs.aborted.Load() != 1 {
		t.Fatalf("%d aborts after the caller's context ended, want 1", cs.aborted.Load())
	}
}

func TestR_BAK_005_WriterRefusesAnObjectAboveTheLimitUpFront(t *testing.T) {
	e := newTargetEnv(t, "none")
	if _, err := e.t.Writer(context.Background(), "pool/x", target.PutOptions{SizeHint: 6 << 40}); err == nil {
		t.Fatal("a hint above 5 TiB was accepted")
	}
	if w, err := e.t.Writer(context.Background(), "pool/x", target.PutOptions{SizeHint: 200 << 30}); err != nil || w == nil {
		t.Fatalf("Writer = %v, %v", w, err)
	}
}

func TestR_STOR_033_AReaderThatPausesIsNotAStalledStore(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789abcdef"), 1<<18) // 4 MiB
	e := newTargetEnv(t, "none", s3.WithIdleTimeout(200*time.Millisecond))
	e.fake.objects["trove/big"] = data
	e.fake.fail = nil
	rc, o, err := e.t.Get(context.Background(), "trove/big")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	if o.Size != int64(len(data)) {
		t.Fatalf("size = %d", o.Size)
	}
	head := make([]byte, 10)
	if _, err := io.ReadFull(rc, head); err != nil {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond) // the consumer is busy elsewhere for more than the idle timeout
	rest, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("a consumer that paused broke the download: %v", err)
	}
	if !bytes.Equal(append(head, rest...), data) {
		t.Fatal("the content differs")
	}
}
