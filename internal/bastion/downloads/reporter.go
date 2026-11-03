package downloads

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/cache"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastion/mainclient"
	"github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/config"
)

// Sender sends a batch to the main installation; mainclient.Client implements it. A 2xx is nil.
type Sender interface {
	Downloads(ctx context.Context, req iface.DownloadsRequest) error
}

// Spool is the audit spool of the cache backend (R-BAS-039): cache.Backend implements it.
type Spool interface {
	SpoolWrite(ctx context.Context, replica string, jsonl []byte) error
	SpoolList(ctx context.Context) ([]cache.SpoolFile, error)
	SpoolRead(ctx context.Context, f cache.SpoolFile) ([]byte, error)
	SpoolDelete(ctx context.Context, f cache.SpoolFile) error
}

// DefaultAdoptEvery is how often a replica looks for spool files.
const DefaultAdoptEvery = 30 * time.Second

// ReporterOptions configure a [Reporter].
type ReporterOptions struct {
	// Config is bastion.audit: buffer_events, batch_size, flush_interval, spool_max_events.
	Config config.BastionAudit
	// Replica names the spool directory this replica writes to and is the replica of an event.
	Replica string
	Sender  Sender
	// Spool is the backend's audit spool; nil spools nothing, and a drain drops what it cannot send.
	Spool Spool
	// Reachable tells whether the main installation is reachable (link.Link.Reachable); nil means always.
	Reachable func() bool
	// Claim takes the exclusive right to deliver one spool file, so that two replicas do not send the same
	// file (shared.FlightLock.TryAcquire with Valkey). nil, or a claim that fails, delivers without one.
	Claim   func(ctx context.Context, keyHash string) (held bool, release func(), err error)
	Metrics *metrics.Metrics
	Log     *slog.Logger
	// AdoptEvery is how often the spool is looked at; zero means [DefaultAdoptEvery].
	AdoptEvery time.Duration
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Reporter is the download buffer of a replica (R-BAS-016, R-BAS-039, Q-BAS-45). It implements [Buffer].
type Reporter struct {
	o   ReporterOptions
	log *slog.Logger
	now func() time.Time

	kick chan struct{}

	sendMu sync.Mutex // one send at a time: the flush loop, the drain
	mu     sync.Mutex
	enable bool
	events []iface.DownloadEvent
	// dropped counts the events lost since droppedSince and not yet reported.
	dropped      int
	droppedSince time.Time
	drained      bool
}

var _ Buffer = (*Reporter)(nil)

// NewReporter returns a buffer that sends nothing until [Reporter.SetEnabled] says recordDownloads.
func NewReporter(o ReporterOptions) *Reporter {
	if o.AdoptEvery <= 0 {
		o.AdoptEvery = DefaultAdoptEvery
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Reachable == nil {
		o.Reachable = func() bool { return true }
	}
	return &Reporter{o: o, log: o.Log.With("component", "downloads"), now: o.Now, kick: make(chan struct{}, 1)}
}

func (r *Reporter) gaugeLocked() { r.o.Metrics.AuditBuffer.Set(float64(len(r.events))) }

// Add implements [Buffer].
func (r *Reporter) Add(e iface.DownloadEvent) {
	r.mu.Lock()
	if !r.enable {
		r.mu.Unlock()
		return
	}
	if r.drained || len(r.events) >= r.o.Config.BufferEvents {
		r.dropLocked(1, e.At)
		r.mu.Unlock()
		return
	}
	r.events = append(r.events, e)
	r.gaugeLocked()
	full := len(r.events) >= r.o.Config.BatchSize
	r.mu.Unlock()
	if full {
		select {
		case r.kick <- struct{}{}:
		default:
		}
	}
}

// dropLocked counts n lost events; since is the time of the oldest. r.mu is held.
func (r *Reporter) dropLocked(n int, since time.Time) {
	if n <= 0 {
		return
	}
	if r.dropped == 0 {
		if since.IsZero() {
			since = r.now()
		}
		r.droppedSince = since.UTC()
	}
	r.dropped += n
	r.o.Metrics.AuditDropped.Add(float64(n))
	r.o.Metrics.AuditEvents.WithLabelValues(metrics.AuditDropped).Add(float64(n))
}

// SetEnabled implements [Buffer]. Turning it off discards what is buffered: the main installation records
// nothing (R-BAS-016), so nothing is owed.
func (r *Reporter) SetEnabled(on bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.enable == on {
		return
	}
	r.enable = on
	if !on {
		r.events, r.dropped, r.droppedSince = nil, 0, time.Time{}
		r.gaugeLocked()
	}
}

// Run implements [Buffer]: it flushes every flush interval and when a batch is full, and looks at the spool.
func (r *Reporter) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); r.adoptLoop(ctx) }()
	t := time.NewTicker(r.o.Config.FlushInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return nil
		case <-t.C:
		case <-r.kick:
		}
		r.flush(ctx)
	}
}

func (r *Reporter) adoptLoop(ctx context.Context) {
	t := time.NewTicker(r.o.AdoptEvery)
	defer t.Stop()
	for {
		if err := r.Adopt(ctx); err != nil && ctx.Err() == nil {
			r.log.Warn("the audit spool could not be delivered", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// batch returns the next batch without removing it.
func (r *Reporter) batch() (req iface.DownloadsRequest, n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n = min(len(r.events), r.o.Config.BatchSize)
	req.Events = append(make([]iface.DownloadEvent, 0, n), r.events[:n]...)
	req.Dropped = r.dropped
	if r.dropped > 0 {
		since := r.droppedSince
		req.DroppedSince = &since
	}
	return req, n
}

// sent removes the n events and the dropped count a batch carried.
func (r *Reporter) sent(n, dropped int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events[:0], r.events[n:]...)
	r.dropped -= dropped
	if r.dropped <= 0 {
		r.dropped, r.droppedSince = 0, time.Time{}
	}
	r.gaugeLocked()
	r.o.Metrics.AuditEvents.WithLabelValues(metrics.AuditSent).Add(float64(n))
}

// rejected removes a batch the main installation refused for good and counts its events as lost.
func (r *Reporter) rejected(req iface.DownloadsRequest, n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var since time.Time
	if n > 0 {
		since = req.Events[0].At
	}
	r.events = append(r.events[:0], r.events[n:]...)
	r.dropLocked(n, since)
	r.gaugeLocked()
}

// permanent reports whether the main installation refused the batch itself, so that sending it again cannot
// help: 400, 413 or 422. Every other failure is retried (Q-BAS-78).
func permanent(err error) bool {
	var ae *mainclient.AnswerError
	if !errors.As(err, &ae) {
		return false
	}
	switch ae.Status {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return true
	}
	return false
}

// flush sends the buffer in batches while the main installation is reachable.
func (r *Reporter) flush(ctx context.Context) {
	r.sendMu.Lock()
	defer r.sendMu.Unlock()
	r.flushLocked(ctx)
}

func (r *Reporter) flushLocked(ctx context.Context) (ok bool) {
	for ctx.Err() == nil {
		if !r.o.Reachable() {
			return false
		}
		req, n := r.batch()
		if n == 0 && req.Dropped == 0 {
			return true
		}
		err := r.o.Sender.Downloads(ctx, req)
		switch {
		case err == nil:
			r.sent(n, req.Dropped)
		case permanent(err):
			r.log.Error("the main installation refused a batch of download events; its events are counted as dropped", "events", n, "error", err)
			r.rejected(req, n)
		default:
			r.log.Debug("download events could not be sent; they stay buffered", "events", n, "error", err)
			return false
		}
	}
	return false
}

// Drain implements [Buffer]: it sends what is buffered while the main installation is reachable and spools the
// rest, at most bastion.audit.spool_max_events (R-BAS-039). Calling it again does nothing.
func (r *Reporter) Drain(ctx context.Context) error {
	r.sendMu.Lock()
	defer r.sendMu.Unlock()
	r.mu.Lock()
	if r.drained {
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()
	r.flushLocked(ctx)

	r.mu.Lock()
	r.drained = true
	events, dropped, since := r.events, r.dropped, r.droppedSince
	r.events, r.dropped, r.droppedSince = nil, 0, time.Time{}
	r.gaugeLocked()
	r.mu.Unlock()
	if len(events) == 0 && dropped == 0 {
		return nil
	}
	return r.spool(ctx, events, dropped, since)
}

// spool writes the events into one file of the audit spool, one batch per line.
func (r *Reporter) spool(ctx context.Context, events []iface.DownloadEvent, dropped int, since time.Time) error {
	m := r.o.Metrics
	keep := min(len(events), r.o.Config.SpoolMaxEvents)
	lost := len(events) - keep
	if r.o.Spool == nil {
		keep, lost = 0, len(events)
	}
	var reqs []iface.DownloadsRequest
	for i := 0; i < keep; i += r.o.Config.BatchSize {
		end := min(i+r.o.Config.BatchSize, keep)
		reqs = append(reqs, iface.DownloadsRequest{Events: append(make([]iface.DownloadEvent, 0, end-i), events[i:end]...)})
	}
	if dropped > 0 && r.o.Spool != nil {
		if len(reqs) == 0 {
			reqs = append(reqs, iface.DownloadsRequest{Events: []iface.DownloadEvent{}})
		}
		s := since
		reqs[0].Dropped, reqs[0].DroppedSince = dropped, &s
	}
	var buf bytes.Buffer
	for _, req := range reqs {
		line, err := iface.Marshal(req)
		if err != nil {
			return fmt.Errorf("downloads: encoding the spool: %w", err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if lost > 0 {
		m.AuditDropped.Add(float64(lost))
		m.AuditEvents.WithLabelValues(metrics.AuditDropped).Add(float64(lost))
		r.log.Error("download events were lost at shutdown: no spool, or more than spool_max_events", "events", lost)
	}
	if buf.Len() == 0 {
		return nil
	}
	if err := r.o.Spool.SpoolWrite(ctx, r.o.Replica, buf.Bytes()); err != nil {
		m.AuditDropped.Add(float64(keep))
		m.AuditEvents.WithLabelValues(metrics.AuditDropped).Add(float64(keep))
		return fmt.Errorf("downloads: writing the audit spool: %w", err)
	}
	m.AuditEvents.WithLabelValues(metrics.AuditSpooled).Add(float64(keep))
	r.log.Info("download events spooled", "events", keep, "dropped", dropped)
	return nil
}

// Adopt implements [Buffer]: any replica delivers the files of the audit spool once the main installation is
// reachable and deletes each after a 2xx (R-BAS-039).
func (r *Reporter) Adopt(ctx context.Context) error {
	if r.o.Spool == nil {
		return nil
	}
	r.mu.Lock()
	done := r.drained
	r.mu.Unlock()
	if done || !r.o.Reachable() {
		return nil
	}
	files, err := r.o.Spool.SpoolList(ctx)
	if err != nil {
		return fmt.Errorf("downloads: listing the audit spool: %w", err)
	}
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !r.o.Reachable() {
			return nil
		}
		if err := r.adoptFile(ctx, f); err != nil {
			return err
		}
	}
	return nil
}

func (r *Reporter) adoptFile(ctx context.Context, f cache.SpoolFile) error {
	if r.o.Claim != nil {
		sum := sha256.Sum256([]byte("audit-spool/" + f.Replica + "/" + f.Name))
		held, release, err := r.o.Claim(ctx, hex.EncodeToString(sum[:]))
		if err == nil {
			if !held {
				return nil // another replica delivers it
			}
			defer release()
		}
	}
	data, err := r.o.Spool.SpoolRead(ctx, f)
	if errors.Is(err, cache.ErrNotFound) {
		return nil // delivered by another replica meanwhile
	}
	if err != nil {
		return fmt.Errorf("downloads: reading spool file %s/%s: %w", f.Replica, f.Name, err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	progressed := false
	for i, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var req iface.DownloadsRequest
		if err := iface.DecodeStrict(bytes.NewReader(line), &req); err != nil {
			r.log.Error("a line of the audit spool does not decode and is skipped", "file", f.Name, "line", i+1, "error", err)
			continue
		}
		if err := r.o.Sender.Downloads(ctx, req); err != nil {
			if permanent(err) {
				r.log.Error("the main installation refused a spooled batch; it is skipped", "file", f.Name, "line", i+1, "events", len(req.Events), "error", err)
				r.o.Metrics.AuditEvents.WithLabelValues(metrics.AuditDropped).Add(float64(len(req.Events)))
				progressed = true
				continue
			}
			if progressed {
				r.keepRest(ctx, f, lines[i:])
			}
			return fmt.Errorf("downloads: delivering spool file %s/%s: %w", f.Replica, f.Name, err)
		}
		r.o.Metrics.AuditEvents.WithLabelValues(metrics.AuditSent).Add(float64(len(req.Events)))
		progressed = true
	}
	if err := r.o.Spool.SpoolDelete(ctx, f); err != nil {
		return fmt.Errorf("downloads: deleting spool file %s/%s: %w", f.Replica, f.Name, err)
	}
	return nil
}

// keepRest replaces a file of which the first lines were delivered by one with the lines that were not, so that
// the next delivery does not send the first ones again (H-444). The caller passes the line that failed first.
func (r *Reporter) keepRest(ctx context.Context, f cache.SpoolFile, rest [][]byte) {
	data := append(bytes.Join(rest, []byte("\n")), '\n')
	if err := r.o.Spool.SpoolWrite(ctx, r.o.Replica, data); err != nil {
		r.log.Warn("the undelivered rest of a spool file could not be saved; the whole file is sent again", "file", f.Name, "error", err)
		return
	}
	if err := r.o.Spool.SpoolDelete(ctx, f); err != nil {
		r.log.Warn("a spool file could not be deleted after its rest was saved", "file", f.Name, "error", err)
	}
}
