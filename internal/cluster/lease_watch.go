package cluster

import (
	"time"

	"github.com/guided-traffic/trove/internal/observability/health"
)

// Watchdog is the part of the liveness registry that the loops of this package
// and of the jobs package use (R-ARCH-016). *health.Health implements it.
type Watchdog interface {
	Loop(name string, max time.Duration) *health.Loop
}

// WatchedLoop is a loop registered with the watchdog. A nil *WatchedLoop is
// valid and does nothing, for callers without a registry.
type WatchedLoop struct{ l *health.Loop }

// WatchLoop registers a loop under the name the stuck list of /healthz shows.
// max is health.watchdog_timeout. A nil watchdog gives a no-op loop.
func WatchLoop(w Watchdog, name string, max time.Duration) *WatchedLoop {
	if w == nil {
		return nil
	}
	return &WatchedLoop{l: w.Loop(name, max)}
}

// Beat records that an iteration finished, failed ones included.
func (w *WatchedLoop) Beat() {
	if w != nil {
		w.l.Beat()
	}
}

// Stop leaves the watchdog; a loop that ends on purpose calls it first
// (R-ARCH-024).
func (w *WatchedLoop) Stop() {
	if w != nil {
		w.l.Stop()
	}
}

// callTimeout is the deadline of every call a watched loop makes: shorter than
// the watchdog bound, so a blackholed connection yields failed iterations, not
// a stuck loop (R-ARCH-016).
func callTimeout(watchdog time.Duration) time.Duration {
	if watchdog <= 0 {
		return 10 * time.Second
	}
	return min(watchdog/2, 10*time.Second)
}

// CallTimeout is callTimeout for other packages' watched loops.
func CallTimeout(watchdog time.Duration) time.Duration { return callTimeout(watchdog) }
