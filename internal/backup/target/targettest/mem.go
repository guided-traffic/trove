// Package targettest is an in-memory backup target for the tests of the backup lane. It keeps the semantics the lane
// depends on: listings in lexicographic order, a conditional create, a missing key on Delete that is no error, and
// server-side encryption that is applied to every write.
package targettest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/backup/target"
)

// Mem is a target.Store in memory. The zero value is not usable; use New.
type Mem struct {
	mu   sync.Mutex
	objs map[string]entry
	now  func() time.Time
	// SSE is the encryption every write reports, "AES256" by default.
	SSE string
	// Source resolves the ObjectRef of a server-side copy; nil makes Copy return target.ErrServerCopyUnavailable.
	Source func(target.ObjectRef) ([]byte, bool)
	// Calls counts the requests by name (put, writer, copy, head, get, list, delete), so a test can assert that a retry
	// wrote no object that was already present (R-BAK-013).
	Calls map[string]int
}

type entry struct {
	data []byte
	at   time.Time
	sse  string
}

// New returns an empty target.
func New() *Mem {
	return &Mem{objs: map[string]entry{}, now: time.Now, SSE: "AES256", Calls: map[string]int{}}
}

var _ target.Store = (*Mem)(nil)

// Keys lists every key, sorted.
func (m *Mem) Keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.objs))
	for k := range m.objs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Data returns the bytes of an object.
func (m *Mem) Data(key string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.objs[key]
	return append([]byte(nil), e.data...), ok
}

// Set stores an object directly, as a test that edits the target by hand does.
func (m *Mem) Set(key string, data []byte) {
	m.mu.Lock()
	m.objs[key] = entry{data: append([]byte(nil), data...), at: m.now(), sse: m.SSE}
	m.mu.Unlock()
}

// Remove deletes an object directly.
func (m *Mem) Remove(key string) {
	m.mu.Lock()
	delete(m.objs, key)
	m.mu.Unlock()
}

func (m *Mem) count(name string) { m.Calls[name]++ }

func (m *Mem) info(key string, e entry) target.Object {
	sum := sha256.Sum256(e.data)
	return target.Object{Key: key, Size: int64(len(e.data)), LastModified: e.at, SSE: e.sse, SHA256: hex.EncodeToString(sum[:]), ETag: hex.EncodeToString(sum[:8])}
}

// Head implements target.Store.
func (m *Mem) Head(_ context.Context, key string) (target.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.count("head")
	e, ok := m.objs[key]
	if !ok {
		return target.Object{}, target.ErrNotFound
	}
	return m.info(key, e), nil
}

// Get implements target.Store.
func (m *Mem) Get(_ context.Context, key string) (io.ReadCloser, target.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.count("get")
	e, ok := m.objs[key]
	if !ok {
		return nil, target.Object{}, target.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(e.data)), m.info(key, e), nil
}

// Put implements target.Store.
func (m *Mem) Put(_ context.Context, key string, body []byte, opts target.PutOptions) (target.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.count("put")
	if _, ok := m.objs[key]; ok && opts.IfAbsent {
		return target.Object{}, target.ErrExists
	}
	e := entry{data: append([]byte(nil), body...), at: m.now(), sse: m.SSE}
	m.objs[key] = e
	return m.info(key, e), nil
}

type writer struct {
	m    *Mem
	key  string
	opts target.PutOptions
	buf  bytes.Buffer
	done bool
}

// Writer implements target.Store.
func (m *Mem) Writer(_ context.Context, key string, opts target.PutOptions) (target.ObjectWriter, error) {
	m.mu.Lock()
	m.count("writer")
	m.mu.Unlock()
	return &writer{m: m, key: key, opts: opts}, nil
}

func (w *writer) Write(p []byte) (int, error) {
	if w.done {
		return 0, fmt.Errorf("targettest: write after close")
	}
	return w.buf.Write(p)
}

func (w *writer) Close(_ context.Context) (target.Object, error) {
	w.done = true
	w.m.mu.Lock()
	defer w.m.mu.Unlock()
	if _, ok := w.m.objs[w.key]; ok && w.opts.IfAbsent {
		return target.Object{}, target.ErrExists
	}
	e := entry{data: w.buf.Bytes(), at: w.m.now(), sse: w.m.SSE}
	w.m.objs[w.key] = e
	return w.m.info(w.key, e), nil
}

func (w *writer) Abort(context.Context) error { w.done = true; return nil }

// List implements target.Store.
func (m *Mem) List(_ context.Context, prefix string, fn func(target.Object) error) error {
	m.mu.Lock()
	m.count("list")
	var keys []string
	for k := range m.objs {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	infos := make([]target.Object, len(keys))
	for i, k := range keys {
		infos[i] = m.info(k, m.objs[k])
	}
	m.mu.Unlock()
	for _, o := range infos {
		if err := fn(o); err != nil {
			return err
		}
	}
	return nil
}

// Delete implements target.Store.
func (m *Mem) Delete(_ context.Context, keys []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.count("delete")
	if len(keys) > 1000 {
		return fmt.Errorf("targettest: %d keys in one delete request, at most 1000", len(keys))
	}
	for _, k := range keys {
		delete(m.objs, k)
	}
	return nil
}

// Copy implements target.Store.
func (m *Mem) Copy(_ context.Context, src target.ObjectRef, dstKey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.count("copy")
	if m.Source == nil {
		return target.ErrServerCopyUnavailable
	}
	data, ok := m.Source(src)
	if !ok {
		return target.ErrNotFound
	}
	m.objs[dstKey] = entry{data: append([]byte(nil), data...), at: m.now(), sse: m.SSE}
	if src.Progress != nil {
		src.Progress(int64(len(data)))
	}
	return nil
}

// Probe implements target.Store.
func (m *Mem) Probe(ctx context.Context, key string) (target.Object, error) {
	o, err := m.Put(ctx, key, []byte("probe"), target.PutOptions{})
	if err != nil {
		return target.Object{}, err
	}
	m.mu.Lock()
	delete(m.objs, key)
	m.mu.Unlock()
	return o, nil
}
