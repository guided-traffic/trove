import { Injectable, inject } from '@angular/core';
import { ApiClient } from './client';
import { PageFetcher } from './pagination';
import { cleanQuery, seg } from './query';
import { Schema } from './types';

/*
 * The backup job as the management API shows it (R-API-050): its runs, its summary and the stored backups the newest listing run
 * found. Read-only by design: no operation starts, deletes, verifies or restores a backup (Q-API-10), so this service has no
 * method that is not a GET.
 */

export type BackupRun = Schema<'BackupRun'>;
export type BackupSummary = Schema<'BackupSummary'>;
export type StoredBackup = Schema<'StoredBackup'>;
export type StoredBackups = Schema<'StoredBackups'>;
export type BackupProgress = Schema<'BackupProgress'>;

/** The operations of a run (`backup_run.operation`, R-BAK-009). */
export const BACKUP_OPERATIONS = ['create', 'delete', 'verify'] as const;

/** The states of a run (R-BAK-009), in the order of its life. */
export const BACKUP_STATES = [
  'starting',
  'snapshot',
  'copying',
  'finalizing',
  'pruning',
  'completed',
  'completed_with_warnings',
  'failed',
  'interrupted',
  'abandoned',
] as const;

/** The states in which a run has not ended (R-BAK-010). */
export const BACKUP_ACTIVE_STATES: readonly string[] = [
  'starting',
  'snapshot',
  'copying',
  'finalizing',
  'pruning',
];

export interface BackupRunFilters {
  readonly operation?: string;
  readonly state?: string;
}

export abstract class BackupsApi {
  abstract runs(filters: BackupRunFilters): PageFetcher<BackupRun>;
  abstract run(id: string): Promise<BackupRun>;
  abstract summary(): Promise<BackupSummary>;
}

@Injectable()
export class HttpBackupsApi extends BackupsApi {
  private readonly client = inject(ApiClient);

  runs(filters: BackupRunFilters): PageFetcher<BackupRun> {
    return this.client.page<BackupRun>('/api/v1/system/backups', cleanQuery(filters));
  }

  async run(id: string): Promise<BackupRun> {
    return (await this.client.getJson<BackupRun>(`/api/v1/system/backups/${seg(id)}`)).body;
  }

  async summary(): Promise<BackupSummary> {
    return (await this.client.get('/api/v1/system/backups/summary')).body;
  }
}
