package untrusted_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
)

// TestR_ARCH_028_StreamKeepsNothingAndStaysBounded: Stream passes the content of an entry to a writer without counting it as read into
// memory, so a pass that digests every member of an archive larger than UncompressedBytes works, and the inflated cap and the
// compression ratio still bind it.
func TestR_ARCH_028_StreamKeepsNothingAndStaysBounded(t *testing.T) {
	ctx := context.Background()
	body := strings.Repeat("0123456789abcdef", 1<<12) // 64 KiB of a pattern that does not compress by 100:1
	data := tarGz(t, func(tw *tar.Writer) {
		for _, n := range []string{"a", "b", "c"} {
			file(t, tw, n, body)
		}
	})
	t.Run("more than UncompressedBytes passes through", func(t *testing.T) {
		l := untrusted.DefaultLimits()
		l.UncompressedBytes = 1 << 10
		r := untrusted.NewTarGz(ctx, bytes.NewReader(data), l)
		want := sha256.Sum256([]byte(body))
		for range 3 {
			if _, err := r.Next(); err != nil {
				t.Fatal(err)
			}
			h := sha256.New()
			n, err := r.Stream(h)
			if err != nil || n != int64(len(body)) || !bytes.Equal(h.Sum(nil), want[:]) {
				t.Fatalf("Stream = %d, %v, digest ok %v", n, err, bytes.Equal(h.Sum(nil), want[:]))
			}
		}
		if _, err := r.Next(); !errors.Is(err, io.EOF) {
			t.Fatalf("end = %v", err)
		}
	})
	t.Run("the compression ratio binds it", func(t *testing.T) {
		zeros := tarGz(t, func(tw *tar.Writer) { file(t, tw, "z", strings.Repeat("\x00", 8<<20)) })
		r := untrusted.NewTarGz(ctx, bytes.NewReader(zeros), untrusted.DefaultLimits())
		if _, err := r.Next(); err != nil {
			t.Fatal(err)
		}
		_, err := r.Stream(io.Discard)
		wantKind(t, err, errs.TooLarge)
	})
	t.Run("a directory has no content", func(t *testing.T) {
		dir := tarGz(t, func(tw *tar.Writer) {
			if err := tw.WriteHeader(&tar.Header{Name: "d/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
				t.Fatal(err)
			}
		})
		r := untrusted.NewTarGz(ctx, bytes.NewReader(dir), untrusted.DefaultLimits())
		if _, err := r.Next(); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Stream(io.Discard); !untrusted.IsRefused(err) {
			t.Fatalf("Stream of a directory = %v", err)
		}
	})
}
