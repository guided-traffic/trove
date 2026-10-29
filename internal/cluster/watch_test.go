package cluster

import (
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
)

func TestCallTimeouts(t *testing.T) {
	t.Run("R-ARCH-016 every call of a watched loop carries a deadline below the watchdog bound", func(t *testing.T) {
		for _, wd := range []time.Duration{time.Second, 4 * time.Second, 2 * time.Minute, time.Hour} {
			d := CallTimeout(wd)
			if d <= 0 || d >= wd {
				t.Errorf("watchdog %s: call timeout %s", wd, d)
			}
		}
		c, _ := NewChanges(ChangesOptions{DB: nopDB{}, WatchdogTimeout: 2 * time.Second, Config: config.Cluster{}})
		if c.pollTimeout() >= 2*time.Second {
			t.Errorf("the change-log poll's deadline %s is not below the watchdog bound", c.pollTimeout())
		}
	})
	t.Run("R-ARCH-016 a nil watchdog gives loops that do nothing", func(t *testing.T) {
		l := WatchLoop(nil, "x", time.Second)
		l.Beat()
		l.Stop()
	})
}
