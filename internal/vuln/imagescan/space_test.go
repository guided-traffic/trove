package imagescan

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/vuln/store"
)

// fakeSpace is a spool with a byte budget: it holds what it was asked for and refuses with ErrNoSpace beyond the budget.
type fakeSpace struct {
	mu     sync.Mutex
	budget int64
	held   int64
}

func (f *fakeSpace) Hold(n int64) (func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.held+n > f.budget {
		return nil, fmt.Errorf("%w: %d held of %d", ErrNoSpace, f.held, f.budget)
	}
	f.held += n
	return f.once(n), nil
}

func (f *fakeSpace) Adopt(n int64) func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.held += n
	return f.once(n)
}

func (f *fakeSpace) once(n int64) func() {
	var done sync.Once
	return func() {
		done.Do(func() {
			f.mu.Lock()
			f.held -= n
			f.mu.Unlock()
		})
	}
}

func (f *fakeSpace) Budget() int64 { return f.budget }

func (f *fakeSpace) Held() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.held
}

func serviceWith(sp Space, wait time.Duration) *Service {
	return &Service{o: Options{Space: sp, SpaceWait: wait, SpacePoll: time.Millisecond}, log: slog.Default()}
}

func TestQ_VULN_145_AScanHoldsItsRoomAndGivesItBackOnEveryPath(t *testing.T) {
	ctx := context.Background()
	t.Run("a scan that fits holds its bytes until the lease is released, and releasing twice is harmless", func(t *testing.T) {
		sp := &fakeSpace{budget: 100}
		var l lease
		if err := serviceWith(sp, time.Second).admit(ctx, &l, 40, 0); err != nil {
			t.Fatal(err)
		}
		if sp.Held() != 40 {
			t.Fatalf("held %d", sp.Held())
		}
		l.Release()
		l.Release()
		if sp.Held() != 0 {
			t.Fatalf("held %d after the release", sp.Held())
		}
	})
	t.Run("a scan that does not fit now waits and takes the room when other work gives it back", func(t *testing.T) {
		sp := &fakeSpace{budget: 100}
		other, err := sp.Hold(80)
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			time.Sleep(20 * time.Millisecond)
			other()
		}()
		var l lease
		if err := serviceWith(sp, time.Minute).admit(ctx, &l, 50, 0); err != nil {
			t.Fatal(err)
		}
		if sp.Held() != 50 {
			t.Fatalf("held %d", sp.Held())
		}
		l.Release()
	})
	t.Run("a scan that cannot fit next to the database is refused at once, holds nothing and is put off for an hour", func(t *testing.T) {
		sp := &fakeSpace{budget: 100}
		var l lease
		err := serviceWith(sp, time.Minute).admit(ctx, &l, 50, 60)
		var se *ScanError
		if !errors.As(err, &se) || se.Code != CodeSpoolTooSmall || se.Defer != refusedBackoff || se.Terminal {
			t.Fatalf("%v", err)
		}
		if sp.Held() != 0 || l.release != nil {
			t.Fatalf("held %d", sp.Held())
		}
	})
	t.Run("a scan that waits longer than the wait is put off with the reason and holds nothing", func(t *testing.T) {
		sp := &fakeSpace{budget: 100}
		if _, err := sp.Hold(80); err != nil {
			t.Fatal(err)
		}
		var l lease
		err := serviceWith(sp, 30*time.Millisecond).admit(ctx, &l, 50, 0)
		var se *ScanError
		if !errors.As(err, &se) || se.Code != CodeSpoolBusy || se.Defer != busyBackoff || se.Terminal {
			t.Fatalf("%v", err)
		}
		if sp.Held() != 80 {
			t.Fatalf("held %d, want only the other work's 80", sp.Held())
		}
	})
	t.Run("a cancelled run stops the wait and holds nothing", func(t *testing.T) {
		sp := &fakeSpace{budget: 100}
		if _, err := sp.Hold(80); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancelCause(ctx)
		cause := errors.New("drain")
		done := make(chan error, 1)
		var l lease
		go func() { done <- serviceWith(sp, time.Hour).admit(ctx, &l, 50, 0) }()
		cancel(cause)
		if err := <-done; !errors.Is(err, cause) {
			t.Fatalf("%v", err)
		}
		if sp.Held() != 80 || l.release != nil {
			t.Fatalf("held %d", sp.Held())
		}
	})
	t.Run("an error of the spool that is not a lack of room ends the admission", func(t *testing.T) {
		var l lease
		boom := errors.New("boom")
		err := serviceWith(failingSpace{boom}, time.Minute).admit(ctx, &l, 1, 0)
		if !errors.Is(err, boom) {
			t.Fatalf("%v", err)
		}
	})
}

type failingSpace struct{ err error }

func (f failingSpace) Hold(int64) (func(), error) { return nil, f.err }
func (f failingSpace) Adopt(int64) func()         { return func() {} }
func (f failingSpace) Budget() int64              { return 1 << 40 }

func TestQ_VULN_145_TheRoomOfTheBlobsIsAdmittedBeforeTheFirstOneIsWritten(t *testing.T) {
	m := imageWith(tarOf(t, map[string]string{"etc/os-release": "alpine"}, true))
	var total int64
	for _, b := range m.blobs {
		total += int64(len(b))
	}
	t.Run("the admission sees the compressed size of the config and the layers", func(t *testing.T) {
		var got int64
		err := buildLayout(context.Background(), m, ImageRef{}, t.TempDir(), lims(), func(n int64) error { got = n; return nil })
		if err != nil || got != total {
			t.Fatalf("%v: admitted %d, the blobs are %d", err, got, total)
		}
	})
	t.Run("a refusal ends the build before a file exists", func(t *testing.T) {
		dir := t.TempDir()
		refused := &ScanError{Code: CodeSpoolBusy, Defer: time.Minute}
		err := buildLayout(context.Background(), m, ImageRef{}, dir, lims(), func(int64) error { return refused })
		if !errors.Is(err, refused) {
			t.Fatalf("%v", err)
		}
		if ents, _ := os.ReadDir(dir); len(ents) != 0 {
			t.Fatalf("%d entries written before the admission", len(ents))
		}
	})
	t.Run("an image over the limit is refused as before and never asks for room", func(t *testing.T) {
		l := lims()
		l.MaxImageBytes = 10
		asked := false
		err := buildLayout(context.Background(), m, ImageRef{}, t.TempDir(), l, func(int64) error { asked = true; return nil })
		wantScanError(t, err, CodeImageTooLarge)
		if asked {
			t.Fatal("asked for room for an image that is refused")
		}
	})
}

// dbArchive is a database archive with the two files, of the given sizes.
func dbArchive(t *testing.T, dbBytes int) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	meta := `{"Version":2,"UpdatedAt":"2026-11-08T12:51:20Z","NextUpdate":"2026-11-09T12:51:20Z"}`
	for name, body := range map[string][]byte{dbMetaName: []byte(meta), dbFileName: bytes.Repeat([]byte("d"), dbBytes)} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type archiveBlobs map[[32]byte][]byte

func (a archiveBlobs) Open(_ context.Context, sum [32]byte, _ int64) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(a[sum])), nil
}

type currentDB struct{ db store.ImageDB }

func (c *currentDB) DB(context.Context) (store.ImageDB, bool, error) { return c.db, true, nil }

func dbOf(n byte) store.ImageDB {
	return store.ImageDB{Digest: "sha256:" + fmt.Sprintf("%064x", n), Blob: [32]byte{n}}
}

func TestQ_VULN_145_TheUnpackedDatabaseIsHeldOnceBySizeAndFollowsItsDirectory(t *testing.T) {
	ctx := context.Background()
	one, two := dbOf(1), dbOf(2)
	blobs := archiveBlobs{one.Blob: dbArchive(t, 1000), two.Blob: dbArchive(t, 3000)}
	meta := int64(len(`{"Version":2,"UpdatedAt":"2026-11-08T12:51:20Z","NextUpdate":"2026-11-09T12:51:20Z"}`))
	rows := &currentDB{db: one}

	t.Run("the room is the size of the unpacked files, taken once however often the database is acquired", func(t *testing.T) {
		sp := &fakeSpace{budget: 1 << 20}
		d := &LocalDatabase{Dir: t.TempDir(), Blobs: blobs, Rows: rows, Space: sp}
		a1, err := d.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if a1.Held != 1000+meta || sp.Held() != 1000+meta {
			t.Fatalf("access %d, spool %d, want %d", a1.Held, sp.Held(), 1000+meta)
		}
		a2, err := d.Acquire(ctx)
		if err != nil || sp.Held() != 1000+meta {
			t.Fatalf("%v: spool %d after the second acquire", err, sp.Held())
		}
		a1.Release()
		a2.Release()
		if sp.Held() != 1000+meta {
			t.Fatalf("the room left with the last use of a database that is still the current one: %d", sp.Held())
		}
	})
	t.Run("a spool without room refuses the unpacking before a byte is written and leaves nothing held", func(t *testing.T) {
		sp := &fakeSpace{budget: 500}
		dir := t.TempDir()
		d := &LocalDatabase{Dir: dir, Blobs: blobs, Rows: rows, Space: sp}
		_, err := d.Acquire(ctx)
		if !errors.Is(err, ErrNoSpace) {
			t.Fatalf("%v", err)
		}
		if sp.Held() != 0 {
			t.Fatalf("held %d after the refusal", sp.Held())
		}
		if ents, _ := os.ReadDir(dir); len(ents) != 0 {
			t.Fatalf("%d entries left in the cache", len(ents))
		}
	})
	t.Run("a new database frees the room of the old one first, so a spool sized for one database is enough", func(t *testing.T) {
		sp := &fakeSpace{budget: 3000 + meta + 100}
		d := &LocalDatabase{Dir: t.TempDir(), Blobs: blobs, Rows: &currentDB{db: one}, Space: sp}
		a, err := d.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		a.Release()
		d.Rows = &currentDB{db: two}
		a, err = d.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer a.Release()
		if sp.Held() != 3000+meta {
			t.Fatalf("held %d, want only the new database's %d", sp.Held(), 3000+meta)
		}
	})
	t.Run("a database that is in use stays held while a new one is unpacked next to it", func(t *testing.T) {
		sp := &fakeSpace{budget: 1 << 20}
		d := &LocalDatabase{Dir: t.TempDir(), Blobs: blobs, Rows: &currentDB{db: one}, Space: sp}
		a1, err := d.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		d.Rows = &currentDB{db: two}
		a2, err := d.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if sp.Held() != 1000+3000+2*meta {
			t.Fatalf("held %d", sp.Held())
		}
		a1.Release()
		a2.Release()
	})
	t.Run("a database a restarted container finds in its volume is adopted at the size it has on the disk", func(t *testing.T) {
		dir := t.TempDir()
		first := &LocalDatabase{Dir: dir, Blobs: blobs, Rows: &currentDB{db: one}, Space: &fakeSpace{budget: 1 << 20}}
		if _, err := first.Acquire(ctx); err != nil {
			t.Fatal(err)
		}
		sp := &fakeSpace{budget: 100} // smaller than the database: the bytes exist whatever the budget says
		restarted := &LocalDatabase{Dir: dir, Blobs: archiveBlobs{}, Rows: &currentDB{db: one}, Space: sp}
		a, err := restarted.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer a.Release()
		if sp.Held() != 1000+meta || a.Held != 1000+meta {
			t.Fatalf("spool %d, access %d, want %d", sp.Held(), a.Held, 1000+meta)
		}
		if _, err := os.Stat(filepath.Join(dir, hexOf(one.Digest), "db", dbFileName)); err != nil {
			t.Fatal(err)
		}
	})
}
