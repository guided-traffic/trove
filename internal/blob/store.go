package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/guided-traffic/trove/internal/config"
)

// Store is the blob store of one replica: its spool, its object-store client
// and the database it commits to. Several Stores (several replicas) work on
// the same bucket and database without coordinating, which is what the commit
// protocol and garbage collection are built for.
type Store struct {
	cfg       config.Storage
	objects   ObjectStore
	db        DB
	spool     *Spool
	log       *slog.Logger
	metrics   *Metrics
	now       func() time.Time
	wait      time.Duration
	parts     chan struct{}
	onDamage  func(ctx context.Context, d Digest, status string)
	spoolOpts []SpoolOption
}

// Option configures a Store.
type Option func(*Store)

// WithLogger sets the logger.
func WithLogger(l *slog.Logger) Option { return func(s *Store) { s.log = l } }

// WithMetrics sets the metrics.
func WithMetrics(m *Metrics) Option { return func(s *Store) { s.metrics = m } }

// WithClock replaces the clock of the commit deadline; tests use it.
func WithClock(now func() time.Time) Option { return func(s *Store) { s.now = now } }

// WithWaitPoll sets how often a commit that met a deleting row looks again.
func WithWaitPoll(d time.Duration) Option { return func(s *Store) { s.wait = d } }

// WithSpoolOptions passes options to the spool the store creates.
func WithSpoolOptions(opts ...SpoolOption) Option {
	return func(s *Store) { s.spoolOpts = append(s.spoolOpts, opts...) }
}

// WithDamageHook is called after a download verification marked a blob damaged
// (R-STOR-013): the audit event of the detection hangs here.
func WithDamageHook(f func(ctx context.Context, d Digest, status string)) Option {
	return func(s *Store) { s.onDamage = f }
}

// New builds the store and its spool; the spool directory is emptied.
func New(cfg config.Storage, objects ObjectStore, db DB, opts ...Option) (*Store, error) {
	if objects == nil || db == nil {
		return nil, errors.New("blob store needs an object store and a database")
	}
	s := &Store{
		cfg: cfg, objects: objects, db: db, log: slog.Default(), now: time.Now, wait: 250 * time.Millisecond,
	}
	for _, o := range opts {
		o(s)
	}
	spoolOpts := append([]SpoolOption{WithSpoolLogger(s.log), WithSpoolMetrics(s.metrics)}, s.spoolOpts...)
	sp, err := NewSpool(cfg.Spool, spoolOpts...)
	if err != nil {
		return nil, fmt.Errorf("blob store: %w", err)
	}
	s.spool = sp
	n := cfg.S3.Multipart.MaxConcurrentParts
	if n < 1 {
		n = 1
	}
	s.parts = make(chan struct{}, n)
	return s, nil
}

// Spool is the store's spool.
func (s *Store) Spool() *Spool { return s.spool }

// Ingest spools and hashes a body with the store's default maximum size
// (R-STOR-004). The caller closes the result.
func (s *Store) Ingest(ctx context.Context, body io.Reader, o IngestOptions) (*Spooled, error) {
	max := int64(s.cfg.Upload.MaxSize)
	if o.MaxSize <= 0 || o.MaxSize > max {
		o.MaxSize = max
	}
	return s.spool.Ingest(ctx, body, o)
}

// Key is the object key of a blob under the configured prefix.
func (s *Store) Key(d Digest) string { return Key(s.cfg.S3.Prefix, d) }

// Row is a blob row (R-STOR-003).
type Row struct {
	Digests
	State         string
	ContentStatus string
	CreatedAt     time.Time
}
