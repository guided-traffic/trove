// Package sync mirrors the advisory data of the vulnerability scanning into PostgreSQL (docs/spec/vulnerability-scanning.md
// R-VULN-003 to R-VULN-008, the sync half of R-VULN-024, R-VULN-026 and R-VULN-027): the OSV archives of the ecosystems trove
// serves, online (R-VULN-004) or from a generic local repository (R-VULN-005), the CISA known-exploited catalog (R-VULN-008),
// and the replica's copy of each ecosystem's state that [vuln.States] answers from.
//
// The jobs run on the worker role. A run validates before it writes: a full sync reads the archive twice, the first pass to
// count what is invalid and what the archive no longer has, so that the integrity guards stop a truncated or tampered source
// before anything is written; the second pass writes in batches of 1,000 records and checkpoints after each one, so that a run
// released by a drain resumes on another worker where it stopped.
//
// The package is a domain package. The pure parts it uses are leaves (internal/vuln/osv, advisory, match).
package sync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	stdsync "sync"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/match"
	vmetrics "github.com/guided-traffic/trove/internal/vuln/metrics"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// Purpose is the purpose label of the outbound client of the advisory sources (R-ARCH-022, R-OPS-011): the label of the
// outbound metrics and the purpose field of the upstream log. The factory takes any purpose; the label is declared here, as the
// Bastion's client declares its own.
const Purpose = "vuln_source"

// The batch and fetch bounds of R-VULN-004.
const (
	// BatchSize is the number of records one transaction upserts.
	BatchSize = 1000
	// BatchBytes is the size of the records, as received, above which a batch is cut before BatchSize records: a batch is one
	// jsonb document of its records, and 1,000 records of the largest size must not become one statement.
	BatchBytes = 32 << 20
	// RangeSize is the part of the change file one ranged request asks for.
	RangeSize = 64 << 10
	// FetchParallel is the number of single-record requests in flight.
	FetchParallel = 8
	// MaxWatermarkAge is the age of the watermark above which an incremental run turns full.
	MaxWatermarkAge = 7 * 24 * time.Hour
	// PartTimeout is the timeout of one record or one part of the change file.
	PartTimeout = 30 * time.Second
	// invalidPercent is the share of rejected records, in percent, above which a full sync fails (R-VULN-004: 1 %).
	invalidPercent = 1
	maxRedirects   = 3
	// maxChangeFile bounds what is read of a change file served without ranges.
	maxChangeFile = 64 << 20
	// failedEventEvery is the least time between two vulnerability.source.failed events of one source and ecosystem.
	failedEventEvery = time.Hour
)

// Database is the PostgreSQL store: transactions and plain reads.
type Database interface {
	meta.Runner
	Read(ctx context.Context, fn func(q.DBTX) error) error
}

// Changes notifies a change area in the writing transaction. *cluster.Changes implements it.
type Changes interface {
	Notify(tx meta.Tx, area string, version int, payload any) error
}

// Spool admits and hashes a body (R-STOR-004). *blob.Store implements it.
type Spool interface {
	Ingest(ctx context.Context, body io.Reader, o blob.IngestOptions) (*blob.Spooled, error)
}

// Enqueuer enqueues a job in the caller's transaction. *jobs.Queue implements it.
type Enqueuer interface {
	Enqueue(ctx context.Context, tx meta.Tx, e jobs.EnqueueSpec) (jobs.Result, error)
}

// Run is the part of a job's run handle the sync uses: its checkpoint and its progress. *jobs.Run implements it.
type Run interface {
	LoadCheckpoint(v any) (bool, error)
	SaveCheckpoint(ctx context.Context, v any) error
	SaveProgress(ctx context.Context, done, total int64, unit string) error
	SetResult(v any) error
}

// Params are the parameters of one run of a job.
type Params struct {
	// AcceptShrink overrides the shrink guard once (the task parameter {"acceptShrink": true}).
	AcceptShrink bool
	// Principal is the ID of the principal that started a manual run, empty for a scheduled one.
	Principal string
}

// Options configure [New].
type Options struct {
	DB      Database
	Changes Changes
	// Caches registers the per-replica cache of the source state.
	Caches *cache.Registry
	// HTTP is the outbound client of purpose [Purpose]: egress rules, https-only redirects at most 3, trust store.
	HTTP *http.Client
	// Spool admits an archive to the worker's spool.
	Spool Spool
	// Jobs enqueues vuln-evaluate; nil enqueues nothing.
	Jobs Enqueuer
	// Recorder records the audit events of the sync; nil discards them.
	Recorder audit.Recorder
	// Metrics are the collectors of R-VULN-024; nil counts nothing.
	Metrics *vmetrics.Metrics
	// Mirror reads the files of mode repository; nil makes that mode fail with mirror_repository_invalid.
	Mirror Mirror
	// Config is the vulnerabilities section.
	Config config.Vulnerabilities
	// Adapters returns the adapter of every ecosystem the release can sync, read at run time because the format registry is
	// complete only after the composition root has run.
	Adapters func() map[string]match.Adapter
	Log      *slog.Logger
	// Now overrides the clock, for tests.
	Now func() time.Time
	// AfterBatch is called after a full sync committed a batch and saved its checkpoint, with the ecosystem and the number of
	// batches committed; a test releases the job from it to prove the resumption. Production leaves it nil.
	AfterBatch func(ecosystem string, batches int)
}

// Service runs the sync jobs and answers the state of the advisory data.
type Service struct {
	o      Options
	st     store.Sync
	states *States
	log    *slog.Logger
	mu     stdsync.Mutex
	// startPartTimer replaces time.AfterFunc for the timeout of a record, so that a test fires it by hand; nil means the clock.
	startPartTimer func(d time.Duration, f func()) partTimer
}

// partTimer is the part of *time.Timer that the timeout of a record uses.
type partTimer interface {
	Stop() bool
	Reset(d time.Duration) bool
}

// New creates the service and registers the cache of the source state.
func New(o Options) (*Service, error) {
	switch {
	case o.DB == nil:
		return nil, errors.New("vulnerability sync: no database")
	case o.Changes == nil:
		return nil, errors.New("vulnerability sync: no change log")
	case o.Caches == nil:
		return nil, errors.New("vulnerability sync: no cache registry")
	case o.HTTP == nil:
		return nil, errors.New("vulnerability sync: no outbound client")
	case o.Spool == nil:
		return nil, errors.New("vulnerability sync: no spool")
	case o.Adapters == nil:
		return nil, errors.New("vulnerability sync: no adapters")
	}
	if o.Recorder == nil {
		o.Recorder = audit.Nop{}
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	s := &Service{o: o, log: o.Log.With("component", "vulnerabilities")}
	s.states = newStates(o.DB, o.Caches, o.Config.StaleAfter, o.Now, s.log)
	ecosystems, _ := s.ecosystems()
	for _, eco := range ecosystems {
		s.zeroRejected(eco)
	}
	return s, nil
}

// SetBatchHook installs the hook that is called after a full sync committed a batch and saved its checkpoint ([Options.AfterBatch]).
// It is a seam of the tests that release a job mid-run to prove the resumption; production never calls it.
func (s *Service) SetBatchHook(f func(ecosystem string, batches int)) {
	s.mu.Lock()
	s.o.AfterBatch = f
	s.mu.Unlock()
}

func (s *Service) batchHook() func(string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.o.AfterBatch
}

// States returns the replica's view of the advisory data state.
func (s *Service) States() vuln.States { return s.states }

// now is the clock.
func (s *Service) now() time.Time { return s.o.Now().UTC() }

// ecosystems returns the ecosystems a run syncs, sorted: the configured ones that have an adapter in this release, else every
// ecosystem with one (R-VULN-003: the list may narrow, never widen). An ecosystem the configuration names and no adapter
// serves cannot be reached through the validated configuration, so it is dropped here.
func (s *Service) ecosystems() ([]string, map[string]match.Adapter) {
	all := s.o.Adapters()
	var out []string
	if want := s.o.Config.Sources.OSV.Ecosystems; len(want) > 0 {
		for _, e := range want {
			if _, ok := all[e]; ok && !slices.Contains(out, e) {
				out = append(out, e)
			}
		}
	} else {
		for e := range all {
			out = append(out, e)
		}
	}
	slices.Sort(out)
	return out, all
}

// released reports a context that ends because the job is released for a drain: the run's state stays as the checkpoint
// left it and no failure is recorded.
func released(ctx context.Context) bool { return errors.Is(context.Cause(ctx), jobs.ErrReleased) }

// Result is what one run of one ecosystem did.
type Result struct {
	Ecosystem string `json:"ecosystem"`
	// Kind is full or incremental.
	Kind string `json:"kind"`
	// Outcome is changed or unchanged.
	Outcome     string `json:"outcome"`
	Written     int    `json:"written"`
	Skipped     int    `json:"skipped"`
	Rejected    int    `json:"rejected"`
	Deleted     int    `json:"deleted"`
	Unevaluable int    `json:"unevaluable"`
	Records     int    `json:"records"`
	// AcceptedShrink is true when the run applied an archive that the shrink guard would have refused.
	AcceptedShrink bool `json:"acceptedShrink,omitempty"`
	// Note says why a run did nothing, for a mirror item that is missing.
	Note string `json:"note,omitempty"`
}

// The outcomes and kinds of R-VULN-024.
const (
	outcomeChanged   = vmetrics.RunChanged
	outcomeUnchanged = vmetrics.RunUnchanged
	outcomeFailed    = vmetrics.RunFailed
	outcomeCancelled = vmetrics.RunCancelled
	kindFull         = vmetrics.RunFull
	kindIncremental  = vmetrics.RunIncremental
)

// count adds to a record counter of R-VULN-024.
func (s *Service) count(source, eco, result string, n int) {
	if s.o.Metrics != nil && n > 0 {
		s.o.Metrics.SyncRecords.WithLabelValues(source, eco, result).Add(float64(n))
	}
}

// zeroRejected creates the series of the rejected-record counters of an ecosystem at 0, for every result the alerts read, when the
// service is built: a counter that first appears with a value is invisible to increase() (Q-VULN-122).
func (s *Service) zeroRejected(eco string) {
	if s.o.Metrics == nil {
		return
	}
	for _, result := range []string{vmetrics.RecordRejected, vmetrics.RecordInvalid, vmetrics.RecordRejectedMalicious} {
		s.o.Metrics.SyncRecords.WithLabelValues(store.SourceOSV, eco, result).Add(0)
	}
}

func (s *Service) countRun(source, eco, kind, outcome string) {
	if s.o.Metrics != nil {
		s.o.Metrics.SyncRuns.WithLabelValues(source, eco, kind, outcome).Inc()
	}
}

// problemf builds the error of a failed run: the code is the one of vuln_source_state.last_error, the kind that of a source
// that failed (502).
func problemf(code, detail string, cause error) *failure {
	return &failure{kind: errs.UpstreamFailed, code: code, detail: detail, cause: cause}
}

// failure is the error of a failed run.
type failure struct {
	kind         errs.Kind
	code, detail string
	cause        error
}

func (f *failure) Error() string {
	if f.cause != nil {
		return fmt.Sprintf("%s: %s: %v", f.code, f.detail, f.cause)
	}
	return fmt.Sprintf("%s: %s", f.code, f.detail)
}

func (f *failure) Unwrap() error { return f.cause }
