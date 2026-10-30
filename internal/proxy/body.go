package proxy

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// errTooLargeBody is returned by the reader of a body that crossed the size limit
// (R-REPO-040).
var errTooLargeBody = errors.New("proxy: the upstream item exceeds the size limit")

// limitReader returns errTooLargeBody when the body crosses max bytes; max <= 0
// bounds nothing. The bytes of the read that crosses the limit are returned with the
// error and are discarded by the caller, which stops.
type limitReader struct {
	r    io.Reader
	max  int64
	read int64
}

func (l *limitReader) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	l.read += int64(n)
	if l.max > 0 && l.read > l.max {
		return n, errTooLargeBody
	}
	return n, err
}

// idleBody ends a transfer that stops sending: remote.readTimeoutMs is "the longest
// time without receiving a byte" (R-REPO-024), and the transport bounds only the wait
// for the headers. The timer runs while a read waits for the upstream and not while
// the caller works on what it got, so a slow client of the leader never counts as a
// silent upstream. When it fires it cancels the request and the read fails with the
// error of the cancelled context, which the caller maps to 504 upstream_timeout
// through the flag.
type idleBody struct {
	rc      io.ReadCloser
	d       time.Duration
	t       *time.Timer
	fired   *atomic.Bool
	onClose func()
	once    sync.Once
}

func newIdleBody(rc io.ReadCloser, d time.Duration, cancel context.CancelFunc, fired *atomic.Bool, onClose func()) io.ReadCloser {
	b := &idleBody{rc: rc, d: d, fired: fired, onClose: onClose}
	if d > 0 {
		b.t = time.AfterFunc(d, func() {
			fired.Store(true)
			cancel()
		})
		b.t.Stop()
	}
	return b
}

func (b *idleBody) Read(p []byte) (int, error) {
	if b.t != nil {
		b.t.Reset(b.d)
	}
	n, err := b.rc.Read(p)
	if b.t != nil {
		b.t.Stop()
	}
	return n, err
}

func (b *idleBody) Close() error {
	if b.t != nil {
		b.t.Stop()
	}
	b.once.Do(b.onClose)
	return b.rc.Close()
}
