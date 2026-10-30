package untrusted_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/formattest/crafted"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
)

func tarGz(t *testing.T, build func(tw *tar.Writer)) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	build(tw)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func file(t *testing.T, tw *tar.Writer, name, body string) {
	t.Helper()
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
}

func wantKind(t *testing.T, err error, kind errs.Kind) {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) || e.Kind != kind {
		t.Fatalf("error = %v, want kind %v", err, kind)
	}
}

// counting counts the bytes a reader asked its source for: a refusal that comes
// before the gigabytes were produced is shown by how little was read, not by how
// long it took, which a loaded machine bends.
type counting struct {
	io.ReadCloser
	n int64
}

func (c *counting) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	c.n += int64(n)
	return n, err
}

type countingAt struct {
	io.ReaderAt
	n int64
}

func (c *countingAt) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.ReaderAt.ReadAt(p, off)
	c.n += int64(n)
	return n, err
}

// wantReadLess fails when more than max bytes were read.
func wantReadLess(t *testing.T, got, max int64) {
	t.Helper()
	if got > max {
		t.Fatalf("read %d bytes of the source, want at most %d", got, max)
	}
}

func TestR_ARCH_028_BoundedReads(t *testing.T) {
	t.Run("R-ARCH-028 a read of exactly max bytes passes and one byte more is TooLarge, never a quiet cut", func(t *testing.T) {
		got, err := untrusted.ReadAll(strings.NewReader("12345"), 5)
		if err != nil || string(got) != "12345" {
			t.Fatalf("got %q, %v", got, err)
		}
		_, err = untrusted.ReadAll(strings.NewReader("123456"), 5)
		wantKind(t, err, errs.TooLarge)
		if !untrusted.IsTooLarge(err) || untrusted.IsRefused(err) {
			t.Fatalf("classification of %v", err)
		}
		_, err = untrusted.ReadAll(strings.NewReader(""), 0)
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-ARCH-028 a read after the context ended fails with its error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := io.ReadAll(untrusted.WithContext(ctx, strings.NewReader("x"))); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-ARCH-028 the context carries the deadline of the limits, the default for an unset one", func(t *testing.T) {
		ctx, cancel := untrusted.Limits{Deadline: time.Minute}.Context(context.Background())
		defer cancel()
		if d, ok := ctx.Deadline(); !ok || time.Until(d) > time.Minute {
			t.Fatalf("deadline %v %v", d, ok)
		}
		ctx2, cancel2 := untrusted.Limits{}.Context(context.Background())
		defer cancel2()
		if d, ok := ctx2.Deadline(); !ok || time.Until(d) > 30*time.Second || time.Until(d) < 29*time.Second {
			t.Fatalf("default deadline %v %v", d, ok)
		}
	})
}

func TestR_ARCH_028_JSON(t *testing.T) {
	type doc struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	}
	l := untrusted.DefaultLimits()
	t.Run("R-ARCH-028 a document within the limits decodes", func(t *testing.T) {
		var d doc
		if err := untrusted.DecodeJSON(strings.NewReader(`{"name":"a","tags":["x","y"]}`), l, &d); err != nil || d.Name != "a" || len(d.Tags) != 2 {
			t.Fatalf("%+v %v", d, err)
		}
	})
	t.Run("R-ARCH-028 nesting at the bound passes and one level more is TooLarge", func(t *testing.T) {
		at := strings.Repeat("[", 64) + strings.Repeat("]", 64)
		if err := untrusted.CheckJSON([]byte(at), l); err != nil {
			t.Fatal(err)
		}
		over := strings.Repeat("[", 65) + strings.Repeat("]", 65)
		wantKind(t, untrusted.CheckJSON([]byte(over), l), errs.TooLarge)
	})
	t.Run("R-ARCH-028 a document nested 100,000 deep is refused at once and without building a tree", func(t *testing.T) {
		var v any
		wantKind(t, untrusted.DecodeJSON(crafted.DeepJSON(100_000).Open(), l, &v), errs.TooLarge)
	})
	t.Run("R-ARCH-028 brackets inside strings do not count", func(t *testing.T) {
		s := `{"a":"` + strings.Repeat("[", 500) + `\"` + strings.Repeat("{", 500) + `"}`
		if err := untrusted.CheckJSON([]byte(s), l); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-ARCH-028 the number of values is bounded", func(t *testing.T) {
		small := untrusted.Limits{JSONMembers: 100}
		wide := []byte("[" + strings.Repeat("0,", 150) + "0]")
		wantKind(t, untrusted.CheckJSON(wide, small), errs.TooLarge)
		ok := []byte("[" + strings.Repeat("0,", 90) + "0]")
		if err := untrusted.CheckJSON(ok, small); err != nil {
			t.Fatal(err)
		}
		var v any
		err := untrusted.DecodeJSON(crafted.WideJSON(3_000_000).Open(), untrusted.Limits{DocumentBytes: 16 << 20}, &v)
		wantKind(t, err, errs.TooLarge)
		if !strings.Contains(err.Error(), "values") {
			t.Fatalf("not the value count: %v", err)
		}
	})
	t.Run("R-ARCH-028 a document above the byte bound is TooLarge, the document bound when the core set one", func(t *testing.T) {
		var v any
		body := `["` + strings.Repeat("a", 2<<20) + `"]`
		wantKind(t, untrusted.DecodeJSON(strings.NewReader(body), l, &v), errs.TooLarge) // ManifestEntryBytes, 1 MiB
		big := untrusted.Limits{DocumentBytes: 4 << 20}
		if err := untrusted.DecodeJSON(strings.NewReader(body), big, &v); err != nil {
			t.Fatal(err)
		}
		tight := untrusted.Limits{DocumentBytes: 100}
		wantKind(t, untrusted.DecodeJSON(strings.NewReader(`["`+strings.Repeat("a", 200)+`"]`), tight, &v), errs.TooLarge)
	})
	t.Run("R-ARCH-028 a malformed document and trailing content are refused", func(t *testing.T) {
		var v any
		wantKind(t, untrusted.DecodeJSON(strings.NewReader(`{"a":`), l, &v), errs.Unprocessable)
		wantKind(t, untrusted.DecodeJSON(strings.NewReader(`{} {}`), l, &v), errs.Unprocessable)
		var d doc
		wantKind(t, untrusted.DecodeJSON(strings.NewReader(`{"name":5}`), l, &d), errs.Unprocessable)
	})
	t.Run("R-ARCH-028 a zero Limits is not unbounded", func(t *testing.T) {
		wantKind(t, untrusted.CheckJSON([]byte(strings.Repeat("[", 100)+strings.Repeat("]", 100)), untrusted.Limits{}), errs.TooLarge)
	})
}

func TestR_ARCH_028_XML(t *testing.T) {
	type pkg struct {
		ID string `xml:"metadata>id"`
	}
	l := untrusted.DefaultLimits()
	t.Run("R-ARCH-028 a document decodes, the predefined entities included", func(t *testing.T) {
		var p pkg
		if err := untrusted.DecodeXML(strings.NewReader(`<?xml version="1.0" encoding="utf-8"?><package><metadata><id>a&amp;b</id></metadata></package>`), l, &p); err != nil || p.ID != "a&b" {
			t.Fatalf("%+v %v", p, err)
		}
	})
	t.Run("R-ARCH-028 a document type declaration is refused and no entity is expanded", func(t *testing.T) {
		var p pkg
		wantKind(t, untrusted.DecodeXML(crafted.XMLDoctype().Open(), l, &p), errs.Unprocessable)
		wantKind(t, untrusted.CheckXML([]byte(`<package><metadata><id>&unknown;</id></metadata></package>`), l), errs.Unprocessable)
	})
	t.Run("R-ARCH-028 nesting at the bound passes and deeper is TooLarge", func(t *testing.T) {
		if err := untrusted.CheckXML([]byte(strings.Repeat("<a>", 64)+strings.Repeat("</a>", 64)), l); err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(crafted.DeepXML(100_000).Open())
		wantKind(t, untrusted.CheckXML(data, l), errs.TooLarge)
	})
	t.Run("R-ARCH-028 another encoding and an unbalanced document are refused", func(t *testing.T) {
		wantKind(t, untrusted.CheckXML([]byte(`<?xml version="1.0" encoding="UTF-16"?><a/>`), l), errs.Unprocessable)
		wantKind(t, untrusted.CheckXML([]byte(`<a><b></a>`), l), errs.Unprocessable)
		wantKind(t, untrusted.CheckXML([]byte(`<a>`), l), errs.Unprocessable)
	})
	t.Run("R-ARCH-028 a document above the byte bound is TooLarge", func(t *testing.T) {
		var p pkg
		wantKind(t, untrusted.DecodeXML(strings.NewReader("<a>"+strings.Repeat("x", 2<<20)+"</a>"), l, &p), errs.TooLarge)
	})
}

func TestR_ARCH_028_TarGz(t *testing.T) {
	ctx := context.Background()
	l := untrusted.DefaultLimits()
	t.Run("R-ARCH-028 entries are listed and a manifest is read within its bound", func(t *testing.T) {
		data := tarGz(t, func(tw *tar.Writer) {
			file(t, tw, "package/package.json", `{"name":"a"}`)
			file(t, tw, "package/index.js", strings.Repeat("x", 1000))
		})
		r := untrusted.NewTarGz(ctx, bytes.NewReader(data), l)
		e, err := r.Next()
		if err != nil || e.Name != "package/package.json" || e.Size != 12 {
			t.Fatalf("%+v %v", e, err)
		}
		got, err := r.ReadEntry(0)
		if err != nil || string(got) != `{"name":"a"}` {
			t.Fatalf("%q %v", got, err)
		}
		if e, err = r.Next(); err != nil || e.Name != "package/index.js" {
			t.Fatalf("%+v %v", e, err)
		}
		if _, err = r.Next(); !errors.Is(err, io.EOF) {
			t.Fatalf("end = %v", err)
		}
		if r.Entries() != 2 || r.Files() != 2 || r.Declared() != 1012 {
			t.Fatalf("counts %d %d %d", r.Entries(), r.Files(), r.Declared())
		}
	})
	t.Run("R-ARCH-028 a manifest of 2 GiB is TooLarge before one byte of it is inflated", func(t *testing.T) {
		rc := &counting{ReadCloser: crafted.TarGzEntry("package/package.json", 2<<30).Open()}
		defer rc.Close()
		r := untrusted.NewTarGz(ctx, rc, l)
		if _, err := r.Next(); err != nil {
			t.Fatal(err)
		}
		_, err := r.ReadEntry(0)
		wantKind(t, err, errs.TooLarge)
		wantReadLess(t, rc.n, 1<<20) // the 2 GiB entry was never inflated
	})
	t.Run("R-ARCH-028 an archive that unpacks past the stream bound is TooLarge at its header", func(t *testing.T) {
		rc := &counting{ReadCloser: crafted.TarGzEntry("package/blob", 3<<30).Open()}
		defer rc.Close()
		_, err := untrusted.NewTarGz(ctx, rc, l).Next()
		wantKind(t, err, errs.TooLarge)
		wantReadLess(t, rc.n, 1<<20)
	})
	t.Run("R-ARCH-028 more entries than the bound are TooLarge", func(t *testing.T) {
		in := crafted.TarGzEntries(10_001)
		rc := in.Open()
		defer rc.Close()
		r := untrusted.NewTarGz(ctx, rc, l)
		var err error
		for err == nil {
			_, err = r.Next()
		}
		wantKind(t, err, errs.TooLarge)
		if r.Entries() != 10_001 {
			t.Fatalf("stopped after %d entries", r.Entries())
		}
	})
	t.Run("R-ARCH-028 a ratio above the bound is TooLarge once the entry is read, a small entry passes on the read-ahead allowance", func(t *testing.T) {
		data := tarGz(t, func(tw *tar.Writer) {
			file(t, tw, "small", strings.Repeat("\x00", 64<<10))
			file(t, tw, "bomb", strings.Repeat("\x00", 1<<20))
		})
		r := untrusted.NewTarGz(ctx, bytes.NewReader(data), l)
		if _, err := r.Next(); err != nil {
			t.Fatal(err)
		}
		if _, err := r.ReadEntry(0); err != nil {
			t.Fatalf("small entry: %v", err)
		}
		if _, err := r.Next(); err != nil {
			t.Fatal(err)
		}
		_, err := r.ReadEntry(2 << 20)
		wantKind(t, err, errs.TooLarge)
		if !strings.Contains(err.Error(), ":1") {
			t.Fatalf("not the ratio: %v", err)
		}
	})
	t.Run("R-ARCH-028 the bytes read into memory are bounded in total", func(t *testing.T) {
		data := tarGz(t, func(tw *tar.Writer) {
			file(t, tw, "a", strings.Repeat("abcdefgh", 100))
			file(t, tw, "b", strings.Repeat("abcdefgh", 100))
		})
		r := untrusted.NewTarGz(ctx, bytes.NewReader(data), untrusted.Limits{UncompressedBytes: 1000})
		_, _ = r.Next()
		if _, err := r.ReadEntry(0); err != nil {
			t.Fatal(err)
		}
		_, _ = r.Next()
		_, err := r.ReadEntry(0)
		wantKind(t, err, errs.TooLarge)
	})
	t.Run("R-ARCH-028 a name used twice is refused, a directory's trailing slash is not a second name", func(t *testing.T) {
		r := untrusted.NewTarGz(ctx, crafted.TarGzDuplicates("package/package.json").Open(), l)
		_, _ = r.Next()
		_, err := r.Next()
		wantKind(t, err, errs.Unprocessable)

		data := tarGz(t, func(tw *tar.Writer) {
			_ = tw.WriteHeader(&tar.Header{Name: "package/", Typeflag: tar.TypeDir, Mode: 0o755})
			file(t, tw, "package", "x")
		})
		r = untrusted.NewTarGz(ctx, bytes.NewReader(data), l)
		_, _ = r.Next()
		_, err = r.Next()
		wantKind(t, err, errs.Unprocessable)
	})
	t.Run("R-ARCH-028 a link entry is refused", func(t *testing.T) {
		rc := crafted.TarGzSymlink("package/package.json", "../../etc/passwd").Open()
		_, err := untrusted.NewTarGz(ctx, rc, l).Next()
		wantKind(t, err, errs.Unprocessable)
	})
	t.Run("R-ARCH-028 data after the archive and a damaged stream are refused", func(t *testing.T) {
		data := tarGz(t, func(tw *tar.Writer) { file(t, tw, "a", "x") })
		r := untrusted.NewTarGz(ctx, bytes.NewReader(append(append([]byte(nil), data...), data...)), l)
		_, _ = r.Next()
		_, err := r.Next()
		wantKind(t, err, errs.Unprocessable)

		r = untrusted.NewTarGz(ctx, bytes.NewReader(data[:len(data)/2]), l)
		for err = nil; err == nil; _, err = r.Next() { //nolint:revive // drain to the first error
		}
		wantKind(t, err, errs.Unprocessable)

		_, err = untrusted.NewTarGz(ctx, strings.NewReader("not gzip at all"), l).Next()
		wantKind(t, err, errs.Unprocessable)
	})
	t.Run("R-ARCH-028 the deadline stops a read", func(t *testing.T) {
		c, cancel := context.WithCancel(ctx)
		data := tarGz(t, func(tw *tar.Writer) { file(t, tw, "a", "x") })
		r := untrusted.NewTarGz(c, bytes.NewReader(data), l)
		cancel()
		if _, err := r.Next(); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-ARCH-028 the streaming pass counts files and sizes without keeping content, with finite limits of its own", func(t *testing.T) {
		data := tarGz(t, func(tw *tar.Writer) {
			_ = tw.WriteHeader(&tar.Header{Name: "package/", Typeflag: tar.TypeDir, Mode: 0o755})
			file(t, tw, "package/a", strings.Repeat("a", 300))
			file(t, tw, "package/b", strings.Repeat("b", 200))
		})
		n, total, err := untrusted.CountTarGz(ctx, bytes.NewReader(data), 100, 1<<20)
		if err != nil || n != 2 || total != 500 {
			t.Fatalf("%d %d %v", n, total, err)
		}
		_, _, err = untrusted.CountTarGz(ctx, bytes.NewReader(data), 2, 1<<20)
		wantKind(t, err, errs.TooLarge)
		_, _, err = untrusted.CountTarGz(ctx, bytes.NewReader(data), 100, 400)
		wantKind(t, err, errs.TooLarge)
		rc := &counting{ReadCloser: crafted.TarGzEntry("package/blob", 1<<30).Open()}
		defer rc.Close()
		_, _, err = untrusted.CountTarGz(ctx, rc, 100, 64<<20)
		wantKind(t, err, errs.TooLarge)
		wantReadLess(t, rc.n, 1<<20)
	})
}

func buildZip(t *testing.T, names ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, n := range names {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: n, Method: zip.Deflate})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte("content of " + n))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestR_ARCH_028_Zip(t *testing.T) {
	ctx := context.Background()
	l := untrusted.DefaultLimits()
	open := func(b []byte, l untrusted.Limits) (*untrusted.ZipReader, error) {
		return untrusted.NewZip(ctx, bytes.NewReader(b), int64(len(b)), l)
	}
	t.Run("R-ARCH-028 entries are listed and read within the bounds", func(t *testing.T) {
		z, err := open(buildZip(t, "a.nuspec", "lib/b.dll"), l)
		if err != nil {
			t.Fatal(err)
		}
		es := z.Entries()
		if len(es) != 2 || es[0].Name != "a.nuspec" || es[1].Name != "lib/b.dll" {
			t.Fatalf("entries = %+v", es)
		}
		got, err := z.ReadEntry(es[0], 0)
		if err != nil || string(got) != "content of a.nuspec" {
			t.Fatalf("%q %v", got, err)
		}
		wantKind(t, errOf(z.ReadEntry(es[1], 5)), errs.TooLarge)
	})
	t.Run("R-ARCH-028 an archive that expands to 10 GiB is TooLarge at its central directory", func(t *testing.T) {
		in := crafted.ZipBomb("package.nuspec", 10<<30)
		ra := &countingAt{ReaderAt: in.ReaderAt}
		_, err := untrusted.NewZip(ctx, ra, in.Size, l)
		wantKind(t, err, errs.TooLarge)
		wantReadLess(t, ra.n, 1<<20) // the 10 MB of compressed data were never read
	})
	t.Run("R-ARCH-028 an entry is TooLarge by its size, by what remains of the memory bound, then by its ratio", func(t *testing.T) {
		in := crafted.ZipBomb("package.nuspec", 30<<20)
		z, err := untrusted.NewZip(ctx, in.ReaderAt, in.Size, l)
		if err != nil {
			t.Fatal(err)
		}
		wantKind(t, errOf(z.ReadEntry(z.Entries()[0], 0)), errs.TooLarge) // ManifestEntryBytes
		_, err = z.ReadEntry(z.Entries()[0], 40<<20)
		wantKind(t, err, errs.TooLarge)
		if !strings.Contains(err.Error(), ":1") {
			t.Fatalf("not the ratio: %v", err)
		}
		huge := crafted.ZipBomb("package.nuspec", 100<<20)
		z, err = untrusted.NewZip(ctx, huge.ReaderAt, huge.Size, l)
		if err != nil {
			t.Fatal(err)
		}
		_, err = z.ReadEntry(z.Entries()[0], 200<<20)
		wantKind(t, err, errs.TooLarge)
		if !strings.Contains(err.Error(), "read into memory") {
			t.Fatalf("not the memory bound: %v", err)
		}
	})
	t.Run("R-ARCH-028 the crafted archive is a valid zip: the standard library inflates it with a correct CRC", func(t *testing.T) {
		in := crafted.ZipBomb("x.bin", 3<<20+17)
		zr, err := zip.NewReader(in.ReaderAt, in.Size)
		if err != nil {
			t.Fatal(err)
		}
		rc, err := zr.File[0].Open()
		if err != nil {
			t.Fatal(err)
		}
		n, err := io.Copy(io.Discard, rc)
		if err != nil || n != 3<<20+17 {
			t.Fatalf("%d %v", n, err)
		}
	})
	t.Run("R-ARCH-028 more entries than the bound are TooLarge before the standard library parses the directory", func(t *testing.T) {
		in := crafted.ZipEntries(10_001)
		_, err := untrusted.NewZip(ctx, in.ReaderAt, in.Size, l)
		wantKind(t, err, errs.TooLarge)
		ok := crafted.ZipEntries(10_000)
		if _, err := untrusted.NewZip(ctx, ok.ReaderAt, ok.Size, l); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-ARCH-028 a central directory and a local header that name an entry differently are refused", func(t *testing.T) {
		b := buildZip(t, "a.txt")
		i := bytes.LastIndex(b, []byte("a.txt"))
		copy(b[i:], "b.txt")
		_, err := open(b, l)
		wantKind(t, err, errs.Unprocessable)
	})
	t.Run("R-ARCH-028 a name used twice is refused", func(t *testing.T) {
		_, err := open(buildZip(t, "a/b", "a/b"), l)
		wantKind(t, err, errs.Unprocessable)
		_, err = open(buildZip(t, "a", "a/"), l)
		wantKind(t, err, errs.Unprocessable)
	})
	t.Run("R-ARCH-028 a link, an encrypted entry and another method are refused", func(t *testing.T) {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		h := &zip.FileHeader{Name: "link", Method: zip.Store}
		h.SetMode(os.ModeSymlink | 0o777)
		w, _ := zw.CreateHeader(h)
		_, _ = w.Write([]byte("/etc/passwd"))
		_ = zw.Close()
		_, err := open(buf.Bytes(), l)
		wantKind(t, err, errs.Unprocessable)

		buf.Reset()
		zw = zip.NewWriter(&buf)
		h = &zip.FileHeader{Name: "enc", Method: zip.Store, Flags: 0x1}
		_, _ = zw.CreateHeader(h)
		_ = zw.Close()
		_, err = open(buf.Bytes(), l)
		wantKind(t, err, errs.Unprocessable)

		buf.Reset()
		zw = zip.NewWriter(&buf)
		zw.RegisterCompressor(14, func(w io.Writer) (io.WriteCloser, error) { return nopWriteCloser{w}, nil })
		_, _ = zw.CreateHeader(&zip.FileHeader{Name: "lzma", Method: 14})
		_ = zw.Close()
		_, err = open(buf.Bytes(), l)
		wantKind(t, err, errs.Unprocessable)
	})
	t.Run("R-ARCH-028 content that is not a zip archive, or is cut, is refused", func(t *testing.T) {
		for _, b := range [][]byte{nil, []byte("PK"), []byte(strings.Repeat("x", 100)), buildZip(t, "a")[:30]} {
			_, err := open(b, l)
			wantKind(t, err, errs.Unprocessable)
		}
	})
	t.Run("R-ARCH-028 a central directory with bytes its entries do not account for is refused", func(t *testing.T) {
		b := buildZip(t, "a.txt")
		// grow the central directory size of the end record by one byte
		end := bytes.LastIndex(b, []byte{0x50, 0x4b, 0x05, 0x06})
		b[end+12]++
		_, err := open(b, l)
		wantKind(t, err, errs.Unprocessable)
	})
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func errOf(_ []byte, err error) error { return err }
