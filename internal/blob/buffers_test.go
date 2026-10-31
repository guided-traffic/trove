package blob

import (
	"math"
	"testing"

	"github.com/guided-traffic/trove/internal/config"
)

func TestDownloadBuffers(t *testing.T) {
	t.Run("R-STOR-012 a verified download takes one buffer of storage.download.buffer_size, at least twice the hold-back, and no more than its object", func(t *testing.T) {
		for _, c := range []struct {
			buffer, hold int
			size         int64
			want         int
		}{
			{256 << 10, 64 << 10, 1 << 20, 256 << 10},
			{256 << 10, 64 << 10, 50 << 10, 50<<10 + 1}, // the byte after the object shows its end
			{256 << 10, 64 << 10, 256 << 10, 256 << 10},
			{32 << 10, 64 << 10, 1 << 20, 128 << 10},
			{1 << 10, 1 << 10, 1 << 20, 4 << 10},
			{256 << 10, 64 << 10, 0, 1},
		} {
			if got := verifyBuffer(c.buffer, c.hold, c.size); got != c.want {
				t.Errorf("verifyBuffer(%d, %d, %d) = %d, want %d", c.buffer, c.hold, c.size, got, c.want)
			}
		}
	})

	t.Run("R-STOR-012 buffers come in power-of-two classes from 4 KiB and a released one is handed out again", func(t *testing.T) {
		for n, want := range map[int]int{1: 4 << 10, 4 << 10: 4 << 10, 50<<10 + 1: 64 << 10, 256 << 10: 256 << 10, 300 << 10: 512 << 10} {
			b := Buffer(n)
			if len(*b) != want {
				t.Errorf("Buffer(%d) has %d bytes, want %d", n, len(*b), want)
			}
			Release(b)
		}
		if b := Buffer(1<<maxBufferShift + 1); len(*b) != 1<<maxBufferShift+1 {
			t.Errorf("a buffer above the largest class has %d bytes", len(*b))
		}
		Release(nil)
		odd := make([]byte, 5000)
		Release(&odd) // not a class: dropped, not pooled
		if b := Buffer(5000); len(*b) != 8<<10 {
			t.Errorf("a buffer of another length came back from the pool: %d bytes", len(*b))
		}
		reused := 0
		for range 100 {
			b := Buffer(64 << 10)
			(*b)[0] = 42
			Release(b)
			if c := Buffer(64 << 10); (*c)[0] == 42 {
				reused++
				Release(c)
			}
		}
		if reused == 0 {
			t.Error("a released buffer was never handed out again")
		}
	})

	t.Run("R-STOR-012 the memory a download's buffer takes is the size class of the full buffer", func(t *testing.T) {
		for _, c := range []struct {
			buffer, hold config.ByteSize
			want         int64
		}{
			{256 << 10, 64 << 10, 256 << 10},
			{300 << 10, 64 << 10, 512 << 10},
			{256 << 10, 200 << 10, 512 << 10},
			{4 << 10, 1 << 10, 4 << 10},
		} {
			if got := DownloadBufferBytes(config.StorageDownload{BufferSize: c.buffer, HoldBack: c.hold}); got != c.want {
				t.Errorf("buffer %d, hold %d: %d bytes, want %d", c.buffer, c.hold, got, c.want)
			}
		}
		if verifyBuffer(1, 1, math.MaxInt64) != 4<<10 {
			t.Error("the smallest buffer is 4 KiB")
		}
	})
}
