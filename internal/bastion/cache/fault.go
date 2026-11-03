package cache

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"time"
)

// Fault is the switch with which a test of the Bastion makes a cache's storage fail without a real outage:
// the tests of the engine and of the server need the backend to be down at the first check, to refuse a write,
// or to fail later. Pass it in [Options]; production leaves it nil, and the storage is not wrapped.
type Fault struct {
	down    atomic.Bool
	failPut atomic.Bool
}

// ErrFault is what a faulty storage returns.
var ErrFault = errors.New("cache: the storage is down (injected by a test)")

// SetDown makes every operation of the storage fail, or stops that.
func (f *Fault) SetDown(on bool) { f.down.Store(on) }

// SetFailPut makes every write of bytes fail, or stops that; reads keep working.
func (f *Fault) SetFailPut(on bool) { f.failPut.Store(on) }

func (f *Fault) read() error {
	if f.down.Load() {
		return ErrFault
	}
	return nil
}

func (f *Fault) write() error {
	if f.down.Load() || f.failPut.Load() {
		return ErrFault
	}
	return nil
}

// faultStore wraps a store and fails operations while the [Fault] says so.
type faultStore struct {
	store
	f *Fault
}

func (s *faultStore) putFile(ctx context.Context, key, name string, size int64, sum []byte) error {
	if err := s.f.write(); err != nil {
		return err
	}
	return s.store.putFile(ctx, key, name, size, sum)
}

func (s *faultStore) putBytes(ctx context.Context, key string, b []byte) error {
	if err := s.f.write(); err != nil {
		return err
	}
	return s.store.putBytes(ctx, key, b)
}

func (s *faultStore) get(ctx context.Context, key string, off, n int64) (io.ReadCloser, error) {
	if err := s.f.read(); err != nil {
		return nil, err
	}
	return s.store.get(ctx, key, off, n)
}

func (s *faultStore) stat(ctx context.Context, key string) (int64, time.Time, error) {
	if err := s.f.read(); err != nil {
		return 0, time.Time{}, err
	}
	return s.store.stat(ctx, key)
}

func (s *faultStore) list(ctx context.Context, prefix string, fn func(string, int64, time.Time) error) error {
	if err := s.f.read(); err != nil {
		return err
	}
	return s.store.list(ctx, prefix, fn)
}

func (s *faultStore) remove(ctx context.Context, key string) error {
	if err := s.f.read(); err != nil {
		return err
	}
	return s.store.remove(ctx, key)
}

func (s *faultStore) touch(ctx context.Context, key string) error {
	if err := s.f.read(); err != nil {
		return err
	}
	return s.store.touch(ctx, key)
}
