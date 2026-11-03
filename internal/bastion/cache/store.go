package cache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/outbound"
)

// Options are what [New], [NewVolume] and [NewBucket] need besides the configuration. Every field is optional.
type Options struct {
	// Metrics receives the errors, corrupt objects, evictions and gauges; nil creates collectors that are
	// registered nowhere.
	Metrics *metrics.Metrics
	Logger  *slog.Logger
	// Factory is the outbound HTTP client factory the bucket's client is built with (R-ARCH-022); nil creates one
	// with the system roots.
	Factory *outbound.Factory
	// TempDir is where the bucket backend spools fills: the pod's temporary volume. Empty is os.TempDir().
	TempDir string
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// HoldBack is the withheld tail of a verified read; zero is [DefaultHoldBack].
	HoldBack int
	// Fault lets a test make the storage fail; nil in production.
	Fault *Fault
	// WrapTransport lets a test count the requests the bucket's client sends; nil leaves the transport alone.
	WrapTransport func(http.RoundTripper) http.RoundTripper
}

func (o *Options) fill() {
	if o.Metrics == nil {
		o.Metrics, _ = metrics.New(nil)
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.HoldBack <= 0 {
		o.HoldBack = DefaultHoldBack
	}
}

func newBackend(s store, o Options) *backend {
	if o.Fault != nil {
		s = &faultStore{store: s, f: o.Fault}
	}
	return &backend{s: s, now: o.Now, count: func(op string) { o.Metrics.CacheErrors.WithLabelValues(op).Inc() }}
}

// NewVolume returns the backend of a local directory (R-BAS-030).
func NewVolume(dir string, o Options) (Backend, error) {
	o.fill()
	s, err := newVolumeStore(dir, o.Now)
	if err != nil {
		return nil, err
	}
	return newBackend(s, o), nil
}

// NewBucket returns the backend of the Bastion's own bucket (R-BAS-030).
func NewBucket(ctx context.Context, cfg config.BastionCacheBucket, o Options) (Backend, error) {
	o.fill()
	b, err := openBucket(ctx, cfg, o)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func openBucket(ctx context.Context, cfg config.BastionCacheBucket, o Options) (*backend, error) {
	s3cfg := cfg.StorageS3()
	f := o.Factory
	if f == nil {
		f = outbound.New(config.Outbound{TLS: config.OutboundTLS{UseSystemRoots: true}})
	}
	c, err := s3.NewFromFactory(ctx, s3cfg, f)
	if err != nil {
		return nil, fmt.Errorf("cache bucket: %w", err)
	}
	if o.WrapTransport != nil {
		hc := c.HTTPClient()
		hc.Transport = o.WrapTransport(hc.Transport)
	}
	s, err := newBucketStore(c, cfg.Prefix, o.TempDir, o.Now())
	if err != nil {
		return nil, err
	}
	return newBackend(s, o), nil
}

// Cache is what the engine and the download buffer use (work packages 4 and 7): a [Backend] with the signing of
// entries, the verified read, the fill, the throttled access marker and the sweep around it. It adds nothing a
// backend does not allow; it is the place where R-BAS-022, R-BAS-031 and R-BAS-032 are applied once.
type Cache struct {
	back   *backend
	signer *Signer
	cfg    config.BastionCache
	m      *metrics.Metrics
	log    *slog.Logger
	now    func() time.Time
	hold   int
	// spool bounds the bytes of the fills in progress (bastion.cache.spool_max_bytes, Q-BAS-131).
	spool *spoolBudget

	ready atomic.Bool

	touchMu sync.Mutex
	touched map[string]time.Time
}

// New opens the backend of cfg.Backend and returns the cache. The signer holds the signing key of
// bastion.signing_key_file (R-BAS-032); it may be nil only where the configuration allows no key (the volume
// backend without Valkey), and entries are then stored without a signature (Q-BAS-98). New does not touch the backend's storage beyond preparing directories;
// [Cache.Ready] is the first check.
func New(ctx context.Context, cfg config.BastionCache, signer *Signer, o Options) (*Cache, error) {
	o.fill()
	var b *backend
	switch cfg.Backend {
	case "volume":
		s, err := newVolumeStore(cfg.Volume.Dir, o.Now)
		if err != nil {
			return nil, err
		}
		b = newBackend(s, o)
	case "bucket":
		var err error
		if b, err = openBucket(ctx, cfg.Bucket, o); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("cache: unknown backend %q", cfg.Backend)
	}
	return build(b, signer, cfg, o), nil
}

// build assembles the cache around a backend and publishes the capacity gauges.
func build(b *backend, signer *Signer, cfg config.BastionCache, o Options) *Cache {
	c := &Cache{
		back: b, signer: signer, cfg: cfg, m: o.Metrics, log: o.Logger, now: o.Now, hold: o.HoldBack, touched: map[string]time.Time{},
		spool: newSpoolBudget(int64(cfg.SpoolMaxBytes)),
	}
	arts, docs := c.limits()
	c.m.CacheCapacity.WithLabelValues(metrics.AreaArtifacts).Set(float64(arts))
	c.m.CacheCapacity.WithLabelValues(metrics.AreaDocuments).Set(float64(docs))
	return c
}

// Backend returns the backend, for the audit spool (R-BAS-039) and for tests.
func (c *Cache) Backend() Backend { return c.back }

// limits are the size limits of the areas (R-BAS-031): documents get their share, artifacts the rest, so that
// documents never evict artifacts and the two together stay within max_bytes (Q-BAS-88).
func (c *Cache) limits() (artifacts, documents int64) {
	documents = int64(c.cfg.DocumentsMaxBytes)
	return max(int64(c.cfg.MaxBytes)-documents, 0), documents
}

// Ready is the check that gates the first readiness (R-BAS-043): it checks the backend until one check
// succeeds and afterwards answers nil without a request. A backend that fails later is a cache that misses
// (R-BAS-030), not an unready replica.
func (c *Cache) Ready(ctx context.Context) error {
	if c.ready.Load() {
		return nil
	}
	if err := c.back.Check(ctx); err != nil {
		return err
	}
	c.ready.Store(true)
	return nil
}

func validEntry(e Entry) bool {
	return (e.Kind == EntryArtifact || e.Kind == EntryDocument) && validHash(e.SHA256) && e.Size >= 0
}

// StoreEntry signs the entry under its cache key and stores it. The bytes it points at are committed before
// (a [Fill]), so a reader never finds an entry without its bytes.
func (c *Cache) StoreEntry(ctx context.Context, cacheKey string, e Entry) error {
	if !validEntry(e) {
		return fmt.Errorf("%w: entry of kind %q with content address %q", ErrInvalid, e.Kind, shorten(e.SHA256))
	}
	rec, err := c.signer.SignEntry(cacheKey, e)
	if err != nil {
		return err
	}
	return c.back.PutEntry(ctx, KeyHash(cacheKey), rec)
}

// DeleteEntry removes the entry of a cache key.
func (c *Cache) DeleteEntry(ctx context.Context, cacheKey string) error {
	return c.back.DeleteEntry(ctx, KeyHash(cacheKey))
}

// LookupEntry finds the entry of a cache key and checks it (R-BAS-031, R-BAS-032). [ErrNotFound] is a miss and
// covers four cases: no entry; an entry whose signature fails, which is deleted and counted as stale; an entry
// whose bytes are gone, which is deleted and counted as missing; an entry of another kind than the key's
// bytes. Any other error is the backend's, already counted in trove_bastion_cache_errors_total, and the caller
// serves the request as a miss.
func (c *Cache) LookupEntry(ctx context.Context, cacheKey string) (Entry, ObjectInfo, error) {
	kh := KeyHash(cacheKey)
	rec, err := c.back.GetEntry(ctx, kh)
	if err != nil {
		return Entry{}, ObjectInfo{}, err
	}
	e, err := c.signer.VerifyEntry(cacheKey, rec)
	if err == nil && !validEntry(e) {
		err = ErrBadSignature
	}
	if err != nil {
		c.drop(ctx, kh, metrics.EvictStale, "an entry failed its signature")
		return Entry{}, ObjectInfo{}, ErrNotFound
	}
	info, err := c.back.HeadObject(WithArea(ctx, e.Kind), e.SHA256)
	if errors.Is(err, ErrNotFound) {
		c.drop(ctx, kh, metrics.EvictMissing, "an entry's bytes are gone")
		return Entry{}, ObjectInfo{}, ErrNotFound
	}
	if err != nil {
		return Entry{}, ObjectInfo{}, err
	}
	if info.Size != e.Size {
		c.corrupt(ctx, e, kh, fmt.Errorf("%w: the object has %d bytes, the entry says %d", ErrCorrupt, info.Size, e.Size))
		return Entry{}, ObjectInfo{}, ErrNotFound
	}
	return e, info, nil
}

func (c *Cache) drop(ctx context.Context, keyHash, reason, why string) {
	if err := c.back.DeleteEntry(ctx, keyHash); err != nil {
		c.log.Warn("could not delete a cache entry", "reason", why, "error", err)
		return
	}
	c.m.CacheEvictions.WithLabelValues(reason).Inc()
}

// corrupt handles bytes that failed verification: it counts, deletes the object and the entry of the key that
// found it, and logs. Other entries that point at the object are removed when they are read (their bytes are
// gone).
func (c *Cache) corrupt(ctx context.Context, e Entry, keyHash string, cause error) {
	c.m.CacheCorrupt.Inc()
	c.log.Error("a cached object failed verification", "sha256", e.SHA256, "kind", string(e.Kind), "error", cause)
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := c.back.DeleteObject(WithArea(dctx, e.Kind), e.SHA256); err != nil {
		c.log.Error("could not delete a corrupt cached object", "sha256", e.SHA256, "error", err)
	}
	if keyHash != "" {
		_ = c.back.DeleteEntry(dctx, keyHash)
	}
}

// Open opens the bytes of an entry found under cacheKey for a full read, verified while they are served with
// the hold-back (R-BAS-032): the last 64 KiB are released only when the SHA-256 and size equal the entry's. A
// mismatch makes Read return [ErrCorrupt] instead of the held bytes, so the caller aborts the transfer; the
// object and the entry of cacheKey are deleted and trove_bastion_cache_corrupt_total is counted, and the next
// lookup is a miss. [ErrNotFound] says the bytes are gone; the entry is deleted as in [Cache.LookupEntry].
func (c *Cache) Open(ctx context.Context, cacheKey string, e Entry) (io.ReadCloser, error) {
	rc, err := c.back.GetObject(WithArea(ctx, e.Kind), e.SHA256, 0, -1)
	if errors.Is(err, ErrNotFound) {
		c.drop(ctx, KeyHash(cacheKey), metrics.EvictMissing, "an entry's bytes are gone")
	}
	if err != nil {
		return nil, err
	}
	return newVerifyReader(rc, e.SHA256, e.Size, c.hold, func(cause error) {
		c.corrupt(ctx, e, KeyHash(cacheKey), cause)
	}), nil
}

// OpenRange opens length bytes of an entry's object from offset (length -1 is to the end). A range cannot be
// verified, as in R-STOR-013; the digest of a whole read is checked by the next [Cache.Open] (H-451).
func (c *Cache) OpenRange(ctx context.Context, e Entry, offset, length int64) (io.ReadCloser, error) {
	return c.back.GetObject(WithArea(ctx, e.Kind), e.SHA256, offset, length)
}

// Touch writes the access marker of an entry's bytes, at most once per bastion.cache.access_resolution per
// replica and content address (R-BAS-031). It is best effort: a failure is counted and not returned.
func (c *Cache) Touch(ctx context.Context, e Entry) {
	now := c.now()
	c.touchMu.Lock()
	if last, ok := c.touched[e.SHA256]; ok && now.Sub(last) < c.cfg.AccessResolution {
		c.touchMu.Unlock()
		return
	}
	if len(c.touched) >= maxTouched {
		for k, t := range c.touched {
			if now.Sub(t) >= c.cfg.AccessResolution {
				delete(c.touched, k)
			}
		}
	}
	if len(c.touched) >= maxTouched {
		clear(c.touched) // a flood of distinct objects: forgetting costs a few extra markers
	}
	c.touched[e.SHA256] = now
	c.touchMu.Unlock()
	if err := c.back.Touch(ctx, e.SHA256); err != nil {
		c.touchMu.Lock()
		delete(c.touched, e.SHA256)
		c.touchMu.Unlock()
	}
}

// maxTouched bounds the memory of the throttle: past it, markers older than the resolution are forgotten.
const maxTouched = 100_000
