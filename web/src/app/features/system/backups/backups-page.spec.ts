import { provideHttpClient, withInterceptors } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { BackupsApi, HttpBackupsApi } from '../../../api/backups-api';
import { ApiError } from '../../../api/problem';
import {
  FakeBackupsApi,
  RUN_ACTIVE,
  RUN_FAILED,
  sampleRun,
  sampleSummary,
} from '../../../api/testing/fakes-backups';
import { problemInterceptor } from '../../../core/http/interceptors';
import { describeBackupProblem } from '../../../core/http/problem-messages-backup';
import { SessionStore } from '../../../core/session-store';
import { button, expectAccessible, renderScreen, settle, text } from '../../../testing/screen';
import { REFRESH_INTERVAL_MS } from '../system-status';
import { BackupsPage } from './backups-page';
import { JOB_NAME_MAX, durationText, manualRunCommand, stateView } from './backups-model';

function fake(get: <A>(token: abstract new (...args: never[]) => A) => A): FakeBackupsApi {
  return get(BackupsApi) as FakeBackupsApi;
}

describe('The backups screen: status (R-UI-048)', () => {
  afterEach(() => vi.useRealTimers());

  it('R-UI-048 shows the last success with its age, the expected interval and no overdue mark', async () => {
    const { root } = await renderScreen(BackupsPage);
    const card = root.querySelector('section[aria-labelledby="bk-status"]')!;
    expect(text(card)).toContain('Last success');
    expect(text(card)).toMatch(/ago/);
    expect(text(card)).toContain('Expected interval1 d');
    expect(text(card)).not.toContain('Overdue');
    expect(text(card)).not.toContain('Last failure');
  });

  it('R-UI-048 marks a last success older than the rule allows as overdue', async () => {
    const { root } = await renderScreen(BackupsPage, {
      setup: (get) => (fake(get).summaryResult = sampleSummary({ overdue: true })),
    });
    expect(text(root.querySelector('section[aria-labelledby="bk-status"]')!)).toContain('Overdue');
  });

  it('R-UI-048 says "No backup yet" as a warning when no run has succeeded', async () => {
    const { root } = await renderScreen(BackupsPage, {
      setup: (get) =>
        (fake(get).summaryResult = sampleSummary({
          lastSuccessAt: null,
          overdue: null,
          storedBackups: null,
          retention: null,
          target: null,
        })),
    });
    const badge = root.querySelector('section[aria-labelledby="bk-status"] .badge.warn')!;
    expect(text(badge)).toBe('warningNo backup yet');
    expect(text(root.querySelector('section[aria-labelledby="bk-stored"]')!)).toContain(
      'No run has listed the target yet.',
    );
  });

  it('R-UI-048 R-UI-042 shows the last failure with the translated code', async () => {
    const { root } = await renderScreen(BackupsPage, {
      setup: (get) =>
        (fake(get).summaryResult = sampleSummary({
          lastFailure: { runId: RUN_FAILED, at: '2026-10-28T02:10:00Z', code: 'pool_damaged' },
        })),
    });
    const t = text(root.querySelector('section[aria-labelledby="bk-status"]')!);
    expect(t).toContain('Last failure');
    expect(t).toContain('A blob of an earlier backup is missing in the target. Run trove backup verify.');
    expect(t).toContain('pool_damaged');
  });

  it('R-UI-048 shows the phase and the growing blob count of a run in progress, and refreshes without a reload', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
    const running = sampleRun({
      id: RUN_ACTIVE,
      state: 'copying',
      finishedAt: null,
      result: null,
      progress: { phase: 'copying', tablesDone: 41, tablesTotal: 58, blobsToCopy: 1204, blobsCopied: 830, bytesCopied: 1_048_576 },
    });
    const { root, fixture, get } = await renderScreen(BackupsPage, {
      setup: (get) => {
        const api = fake(get);
        api.runList = [running, ...api.runList];
        api.summaryResult = sampleSummary({
          active: { id: RUN_ACTIVE, operation: 'create', state: 'copying', startedAt: running.startedAt, leaseExpired: false },
        });
      },
    });
    const card = root.querySelector('section[aria-labelledby="bk-active"]')!;
    expect(text(card)).toContain('Copying');
    expect(text(card)).toContain('Phasecopying');
    expect(text(card)).toContain('Tables exported41 / 58');
    expect(text(card)).toContain('830 / 1204');
    expect(text(card)).toContain('1.0 MiB');
    expect(text(card)).toContain(running.pod);

    const api = fake(get);
    api.runList[0] = { ...running, progress: { ...running.progress, blobsCopied: 900 } };
    vi.advanceTimersByTime(REFRESH_INTERVAL_MS);
    await settle(fixture, 10);
    expect(text(root.querySelector('section[aria-labelledby="bk-active"]')!)).toContain('900 / 1204');
  });

  it('R-UI-048 shows a run whose lease has passed as stopped, with what happens next', async () => {
    const { root } = await renderScreen(BackupsPage, {
      setup: (get) => {
        const api = fake(get);
        api.runList = [sampleRun({ id: RUN_ACTIVE, state: 'copying', leaseExpired: true, finishedAt: null, result: null })];
        api.summaryResult = sampleSummary({
          active: { id: RUN_ACTIVE, operation: 'create', state: 'copying', startedAt: '2026-10-28T02:00:04Z', leaseExpired: true },
        });
      },
    });
    const card = root.querySelector('section[aria-labelledby="bk-active"]')!;
    expect(text(card)).toContain('Stopped');
    expect(text(card)).toContain('Stopped without finishing; the next run marks it abandoned.');
    expect(card.querySelector('.badge.warn')).toBeTruthy();
  });

  it('R-UI-048 pauses the automatic refresh and refreshes on request', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
    const { root, fixture, get } = await renderScreen(BackupsPage);
    const api = fake(get);
    const before = api.summaryCalls;
    button(root, 'Pause automatic refresh').click();
    await settle(fixture);
    expect(button(root, 'Resume automatic refresh').getAttribute('aria-pressed')).toBe('true');
    vi.advanceTimersByTime(REFRESH_INTERVAL_MS * 3);
    await settle(fixture);
    expect(api.summaryCalls).toBe(before);
    button(root, 'Refresh now').click();
    await settle(fixture);
    expect(api.summaryCalls).toBe(before + 1);
  });

  it('R-UI-048 shows a failed load with its translated message', async () => {
    const { root } = await renderScreen(BackupsPage, {
      setup: (get) => (fake(get).failure = new ApiError(403, { code: 'forbidden', requestId: 'r1' }, 'r1')),
    });
    expect(text(root.querySelector('[role="alert"]')!)).toContain('You do not have the permission for this.');
  });
});

describe('The backups screen: runs (R-UI-048)', () => {
  it('R-UI-048 lists the runs with start, operation, pod, state, backup ID, duration, database size and warnings', async () => {
    const { root } = await renderScreen(BackupsPage);
    const rows = [...root.querySelectorAll('section[aria-labelledby="bk-runs"] tbody tr')].map((r) => text(r));
    expect(rows).toHaveLength(2);
    expect(rows[0]).toContain('Create');
    expect(rows[0]).toContain('trove-backup-29885880-x7k2q');
    expect(rows[0]).toContain('Completed');
    expect(rows[0]).toContain('20261027T020000Z-1c9e');
    expect(rows[0]).toContain('47 min');
    expect(rows[0]).toContain('175.0 MiB');
    expect(rows[0]).toContain('1204');
  });

  it('R-UI-048 shows an interrupted run as interrupted with its explanation, not as a failure', async () => {
    const { root } = await renderScreen(BackupsPage);
    const row = [...root.querySelectorAll('section[aria-labelledby="bk-runs"] tbody tr')].find((r) =>
      text(r).includes('Interrupted'),
    )!;
    expect(text(row)).toContain('Interrupted by a drain or eviction; the Job retries it.');
    expect(row.querySelector('.badge.bad')).toBeNull();
    expect(root.querySelector('section[aria-labelledby="bk-status"] .badge.bad')).toBeNull();
    expect(stateView('interrupted', false).tone).toBe('');
  });

  it('R-UI-048 uses green only for a run that ended well', () => {
    const tones = ['starting', 'snapshot', 'copying', 'finalizing', 'pruning', 'completed', 'completed_with_warnings', 'failed', 'interrupted', 'abandoned']
      .filter((s) => stateView(s, false).tone === 'ok');
    expect(tones).toEqual(['completed']);
    expect(stateView('copying', true).tone).toBe('warn');
  });

  it('R-UI-048 filters by operation and state through the query parameters', async () => {
    const { root, get } = await renderScreen(BackupsPage, { inputs: { operation: 'create', state: 'interrupted' } });
    const api = fake(get);
    expect(api.filterCalls.at(-1)).toEqual({ operation: 'create', state: 'interrupted' });
    expect(root.querySelectorAll('section[aria-labelledby="bk-runs"] tbody tr')).toHaveLength(1);
    expect((root.querySelector('#bk-op') as HTMLSelectElement).value).toBe('create');
    expect((root.querySelector('#bk-state') as HTMLSelectElement).value).toBe('interrupted');
  });

  it('R-UI-048 ignores a filter value that is not a state of the API', async () => {
    const { get } = await renderScreen(BackupsPage, { inputs: { state: 'nonsense' } });
    expect(fake(get).filterCalls.at(-1)).toEqual({ operation: '', state: '' });
  });

  it('R-UI-048 says so when no run matches, and loads more pages on request', async () => {
    const many = Array.from({ length: 30 }, (_, i) => sampleRun({ id: `0193aaaa-0000-7000-8000-${String(i).padStart(12, '0')}` }));
    const { root, fixture } = await renderScreen(BackupsPage, { setup: (get) => (fake(get).runList = many) });
    expect(root.querySelectorAll('section[aria-labelledby="bk-runs"] tbody tr')).toHaveLength(25);
    button(root, 'Load more').click();
    await settle(fixture);
    expect(root.querySelectorAll('section[aria-labelledby="bk-runs"] tbody tr')).toHaveLength(30);

    const empty = await renderScreen(BackupsPage, { setup: (get) => (fake(get).runList = []) });
    expect(text(empty.root)).toContain('No backup run matches the filters.');
  });

  it('R-UI-048 R-UI-042 the detail panel shows the translated error code and the result', async () => {
    const { root, fixture } = await renderScreen(BackupsPage, {
      setup: (get) => {
        const api = fake(get);
        api.runList = [
          sampleRun({ id: RUN_FAILED, state: 'failed', result: null, error: { code: 'foreign_target' } }),
          sampleRun({ result: { backupId: '20261027T020000Z-1c9e', warnings: ['pool sweep stopped at a foreign key'] } }),
        ];
      },
    });
    const rows = root.querySelectorAll('section[aria-labelledby="bk-runs"] tbody tr');
    (rows[0]!.querySelector('button') as HTMLButtonElement).click();
    await settle(fixture, 10);
    let detail = text(root.querySelector('section[aria-labelledby="bk-detail"]')!);
    expect(detail).toContain('The target belongs to another installation or incarnation.');
    expect(detail).toContain('foreign_target');
    expect(detail).toContain('The run has no result yet.');

    (rows[1]!.querySelector('button') as HTMLButtonElement).click();
    await settle(fixture, 10);
    detail = text(root.querySelector('section[aria-labelledby="bk-detail"]')!);
    expect(detail).toContain('pool sweep stopped at a foreign key');
    expect(detail).toContain('"backupId": "20261027T020000Z-1c9e"');

    button(root.querySelector('section[aria-labelledby="bk-detail"]')!, 'Close').click();
    await settle(fixture);
    expect(root.querySelector('section[aria-labelledby="bk-detail"]')).toBeNull();
  });
});

describe('The backups screen: stored backups, the manual command and a restore (R-UI-048)', () => {
  it('R-UI-048 lists the stored backups headed with the recording run, the target, the retention and the interval', async () => {
    const { root } = await renderScreen(BackupsPage);
    const card = root.querySelector('section[aria-labelledby="bk-stored"]')!;
    const t = text(card);
    expect(t).toContain('As recorded by run 01933a1b-8c22-7e41-b0f3-2a6d9e4c7b10 at');
    expect(t).toContain('trove backup list');
    expect(t).toContain('20261027T020000Z-1c9e');
    expect(t).toContain('0.4.0');
    expect(t).toContain('42');
    expect(t).toContain('175.0 MiB');
    expect(t).toContain('384.0 GiB');
    expect(t).toContain('Targets3://trove-backups/trove');
    expect(t).toContain('Keep the newest7');
    expect(t).toContain('Keep newer thanNot set');
    expect(t).toContain('Expected interval1 d');
    expect(card.querySelector('.badge.bad')).toBeNull();
  });

  it('R-UI-048 marks a damaged backup', async () => {
    const summary = sampleSummary();
    summary.storedBackups!.items = [{ ...summary.storedBackups!.items[0]!, damaged: true }];
    const { root } = await renderScreen(BackupsPage, { setup: (get) => (fake(get).summaryResult = summary) });
    expect(text(root.querySelector('section[aria-labelledby="bk-stored"] .badge.bad')!)).toContain('Damaged');
  });

  it('R-UI-048 shows the command filled from manualRun with a copy button, and no button that starts a backup', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    const { root, fixture } = await renderScreen(BackupsPage);
    const panel = root.querySelector('section[aria-labelledby="bk-manual"]')!;
    const command = text(panel.querySelector('pre')!);
    expect(command).toMatch(/^kubectl -n trove create job --from=cronjob\/trove-backup trove-backup-manual-\d{10}$/);
    (panel.querySelector('trove-copy-button button') as HTMLButtonElement).click();
    await settle(fixture);
    expect(writeText).toHaveBeenCalledWith(command);
    expect(text(panel)).toContain('One backup runs at a time');
    expect(text(panel)).toContain('pod name');
    const labels = [...root.querySelectorAll('button, a')].map((b) => text(b));
    expect(labels.filter((l) => /^(start|run|create|delete|verify|restore)\b/i.test(l))).toEqual([]);
  });

  it('R-UI-048 shows placeholders when manualRun is empty', async () => {
    const { root } = await renderScreen(BackupsPage, {
      setup: (get) =>
        (fake(get).summaryResult = sampleSummary({ manualRun: { namespace: null, cronJob: null } })),
    });
    const panel = root.querySelector('section[aria-labelledby="bk-manual"]')!;
    expect(text(panel.querySelector('pre')!)).toMatch(
      /^kubectl -n <namespace> create job --from=cronjob\/<cronjob> <cronjob>-manual-\d{10}$/,
    );
    expect(text(panel)).toContain('Replace the placeholders');
  });

  it('R-UI-048 shortens the CronJob part of the Job name so that it has at most 63 characters', () => {
    const cronJob = 'a'.repeat(51) + '-';
    expect(cronJob).toHaveLength(52);
    const command = manualRunCommand({ namespace: 'trove', cronJob }, 1_793_000_000);
    const name = command.split(' ').at(-1)!;
    expect(name.length).toBeLessThanOrEqual(JOB_NAME_MAX);
    expect(name).toBe('a'.repeat(45) + '-manual-1793000000');
    expect(command).toContain(`--from=cronjob/${cronJob} `);
    const dashed = manualRunCommand({ namespace: 'trove', cronJob: 'a'.repeat(44) + '-' + 'b'.repeat(7) }, 1_793_000_000);
    expect(dashed.split(' ').at(-1)).toBe('a'.repeat(44) + '-manual-1793000000');
  });

  it('R-UI-048 names the backup a restored installation came from and offers no restore action', async () => {
    const { root } = await renderScreen(BackupsPage, {
      setup: (get) =>
        (fake(get).summaryResult = sampleSummary({
          restoredFrom: {
            installationId: 'inst-1',
            incarnation: 'inc-2',
            backupId: '20261020T020000Z-aaaa',
            snapshotAt: '2026-10-20T02:00:08Z',
          },
        })),
    });
    const notice = root.querySelector('.restored')!;
    expect(text(notice)).toContain('20261020T020000Z-aaaa');
    expect(text(notice)).toContain('this screen offers no restore action');
    expect(notice.querySelector('a')?.getAttribute('href')).toBe('/help/admin/operations');
    expect([...root.querySelectorAll('button')].some((b) => /restore/i.test(text(b)) && !/restored/i.test(text(b)))).toBe(false);
  });

  it('R-UI-044 passes the accessibility check with a run in progress and a detail open', async () => {
    const { root, fixture } = await renderScreen(BackupsPage, {
      setup: (get) => {
        const api = fake(get);
        api.runList = [sampleRun({ id: RUN_ACTIVE, state: 'copying', finishedAt: null, result: null }), ...api.runList];
        api.summaryResult = sampleSummary({
          active: { id: RUN_ACTIVE, operation: 'create', state: 'copying', startedAt: '2026-10-28T02:00:04Z', leaseExpired: false },
        });
      },
    });
    (root.querySelector('section[aria-labelledby="bk-runs"] tbody button') as HTMLButtonElement).click();
    await settle(fixture, 10);
    await expectAccessible(root);
  });
});

describe('The backup service (R-API-050)', () => {
  it('R-UI-048 sends GET requests only: the runs, one run and the summary', async () => {
    TestBed.resetTestingModule();
    TestBed.configureTestingModule({
      providers: [
        provideHttpClient(withInterceptors([problemInterceptor])),
        provideHttpClientTesting(),
        { provide: BackupsApi, useClass: HttpBackupsApi },
        { provide: SessionStore, useValue: { markEnded: () => undefined, markReauthRequired: () => undefined, dropCheckCache: () => undefined } },
      ],
    });
    const api = TestBed.inject(BackupsApi);
    const http = TestBed.inject(HttpTestingController);

    const list = api.runs({ operation: 'create', state: '' })(null, 25);
    const listReq = http.expectOne((r) => r.url === '/api/v1/system/backups');
    expect(listReq.request.method).toBe('GET');
    expect(listReq.request.params.get('operation')).toBe('create');
    expect(listReq.request.params.has('state')).toBe(false);
    listReq.flush({ items: [], nextCursor: null });
    await list;

    const one = api.run('01933c4e-2f10-7b3a-9c1d-5e8f7a6b4c21');
    const oneReq = http.expectOne('/api/v1/system/backups/01933c4e-2f10-7b3a-9c1d-5e8f7a6b4c21');
    expect(oneReq.request.method).toBe('GET');
    oneReq.flush(sampleRun());
    await one;

    const summary = api.summary();
    const summaryReq = http.expectOne('/api/v1/system/backups/summary');
    expect(summaryReq.request.method).toBe('GET');
    summaryReq.flush(sampleSummary());
    await summary;
    http.verify();
  });
});

describe('The failure codes of the backup job (R-UI-042)', () => {
  // The codes of internal/backup/exit.go.
  const CODES = [
    'hold_lost', 'superseded', 'pool_damaged', 'invariant_violation', 'stalled', 'lease_expired', 'config', 'busy',
    'foreign_target', 'findings', 'precondition', 'temporary', 'restore_in_progress', 'interrupted', 'internal',
    'deletion_not_allowed', 'key_ring_mismatch', 'journal_unavailable', 'format_unknown', 'schema_step_mismatch',
    'database_not_empty', 'blob_prefix_not_empty', 'owner_privilege_missing', 'backup_grant_missing',
    'target_same_bucket', 'encryption_mismatch', 'database_in_recovery', 'migration_wait', 'table_lock_timeout',
    'catalog_changed', 'missing_pool_object',
  ];

  it('R-UI-042 every failure code of the job has a translated sentence', () => {
    for (const code of CODES) expect(describeBackupProblem(code), code).toBeTruthy();
    expect(describeBackupProblem('no_such_code')).toBeNull();
    expect(describeBackupProblem(null)).toBeNull();
  });

  it('R-UI-048 writes durations in abbreviated units', () => {
    expect(durationText(12)).toBe('12 s');
    expect(durationText(7 * 60)).toBe('7 min');
    expect(durationText(2 * 3600 + 5 * 60)).toBe('2 h 5 min');
    expect(durationText(86_400)).toBe('1 d');
    expect(durationText(86_400 + 4 * 3600)).toBe('1 d 4 h');
  });
});
