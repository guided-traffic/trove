package untrusted

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

func xzOf(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	w, err := xz.NewWriter(&b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func zstdOf(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	w, err := zstd.NewWriter(&b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestBoundedDecompressors(t *testing.T) {
	text := strings.Repeat("Package: hello\n", 1000)
	readers := []struct {
		name string
		pack func(*testing.T, string) []byte
		open func(io.Reader, Limits) (io.Reader, error)
	}{
		{"xz", xzOf, NewXZReader},
		{"zstd", zstdOf, NewZstdReader},
	}
	for _, c := range readers {
		t.Run(c.name+" within the cap", func(t *testing.T) {
			r, err := c.open(bytes.NewReader(c.pack(t, text)), Limits{UncompressedBytes: int64(len(text))})
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(r)
			if err != nil || string(got) != text {
				t.Fatalf("read = %d bytes, %v", len(got), err)
			}
		})
		t.Run(c.name+" above the cap fails instead of ending quietly", func(t *testing.T) {
			r, err := c.open(bytes.NewReader(c.pack(t, text)), Limits{UncompressedBytes: 100})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.ReadAll(r); !IsTooLarge(err) {
				t.Fatalf("err = %v, want too large", err)
			}
		})
		t.Run(c.name+" refuses what is no stream", func(t *testing.T) {
			if r, err := c.open(strings.NewReader("not compressed at all, no magic"), Limits{}); err == nil {
				if _, err = io.ReadAll(r); err == nil {
					t.Fatal("garbage was read")
				}
			}
		})
	}
}
