package retention

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/target"
)

// CheckMarker reads the target marker and compares it with the installation ID and incarnation of the source
// (R-BAK-002). Every delete run, and the pruning phase of a create run, does it before it deletes anything. A target without
// a marker has never been written by a trove installation, so there is nothing to delete and the run exits 2; a marker of
// another installation or incarnation is [format.ErrForeignTarget] (exit 4).
func CheckMarker(ctx context.Context, store target.Store, l format.Layout, facts backup.Facts) error {
	sf, err := facts.SystemFacts(ctx)
	if err != nil {
		return fmt.Errorf("reading the installation facts: %w", err)
	}
	rc, _, err := store.Get(ctx, l.Marker())
	if errors.Is(err, target.ErrNotFound) {
		return backup.Failf(backup.ExitUsage, backup.CodeConfig, "the target has no marker (%s): no installation has written backups here", l.Marker())
	}
	if err != nil {
		return fmt.Errorf("reading the target marker: %w", err)
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, 1<<20))
	if err != nil {
		return fmt.Errorf("reading the target marker: %w", err)
	}
	m, err := format.DecodeMarker(raw)
	if err != nil {
		return err
	}
	return m.Check(sf.InstallationID, sf.Incarnation)
}

// RunSweepClock answers [SweepClock] from the recorded results of the newest create and delete runs: the newest poolSweptAt
// among them. Runs that did not sweep record none, so a few runs are read.
type RunSweepClock struct {
	Runs interface {
		List(ctx context.Context, f backup.ListFilter) ([]backup.Run, error)
	}
	// Depth is how many runs of each operation are read; the default is 50.
	Depth int
}

// LastSweep implements [SweepClock].
func (c RunSweepClock) LastSweep(ctx context.Context) (time.Time, bool, error) {
	depth := c.Depth
	if depth <= 0 {
		depth = 50
	}
	var last time.Time
	found := false
	for _, op := range []backup.Operation{backup.OpCreate, backup.OpDelete} {
		runs, err := c.Runs.List(ctx, backup.ListFilter{Operation: string(op), Limit: depth})
		if err != nil {
			return time.Time{}, false, err
		}
		for _, r := range runs {
			if !r.State.Succeeded() || len(r.Result) == 0 {
				continue
			}
			var res struct {
				At *time.Time `json:"poolSweptAt"`
			}
			if json.Unmarshal(r.Result, &res) != nil || res.At == nil {
				continue
			}
			if !found || res.At.After(last) {
				last, found = *res.At, true
			}
		}
	}
	return last, found, nil
}
