package ci

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
)

// The per-replica aggregation of the denial events (R-CI-021): the first denial of a client address and reason
// in a minute writes its ci.exchange.denied event at once, the later ones of that minute are written as one event
// with their count when the minute ends. With n web replicas that is at most 2n events per address, reason and
// minute, so an anonymous flood costs a bounded number of audit rows. The buffer is per-replica state that
// requests write and never read (R-ARCH-032): no answer depends on it. A crashed replica loses at most one minute
// of counts, never the first denial's event, because that one is recorded before the request is answered.
//
// The end of a minute is a timer that the first entry arms and that re-arms itself while entries remain; it runs
// only while denials arrive, and Stop writes what is left at shutdown.

// maxDenialKeys bounds the buffer. A key that does not fit is counted under the reason alone (the address "*"), so
// a flood from many addresses still writes at most one immediate event and one summary per reason and minute
// beyond the keys the buffer holds.
const maxDenialKeys = 10000

// overflowAddr is the address of the keys that did not fit.
const overflowAddr = "*"

type denialKey struct {
	addr   string
	reason Reason
}

type denialEntry struct {
	window time.Time
	count  int64
	// issuer is the issuer of the first denial of the window, "" before one was resolved.
	issuer string
}

// denials is the aggregation buffer of one replica.
type denials struct {
	rec audit.Recorder
	now func() time.Time

	mu      sync.Mutex
	entries map[denialKey]*denialEntry
	timer   *time.Timer
	stopped bool
}

func newDenials(rec audit.Recorder, now func() time.Time) *denials {
	return &denials{rec: rec, now: now, entries: map[denialKey]*denialEntry{}}
}

// record counts a denial and writes ev when it is the first of its address and reason in the current minute.
func (d *denials) record(ctx context.Context, issuer string, r Reason, ev audit.Event) {
	ctx = context.WithoutCancel(ctx)
	now := d.now()
	window := now.Truncate(time.Minute)
	key := denialKey{addr: ev.Source.ClientAddr, reason: r}
	var summary *audit.Event
	d.mu.Lock()
	e := d.entries[key]
	if e == nil && len(d.entries) >= maxDenialKeys {
		key.addr = overflowAddr
		e = d.entries[key]
	}
	switch {
	case d.stopped:
		// After the flush of the shutdown every denial is written at once.
		d.mu.Unlock()
		d.rec.Record(ctx, ev)
		return
	case e != nil && e.window.Equal(window):
		e.count++
		d.mu.Unlock()
		return
	case e != nil && e.count > 0:
		// The entry of an ended window whose timer has not fired yet.
		s := d.summary(key, e)
		summary = &s
	}
	d.entries[key] = &denialEntry{window: window, issuer: issuer}
	d.armLocked(window.Add(time.Minute).Sub(now))
	d.mu.Unlock()
	if summary != nil {
		d.rec.Record(ctx, *summary)
	}
	d.rec.Record(ctx, ev)
}

// armLocked starts the timer of the window's end when none is running.
func (d *denials) armLocked(after time.Duration) {
	if d.timer != nil || d.stopped {
		return
	}
	d.timer = time.AfterFunc(max(after, time.Millisecond), d.tick)
}

// tick ends the windows that are over: one summary per entry with a count, and the entries go.
func (d *denials) tick() {
	now := d.now()
	window := now.Truncate(time.Minute)
	var out []audit.Event
	d.mu.Lock()
	d.timer = nil
	for k, e := range d.entries {
		if e.window.Before(window) {
			if e.count > 0 {
				out = append(out, d.summary(k, e))
			}
			delete(d.entries, k)
		}
	}
	if len(d.entries) > 0 {
		d.armLocked(window.Add(time.Minute).Sub(now))
	}
	d.mu.Unlock()
	for _, ev := range out {
		d.rec.Record(context.Background(), ev)
	}
}

// stop writes every count that is left and ends the timer (the flush of R-ARCH-024 step 6); later denials are written
// one by one.
func (d *denials) stop() {
	var out []audit.Event
	d.mu.Lock()
	d.stopped = true
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	for k, e := range d.entries {
		if e.count > 0 {
			out = append(out, d.summary(k, e))
		}
		delete(d.entries, k)
	}
	d.mu.Unlock()
	for _, ev := range out {
		d.rec.Record(context.Background(), ev)
	}
}

// summary is the event of the later denials of one window: the reason, the issuer of the first, the count; no
// claim, no iss, no kid of any of them.
func (d *denials) summary(k denialKey, e *denialEntry) audit.Event {
	det := map[string]any{"reason": string(k.reason), "count": e.count}
	if e.issuer != "" {
		det["issuer"] = e.issuer
	}
	b, _ := json.Marshal(det)
	src := audit.Source{}
	if k.addr != overflowAddr {
		src.ClientAddr = k.addr
	}
	return audit.Event{
		Type: audit.EventCIExchangeDenied, Outcome: audit.Failure, Reason: string(k.reason),
		Actor: audit.Actor{Kind: string(principal.KindAnonymous)}, Source: src, Details: b,
	}
}
