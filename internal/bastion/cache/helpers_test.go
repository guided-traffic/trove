package cache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/config"
)

const (
	kib = 1 << 10
	mib = 1 << 20
)

// clock is a settable clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func digest(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func payload(seed uint64, n int) []byte {
	b := make([]byte, n)
	r := rand.New(rand.NewPCG(seed, seed))
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}

func testSigner(t testing.TB) *Signer {
	t.Helper()
	s, err := NewSigner(bytes.Repeat([]byte{9}, MinKeyBytes))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// rig is a cache on a volume in a temporary directory, with a controllable clock.
type rig struct {
	*Cache
	t     *testing.T
	dir   string
	clock *clock
	m     *metrics.Metrics
	cfg   config.BastionCache
}

// testConfig has an artifact limit of 1000 bytes and a document limit of 200: max_bytes 1200.
func testConfig(dir string) config.BastionCache {
	return config.BastionCache{
		Backend: "volume", MaxBytes: 1200, MaxObjectBytes: 600, DocumentsMaxBytes: 200,
		SweepInterval: time.Minute, AccessResolution: time.Hour,
		Volume: config.BastionCacheVolume{Dir: dir},
	}
}

func newRig(t *testing.T, mod ...func(*config.BastionCache)) *rig {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "volume")
	cfg := testConfig(dir)
	for _, m := range mod {
		m(&cfg)
	}
	clk := newClock()
	m, err := metrics.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	// The store is always the volume: a test of the bucket's lease or interval sets cfg.Backend to bucket and
	// keeps the volume under it. The bucket store itself is tested against S3 (test/integration/bastioncache).
	o := Options{Metrics: m, Now: clk.Now}
	o.fill()
	vs, err := newVolumeStore(dir, clk.Now)
	if err != nil {
		t.Fatal(err)
	}
	c := build(newBackend(vs, o), testSigner(t), cfg, o)
	return &rig{Cache: c, t: t, dir: dir, clock: clk, m: m, cfg: cfg}
}

// fillObject stores bytes through a fill, as the engine does, and returns their digest.
func (r *rig) fillObject(kind EntryKind, data []byte) string {
	r.t.Helper()
	f, err := r.NewFill(kind)
	if err != nil {
		r.t.Fatal(err)
	}
	if _, err := f.Write(data); err != nil {
		r.t.Fatal(err)
	}
	sha := digest(data)
	if err := f.Commit(context.Background(), sha); err != nil {
		r.t.Fatal(err)
	}
	return sha
}

func (r *rig) entry(kind EntryKind, data []byte) Entry {
	return Entry{Kind: kind, SHA256: digest(data), Size: int64(len(data)), Repository: "npm-all", Status: 200, ValidatedAt: r.clock.Now()}
}

// objectPath is where the volume keeps the bytes of a content address.
func (r *rig) objectPath(kind EntryKind, sha string) string {
	key, err := objectKey(kind, sha)
	if err != nil {
		r.t.Fatal(err)
	}
	return filepath.Join(r.dir, filepath.FromSlash(key))
}

func (r *rig) setTime(p string, at time.Time) {
	r.t.Helper()
	if err := os.Chtimes(p, at, at); err != nil {
		r.t.Fatal(err)
	}
}

func errs(t testing.TB, m *metrics.Metrics, op string) float64 {
	t.Helper()
	return val(t, m.CacheErrors.WithLabelValues(op))
}

func evictions(t testing.TB, m *metrics.Metrics, reason string) float64 {
	t.Helper()
	return val(t, m.CacheEvictions.WithLabelValues(reason))
}

func readAllT(t testing.TB, rc io.ReadCloser) []byte {
	t.Helper()
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// val reads the value of a counter or a gauge.
func val(t testing.TB, c interface{ Write(*dto.Metric) error }) float64 {
	t.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatal(err)
	}
	if m.Counter != nil {
		return m.Counter.GetValue()
	}
	return m.Gauge.GetValue()
}
