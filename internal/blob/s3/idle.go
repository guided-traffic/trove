package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
)

// idle detects a transfer in which no byte moved for d (R-STOR-033): body
// transfers have no fixed total timeout, they fail when they stall. It cancels
// the context of the request when the timer fires.
type idle struct {
	d      time.Duration
	cancel context.CancelFunc
	mu     sync.Mutex
	t      *time.Timer
	fired  atomic.Bool
}

func newIdle(d time.Duration, cancel context.CancelFunc) *idle {
	return &idle{d: d, cancel: cancel}
}

func (w *idle) touch() {
	if w == nil || w.d <= 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.t == nil {
		w.t = time.AfterFunc(w.d, func() { w.fired.Store(true); w.cancel() })
		return
	}
	w.t.Reset(w.d)
}

func (w *idle) stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.t != nil {
		w.t.Stop()
	}
}

// stalled returns the error a stalled transfer fails with, or nil.
func (w *idle) stalled(op, key string) error {
	if w == nil || !w.fired.Load() {
		return nil
	}
	return &blob.StoreError{
		Op: op, Key: key, Kind: blob.KindTransient,
		Err: fmt.Errorf("no byte moved for %s: %w", w.d, io.ErrUnexpectedEOF),
	}
}

// seeker wraps a request body: every read or seek counts as progress, the end of
// the body stops the timer (the wait for the answer is bounded by the response
// header timeout instead).
type seeker struct {
	rs io.ReadSeeker
	w  *idle
}

func (s *seeker) Read(p []byte) (int, error) {
	s.w.touch()
	n, err := s.rs.Read(p)
	if errors.Is(err, io.EOF) {
		s.w.stop()
	} else {
		s.w.touch()
	}
	return n, err
}

func (s *seeker) Seek(off int64, whence int) (int64, error) {
	s.w.touch()
	return s.rs.Seek(off, whence)
}

// body wraps a response body: reads count as progress, Close ends the watch and
// the request context.
type body struct {
	io.ReadCloser
	w      *idle
	cancel context.CancelFunc
	op     string
	key    string
}

func (b *body) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		if serr := b.w.stalled(b.op, b.key); serr != nil {
			return n, serr
		}
		return n, err
	}
	b.w.touch()
	return n, err
}

func (b *body) Close() error {
	b.w.stop()
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}
