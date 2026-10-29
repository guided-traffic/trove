package audit

import (
	"bytes"
	"encoding/json"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/config"
)

// Event types the writer treats specially (R-OPS-015). The owning areas register
// them in events_<area>.go; the writer knows only their names.
const (
	typeDenied     = "authz.denied"
	typeRejected   = "token.rejected"
	typeDownloaded = "artifact.downloaded"
)

// deduper is the deduplication without updates of R-OPS-015. The first
// occurrence of a key is written at once with details.count = 1. Repeats within
// the window are counted in memory; when the window closes, one summary event
// with details.count = the number of repeats and details.repeat_of = the ID of
// the first event is written. The state is per replica and in process memory
// only: an audit record must not depend on shared ephemeral state (INV-3), so n
// replicas yield up to n first events and n summaries per key and window, and
// the counts still sum exactly.
//
// The state is bounded: maxKeys open windows. A key that finds the table full
// is written without deduplication, which the bounded buffer then absorbs.
type deduper struct {
	denial, rejection time.Duration
	maxKeys           int
	now               func() time.Time

	mu      sync.Mutex
	entries map[string]*openWindow
}

// openWindow is one open deduplication window.
type openWindow struct {
	first   row
	end     time.Time
	repeats int
}

func newDeduper(c config.Audit, now func() time.Time) *deduper {
	return &deduper{
		denial: c.DenialDedupWindow, rejection: c.TokenRejectionDedupWindow,
		maxKeys: max(c.AsyncBuffer, 1), now: now, entries: map[string]*openWindow{},
	}
}

// admit decides what to write for an event at now: the rows to enqueue, in
// order. An event of a type without deduplication is written as it is. A repeat
// inside an open window is counted and yields nothing. An event after its
// window closed yields the old window's summary (when it had repeats) and then
// itself as the first of a new window.
func (d *deduper) admit(ev Event, r row, now time.Time) []row {
	span, key := d.rule(ev)
	if span <= 0 || key == "" {
		return []row{r}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []row
	if w, ok := d.entries[key]; ok {
		if now.Before(w.end) {
			w.repeats++
			return nil
		}
		if s, ok := summary(w, now); ok {
			out = append(out, s)
		}
		delete(d.entries, key)
	}
	r.Details = withCount(r.Details, 1, "")
	if len(d.entries) < d.maxKeys {
		d.entries[key] = &openWindow{first: r, end: now.Add(span)}
	}
	return append(out, r)
}

// expire returns the summaries of the windows that have closed and forgets
// them; a window without repeats leaves nothing to write.
func (d *deduper) expire(now time.Time) []row {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []row
	for k, w := range d.entries {
		if now.Before(w.end) {
			continue
		}
		if s, ok := summary(w, now); ok {
			out = append(out, s)
		}
		delete(d.entries, k)
	}
	return out
}

// all returns the summaries of every open window, closing them: a replica that
// shuts down writes its open summaries before it exits.
func (d *deduper) all(now time.Time) []row {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []row
	for k, w := range d.entries {
		if s, ok := summary(w, now); ok {
			out = append(out, s)
		}
		delete(d.entries, k)
	}
	return out
}

// summary is the event that closes a window: the first event's fields, a new ID
// and time, count = repeats and repeat_of = the first event's ID.
func summary(w *openWindow, now time.Time) (row, bool) {
	if w.repeats == 0 {
		return row{}, false
	}
	s := w.first
	s.ID = newID(now)
	s.At = now.UTC().Format(time.RFC3339Nano)
	s.Details = withCount(w.first.Details, w.repeats, w.first.ID)
	return s, true
}

// rule returns the window and the key of an event, or a zero window for a type
// that is not deduplicated.
//
// authz.denied: per principal, action, repository and path; an anonymous caller
// per client address (one scanner probing many paths collapses into one key).
// token.rejected: per token ID, or per client address when the token is unknown.
func (d *deduper) rule(ev Event) (time.Duration, string) {
	switch ev.Type {
	case typeDenied:
		if ev.Actor.Kind == "anonymous" {
			return d.denial, "d\x00anon\x00" + ev.Source.ClientAddr
		}
		return d.denial, "d\x00" + firstNonEmpty(ev.Actor.ID, ev.Actor.Name) + "\x00" + detailString(ev.Details, "required_action") +
			"\x00" + ev.Repository + "\x00" + ev.Path
	case typeRejected:
		id := firstNonEmpty(ev.Subject.ID, detailString(ev.Details, "token_id"))
		if id == "" {
			return d.rejection, "r\x00addr\x00" + ev.Source.ClientAddr
		}
		return d.rejection, "r\x00" + id
	}
	return 0, ""
}

// detailString reads one string member of a details object; absent or not a
// string gives "".
func detailString(raw json.RawMessage, key string) string {
	if !bytes.Contains(raw, []byte(key)) {
		return ""
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	var s string
	_ = json.Unmarshal(m[key], &s)
	return s
}

// withCount sets details.count, and details.repeat_of when given, keeping the
// other members. A details object that cannot be parsed is replaced by one that
// holds only the counters.
func withCount(raw json.RawMessage, count int, repeatOf string) json.RawMessage {
	m := map[string]json.RawMessage{}
	_ = json.Unmarshal(raw, &m)
	m["count"] = json.RawMessage(itoa(count))
	if repeatOf != "" {
		b, _ := json.Marshal(repeatOf)
		m["repeat_of"] = b
	}
	out, err := json.Marshal(m)
	if err != nil {
		return json.RawMessage(`{"count":` + itoa(count) + `}`)
	}
	return out
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
