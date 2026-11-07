import { Provider } from '@angular/core';
import {
  BackupRun,
  BackupRunFilters,
  BackupsApi,
  BackupSummary,
} from '../backups-api';
import { Page, PageFetcher } from '../pagination';
import { ApiError } from '../problem';

/*
 * In-memory fake of the backup API (R-API-050, R-UI-047 component tier). It filters the runs by operation and state, pages them
 * with a numeric cursor, and answers a run or the summary from fields a spec sets. Like the real API it has no write method.
 */

export const RUN_ACTIVE = '01933c4e-2f10-7b3a-9c1d-5e8f7a6b4c21';
export const RUN_DONE = '01933a1b-8c22-7e41-b0f3-2a6d9e4c7b10';
export const RUN_INTERRUPTED = '01933b00-1111-7e41-b0f3-2a6d9e4c7b11';
export const RUN_FAILED = '01933b00-2222-7e41-b0f3-2a6d9e4c7b12';

export function sampleRun(over: Partial<BackupRun> = {}): BackupRun {
  return {
    id: RUN_DONE,
    operation: 'create',
    backupId: '20261027T020000Z-1c9e',
    state: 'completed',
    trigger: 'schedule',
    pod: 'trove-backup-29885880-x7k2q',
    troveVersion: '0.4.0',
    target: 's3://trove-backups/trove',
    startedAt: '2026-10-27T02:00:04.112Z',
    heartbeatAt: '2026-10-27T02:47:30.870Z',
    leaseExpired: false,
    finishedAt: '2026-10-27T02:47:31.009Z',
    snapshotAt: '2026-10-27T02:00:08.913Z',
    progress: {
      phase: 'pruning',
      tablesDone: 58,
      tablesTotal: 58,
      blobsToCopy: 1204,
      blobsCopied: 1204,
      bytesCopied: 9_123_456_789,
    },
    result: {
      backupId: '20261027T020000Z-1c9e',
      databaseBytes: 183_500_800,
      blobsCopied: 1204,
      warnings: [],
    },
    error: null,
    ...over,
  };
}

export function sampleSummary(over: Partial<BackupSummary> = {}): BackupSummary {
  return {
    expectedIntervalSeconds: 86_400,
    lastSuccessAt: '2026-10-27T02:47:31.009Z',
    overdue: false,
    lastFailure: null,
    active: null,
    target: 's3://trove-backups/trove',
    retention: { keepLast: 7, keepWithinSeconds: 0 },
    storedBackups: {
      asOf: '2026-10-27T02:47:31.009Z',
      runId: RUN_DONE,
      items: [
        {
          backupId: '20261027T020000Z-1c9e',
          snapshotAt: '2026-10-27T02:00:08.913Z',
          troveVersion: '0.4.0',
          schemaStep: 42,
          databaseBytes: 183_500_800,
          blobCount: 51_234,
          blobBytes: 412_316_860_416,
          damaged: false,
        },
      ],
    },
    restoredFrom: null,
    manualRun: { namespace: 'trove', cronJob: 'trove-backup' },
    ...over,
  };
}

export class FakeBackupsApi extends BackupsApi {
  runList: BackupRun[] = [
    sampleRun(),
    sampleRun({
      id: RUN_INTERRUPTED,
      state: 'interrupted',
      backupId: null,
      snapshotAt: null,
      error: { code: 'interrupted' },
      result: null,
      startedAt: '2026-10-26T02:00:04.112Z',
      finishedAt: '2026-10-26T02:10:00.000Z',
    }),
  ];
  summaryResult: BackupSummary = sampleSummary();
  /** Set to make the next calls fail with this error. */
  failure: ApiError | null = null;
  filterCalls: BackupRunFilters[] = [];
  summaryCalls = 0;

  override runs(filters: BackupRunFilters): PageFetcher<BackupRun> {
    this.filterCalls.push(filters);
    return async (cursor, limit): Promise<Page<BackupRun>> => {
      if (this.failure) throw this.failure;
      const all = this.runList
        .filter((r) => !filters.operation || r.operation === filters.operation)
        .filter((r) => !filters.state || r.state === filters.state);
      const start = cursor === null ? 0 : Number(cursor);
      const end = start + limit;
      return { items: all.slice(start, end), nextCursor: end < all.length ? String(end) : null };
    };
  }

  override async run(id: string): Promise<BackupRun> {
    if (this.failure) throw this.failure;
    const found = this.runList.find((r) => r.id === id);
    if (!found) {
      throw new ApiError(404, { code: 'not_found', detail: 'not found', requestId: 'rid-404' }, 'rid-404');
    }
    return found;
  }

  override async summary(): Promise<BackupSummary> {
    this.summaryCalls++;
    if (this.failure) throw this.failure;
    return this.summaryResult;
  }
}

export function provideBackupFakes(): Provider[] {
  return [{ provide: BackupsApi, useClass: FakeBackupsApi }];
}
