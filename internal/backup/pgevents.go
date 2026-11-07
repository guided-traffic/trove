package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// PGEvents writes the audit events of the backup lane (R-BAK-014) through backup_record_event(), a function of the schema owner
// that writes system.backup.completed, failed, deleted and target_adopted as the actor system:backup and nothing else
// (Q-BAK-50). The backup login holds EXECUTE on it and no INSERT on the audit trail, so a compromised backup pod cannot forge
// another event. The audit writer's log copy is replaced by a log line here.
type PGEvents struct {
	db      DB
	replica string
	log     *slog.Logger
}

// NewPGEvents builds the writer; replica is the pod name, stored in the replica column.
func NewPGEvents(db DB, replica string, log *slog.Logger) *PGEvents {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &PGEvents{db: db, replica: replica, log: log}
}

// Record writes one event of the lane in a transaction of its own.
func (e *PGEvents) Record(ctx context.Context, ev audit.Event) error {
	details := ev.Details
	if len(details) == 0 {
		details = json.RawMessage(`{}`)
	}
	outcome := string(ev.Outcome)
	if outcome == "" {
		outcome = string(audit.Success)
	}
	err := e.db.Read(ctx, func(c q.DBTX) error {
		return q.New(c).RecordBackupEvent(ctx, q.RecordBackupEventParams{
			Action: ev.Type, Outcome: outcome, Replica: e.replica, Reason: ev.Reason, Details: string(details),
		})
	})
	if err != nil {
		return fmt.Errorf("record %s: %w", ev.Type, err)
	}
	e.log.Info("audit", "event", ev.Type, "outcome", outcome, "actor", "system:backup", "details", string(details))
	return nil
}

// BackupDeleted writes system.backup.deleted (R-BAK-016); it implements the audit port of the retention stage.
func (e *PGEvents) BackupDeleted(ctx context.Context, id format.ID, reason string) error {
	details, err := json.Marshal(map[string]string{"backupId": string(id), "reason": reason})
	if err != nil {
		return err
	}
	return e.Record(ctx, audit.Event{Type: audit.EventBackupDeleted, Outcome: audit.Success, Details: details})
}
