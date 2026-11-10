package app

import (
	"io"
	"log/slog"
	"testing"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/secrets"
)

// fakePool is a pool that is told apart by its name.
type fakePool struct {
	q.DBTX
	name string
}

type fakeRegistryPools struct{ control, main fakePool }

func (f fakeRegistryPools) ControlPool() fakePool { return f.control }
func (f fakeRegistryPools) Main() fakePool        { return f.main }

// R-ARCH-024 step 6, R-OPS-003 (Q-HA-202): the registry heartbeats on the control connection and deletes its row on the request
// pool. Without the second option the delete would fall back to the control pool and queue behind the change-log poll and the
// readiness checks, and no other test would notice.
func TestR_ARCH_024_RegistryDeletesItsRowOnTheRequestPool(t *testing.T) {
	a := &App{cfg: &config.Config{}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	pools := fakeRegistryPools{control: fakePool{name: "control"}, main: fakePool{name: "main"}}
	o := registryOptionsOn(a, pools, &secrets.KeyRing{})
	if o.DB != q.DBTX(pools.control) {
		t.Error("the heartbeat is not on the control pool")
	}
	if o.ShutdownDB != q.DBTX(pools.main) {
		t.Error("the final deletion is not on the request pool")
	}
}
