import { Router } from '@angular/router';
import { ApiError } from '../../api/problem';
import { EncryptionApi, EncryptionStatus } from '../../api/system-api';
import { JobsApi } from '../../api/jobs-api';
import { FakeEncryptionApi } from '../../api/testing/fakes';
import { FakeJobsApi, sampleJob } from '../../api/testing/fakes-catalog';
import { button, expectAccessible, renderScreen, settle, text } from '../../testing/screen';
import { JobProgress } from '../../shared/job-progress';
import { JobsPage } from './jobs-page';
import { SystemStatus } from './system-status';
import { ringGaps, rotationStep } from './system-status-model';

const RING: EncryptionStatus = {
  answeredBy: 'trove-web-0',
  activeKeyId: '2026-10',
  keys: [
    { id: '2026-10', active: true, sealedValues: 14, canary: 'ok' },
    { id: '2026-04', active: false, sealedValues: 3, canary: 'ok' },
  ],
  replicas: [
    { name: 'trove-web-0', role: 'web', activeKeyId: '2026-10', keyIds: ['2026-04', '2026-10'] },
    { name: 'trove-web-1', role: 'web', activeKeyId: '2026-04', keyIds: ['2026-04'] },
    {
      name: 'trove-worker-0',
      role: 'worker',
      activeKeyId: '2026-10',
      keyIds: ['2026-04', '2026-10'],
    },
  ],
  ringsCompatible: false,
  lastReencryption: { jobId: 'j1', state: 'succeeded', finishedAt: '2026-09-01T10:00:00Z' },
};

describe('Stored-secret keys card (E1.9)', () => {
  it('R-UI-038 the card shows the same ring state the API reports, including the replica whose ring lacks a key', async () => {
    const { root } = await renderScreen(SystemStatus, {
      setup: (get) => ((get(EncryptionApi) as FakeEncryptionApi).statusResult = RING),
    });
    const card = root.querySelector('section[aria-labelledby="card-keys"]')!;
    const t = text(card);
    expect(t).toContain('Key rings are not compatible');
    expect(t).toContain('Canary (reported by trove-web-0)');
    expect(t).toContain('trove-web-1');
    expect(t).toContain('lacks 2026-10');
    expect(t).toContain('Next step: give every replica the key files it lacks');
    expect(t).toContain('Last re-encryption');
    const rows = [...card.querySelectorAll('tbody tr')].map((r) => text(r));
    expect(rows.find((r) => r.startsWith('trove-worker-0'))).toContain('complete');
    await expectAccessible(root);
  });

  it('R-UI-038 ringGaps names what each replica lacks, from the active IDs and the IDs that still seal values', () => {
    expect(ringGaps(RING)).toEqual([{ replica: 'trove-web-1', role: 'web', missing: ['2026-10'] }]);
    expect(
      ringGaps({
        ...RING,
        replicas: RING.replicas.map((r) => ({ ...r, keyIds: ['2026-04', '2026-10'] })),
      }),
    ).toEqual([]);
  });

  it('R-UI-038 the next rotation step follows the state: equalise the rings, re-encrypt, then remove the old key file', () => {
    expect(rotationStep(RING).kind).toBe('incompatible');
    expect(rotationStep({ ...RING, ringsCompatible: true })).toEqual({
      kind: 'reencrypt',
      keyIds: ['2026-04'],
    });
    const drained = {
      ...RING,
      ringsCompatible: true,
      keys: RING.keys.map((k) => ({ ...k, sealedValues: k.active ? 14 : 0 })),
    };
    expect(rotationStep(drained)).toEqual({ kind: 'remove', keyIds: ['2026-04'] });
    expect(rotationStep({ ...drained, keys: [drained.keys[0]!] }).kind).toBe('none');
  });

  it('R-UI-038 after "Re-encrypt now" the card follows the job', async () => {
    const { root, fixture, get } = await renderScreen(SystemStatus, {
      setup: (get) => {
        const enc = get(EncryptionApi) as FakeEncryptionApi;
        enc.statusResult = { ...RING, ringsCompatible: true };
        const jobs = get(JobsApi) as FakeJobsApi;
        jobs.jobList.push(
          sampleJob({ id: 'job-1', kind: 'secrets-reencrypt', state: 'running', result: null }),
        );
      },
    });
    button(root, 'Re-encrypt now').click();
    await settle(fixture, 10);
    expect((get(EncryptionApi) as FakeEncryptionApi).reencryptKeys).toHaveLength(1);
    expect(root.querySelector('trove-job-progress')).toBeTruthy();
  });
});

describe('Scheduled tasks and recent jobs on the status screen', () => {
  it('R-UI-038 lists the tasks with schedule, next run and last outcome, and the recent jobs, and passes the accessibility check', async () => {
    const { root } = await renderScreen(SystemStatus);
    const tasks = root.querySelector('section[aria-labelledby="card-tasks"]')!;
    expect(text(tasks)).toContain('0 3 * * *');
    expect(text(tasks)).toContain('manual only');
    expect(text(tasks)).toContain('succeeded');
    const jobs = root.querySelector('section[aria-labelledby="card-jobs"]')!;
    expect(text(jobs)).toContain('gc-full');
    expect(text(jobs)).toContain('3/10 items');
    await expectAccessible(root);
  });

  it('R-UI-038 and R-UI-042 "Run now" sends one run with an Idempotency-Key and follows the job', async () => {
    const { root, fixture, get } = await renderScreen(SystemStatus, {
      setup: (get) =>
        (get(JobsApi) as FakeJobsApi).jobList.push(
          sampleJob({ id: 'job-run-1', kind: 'gc', state: 'queued', result: null }),
        ),
    });
    const jobs = get(JobsApi) as FakeJobsApi;
    const run = button(root.querySelector('section[aria-labelledby="card-tasks"]')!, 'Run now');
    run.click();
    run.click();
    await settle(fixture, 10);
    expect(jobs.runs).toHaveLength(1);
    expect(jobs.runs[0]?.key).toBeTruthy();
    expect(
      root.querySelector('section[aria-labelledby="card-tasks"] trove-job-progress'),
    ).toBeTruthy();
  });

  it('R-UI-038 a run that is refused shows the reason and offers the next click as a new run', async () => {
    const { root, fixture, get } = await renderScreen(SystemStatus, {
      setup: (get) =>
        ((get(JobsApi) as FakeJobsApi).runError = new ApiError(
          409,
          { code: 'key_ring_incompatible', detail: 'x' },
          null,
        )),
    });
    button(root.querySelector('section[aria-labelledby="card-tasks"]')!, 'Run now').click();
    await settle(fixture, 10);
    expect(text(root)).toContain('do not hold the same key IDs');
    expect((get(JobsApi) as FakeJobsApi).runs).toHaveLength(1);
  });

  it('R-UI-038 "Run now" is offered only to a caller who holds settings:write', async () => {
    const { root } = await renderScreen(SystemStatus, { capabilities: ['settings:read'] });
    expect(root.querySelector('section[aria-labelledby="card-tasks"]')?.textContent).not.toContain(
      'Run now',
    );
  });

  it('R-UI-038 "Cancel" is offered only for queued and running jobs', async () => {
    const { root, fixture, get } = await renderScreen(SystemStatus);
    const card = root.querySelector('section[aria-labelledby="card-jobs"]')!;
    const cancels = [...card.querySelectorAll('button')].filter(
      (b) => b.textContent?.trim() === 'Cancel',
    );
    expect(cancels).toHaveLength(1);
    cancels[0]!.click();
    await settle(fixture);
    expect((get(JobsApi) as FakeJobsApi).cancelled).toEqual([
      '01933b2a-7c41-7d1e-9a52-3f0e4b8c2d12',
    ]);
  });

  it('R-UI-038 a failing task list shows its own error and leaves the rest of the screen alone', async () => {
    const { root } = await renderScreen(SystemStatus, {
      setup: (get) =>
        ((get(JobsApi) as FakeJobsApi).tasks = () =>
          Promise.reject(new ApiError(403, { code: 'forbidden' }, null))),
    });
    expect(text(root.querySelector('section[aria-labelledby="card-tasks"]')!)).toContain(
      'You do not have the permission',
    );
    expect(text(root)).toContain('Installation');
  });
});

describe('Jobs screen', () => {
  async function open(inputs: Record<string, unknown> = {}, setup?: (jobs: FakeJobsApi) => void) {
    const rendered = await renderScreen(JobsPage, {
      inputs,
      setup: (get) => {
        vi.spyOn(get(Router), 'navigate').mockResolvedValue(true);
        setup?.(get(JobsApi) as FakeJobsApi);
      },
    });
    return {
      ...rendered,
      jobs: rendered.get(JobsApi) as FakeJobsApi,
      router: rendered.get(Router),
    };
  }

  it('R-UI-038 lists the jobs with state, progress and result and passes the accessibility check', async () => {
    const { root } = await open();
    const t = text(root);
    expect(t).toContain('gc-full');
    expect(t).toContain('deleted: 2');
    await expectAccessible(root);
  });

  it('R-UI-038 filters by kind and state reach the API', async () => {
    const { jobs, root } = await open({ kind: 'gc,gc-full', state: ['running', 'cancelling'] });
    expect(jobs.filterCalls.at(-1)).toEqual({
      kind: ['gc', 'gc-full'],
      state: ['running', 'cancelling'],
    });
    expect(text(root)).toContain('gc-full');
    expect(root.querySelectorAll('tbody tr')).toHaveLength(1);
  });

  it('R-UI-038 cancelling a running job lists it as cancelling under that state filter until it stops', async () => {
    const { root, fixture, jobs } = await open();
    const cancel = [...root.querySelectorAll('button')].find(
      (b) => b.textContent?.trim() === 'Cancel',
    )!;
    cancel.click();
    await settle(fixture);
    expect(jobs.cancelled).toHaveLength(1);
    expect(text(root)).toContain('cancelling');
    expect(root.querySelectorAll('tbody button')).toHaveLength(0);

    const filtered = await open(
      { state: 'cancelling' },
      (j) => (j.jobList[1] = { ...j.jobList[1]!, state: 'cancelling' }),
    );
    expect(filtered.root.querySelectorAll('tbody tr')).toHaveLength(1);
    expect(text(filtered.root)).toContain('cancelling');
  });

  it('R-UI-038 the empty result and a failing list are explained and pass the accessibility check', async () => {
    const empty = await open({ kind: 'nothing' });
    expect(text(empty.root)).toContain('No job matches the filters.');
    await expectAccessible(empty.root);

    const failing = await open(
      {},
      (j) =>
        (j.jobs = () => () => Promise.reject(new ApiError(503, { code: 'unavailable' }, null))),
    );
    expect(text(failing.root)).toContain('temporarily unavailable');
    await expectAccessible(failing.root);
  });

  it('R-UI-038 the automatic refresh can be paused (WCAG 2.2.2)', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
    try {
      const { root, fixture, jobs } = await open();
      const calls = jobs.filterCalls.length;
      vi.advanceTimersByTime(5_000);
      await settle(fixture);
      expect(jobs.filterCalls.length).toBe(calls + 1);
      const pause = root.querySelector<HTMLButtonElement>('header button[aria-pressed]')!;
      pause.click();
      await settle(fixture);
      expect(pause.getAttribute('aria-pressed')).toBe('true');
      vi.advanceTimersByTime(30_000);
      await settle(fixture);
      expect(jobs.filterCalls.length).toBe(calls + 1);
    } finally {
      vi.useRealTimers();
    }
  });
});

describe('Job progress', () => {
  it('R-UI-042 polls a job until it ends, shows progress and announces at most every 10 seconds', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] });
    try {
      const { fixture, get } = await renderScreen(JobProgress, {
        inputs: { jobId: 'j-1', intervalMs: 1000 },
        setup: (get) => {
          (get(JobsApi) as FakeJobsApi).jobList = [
            sampleJob({
              id: 'j-1',
              state: 'running',
              progress: { done: 1, total: 4, unit: 'items' },
              result: null,
            }),
          ];
        },
      });
      const jobs = get(JobsApi) as FakeJobsApi;
      await settle(fixture);
      const root = fixture.nativeElement as HTMLElement;
      expect(root.querySelector('progress')?.getAttribute('value')).toBe('1');
      const first = root.querySelector('[role="status"]')?.textContent;
      expect(first).toContain('1 of 4');

      jobs.jobList[0] = { ...jobs.jobList[0]!, progress: { done: 2, total: 4, unit: 'items' } };
      vi.advanceTimersByTime(1000);
      await settle(fixture);
      expect(root.querySelector('progress')?.getAttribute('value')).toBe('2');
      expect(root.querySelector('[role="status"]')?.textContent).toBe(first);

      jobs.jobList[0] = {
        ...jobs.jobList[0]!,
        state: 'succeeded',
        progress: { done: 4, total: 4, unit: 'items' },
      };
      vi.advanceTimersByTime(1000);
      await settle(fixture);
      expect(root.querySelector('[role="status"]')?.textContent).toContain('succeeded');
    } finally {
      vi.useRealTimers();
    }
  });
});
