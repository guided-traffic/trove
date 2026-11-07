/**
 * Translated messages of the failure codes of the backup job (R-UI-042, R-UI-048, R-BAK-009, R-BAK-013): the `code` of a run's
 * stored `error`, which the backups screen shows for a failed, interrupted or abandoned run and for the last failure. They are
 * the codes of `internal/backup/exit.go`; they never arrive as the code of an API answer, so `describeProblem` does not
 * consult this table. Returns null for a code it does not know.
 */
export function describeBackupProblem(code: string | null): string | null {
  switch (code) {
    case 'hold_lost':
      return $localize`:@@problem.backup.hold-lost:The hold that keeps garbage collection from deleting the blobs was lost, and a blob that still had to be copied left the source.`;
    case 'superseded':
      return $localize`:@@problem.backup.superseded:Another run took over this one after its lease had passed.`;
    case 'pool_damaged':
      return $localize`:@@problem.backup.pool-damaged:A blob of an earlier backup is missing in the target. Run trove backup verify.`;
    case 'invariant_violation':
      return $localize`:@@problem.backup.invariant-violation:The exported data breaks a rule of trove's schema, so no backup was written.`;
    case 'stalled':
      return $localize`:@@problem.backup.stalled:The run made no progress for the stall timeout and was stopped.`;
    case 'lease_expired':
      return $localize`:@@problem.backup.lease-expired:The run stopped without finishing, and a later run marked it abandoned.`;
    case 'config':
      return $localize`:@@problem.backup.config:The backup configuration is not valid.`;
    case 'busy':
      return $localize`:@@problem.backup.busy:Another backup run was writing to the target.`;
    case 'foreign_target':
      return $localize`:@@problem.backup.foreign-target:The target belongs to another installation or incarnation.`;
    case 'findings':
      return $localize`:@@problem.backup.findings:The verification found problems in the stored backups.`;
    case 'precondition':
      return $localize`:@@problem.backup.precondition:A precondition of the operation does not hold.`;
    case 'temporary':
      return $localize`:@@problem.backup.temporary:The database or the target was not reachable. The Job retries the run.`;
    case 'restore_in_progress':
      return $localize`:@@problem.backup.restore-in-progress:A restore is in progress.`;
    case 'interrupted':
      return $localize`:@@problem.backup.interrupted:Interrupted by a drain or eviction. The Job retries the run.`;
    case 'internal':
      return $localize`:@@problem.backup.internal:An internal error stopped the run. The log of its pod has the details.`;
    case 'deletion_not_allowed':
      return $localize`:@@problem.backup.deletion-not-allowed:The deletion was refused: the only complete backup is not deleted without saying so.`;
    case 'key_ring_mismatch':
      return $localize`:@@problem.backup.key-ring-mismatch:The key files of this installation do not match the key IDs of the backup.`;
    case 'journal_unavailable':
      return $localize`:@@problem.backup.journal-unavailable:The revocation journal could not be read or written.`;
    case 'format_unknown':
      return $localize`:@@problem.backup.format-unknown:The backup has a format this version does not know.`;
    case 'schema_step_mismatch':
      return $localize`:@@problem.backup.schema-step-mismatch:The schema step of the backup is newer than this version supports.`;
    case 'database_not_empty':
      return $localize`:@@problem.backup.database-not-empty:The database is not empty, so nothing was restored into it.`;
    case 'blob_prefix_not_empty':
      return $localize`:@@problem.backup.blob-prefix-not-empty:The blob bucket or prefix is not empty, so nothing was restored into it.`;
    case 'owner_privilege_missing':
      return $localize`:@@problem.backup.owner-privilege-missing:The database role of the backup does not own the tables it has to export.`;
    case 'backup_grant_missing':
      return $localize`:@@problem.backup.grant-missing:The database role of the backup lacks a grant it needs.`;
    case 'target_same_bucket':
      return $localize`:@@problem.backup.target-same-bucket:The backup target is the bucket that holds the blobs. Use a separate bucket.`;
    case 'encryption_mismatch':
      return $localize`:@@problem.backup.encryption-mismatch:The encryption of the target does not match the configuration.`;
    case 'database_in_recovery':
      return $localize`:@@problem.backup.database-in-recovery:The database is a standby in recovery and cannot be exported.`;
    case 'migration_wait':
      return $localize`:@@problem.backup.migration-wait:A schema migration was running longer than the run waits for it. The Job retries the run.`;
    case 'table_lock_timeout':
      return $localize`:@@problem.backup.table-lock-timeout:A table stayed locked for longer than the run waits for it. The Job retries the run.`;
    case 'catalog_changed':
      return $localize`:@@problem.backup.catalog-changed:The schema changed while the run exported it.`;
    case 'missing_pool_object':
      return $localize`:@@problem.backup.missing-pool-object:A blob that the backup lists is missing in the target.`;
    default:
      return null;
  }
}
