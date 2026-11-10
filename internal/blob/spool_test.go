package blob

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
)

func newSpool(t *testing.T, mutate func(*config.StorageSpool), opts ...SpoolOption) *Spool {
	t.Helper()
	cfg := testStorage(t, t.TempDir())
	if mutate != nil {
		mutate(&cfg.Spool)
	}
	// The spool's admission must not depend on the free space of the machine that runs the tests (the default
	// min_free_bytes is 2 GiB); a test of the volume check passes its own WithFreeBytes, which comes later and wins.
	plenty := WithFreeBytes(func(string) (uint64, error) { return 1 << 40, nil })
	sp, err := NewSpool(cfg.Spool, append([]SpoolOption{plenty}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return sp
}

func files(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

// countingReader counts Read calls and bytes, and fails after limit bytes when
// failAt is set.
type countingReader struct {
	r      io.Reader
	reads  int
	bytes  int64
	failAt int64
	err    error
}

func (c *countingReader) Read(p []byte) (int, error) {
	c.reads++
	if c.failAt > 0 && c.bytes >= c.failAt {
		return 0, c.err
	}
	n, err := c.r.Read(p)
	c.bytes += int64(n)
	return n, err
}

func data(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31 + i>>8)
	}
	return b
}

func TestSpoolDigests(t *testing.T) {
	body := data(3 << 20)
	t.Run("R-STOR-002 four digests and the size come out of one pass over the body", func(t *testing.T) {
		sp := newSpool(t, nil)
		cr := &countingReader{r: bytes.NewReader(body)}
		got, err := sp.Ingest(context.Background(), cr, IngestOptions{ContentLength: -1})
		if err != nil {
			t.Fatal(err)
		}
		defer got.Close()
		s256, s1, m5, s512 := sha256.Sum256(body), sha1.Sum(body), md5.Sum(body), sha512.Sum512(body)
		if got.SHA256 != s256 || got.SHA1 != s1 || got.MD5 != m5 || got.SHA512 != s512 || got.Size != int64(len(body)) {
			t.Fatalf("digests differ from the reference: %+v", got.Digests)
		}
		if cr.bytes != int64(len(body)) {
			t.Fatalf("the body was read %d bytes, want one pass of %d", cr.bytes, len(body))
		}
		// The spool file holds the same bytes, which is what multipart reads.
		saved, _ := io.ReadAll(got.Reader())
		if !bytes.Equal(saved, body) {
			t.Fatal("the spool file differs from the body")
		}
	})
	t.Run("R-STOR-002 SHA-512 is computed for every upload", func(t *testing.T) {
		sp := newSpool(t, nil)
		got, err := sp.Ingest(context.Background(), strings.NewReader("abc"), IngestOptions{ContentLength: 3})
		if err != nil {
			t.Fatal(err)
		}
		defer got.Close()
		want := sha512.Sum512([]byte("abc"))
		if got.SHA512 != want {
			t.Fatal("small bodies in memory miss the SHA-512")
		}
	})
}

func TestSpoolAdmission(t *testing.T) {
	big := data(2 << 20)
	t.Run("R-STOR-004 a declared length above the maximum answers 413 without reading the body", func(t *testing.T) {
		sp := newSpool(t, nil)
		cr := &countingReader{r: bytes.NewReader(big)}
		_, err := sp.Ingest(context.Background(), cr, IngestOptions{ContentLength: int64(len(big)), MaxSize: 1 << 20})
		if !errors.Is(err, ErrTooLarge) || errs.KindOf(err) != errs.TooLarge {
			t.Fatalf("got %v", err)
		}
		if cr.reads != 0 || len(files(t, sp.Dir())) != 0 {
			t.Fatalf("reads=%d files=%v", cr.reads, files(t, sp.Dir()))
		}
	})
	t.Run("R-STOR-004 a chunked body that passes the maximum fails 413 and leaves nothing", func(t *testing.T) {
		sp := newSpool(t, nil)
		_, err := sp.Ingest(context.Background(), bytes.NewReader(big), IngestOptions{ContentLength: -1, MaxSize: 1 << 20})
		if !errors.Is(err, ErrTooLarge) {
			t.Fatalf("got %v", err)
		}
		if len(files(t, sp.Dir())) != 0 || sp.Reserved() != 0 {
			t.Fatalf("files=%v reserved=%d", files(t, sp.Dir()), sp.Reserved())
		}
	})
	t.Run("R-STOR-004 a known length that does not fit the budget answers 503 with Retry-After", func(t *testing.T) {
		sp := newSpool(t, func(c *config.StorageSpool) { c.MaxBytes = 3 << 20 })
		first, err := sp.Ingest(context.Background(), bytes.NewReader(big), IngestOptions{ContentLength: int64(len(big))})
		if err != nil {
			t.Fatal(err)
		}
		_, err = sp.Ingest(context.Background(), bytes.NewReader(big), IngestOptions{ContentLength: int64(len(big))})
		var e *errs.Error
		if !errors.Is(err, ErrSpoolFull) || !errors.As(err, &e) || e.Kind != errs.Unavailable || e.RetryAfter <= 0 {
			t.Fatalf("got %#v", err)
		}
		first.Close()
		if again, err := sp.Ingest(context.Background(), bytes.NewReader(big), IngestOptions{ContentLength: int64(len(big))}); err != nil {
			t.Fatalf("after the first upload ended: %v", err)
		} else {
			again.Close()
		}
	})
	t.Run("R-STOR-004 two chunked uploads: one fits, the other answers 503 and leaves no file", func(t *testing.T) {
		sp := newSpool(t, func(c *config.StorageSpool) { c.MaxBytes = 6 << 20 })
		// The first upload reads 5 MiB, then waits; the second starts meanwhile.
		pr, pw := io.Pipe()
		done := make(chan error, 1)
		var first *Spooled
		go func() {
			var err error
			first, err = sp.Ingest(context.Background(), pr, IngestOptions{ContentLength: -1})
			done <- err
		}()
		if _, err := pw.Write(data(5 << 20)); err != nil {
			t.Fatal(err)
		}
		// The write returns once the first upload has read the bytes; it reserves the budget for the last of them after that
		// read, in its own goroutine (copyHashed). Until it has, the second upload could take that budget and the first one
		// fail instead: wait for the first upload's 5 MiB, bounded, before the second starts.
		deadline := time.Now().Add(10 * time.Second)
		for sp.Reserved() < 5<<20 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if got := sp.Reserved(); got != 5<<20 {
			t.Fatalf("the first upload reserved %d bytes after reading 5 MiB, want %d", got, 5<<20)
		}
		_, err := sp.Ingest(context.Background(), bytes.NewReader(data(5<<20)), IngestOptions{ContentLength: -1})
		if !errors.Is(err, ErrSpoolFull) {
			t.Fatalf("second upload: %v", err)
		}
		pw.Close()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		first.Close()
		if len(files(t, sp.Dir())) != 0 || sp.Reserved() != 0 {
			t.Fatalf("files=%v reserved=%d", files(t, sp.Dir()), sp.Reserved())
		}
	})
	t.Run("R-STOR-004 a volume below min_free_bytes answers 503", func(t *testing.T) {
		sp := newSpool(t, func(c *config.StorageSpool) { c.MinFreeBytes = 1 << 30 },
			WithFreeBytes(func(string) (uint64, error) { return 1 << 20, nil }))
		_, err := sp.Ingest(context.Background(), bytes.NewReader(big), IngestOptions{ContentLength: int64(len(big))})
		if !errors.Is(err, ErrSpoolFull) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestSpoolFiles(t *testing.T) {
	big := data(2 << 20)
	t.Run("R-STOR-004 a body above the memory threshold goes to a 0600 file named by a UUID", func(t *testing.T) {
		sp := newSpool(t, nil)
		got, err := sp.Ingest(context.Background(), bytes.NewReader(big), IngestOptions{ContentLength: int64(len(big))})
		if err != nil {
			t.Fatal(err)
		}
		defer got.Close()
		if got.InMemory() {
			t.Fatal("2 MiB stayed in memory")
		}
		fi, err := os.Stat(got.Path())
		if err != nil {
			t.Fatal(err)
		}
		uuid := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
		if fi.Mode().Perm() != 0o600 || !uuid.MatchString(filepath.Base(got.Path())) {
			t.Fatalf("mode %v name %s", fi.Mode().Perm(), filepath.Base(got.Path()))
		}
	})
	t.Run("R-STOR-004 a small body of known length stays in memory", func(t *testing.T) {
		sp := newSpool(t, nil)
		got, err := sp.Ingest(context.Background(), strings.NewReader("hello"), IngestOptions{ContentLength: 5})
		if err != nil {
			t.Fatal(err)
		}
		defer got.Close()
		if !got.InMemory() || len(files(t, sp.Dir())) != 0 || sp.Reserved() != 0 {
			t.Fatal("a 5-byte body used the disk or the budget")
		}
	})
	t.Run("R-STOR-004 Close removes the file and returns the budget, also when called twice", func(t *testing.T) {
		sp := newSpool(t, nil)
		got, err := sp.Ingest(context.Background(), bytes.NewReader(big), IngestOptions{ContentLength: int64(len(big))})
		if err != nil {
			t.Fatal(err)
		}
		if sp.Reserved() != int64(len(big)) {
			t.Fatalf("reserved %d", sp.Reserved())
		}
		got.Close()
		got.Close()
		if len(files(t, sp.Dir())) != 0 || sp.Reserved() != 0 {
			t.Fatalf("files=%v reserved=%d", files(t, sp.Dir()), sp.Reserved())
		}
	})
	t.Run("R-STOR-004 a replica starts with an empty spool directory", func(t *testing.T) {
		dir := t.TempDir()
		for _, n := range []string{"3f1c2d4e-5a6b-4c7d-8e9f-0a1b2c3d4e5f", "0a1b2c3d-0000-4000-b000-000000000001"} {
			if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		// The directory a freshly formatted volume carries is left alone.
		if err := os.Mkdir(filepath.Join(dir, "lost+found"), 0o700); err != nil {
			t.Fatal(err)
		}
		cfg := testStorage(t, dir)
		if _, err := NewSpool(cfg.Spool); err != nil {
			t.Fatal(err)
		}
		if f := files(t, dir); len(f) != 1 || f[0] != "lost+found" {
			t.Fatalf("left over: %v, want only lost+found", f)
		}
	})
	t.Run("R-STOR-004 a spool directory with other content is refused and nothing in it is deleted", func(t *testing.T) {
		for name, mk := range map[string]func(dir string) string{
			"a file of another name": func(dir string) string { return mustWrite(t, filepath.Join(dir, "important.db")) },
			"a directory":            func(dir string) string { return mustMkdir(t, filepath.Join(dir, "data")) },
			"an upper-case UUID": func(dir string) string {
				return mustWrite(t, filepath.Join(dir, "3F1C2D4E-5A6B-4C7D-8E9F-0A1B2C3D4E5F"))
			},
			"a UUID that is not v4": func(dir string) string {
				return mustWrite(t, filepath.Join(dir, "3f1c2d4e-5a6b-1c7d-8e9f-0a1b2c3d4e5f"))
			},
			"a directory named as UUID": func(dir string) string {
				return mustMkdir(t, filepath.Join(dir, "3f1c2d4e-5a6b-4c7d-8e9f-0a1b2c3d4e5f"))
			},
		} {
			dir := t.TempDir()
			stale := mustWrite(t, filepath.Join(dir, "0a1b2c3d-0000-4000-b000-000000000001"))
			other := mk(dir)
			_, err := NewSpool(testStorage(t, dir).Spool)
			if err == nil || !strings.Contains(err.Error(), other) {
				t.Errorf("%s: err = %v, want one naming %s", name, err, other)
			}
			for _, p := range []string{stale, other} {
				if _, serr := os.Lstat(p); serr != nil {
					t.Errorf("%s: %s was deleted: %v", name, p, serr)
				}
			}
		}
	})
}

func mustWrite(t *testing.T, path string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustMkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSpoolCleanupOnEveryErrorPath(t *testing.T) {
	big := data(2 << 20)
	boom := errors.New("connection reset")
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	cases := []struct {
		name string
		ctx  context.Context
		body func() io.Reader
		opts IngestOptions
		want error
	}{
		{"client disconnect mid-body", context.Background(), func() io.Reader {
			return &countingReader{r: bytes.NewReader(big), failAt: 512 << 10, err: boom}
		}, IngestOptions{ContentLength: int64(len(big))}, boom},
		{"client disconnect mid-body, chunked", context.Background(), func() io.Reader {
			return &countingReader{r: bytes.NewReader(big), failAt: 512 << 10, err: boom}
		}, IngestOptions{ContentLength: -1}, boom},
		{"short body", context.Background(), func() io.Reader { return bytes.NewReader(big[:1<<20]) }, IngestOptions{ContentLength: int64(len(big))}, ErrLengthMismatch},
		{"long body", context.Background(), func() io.Reader { return bytes.NewReader(big) }, IngestOptions{ContentLength: 1 << 20}, ErrLengthMismatch},
		{
			"wrong declared digest", context.Background(), func() io.Reader { return bytes.NewReader(big) },
			IngestOptions{ContentLength: int64(len(big)), Expect: Expected{SHA256: make([]byte, 32)}},
			ErrDigestMismatch,
		},
		{"cancelled context", cancelled, func() io.Reader { return bytes.NewReader(big) }, IngestOptions{ContentLength: int64(len(big))}, context.Canceled},
		{"too large", context.Background(), func() io.Reader { return bytes.NewReader(big) }, IngestOptions{ContentLength: -1, MaxSize: 1 << 20}, ErrTooLarge},
	}
	for _, c := range cases {
		t.Run("R-STOR-004 "+c.name+" leaves no file and no reservation", func(t *testing.T) {
			sp := newSpool(t, nil)
			got, err := sp.Ingest(c.ctx, c.body(), c.opts)
			if !errors.Is(err, c.want) || got != nil {
				t.Fatalf("got %v, %v want %v", got, err, c.want)
			}
			if f := files(t, sp.Dir()); len(f) != 0 || sp.Reserved() != 0 {
				t.Fatalf("files=%v reserved=%d", f, sp.Reserved())
			}
		})
	}
}

func TestSpoolDeclaredDigests(t *testing.T) {
	body := data(1 << 20)
	sum := sha256.Sum256(body)
	t.Run("R-STOR-004 a matching declared digest passes, a wrong one answers 409", func(t *testing.T) {
		sp := newSpool(t, nil)
		ok, err := sp.Ingest(context.Background(), bytes.NewReader(body), IngestOptions{ContentLength: int64(len(body)), Expect: Expected{SHA256: sum[:]}})
		if err != nil {
			t.Fatal(err)
		}
		ok.Close()
		wrong := sha512.Sum512(nil)
		_, err = sp.Ingest(context.Background(), bytes.NewReader(body), IngestOptions{ContentLength: int64(len(body)), Expect: Expected{SHA512: wrong[:]}})
		if errs.KindOf(err) != errs.Conflict || !errors.Is(err, ErrDigestMismatch) {
			t.Fatalf("got %v", err)
		}
	})
}

// R-VULN-036: the image scanning works below the spool, the one volume a pod may write, so its directory must not stop a worker that
// restarts with the volume still there, while anything else in the directory still does.
func TestR_VULN_036_TheSpoolKeepsTheWorkingDirectoryOfTheImageScanning(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ImageScanDir, "db"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.StorageSpool{Dir: dir, MaxBytes: 1 << 20}
	if _, err := NewSpool(cfg, WithFreeBytes(func(string) (uint64, error) { return 1 << 40, nil })); err != nil {
		t.Fatalf("a spool with the scanning's directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ImageScanDir, "db")); err != nil {
		t.Fatalf("the directory was removed: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "other"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSpool(cfg, WithFreeBytes(func(string) (uint64, error) { return 1 << 40, nil })); err == nil {
		t.Fatal("a spool with an unknown directory was accepted")
	}
}

func TestQ_VULN_145_AHoldIsCountedInTheBudgetOfTheUploads(t *testing.T) {
	sp := newSpool(t, func(c *config.StorageSpool) { c.MaxBytes = 1000; c.MemoryThreshold = 1 })
	h, err := sp.Hold(700)
	if err != nil {
		t.Fatal(err)
	}
	if sp.Reserved() != 700 || h.Size() != 700 {
		t.Fatalf("reserved %d, hold %d", sp.Reserved(), h.Size())
	}
	// An upload that would overrun the budget with the hold is refused with the spool's own error.
	if _, err := sp.Ingest(context.Background(), strings.NewReader(strings.Repeat("x", 400)), IngestOptions{ContentLength: 400}); !errors.Is(err, ErrSpoolFull) {
		t.Fatalf("an upload next to a hold of 700 of 1000: %v", err)
	}
	if _, err := sp.Hold(400); !errors.Is(err, ErrSpoolFull) {
		t.Fatalf("a second hold beyond the budget: %v", err)
	}
	if sp.Reserved() != 700 {
		t.Fatalf("a refused hold took %d", sp.Reserved()-700)
	}
	h.Release()
	h.Release()
	var none *Hold
	none.Release()
	if sp.Reserved() != 0 {
		t.Fatalf("reserved %d after the release", sp.Reserved())
	}
	if _, err := sp.Hold(1000); err != nil {
		t.Fatalf("the budget is whole again: %v", err)
	}
}

func TestQ_VULN_145_AHoldFollowsTheFreeSpaceOfTheVolumeAndAdoptionChecksNothing(t *testing.T) {
	sp := newSpool(t, func(c *config.StorageSpool) { c.MaxBytes = 1 << 30; c.MinFreeBytes = 100 },
		WithFreeBytes(func(string) (uint64, error) { return 600, nil }))
	if _, err := sp.Hold(600); !errors.Is(err, ErrSpoolFull) {
		t.Fatalf("a hold that leaves less than the minimum free: %v", err)
	}
	h, err := sp.Hold(500)
	if err != nil {
		t.Fatal(err)
	}
	h.Release()
	// Bytes that are on the volume already are counted whatever the budget and the free space say.
	a := sp.Adopt(2 << 30)
	if sp.Reserved() != 2<<30 {
		t.Fatalf("reserved %d", sp.Reserved())
	}
	if _, err := sp.Hold(1); !errors.Is(err, ErrSpoolFull) {
		t.Fatalf("a hold next to an adopted directory larger than the budget: %v", err)
	}
	a.Release()
	if sp.Reserved() != 0 {
		t.Fatalf("reserved %d", sp.Reserved())
	}
	if _, err := sp.Hold(-1); err == nil {
		t.Fatal("a negative hold")
	}
}
