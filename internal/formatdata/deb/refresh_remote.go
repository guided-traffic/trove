package deb

import (
	"context"

	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
)

// Refresh is the remote part of the periodic task deb-index-refresh (R-DEB-021, Q-DEB-33): it enqueues the deb-index job of every
// remote repository that has replaced releases past deb.by_hash_keep, under the key index:<repository id> that a reindex and a key
// change use, and so coalesces with them. It deletes nothing itself. The composition root registers it with [RegisterRefreshHook].
//
// The re-verification after a change of the keys is not periodic: the update that removes the fingerprints enqueues the job in its own
// transaction (R-DEB-020), and the queue retries it.
func (j *RemoteJobs) Refresh(ctx context.Context, _ *jobs.Run) error {
	var due []int64
	if err := j.Store.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		due, err = qs.DebRemoteHousekeepingDue(ctx, stamp(j.now().Add(-j.ByHashKeep)))
		return err
	}); err != nil {
		return err
	}
	for _, id := range due {
		if err := j.Store.o.DB.InTx(ctx, func(tx meta.Tx) error { return j.Store.EnqueueBuild(ctx, tx, id, 0) }); err != nil {
			return err
		}
	}
	return nil
}
