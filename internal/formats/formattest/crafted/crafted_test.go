package crafted

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"hash/crc32"
	"io"
	"testing"
)

// TestR_ARCH_010_CraftedGenerators: the generators of check 6 produce what they
// claim, so a format that rejects them rejects the thing the spec names.
func TestR_ARCH_010_CraftedGenerators(t *testing.T) {
	t.Run("R-ARCH-010 the CRC of n zeros equals the CRC computed over n zeros", func(t *testing.T) {
		zeros := make([]byte, 1<<20)
		var crc uint32
		for n := int64(0); n < 5; n++ {
			crc = crc32.Update(crc, crc32.IEEETable, zeros)
			total := (n + 1) << 20
			if got := crcZeros(total); got != crc {
				t.Fatalf("crcZeros(%d) = %08x, want %08x", total, got, crc)
			}
		}
		odd := crc32.Update(crc, crc32.IEEETable, zeros[:12345])
		if got := crcZeros(5<<20 + 12345); got != odd {
			t.Fatalf("odd length: %08x, want %08x", got, odd)
		}
	})
	t.Run("R-ARCH-010 the zip bomb is a valid zip archive whose entry holds the declared zeros", func(t *testing.T) {
		for _, size := range []int64{0, 1, bombBlock - 1, bombBlock, 2*bombBlock + 5} {
			in := ZipBomb("x.bin", size)
			zr, err := zip.NewReader(in.ReaderAt, in.Size)
			if err != nil {
				t.Fatalf("size %d: %v", size, err)
			}
			rc, err := zr.File[0].Open()
			if err != nil {
				t.Fatal(err)
			}
			n, err := io.Copy(io.Discard, rc) // verifies the CRC at the end
			if err != nil || n != size {
				t.Fatalf("size %d: %d %v", size, n, err)
			}
		}
	})
	t.Run("R-ARCH-010 the zip bomb of 10 GiB is a few megabytes and is addressed by offset", func(t *testing.T) {
		in := ZipBomb("x.bin", 10<<30)
		if in.Size > 32<<20 {
			t.Fatalf("archive of %d bytes", in.Size)
		}
		var b [8]byte
		if n, err := in.ReaderAt.ReadAt(b[:], in.Size-8); err != nil || n != 8 {
			t.Fatalf("%d %v", n, err)
		}
		if n, err := in.ReaderAt.ReadAt(b[:], in.Size-4); err != io.EOF || n != 4 {
			t.Fatalf("read across the end: %d %v", n, err)
		}
	})
	t.Run("R-ARCH-010 the tar.gz entry declares its size and streams the zeros", func(t *testing.T) {
		rc := TarGzEntry("package/package.json", 3<<20+1).Open()
		defer rc.Close()
		zr, err := gzip.NewReader(rc)
		if err != nil {
			t.Fatal(err)
		}
		tr := tar.NewReader(zr)
		h, err := tr.Next()
		if err != nil || h.Name != "package/package.json" || h.Size != 3<<20+1 {
			t.Fatalf("%+v %v", h, err)
		}
		n, err := io.Copy(io.Discard, tr)
		if err != nil || n != 3<<20+1 {
			t.Fatalf("%d %v", n, err)
		}
		if _, err := tr.Next(); err != io.EOF {
			t.Fatalf("end: %v", err)
		}
	})
	t.Run("R-ARCH-010 the entry generators count their entries", func(t *testing.T) {
		rc := TarGzEntries(2500).Open()
		zr, _ := gzip.NewReader(rc)
		tr := tar.NewReader(zr)
		n := 0
		for {
			if _, err := tr.Next(); err != nil {
				if err != io.EOF {
					t.Fatal(err)
				}
				break
			}
			n++
		}
		if n != 2500 {
			t.Fatalf("%d entries", n)
		}
		in := ZipEntries(300)
		zrd, err := zip.NewReader(in.ReaderAt, in.Size)
		if err != nil || len(zrd.File) != 300 {
			t.Fatalf("%v", err)
		}
	})
	t.Run("R-ARCH-010 the document generators have the shape they name", func(t *testing.T) {
		read := func(in Input) []byte {
			b, err := io.ReadAll(in.Open())
			if err != nil {
				t.Fatal(err)
			}
			return b
		}
		if b := read(DeepJSON(3)); string(b) != "[[[]]]" {
			t.Fatalf("%q", b)
		}
		if b := read(WideJSON(3)); string(b) != "[0,0,0]" {
			t.Fatalf("%q", b)
		}
		if b := read(WideJSON(1)); string(b) != "[0]" {
			t.Fatalf("%q", b)
		}
		if b := read(DeepXML(2)); string(b) != "<a><a></a></a>" {
			t.Fatalf("%q", b)
		}
		if b := read(XMLDoctype()); !bytes.Contains(b, []byte("<!DOCTYPE")) || !bytes.Contains(b, []byte("<!ENTITY e9")) {
			t.Fatalf("%q", b)
		}
		if got := int64(len(read(DeepJSON(1000)))); got != DeepJSON(1000).Size {
			t.Fatalf("size %d", got)
		}
	})
	t.Run("R-ARCH-010 closing a stream stops its generator", func(t *testing.T) {
		rc := TarGzEntry("big", 10<<30).Open()
		buf := make([]byte, 1024)
		if _, err := io.ReadFull(rc, buf); err != nil {
			t.Fatal(err)
		}
		if err := rc.Close(); err != nil {
			t.Fatal(err)
		}
		if n, err := rc.Read(buf); n != 0 || err != io.EOF {
			t.Fatalf("after close: %d %v", n, err)
		}
	})
}
