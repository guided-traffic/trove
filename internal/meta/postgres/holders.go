package postgres

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// holders records who has a connection of a pool checked out, so that a close that cannot finish names the borrowers instead of
// only counting them (R-HA-011, R-ARCH-024 step 6). The pool tells the tracer about every acquire and every release, whoever
// borrows (an explicit Acquire, a statement run on the pool, a transaction), so no call site is wrapped. A borrower may name itself
// with [WithHolder]; any other is named by the first function of the call stack outside the driver and this package, read from the
// recorded program counters only when the close reports.
type holders struct {
	m sync.Map // *pgx.Conn -> hold
}

type hold struct {
	label string
	pcs   [12]uintptr
	n     int
	since time.Time
}

type holderKey struct{}

// WithHolder names the borrower of the connections acquired with the returned context; the name appears in the error of a close
// that finds a connection still held.
func WithHolder(ctx context.Context, label string) context.Context {
	return context.WithValue(ctx, holderKey{}, label)
}

func (h *holders) acquired(ctx context.Context, c *pgx.Conn) {
	if h == nil || c == nil {
		return
	}
	v := hold{since: time.Now()}
	if l, ok := ctx.Value(holderKey{}).(string); ok {
		v.label = l
	} else {
		v.n = runtime.Callers(2, v.pcs[:])
	}
	h.m.Store(c, v)
}

func (h *holders) released(c *pgx.Conn) {
	if h != nil && c != nil {
		h.m.Delete(c)
	}
}

// describe lists the connections still checked out, longest held first: "label for 8.7s".
func (h *holders) describe(now time.Time) []string {
	if h == nil {
		return nil
	}
	type row struct {
		text string
		for_ time.Duration
	}
	var rows []row
	h.m.Range(func(_, v any) bool {
		e := v.(hold)
		rows = append(rows, row{text: e.name(), for_: now.Sub(e.since)})
		return true
	})
	sort.Slice(rows, func(i, j int) bool { return rows[i].for_ > rows[j].for_ })
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = fmt.Sprintf("%s for %s", r.text, r.for_.Round(10*time.Millisecond))
	}
	return out
}

// name is the label, or the first function of the stack that is neither the driver nor this package.
func (e hold) name() string {
	if e.label != "" {
		return e.label
	}
	frames := runtime.CallersFrames(e.pcs[:e.n])
	for {
		f, more := frames.Next()
		fn := f.Function
		if fn != "" && !strings.HasPrefix(fn, "github.com/jackc/") && !strings.HasPrefix(fn, "github.com/guided-traffic/trove/internal/meta/postgres.") &&
			!strings.HasPrefix(fn, "runtime.") {
			return strings.TrimPrefix(fn, "github.com/guided-traffic/trove/internal/")
		}
		if !more {
			return "unknown borrower"
		}
	}
}
