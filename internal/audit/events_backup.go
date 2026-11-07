package audit

// The event types of the backup lane (R-BAK-014, R-OPS-016), written by the backup and restore commands. All are
// transactional and none is a webhook hook. The backup commands write as the actor system:backup, into the source database;
// the restore writes system.restored and system.restore.journal_replayed into the restored database in the transactions that
// finish the restore (R-BAK-021 step 9) and the replay (R-BAK-024). No detail holds a credential, a key or a name used for
// matching.
//
//   - system.backup.completed: a create run ended completed or completed_with_warnings. An interrupted run writes none.
//   - system.backup.failed: a create run ended failed.
//   - system.backup.deleted: a backup was deleted by retention or by trove backup delete.
//   - system.backup.target_adopted: a run adopted a target of an earlier incarnation (R-BAK-002).
//   - system.restored: a restore finished; source is backup (R-BAK-021) or pitr (R-HA-025). journal is replayed, or skipped
//     with --skip-revocation-journal.
//   - system.restore.journal_replayed: the revocation journal was replayed after a restore (R-BAK-024, R-HA-054); the data are
//     the counts per outcome.

// Names of the backup events, for the producers.
const (
	EventBackupCompleted     = "system.backup.completed"
	EventBackupFailed        = "system.backup.failed"
	EventBackupDeleted       = "system.backup.deleted"
	EventBackupTargetAdopted = "system.backup.target_adopted"
	EventRestored            = "system.restored"
	EventJournalReplayed     = "system.restore.journal_replayed"
)

func init() {
	Register(EventType{
		Name: EventBackupCompleted, Category: CategorySystem, Mode: Transactional,
		Schema: `{"type":"object","properties":{` +
			`"backupId":{"type":"string"},"outcome":{"type":"string","enum":["completed","completed_with_warnings"]},` +
			`"databaseBytes":{"type":"integer"},"blobsCopied":{"type":"integer"},"damaged":{"type":"integer"}},` +
			`"required":["backupId","outcome","databaseBytes","blobsCopied","damaged"],"additionalProperties":false}`,
	})
	Register(EventType{
		Name: EventBackupFailed, Category: CategorySystem, Mode: Transactional,
		Schema: `{"type":"object","properties":{"backupId":{"type":"string"},"code":{"type":"string"}},` +
			`"required":["backupId","code"],"additionalProperties":false}`,
	})
	Register(EventType{
		Name: EventBackupDeleted, Category: CategorySystem, Mode: Transactional,
		Schema: `{"type":"object","properties":{"backupId":{"type":"string"},"reason":{"type":"string","enum":["retention","manual"]}},` +
			`"required":["backupId","reason"],"additionalProperties":false}`,
	})
	Register(EventType{
		Name: EventBackupTargetAdopted, Category: CategorySystem, Mode: Transactional,
		Schema: `{"type":"object","properties":{"previousIncarnation":{"type":"string"}},` +
			`"required":["previousIncarnation"],"additionalProperties":false}`,
	})
	// backupId is empty for a point-in-time restore; t is the time the platform restore went to.
	Register(EventType{
		Name: EventRestored, Category: CategorySystem, Mode: Transactional,
		Schema: `{"type":"object","properties":{` +
			`"source":{"type":"string","enum":["backup","pitr"]},"backupId":{"type":"string"},"snapshotAt":{"type":"string","format":"date-time"},` +
			`"journal":{"type":"string","enum":["replayed","skipped"]}},` +
			`"required":["source","journal"],"additionalProperties":false}`,
	})
	Register(EventType{
		Name: EventJournalReplayed, Category: CategorySystem, Mode: Transactional,
		Schema: `{"type":"object","properties":{` +
			`"applied":{"type":"integer"},"alreadyInEffect":{"type":"integer"},"subjectUnknown":{"type":"integer"},` +
			`"skipped":{"type":"integer"},"suspended":{"type":"integer"},"revived":{"type":"integer"},"unreadable":{"type":"integer"}},` +
			`"required":["applied","alreadyInEffect","subjectUnknown","skipped","suspended","revived"],"additionalProperties":false}`,
	})
}
