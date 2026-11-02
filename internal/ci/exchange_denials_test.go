package ci

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/ci/reason"
)

// clock is a settable clock for the aggregation buffer.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }

func denial(addr string, r Reason) audit.Event {
	return audit.Event{Type: audit.EventCIExchangeDenied, Reason: string(r), Source: audit.Source{ClientAddr: addr}, Details: json.RawMessage(`{"iss":"x"}`)}
}

func countOf(t *testing.T, ev audit.Event) int64 {
	t.Helper()
	var d struct {
		Count int64 `json:"count"`
	}
	_ = json.Unmarshal(ev.Details, &d)
	return d.Count
}

func TestR_CI_021_DenialsAreAggregatedPerAddressReasonAndMinute(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 11, 2, 12, 0, 10, 0, time.UTC)

	t.Run("R-CI-021 the first denial of an address and reason is written at once, the others of the minute as one event with their count", func(t *testing.T) {
		rec, clk := &recorder{}, &clock{t: start}
		d := newDenials(rec, clk.now)
		defer d.stop()
		for range 5 {
			d.record(ctx, "gitlab-main", reason.SignatureInvalid, denial("192.0.2.1", reason.SignatureInvalid))
		}
		d.record(ctx, "", reason.SubjectTokenMalformed, denial("192.0.2.1", reason.SubjectTokenMalformed))
		d.record(ctx, "", reason.SignatureInvalid, denial("192.0.2.2", reason.SignatureInvalid))
		if n := len(rec.of(audit.EventCIExchangeDenied)); n != 3 {
			t.Fatalf("%d events at once, want one per address and reason", n)
		}
		clk.set(start.Add(51 * time.Second)) // the minute has ended
		d.tick()
		got := rec.of(audit.EventCIExchangeDenied)
		if len(got) != 4 {
			t.Fatalf("%d events after the minute, want one summary more", len(got))
		}
		sum := got[3]
		if countOf(t, sum) != 4 || sum.Source.ClientAddr != "192.0.2.1" || sum.Reason != string(reason.SignatureInvalid) {
			t.Errorf("the summary %+v %s", sum, sum.Details)
		}
		if strings.Contains(string(sum.Details), `"iss"`) {
			t.Errorf("a summary records what only the first denial may: %s", sum.Details)
		}
		// The next minute starts over: its first denial is written at once.
		d.record(ctx, "gitlab-main", reason.SignatureInvalid, denial("192.0.2.1", reason.SignatureInvalid))
		if n := len(rec.of(audit.EventCIExchangeDenied)); n != 5 {
			t.Errorf("%d events, the new minute's first denial is missing", n)
		}
	})

	t.Run("R-CI-021 the counts left at shutdown are written by the flush, and a later denial at once", func(t *testing.T) {
		rec, clk := &recorder{}, &clock{t: start}
		d := newDenials(rec, clk.now)
		for range 3 {
			d.record(ctx, "", reason.RateLimited, denial("192.0.2.3", reason.RateLimited))
		}
		d.stop()
		got := rec.of(audit.EventCIExchangeDenied)
		if len(got) != 2 || countOf(t, got[1]) != 2 {
			t.Fatalf("events %v", got)
		}
		d.record(ctx, "", reason.RateLimited, denial("192.0.2.3", reason.RateLimited))
		if len(rec.of(audit.EventCIExchangeDenied)) != 3 {
			t.Error("a denial after the flush was not written")
		}
	})

	t.Run("R-ARCH-032 the buffer is bounded: addresses beyond it are counted under the reason alone", func(t *testing.T) {
		rec, clk := &recorder{}, &clock{t: start}
		d := newDenials(rec, clk.now)
		defer d.stop()
		for i := range maxDenialKeys + 50 {
			d.record(ctx, "", reason.SubjectTokenMalformed, denial(fmt.Sprintf("2001:db8::%x", i), reason.SubjectTokenMalformed))
		}
		d.mu.Lock()
		n := len(d.entries)
		d.mu.Unlock()
		if n > maxDenialKeys+1 {
			t.Errorf("%d entries, bound %d plus the overflow", n, maxDenialKeys)
		}
		if got := len(rec.of(audit.EventCIExchangeDenied)); got != maxDenialKeys+1 {
			t.Errorf("%d events at once, want one per held address and one for the overflow", got)
		}
	})
}
