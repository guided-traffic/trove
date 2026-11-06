package blob

import (
	"math"
	"math/bits"
	"sync"

	"github.com/guided-traffic/trove/internal/config"
)

// Download buffers are pooled by size class, the powers of two from 4 KiB to
// 1 GiB (R-STOR-012, Q-ARCH-241): a download takes a buffer for its own size, at
// most storage.download.buffer_size, and hands it to the next download instead
// of to the garbage collector.
const (
	minBufferShift = 12
	maxBufferShift = 30
)

var bufferPools [maxBufferShift - minBufferShift + 1]sync.Pool

// Buffer returns a buffer of at least n bytes, n rounded up to its size class.
// Give it back with Release once nothing refers to it.
func Buffer(n int) *[]byte {
	s := max(bits.Len(uint(max(n, 1)-1)), minBufferShift)
	if s > maxBufferShift {
		b := make([]byte, n)
		return &b
	}
	if b, ok := bufferPools[s-minBufferShift].Get().(*[]byte); ok {
		return b
	}
	b := make([]byte, 1<<s)
	return &b
}

// DownloadBufferBytes is the most memory one download's buffer takes: the
// buffer of a verified download of a large object, rounded up to its size class.
func DownloadBufferBytes(d config.StorageDownload) int64 { return downloadBuffer(d, math.MaxInt64) }

// downloadBuffer is the size class of the buffer of a download of an object of
// size bytes.
func downloadBuffer(d config.StorageDownload, size int64) int64 {
	n := verifyBuffer(int(d.BufferSize), int(d.HoldBack), size)
	if s := bits.Len(uint(n - 1)); s <= maxBufferShift {
		return 1 << max(s, minBufferShift)
	}
	return int64(n)
}

// DownloadCost is what a download of an object of size bytes holds beyond the
// request itself: its buffer and what the object store has sent ahead of the
// reader, at most the receive window (Q-ARCH-241, Q-ARCH-328).
func DownloadCost(cfg config.Storage, size int64) int64 {
	return downloadBuffer(cfg.Download, size) + min(max(size, 0), int64(cfg.S3.HTTP.ReceiveWindow))
}

// Release returns a buffer of Buffer to its pool; one of another length is left
// to the garbage collector.
func Release(b *[]byte) {
	if b == nil {
		return
	}
	n := len(*b)
	s := bits.Len(uint(n)) - 1
	if s < minBufferShift || s > maxBufferShift || n != 1<<s {
		return
	}
	bufferPools[s-minBufferShift].Put(b)
}
