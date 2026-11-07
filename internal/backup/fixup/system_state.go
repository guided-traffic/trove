package fixup

import (
	"context"
	"encoding/json"
	"time"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

func init() {
	Register(Fixup{Name: "system-state", Order: OrderSystemState, Apply: rewriteSystemState})
}

// rewriteSystemState gives the restored installation a new Valkey keyspace and a new incarnation and records where it came
// from (R-BAK-022): entries of the old installation, including a shared Valkey's, are never read, and the target marker of
// the old lineage can be adopted by this one only through restored_from (R-BAK-002). installation_id stays.
func rewriteSystemState(ctx context.Context, tx meta.Tx, env Env) error {
	qs, err := queries(tx)
	if err != nil {
		return err
	}
	set := func(key string, v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return qs.RestoreSetSystemState(ctx, q.RestoreSetSystemStateParams{Key: key, Value: string(b)})
	}
	if err := set("keyspace_id", env.NewKeyspaceID); err != nil {
		return err
	}
	if err := set("incarnation", env.NewIncarnation); err != nil {
		return err
	}
	if env.Manifest == nil {
		// A platform restore (trove admin post-restore) has no backup to record.
		return nil
	}
	return set("restored_from", map[string]any{
		"installationId": env.Manifest.InstallationID, "incarnation": env.Manifest.Incarnation,
		"backupId": string(env.Manifest.BackupID), "snapshotAt": env.Manifest.SnapshotAt.UTC().Format(time.RFC3339Nano),
	})
}
