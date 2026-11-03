package guard

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/guided-traffic/trove/internal/bastion/shared"
)

type lineWriter struct {
	mu    sync.Mutex
	lines *[]string
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	*w.lines = append(*w.lines, strings.TrimSpace(string(p)))
	return len(p), nil
}

// captureLog returns a logger that appends each line, in text form, to lines.
func captureLog(lines *[]string) *slog.Logger {
	return slog.New(slog.NewTextHandler(&lineWriter{lines: lines}, nil))
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// value reads a counter or a gauge.
func value(m interface{ Write(*dto.Metric) error }) float64 {
	var out dto.Metric
	if err := m.Write(&out); err != nil {
		panic(err)
	}
	if out.Counter != nil {
		return out.Counter.GetValue()
	}
	return out.Gauge.GetValue()
}

// failing is a limiter whose every call fails.
type failing struct{}

var _ shared.Limiter = failing{}

var errBoom = errors.New("boom")

func (failing) Allow(context.Context, string, string, shared.Rate) (bool, time.Duration, error) {
	return false, 0, errBoom
}

func (failing) Add(context.Context, string, string, time.Duration) (int, error) { return 0, errBoom }

func (failing) Count(context.Context, string, string, time.Duration) (int, error) { return 0, errBoom }

func (failing) Replicas() int { return 1 }
