//go:build integration

package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// generator is a reader of n deterministic bytes that keeps no copy of them and
// hashes what it hands out.
type generator struct {
	left int64
	x    uint64
	h    hash.Hash
	buf  [64 << 10]byte
}

func newGenerator(n int64, seed uint64) *generator {
	return &generator{left: n, x: seed*0x9E3779B97F4A7C15 + 1, h: sha256.New()}
}

func (g *generator) Read(p []byte) (int, error) {
	if g.left == 0 {
		return 0, io.EOF
	}
	n := min(int64(len(p)), g.left, int64(len(g.buf)))
	for i := int64(0); i < n; i += 8 {
		g.x ^= g.x << 13
		g.x ^= g.x >> 7
		g.x ^= g.x << 17
		for j := int64(0); j < 8 && i+j < n; j++ {
			g.buf[i+j] = byte(g.x >> (8 * j))
		}
	}
	copy(p, g.buf[:n])
	g.h.Write(g.buf[:n])
	g.left -= n
	return int(n), nil
}

func (g *generator) sum() string { return hex.EncodeToString(g.h.Sum(nil)) }

// sampler records the peak heap of the process and the peak size of a file in a
// directory while a transfer runs.
type sampler struct {
	stop     chan struct{}
	done     chan struct{}
	baseline uint64
	peakHeap atomic.Uint64
	peakFile atomic.Int64
}

func startSampler(dir string) *sampler {
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	s := &sampler{stop: make(chan struct{}), done: make(chan struct{}), baseline: ms.HeapAlloc}
	go func() {
		defer close(s.done)
		for {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > s.peakHeap.Load() {
				s.peakHeap.Store(m.HeapAlloc)
			}
			if dir != "" {
				if entries, err := os.ReadDir(dir); err == nil {
					for _, e := range entries {
						if info, err := e.Info(); err == nil && info.Size() > s.peakFile.Load() {
							s.peakFile.Store(info.Size())
						}
					}
				}
			}
			select {
			case <-s.stop:
				return
			case <-time.After(25 * time.Millisecond):
			}
		}
	}()
	return s
}

func (s *sampler) finish() (heapGrowth uint64, spooled int64) {
	close(s.stop)
	<-s.done
	if p := s.peakHeap.Load(); p > s.baseline {
		heapGrowth = p - s.baseline
	}
	return heapGrowth, s.peakFile.Load()
}

func TestR_STOR_004_LargeUploadIsStreamedThroughTheSpool(t *testing.T) {
	const size = 500 << 20
	e := newEnv(t)
	e.breakGlass()
	r := e.newReplica("a", replicaOpts{})
	r.createRepository("big", nil)
	path := "/files/big/installers/big-500mb.bin"

	up := newGenerator(size, 11)
	req, err := http.NewRequest("PUT", r.baseURL+path, up)
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = size
	req.Header.Set("Authorization", "Bearer admin")
	req.Header.Set("Content-Type", "application/octet-stream")
	s := startSampler(r.spoolDir)
	start := time.Now()
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	heap, spooled := s.finish()
	t.Logf("uploaded %d MiB in %s; heap grew by %d MiB; the spool file reached %d MiB; %d parts, %d puts",
		size>>20, time.Since(start).Round(time.Millisecond), heap>>20, spooled>>20, r.obj.parts.Load(), r.obj.puts.Load())
	if res.StatusCode != 201 {
		t.Fatalf("%d %s", res.StatusCode, body)
	}

	t.Run("R-STOR-004 the body went to a spool file, not into memory", func(t *testing.T) {
		if spooled < size*9/10 {
			t.Errorf("the spool file reached %d bytes of %d: the body was not spooled", spooled, int64(size))
		}
		if heap > 64<<20 {
			t.Errorf("the heap grew by %d MiB during a %d MiB upload", heap>>20, size>>20)
		}
	})
	t.Run("R-STOR-004 R-STOR-006 the digest is that of the bytes sent and a multipart upload stored them", func(t *testing.T) {
		var got struct{ Digests struct{ SHA256 string } }
		if err := json.Unmarshal(body, &got); err != nil || got.Digests.SHA256 != up.sum() {
			t.Fatalf("sha256 = %s, sent %s (%v)", got.Digests.SHA256, up.sum(), err)
		}
		if r.obj.parts.Load() < 2 {
			t.Errorf("%d parts: the object store saw no multipart upload", r.obj.parts.Load())
		}
	})
	t.Run("R-STOR-004 the spool is empty when the request has ended", func(t *testing.T) {
		entries, err := os.ReadDir(r.spoolDir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("spool = %v, %v", entries, err)
		}
	})
	t.Run("R-STOR-012 R-STOR-013 the download is streamed and verified, with the length declared", func(t *testing.T) {
		s := startSampler("")
		resp, err := r.raw("admin", "GET", path, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		h := sha256.New()
		n, err := io.Copy(h, resp.Body)
		growth, _ := s.finish()
		if err != nil || n != size || resp.ContentLength != size || len(resp.TransferEncoding) != 0 || hex.EncodeToString(h.Sum(nil)) != up.sum() {
			t.Fatalf("%d bytes, Content-Length %d, Transfer-Encoding %v, err %v", n, resp.ContentLength, resp.TransferEncoding, err)
		}
		if growth > 64<<20 {
			t.Errorf("the heap grew by %d MiB during a download of %d MiB", growth>>20, size>>20)
		}
		mid := r.get("admin", path, "Range", "bytes=300000000-300000099")
		if mid.status != 206 || len(mid.body) != 100 {
			t.Errorf("range: %d, %d bytes", mid.status, len(mid.body))
		}
	})
}

// slowReader reads from r in chunks and sleeps between them.
type slowReader struct {
	r     io.Reader
	chunk int
	pause time.Duration
}

func (s slowReader) Read(p []byte) (int, error) {
	if len(p) > s.chunk {
		p = p[:s.chunk]
	}
	n, err := s.r.Read(p)
	time.Sleep(s.pause)
	return n, err
}

func TestR_ARCH_024_ADownloadCutAtTheShutdownDeadlineResumesOnAnotherReplica(t *testing.T) {
	const size = 96 << 20
	te := newTransferEnv(t)
	a, b := te.a, te.b
	path := "/files/generic-local/drain/big.bin"
	src := newGenerator(size, 21)
	want := ""
	{
		req, _ := http.NewRequest("PUT", a.baseURL+path, src)
		req.ContentLength = size
		req.Header.Set("Authorization", "Bearer maint")
		res, err := client.Do(req)
		if err != nil || res.StatusCode != 201 {
			t.Fatalf("upload: %v %v", res, err)
		}
		_ = res.Body.Close()
		want = src.sum()
	}

	resp, err := a.raw("reader", "GET", path, nil) //nolint:bodyclose // closed by the defer below
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("download: %d", resp.StatusCode)
	}
	etag := resp.Header.Get("ETag")
	if resp.ContentLength != size {
		t.Fatalf("Content-Length %d", resp.ContentLength)
	}
	var first []byte
	done := make(chan error, 1)
	go func() {
		var rerr error
		first, rerr = io.ReadAll(slowReader{r: resp.Body, chunk: 256 << 10, pause: 20 * time.Millisecond})
		done <- rerr
	}()
	time.Sleep(400 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	shutdownErr := a.server.Shutdown(ctx)
	if shutdownErr == nil || !strings.Contains(shutdownErr.Error(), "cut") {
		t.Fatalf("the shutdown should have cut the download at its deadline: %v", shutdownErr)
	}
	rerr := <-done
	// A cut ends the connection with a reset where the platform supports it, so the
	// client learns of it at once (Q-ARCH-187); elsewhere it sees the body end early.
	if rerr == nil || (!errors.Is(rerr, io.ErrUnexpectedEOF) && !errors.Is(rerr, syscall.ECONNRESET)) {
		t.Fatalf("the client must see an incomplete response, got %v after %d bytes", rerr, len(first))
	}
	if len(first) == 0 || len(first) >= size {
		t.Fatalf("%d bytes before the cut", len(first))
	}
	t.Logf("cut after %d of %d bytes", len(first), size)

	t.Run("R-STOR-012 R-ARCH-024 the resumed range is served with 206 by another replica and the parts add up to the file", func(t *testing.T) {
		rest := b.get("reader", path, "Range", fmt.Sprintf("bytes=%d-", len(first)), "If-Range", etag)
		if rest.status != 206 || int64(len(rest.body)) != size-int64(len(first)) {
			t.Fatalf("%d, %d bytes (want %d)", rest.status, len(rest.body), size-len(first))
		}
		if rest.hdr("Content-Range") != fmt.Sprintf("bytes %d-%d/%d", len(first), size-1, size) {
			t.Errorf("Content-Range = %q", rest.hdr("Content-Range"))
		}
		h := sha256.New()
		h.Write(first)
		h.Write(rest.body)
		if got := hex.EncodeToString(h.Sum(nil)); got != want {
			t.Fatalf("sha256 of the parts %s, of the file %s", got, want)
		}
		// The ETag is the content address: the same SHA-256 the upload announced.
		if etag != `"`+want+`"` {
			t.Errorf("ETag %s", etag)
		}
	})
}

// trickle returns a body of n bytes that arrives over about d and a function that
// reports how many bytes were sent.
func trickle(n int, d time.Duration) (io.Reader, *atomic.Int64) {
	pr, pw := io.Pipe()
	sent := &atomic.Int64{}
	go func() {
		defer pw.Close()
		step := n / 20
		for off := 0; off < n; off += step {
			chunk := content(min(step, n-off), byte(off))
			if _, err := pw.Write(chunk); err != nil {
				return
			}
			sent.Add(int64(len(chunk)))
			time.Sleep(d / 20)
		}
	}()
	return pr, sent
}

func TestR_HA_011_UploadsDuringARolloutRestart(t *testing.T) {
	t.Run("R-HA-011 an upload in flight when the replica is told to stop finishes; the next upload goes to another replica", func(t *testing.T) {
		te := newTransferEnv(t)
		a, b := te.a, te.b
		body, _ := trickle(200_000, 1500*time.Millisecond)
		type result struct {
			res *http.Response
			err error
		}
		out := make(chan result, 1)
		go func() {
			req, _ := http.NewRequest("PUT", a.baseURL+"/files/generic-local/roll/inflight.bin", body)
			req.ContentLength = 200_000
			req.Header.Set("Authorization", "Bearer ci")
			res, err := client.Do(req) //nolint:bodyclose // the receiver of the channel closes it
			out <- result{res, err}
		}()
		time.Sleep(300 * time.Millisecond)
		a.drain.Store(true)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := a.server.Shutdown(ctx); err != nil {
			t.Fatalf("shutdown: %v", err)
		}
		r := <-out
		if r.err != nil || r.res.StatusCode != 201 {
			t.Fatalf("the upload in flight: %v %v", r.res, r.err)
		}
		if r.res.Header.Get("Connection") != "close" && !r.res.Close {
			t.Errorf("a response during the drain asks the client to reconnect: %v", r.res.Header)
		}
		_ = r.res.Body.Close()
		if _, err := a.raw("ci", "PUT", "/files/generic-local/roll/after.bin", strings.NewReader("x")); err == nil { //nolint:bodyclose // the connection must be refused: there is no body
			t.Error("the stopped replica accepted a connection")
		}
		if res := b.put("ci", "/files/generic-local/roll/after.bin", "x"); res.status != 201 {
			t.Fatalf("retry on the other replica: %d", res.status)
		}
		if res := b.get("reader", "/files/generic-local/roll/inflight.bin"); res.status != 200 || len(res.body) != 200_000 {
			t.Fatalf("the finished upload: %d, %d bytes", res.status, len(res.body))
		}
	})

	t.Run("R-ARCH-024 R-STOR-004 an upload whose body is still arriving at the deadline is cut and leaves nothing committed; the retry succeeds elsewhere", func(t *testing.T) {
		te := newTransferEnv(t)
		a, b := te.a, te.b
		body, _ := trickle(200_000, 5*time.Second)
		errc := make(chan error, 1)
		go func() {
			req, _ := http.NewRequest("PUT", a.baseURL+"/files/generic-local/roll/cut.bin", body)
			req.ContentLength = 200_000
			req.Header.Set("Authorization", "Bearer ci")
			res, err := client.Do(req) //nolint:bodyclose // the receiver of the channel closes it
			if err == nil {
				_ = res.Body.Close()
				err = fmt.Errorf("status %d", res.StatusCode)
			}
			errc <- err
		}()
		time.Sleep(300 * time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
		defer cancel()
		if err := a.server.Shutdown(ctx); err == nil {
			t.Fatal("the shutdown should have cut the upload")
		}
		<-errc
		time.Sleep(200 * time.Millisecond)
		if n := te.number("SELECT count(*) FROM items WHERE path = 'roll/cut.bin'"); n != 0 {
			t.Fatalf("%d items committed by a cut upload", n)
		}
		if entries, _ := os.ReadDir(a.spoolDir); len(entries) != 0 {
			t.Errorf("the spool of the cut upload was not removed: %v", entries)
		}
		if res := b.put("ci", "/files/generic-local/roll/cut.bin", string(content(200_000, 1))); res.status != 201 {
			t.Fatalf("retry: %d %s", res.status, res.body)
		}
	})

	t.Run("R-ARCH-024 R-HA-011 an upload whose body has arrived is never cut between its commit and its status line", func(t *testing.T) {
		te := newTransferEnv(t)
		a := te.a
		a.obj.putDelay.Store(int64(1500 * time.Millisecond)) // the object write takes longer than the deadline
		data := content(300_000, 5)
		type result struct {
			status int
			err    error
		}
		out := make(chan result, 1)
		go func() {
			req, _ := http.NewRequest("PUT", a.baseURL+"/files/generic-local/roll/commit.bin", strings.NewReader(string(data)))
			req.Header.Set("Authorization", "Bearer ci")
			res, err := client.Do(req) //nolint:bodyclose // the receiver of the channel closes it
			if err != nil {
				out <- result{err: err}
				return
			}
			_ = res.Body.Close()
			out <- result{status: res.StatusCode}
		}()
		time.Sleep(500 * time.Millisecond) // the body is in the spool, the object write is running
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		_ = a.server.Shutdown(ctx)
		r := <-out
		if r.err != nil || r.status != 201 {
			t.Fatalf("the committing upload: status %d, error %v", r.status, r.err)
		}
		if n := te.number("SELECT count(*) FROM items WHERE path = 'roll/commit.bin'"); n != 1 {
			t.Fatalf("%d items", n)
		}
	})
}

func TestConcurrentWritersOnOnePath(t *testing.T) {
	te := newTransferEnv(t)
	a, b := te.a, te.b
	t.Run("R-STOR-005 concurrent uploads of different bytes to one path serialize: every one succeeds, one item remains, and it is one of the uploads", func(t *testing.T) {
		const n = 12
		bodies := make([]string, n)
		var wg sync.WaitGroup
		statuses := make([]int, n)
		for i := range n {
			bodies[i] = string(content(20_000+i, byte(i)))
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := a
				if i%2 == 1 {
					r = b
				}
				statuses[i] = r.put("maint", "/files/generic-local/race/one.bin", bodies[i]).status
			}()
		}
		wg.Wait()
		for i, s := range statuses {
			if s != 201 && s != 200 {
				t.Errorf("upload %d answered %d", i, s)
			}
		}
		if c := te.number("SELECT count(*) FROM items WHERE path = 'race/one.bin'"); c != 1 {
			t.Fatalf("%d items", c)
		}
		got := a.get("reader", "/files/generic-local/race/one.bin")
		found := false
		for _, b := range bodies {
			found = found || string(got.body) == b
		}
		if !found {
			t.Fatal("the stored content is none of the uploads")
		}
	})

	t.Run("R-STOR-003 deploys under a folder and deletes of its last child never leave an item without its parent folder", func(t *testing.T) {
		stop := make(chan struct{})
		var wg sync.WaitGroup
		var fail atomic.Int64
		for w := range 6 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := []*replica{a, b}[w%2]
				for i := 0; ; i++ {
					select {
					case <-stop:
						return
					default:
					}
					p := fmt.Sprintf("/files/generic-local/orph/d%d/f%d.bin", w%3, i%4)
					if s := r.put("maint", p, fmt.Sprint(w, i)).status; s != 201 && s != 200 {
						fail.Add(1)
					}
					if s := r.do("maint", "DELETE", p, nil).status; s != 204 && s != 404 {
						fail.Add(1)
					}
				}
			}()
		}
		time.Sleep(4 * time.Second)
		close(stop)
		wg.Wait()
		if fail.Load() != 0 {
			t.Errorf("%d requests failed", fail.Load())
		}
		orphans := te.number(`SELECT count(*) FROM items i WHERE i.parent_path <> ''
			AND NOT EXISTS (SELECT 1 FROM items p WHERE p.repo_id = i.repo_id AND p.path = i.parent_path AND p.kind = 'folder')`)
		if orphans != 0 {
			t.Fatalf("%d items without a parent folder", orphans)
		}
	})
}
