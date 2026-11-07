package fixup

import (
	"context"
	"time"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

func init() {
	Register(Fixup{Name: "mirror-state", Order: OrderMirrorState, Apply: resetMirrorState})
}

// resetMirrorState ends the pass that was in progress at the snapshot and makes the first pass due at the restore time; it
// continues from the last complete pass's cursor. A restore without remote caches also forgets that cursor, so the pass is
// full (R-BAK-022, R-REPO-058).
func resetMirrorState(ctx context.Context, tx meta.Tx, env Env) error {
	qs, err := queries(tx)
	if err != nil {
		return err
	}
	_, err = qs.RestoreResetMirrorState(ctx, q.RestoreResetMirrorStateParams{ForgetCursor: env.ExcludeRemoteCaches, Now: env.Now.UTC().Format(time.RFC3339Nano)})
	return err
}
