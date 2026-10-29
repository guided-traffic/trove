package audit

import (
	"context"
	"time"
)

// Record queues an event of an Async type. It never blocks and never fails the
// request (R-OPS-015): an event of the wrong type or an invalid event is logged
// as a programming error and skipped, deduplicated events are counted in memory,
// and a full buffer writes the log copy only and counts
// trove_audit_events_dropped_total.
func (w *Writer) Record(_ context.Context, ev Event) {
	t, ok := Lookup(ev.Type)
	if !ok || t.Mode != Async {
		w.app.Error("audit: Record called with an event that is not a registered async type", "type", ev.Type)
		return
	}
	if !w.wanted(ev) {
		return
	}
	now := w.now()
	r, err := toRow(t, ev, newID(now), now, w.o.Replica)
	if err != nil {
		w.app.Error("audit: invalid event skipped", "type", ev.Type, "error", err)
		return
	}
	for _, out := range w.dedup.admit(ev, r, now) {
		w.enqueue(out)
	}
}

// wanted applies the switches of R-OPS-015: anonymous denials and successful
// downloads are recorded only when enabled; otherwise they appear in the request
// log and the metrics only.
func (w *Writer) wanted(ev Event) bool {
	switch ev.Type {
	case typeDenied:
		return ev.Actor.Kind != "anonymous" || w.o.Config.RecordAnonymousDenials
	case typeDownloaded:
		return w.o.Config.RecordDownloads
	}
	return true
}

// enqueue hands a row to the batch writer without ever waiting.
func (w *Writer) enqueue(r row) {
	if w.closed.Load() {
		w.drop(r, "the writer is closed")
		return
	}
	select {
	case w.ch <- r:
	default:
		w.drop(r, "the buffer is full")
	}
}

// drop is the path of an event that cannot reach the table: it is written to the
// log stream, counted, and announced on the app log at most once a minute.
func (w *Writer) drop(r row, why string) {
	w.logCopy(r)
	w.metrics.Dropped()
	w.mu.Lock()
	w.droppedSince++
	n, due := w.droppedSince, w.now().Sub(w.lastDropWarn) >= time.Minute
	if due {
		w.lastDropWarn, w.droppedSince = w.now(), 0
	}
	w.mu.Unlock()
	if due {
		w.app.Warn("audit events are not reaching the table; they are in the log stream only", "reason", why, "events", n)
	}
}

// stored runs once a row is in the table: the log copy and the counter.
func (w *Writer) stored(r row) {
	w.logCopy(r)
	w.metrics.Recorded(r.Category, Outcome(r.Outcome))
}

// Start launches the batch writer. It runs until Flush.
func (w *Writer) Start() {
	if w.started.CompareAndSwap(false, true) {
		go w.loop()
	}
}

// Flush stops intake, writes what is buffered and the open dedup summaries, and
// stops the writer (R-OPS-015: step 6 of the shutdown). It returns when the
// buffer is empty or ctx ends; whatever could not be written is in the log
// stream and counted as dropped, and Flush returns the error that stopped it. It
// is safe to call more than once.
func (w *Writer) Flush(ctx context.Context) error {
	if !w.started.Load() {
		w.closed.Store(true)
		return nil
	}
	if w.closed.CompareAndSwap(false, true) {
		w.stopCtx = ctx
		close(w.stop)
	}
	select {
	case <-w.done:
		return w.finalErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// loop is the one goroutine that writes asynchronous events.
func (w *Writer) loop() {
	defer close(w.done)
	tick := time.NewTicker(flushEvery)
	defer tick.Stop()
	var (
		pending  []row
		failures int
		retryAt  time.Time
	)
	// attempt writes the pending batch unless a backoff is running.
	attempt := func() {
		if len(pending) == 0 || w.now().Before(retryAt) {
			return
		}
		rest, err := w.writeBatch(context.Background(), pending)
		pending = rest
		if err == nil {
			if failures > 0 {
				w.app.Info("audit writer recovered", "after_failures", failures)
			}
			failures, retryAt = 0, time.Time{}
			return
		}
		if failures == 0 {
			w.app.Warn("audit events cannot be written; retrying", "error", err, "pending", len(pending))
		}
		failures++
		retryAt = w.now().Add(backoff(failures))
	}
	for {
		// A full batch stops the intake until it is written: the channel then
		// fills behind it and Record drops, instead of the writer growing.
		in := w.ch
		if len(pending) >= batchMax {
			in = nil
		}
		select {
		case r := <-in:
			pending = append(pending, r)
			if len(pending) >= batchMax {
				attempt()
			}
		case <-tick.C:
			pending = append(pending, w.dedup.expire(w.now())...)
			attempt()
		case <-w.stop:
			w.finish(pending)
			return
		}
	}
}

// backoff is the wait before attempt n+1 after n consecutive failures.
func backoff(failures int) time.Duration {
	d := retryMin
	for i := 1; i < failures && d < retryMax; i++ {
		d *= 2
	}
	return min(d, retryMax)
}

// writeBatch stores rows under parent. On success it returns nil rest. When the
// failure is the data of the batch, it falls back to one row at a time and drops
// the rows that the table refuses; when it is the database, it returns the rows
// to keep.
func (w *Writer) writeBatch(parent context.Context, rows []row) (rest []row, err error) {
	ctx, cancel := context.WithTimeout(parent, writeTimeout)
	defer cancel()
	if err = w.sink.write(ctx, rows); err == nil {
		w.storedAll(rows)
		return nil, nil
	}
	if !w.isData(err) {
		return rows, err
	}
	for i, r := range rows {
		one := []row{r}
		e := w.sink.write(ctx, one)
		switch {
		case e == nil:
			w.storedAll(one)
		case w.isData(e):
			w.app.Error("audit: an event the table refuses was dropped", "type", r.Action, "id", r.ID, "error", e)
			w.drop(r, "the table refuses the event")
		default:
			return rows[i:], e
		}
	}
	return nil, nil
}

func (w *Writer) storedAll(rows []row) {
	for _, r := range rows {
		w.stored(r)
	}
}

// finish is the shutdown: drain the channel, add every open summary, write
// everything under the flush context and give up on what remains.
func (w *Writer) finish(pending []row) {
	ctx := w.stopCtx
	for draining := true; draining; {
		select {
		case r := <-w.ch:
			pending = append(pending, r)
		default:
			draining = false
		}
	}
	pending = append(pending, w.dedup.all(w.now())...)
	wait := retryMin
	for len(pending) > 0 {
		n := min(len(pending), batchMax)
		rest, err := w.writeBatch(ctx, pending[:n])
		pending = append(rest[:len(rest):len(rest)], pending[n:]...)
		if err == nil {
			continue
		}
		w.finalErr = err
		select {
		case <-ctx.Done():
			for _, r := range pending {
				w.drop(r, "the shutdown ended before the event was written")
			}
			return
		case <-time.After(wait):
			wait = min(wait*2, retryMax)
		}
	}
}
