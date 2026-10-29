package cluster

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/config"
)

// stateListener reads AreaState from inside its callbacks, as an owner that
// compares an index label with the applied epoch does (R-AUTHZ-028).
type stateListener struct {
	c     *Changes
	seen  chan [2]int64
	flush chan string
}

func (l *stateListener) ApplyChange(area string, _ int, _ json.RawMessage) {
	e, _ := l.c.AreaState(area)
	l.seen <- [2]int64{e, 0}
}

func (l *stateListener) FlushArea(area, reason string) {
	e, _ := l.c.AreaState(area)
	l.seen <- [2]int64{e, 1}
	l.flush <- reason
}

func TestPollListenerMayReadAreaState(t *testing.T) {
	t.Run("R-ARCH-018 a listener called by the poll can read AreaState without deadlock", func(t *testing.T) {
		c, err := NewChanges(ChangesOptions{DB: nopDB{}, Config: config.Cluster{EpochPollInterval: time.Second, EpochMaxStaleness: 20 * time.Second}, Metrics: prometheus.NewRegistry()})
		if err != nil {
			t.Fatal(err)
		}
		l := &stateListener{c: c, seen: make(chan [2]int64, 4), flush: make(chan string, 4)}
		c.AddListener(l)
		st := c.areas[AreaCache]
		at := time.Now().UTC()
		st.known = true
		st.set(5, at)

		done := make(chan struct{})
		go func() {
			defer close(done)
			// The database reports a lower epoch: the area is flushed, which calls the listener.
			if err := c.reconcile(context.Background(), nil, st, 2, at.Add(time.Second)); err != nil {
				t.Errorf("reconcile: %v", err)
			}
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("the poll deadlocked: a listener's AreaState waited for the lock the poll holds")
		}
		if got := <-l.flush; got != FlushEpochRegressed {
			t.Fatalf("flush reason = %q", got)
		}
		if e, _ := c.AreaState(AreaCache); e != 2 {
			t.Fatalf("applied epoch after the flush = %d, want 2", e)
		}
	})
}
