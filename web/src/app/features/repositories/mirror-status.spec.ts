import { outputToObservable } from '@angular/core/rxjs-interop';
import { firstValueFrom } from 'rxjs';
import { ApiError } from '../../api/problem';
import { MirrorStatus, RepositoriesApi } from '../../api/repositories-api';
import { FakeRepositoriesApi, sampleMirrorStatus } from '../../api/testing/fakes-catalog';
import { button, expectAccessible, renderScreen, settle, text } from '../../testing/screen';
import { MirrorStatusPanel, mirrorActive, stateInfo } from './mirror-status';

const JOB = {
  id: '0193f2c4-7a7e-7b51-9b1e-2f0d6c9e1a10',
  kind: 'mirror-pass',
  state: 'running' as const,
  href: '/api/v1/system/jobs/0193f2c4-7a7e-7b51-9b1e-2f0d6c9e1a10',
};

const PASS = {
  kind: 'incremental' as const,
  method: 'feed' as const,
  startedAt: '2026-11-04T10:00:00Z',
  namesDiscovered: 120,
  packagesDone: 80,
  packagesPending: 40,
  packagesFailed: 0,
  itemsStored: 311,
  bytesStored: 52428800,
  discoveryDone: false,
  job: JOB,
};

const wait = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

async function render(
  statuses: readonly MirrorStatus[],
  options: { canChange?: boolean; intervalMs?: number; busy?: boolean } = {},
) {
  const r = await renderScreen(MirrorStatusPanel, {
    inputs: {
      key: 'npm-remote',
      canChange: options.canChange ?? true,
      intervalMs: options.intervalMs ?? 60_000,
      busy: options.busy ?? false,
    },
    setup: (get) => {
      const fake = get(RepositoriesApi) as FakeRepositoriesApi;
      fake.repositories.push({ key: 'npm-remote', class: 'remote', format: 'npm' });
      fake.mirrorStatuses = [...statuses];
    },
  });
  return { r, fake: r.get(RepositoriesApi) as FakeRepositoriesApi };
}

/** The terms of a description list with their descriptions. */
const facts = (root: Element): Record<string, string> =>
  Object.fromEntries(
    [...root.querySelectorAll('dt')].map((dt) => [text(dt), text(dt.nextElementSibling!)]),
  );

const stateOf = (root: HTMLElement) => root.querySelector('[data-testid="mirror-state"]')!;

describe('Mirror status panel', () => {
  describe('the state in words (R-UI-021, R-REPO-056, Q-UI-104)', () => {
    const states: readonly [MirrorStatus['state'], string, Partial<MirrorStatus>][] = [
      ['off', 'Off', { mode: 'pullThrough', lastCompletePass: null, nextPassAt: null }],
      ['paused', 'Paused', {}],
      ['scheduled', 'Scheduled', {}],
      ['running', 'Running', { pass: PASS }],
      ['waiting', 'Waiting', { pass: { ...PASS, job: null } }],
      ['bound_reached', 'Bound reached', { bound: { reached: 'bytes', at: '2026-11-04T10:30:00Z', maxBytes: 1048576, usedBytes: 1048577 } }],
      ['failing', 'Failing', { errors: { consecutiveFailures: 4, lastAt: '2026-11-04T10:30:00Z', lastCode: 'upstream_error', lastMessage: 'registry.example.org answered 500' } }],
    ];

    for (const [state, label, over] of states) {
      it(`R-UI-021 state ${state} reads "${label}" as a word, with an icon and a sentence`, async () => {
        const { r } = await render([sampleMirrorStatus({ state, ...over })]);
        const line = stateOf(r.root);
        expect(line.querySelector('strong')?.textContent).toBe(label);
        expect(line.getAttribute('data-state')).toBe(state);
        expect(line.querySelector('.material-icons')?.getAttribute('aria-hidden')).toBe('true');
        expect(line.querySelector('.sentence')?.textContent?.trim().length).toBeGreaterThan(10);
      });
    }

    it('R-UI-021 green (ok) marks only a scheduled mirror after a complete pass; a failing one is bad, a bound one a warning', async () => {
      const tone = async (over: Partial<MirrorStatus>) => {
        const { r } = await render([sampleMirrorStatus(over)]);
        return stateOf(r.root).querySelector('.chip')!.className;
      };
      expect(await tone({})).toContain('ok');
      expect(await tone({ lastCompletePass: null })).not.toContain('ok');
      expect(await tone({ state: 'paused' })).not.toContain('ok');
      expect(await tone({ state: 'running', pass: PASS })).not.toContain('ok');
      expect(await tone({ state: 'failing' })).toContain('bad');
      expect(await tone({ state: 'bound_reached' })).toContain('warn');
    });

    it('stateInfo names the number of failures; mirrorActive is true for running and waiting only', () => {
      const status = sampleMirrorStatus({ state: 'failing', errors: { consecutiveFailures: 5, lastAt: null, lastCode: null, lastMessage: null } });
      expect(stateInfo(status).sentence).toContain('5');
      expect(['running', 'waiting'].every((s) => mirrorActive(s as never))).toBe(true);
      expect(['off', 'paused', 'scheduled', 'bound_reached', 'failing'].some((s) => mirrorActive(s as never))).toBe(false);
    });
  });

  describe('the facts', () => {
    it('R-UI-021 R-REPO-056 shows the pass in progress with its counts and its job link', async () => {
      const { r } = await render([sampleMirrorStatus({ state: 'running', pass: PASS })]);
      const pass = facts(r.root.querySelector('[data-testid="mirror-pass"]')!);
      expect(pass).toMatchObject({
        'Names discovered': '120',
        'Packages done': '80',
        'Packages pending': '40',
        'Packages failed': '0',
        'Items stored': '311',
        'Bytes stored': '50.0 MiB',
        Discovery: 'still running',
      });
      expect(pass['Job']).toContain(JOB.id);
      const link = r.root.querySelector<HTMLAnchorElement>('[data-testid="mirror-pass"] a')!;
      expect(link.getAttribute('href')).toBe('/admin/system/jobs?kind=mirror-pass');
    });

    it('R-UI-021 shows no pass section while none is in progress, and says so for a missing last pass', async () => {
      const { r } = await render([sampleMirrorStatus({ lastCompletePass: null })]);
      expect(r.root.querySelector('[data-testid="mirror-pass"]')).toBeNull();
      expect(text(r.root)).toContain('No pass has completed yet.');
    });

    it('R-UI-021 shows the last complete pass: outcome, kind, method, times, counts and the discovery note', async () => {
      const { r } = await render([
        sampleMirrorStatus({
          lastCompletePass: {
            ...sampleMirrorStatus().lastCompletePass!,
            outcome: 'partial',
            packagesFailed: 3,
            discoveryComplete: false,
            discoveryNote: 'the upstream search stops at 10000 results',
          },
        }),
      ]);
      const last = facts(r.root.querySelector('[data-testid="mirror-last-pass"]')!);
      expect(last).toMatchObject({
        Outcome: 'Partial: some packages failed',
        Kind: 'Full',
        Discovery: 'Change feed',
        Packages: '1,200',
        'Packages failed': '3',
        'Items stored': '5,021',
        'Bytes stored': '8.5 GiB',
      });
      expect(last['Discovery saw everything']).toContain('No');
      expect(last['Discovery saw everything']).toContain('the upstream search stops at 10000 results');
      expect(r.root.querySelector('[data-testid="mirror-last-pass"] time')?.getAttribute('datetime')).toBe(
        '2026-11-03T10:00:00Z',
      );
    });

    it('R-UI-021 shows the next pass, or that it is due at the next dispatch, or that none is planned', async () => {
      const due = await render([sampleMirrorStatus({ nextPassAt: '2026-11-04T11:00:00Z' })]);
      expect(due.r.root.querySelector('[data-testid="mirror-next"] time')?.getAttribute('datetime')).toBe(
        '2026-11-04T11:00:00Z',
      );
      const dispatch = await render([sampleMirrorStatus({ nextPassAt: null })]);
      expect(text(dispatch.r.root.querySelector('[data-testid="mirror-next"]')!)).toContain(
        'At the next dispatch, within a minute.',
      );
      const paused = await render([sampleMirrorStatus({ state: 'paused' })]);
      expect(text(paused.r.root.querySelector('[data-testid="mirror-next"]')!)).toContain('No pass is planned.');
    });

    it('R-UI-021 shows the last error with code, message and time, and "No error." without one', async () => {
      const none = await render([sampleMirrorStatus()]);
      expect(text(none.r.root)).toContain('No error.');
      const { r } = await render([
        sampleMirrorStatus({
          errors: {
            consecutiveFailures: 2,
            lastAt: '2026-11-04T10:30:00Z',
            lastCode: 'upstream_unreachable',
            lastMessage: 'registry.example.org did not answer',
          },
        }),
      ]);
      const error = facts(r.root.querySelector('[data-testid="mirror-error"]')!);
      expect(error).toMatchObject({
        Code: 'upstream_unreachable',
        Message: 'registry.example.org did not answer',
        'Failures in a row': '2',
      });
      expect(error['Time']).not.toBe('–');
    });

    it('R-UI-021 shows the rate-limit waits with the last answer and until when the mirror waits', async () => {
      const { r } = await render([
        sampleMirrorStatus({
          rateLimit: {
            waits: 12,
            waitSeconds: 340,
            lastAt: '2026-11-04T10:20:31Z',
            lastStatus: 429,
            until: '2026-11-04T10:25:00Z',
          },
        }),
      ]);
      const rate = facts(r.root.querySelector('[data-testid="mirror-rate"]')!);
      expect(rate['Waits']).toBe('12');
      expect(rate['Seconds waited']).toBe('340');
      expect(rate['Last answer']).toContain('HTTP 429');
      expect(Object.keys(rate)).toContain('Waiting until');
    });

    it('R-UI-021 R-REPO-052 shows the bound: limit, used bytes and what was reached', async () => {
      const { r } = await render([
        sampleMirrorStatus({
          state: 'bound_reached',
          bound: { reached: 'quota', at: '2026-11-04T10:30:00Z', maxBytes: 107374182400, usedBytes: 9200000000 },
        }),
      ]);
      const bound = facts(r.root.querySelector('[data-testid="mirror-bound"]')!);
      expect(bound['Storage limit']).toBe('100.0 GiB');
      expect(bound['Stored']).toBe('8.6 GiB');
      expect(bound['Reached']).toContain('Yes, by a quota of the repository');
      const open = await render([sampleMirrorStatus()]);
      expect(text(open.r.root.querySelector('[data-testid="mirror-bound"]')!)).toContain('No limit of its own');
    });

    it('R-UI-021 a failed read of the status is shown with a retry', async () => {
      const r = await renderScreen(MirrorStatusPanel, {
        inputs: { key: 'npm-remote', canChange: true },
        setup: (get) => {
          const fake = get(RepositoriesApi) as FakeRepositoriesApi;
          fake.repositories.push({ key: 'npm-remote', class: 'remote', format: 'npm' });
          fake.mirrorStatusError = new ApiError(503, { code: 'unavailable' }, null);
        },
      });
      expect(r.root.querySelector('[role="alert"]')).not.toBeNull();
      const fake = r.get(RepositoriesApi) as FakeRepositoriesApi;
      fake.mirrorStatusError = null;
      button(r.root, 'Retry').click();
      await settle(r.fixture);
      expect(stateOf(r.root).getAttribute('data-state')).toBe('scheduled');
    });
  });

  describe('polling', () => {
    it('R-UI-021 reads the status again while a pass runs and stops when it has ended', async () => {
      const { r, fake } = await render(
        [
          sampleMirrorStatus({ state: 'running', pass: PASS }),
          sampleMirrorStatus({ state: 'waiting', pass: { ...PASS, packagesDone: 90 } }),
          sampleMirrorStatus(),
        ],
        { intervalMs: 15 },
      );
      await wait(150);
      await settle(r.fixture);
      expect(fake.mirrorStatusCalls).toBe(3);
      expect(stateOf(r.root).getAttribute('data-state')).toBe('scheduled');
      await wait(100);
      expect(fake.mirrorStatusCalls).toBe(3);
    });

    it('R-UI-021 reads a mirror that is not running once, and stops polling when the panel is left', async () => {
      const idle = await render([sampleMirrorStatus()], { intervalMs: 15 });
      await wait(80);
      expect(idle.fake.mirrorStatusCalls).toBe(1);

      const running = await render([sampleMirrorStatus({ state: 'running', pass: PASS })], { intervalMs: 15 });
      await wait(80);
      const calls = running.fake.mirrorStatusCalls;
      expect(calls).toBeGreaterThan(2);
      running.r.fixture.destroy();
      const after = running.fake.mirrorStatusCalls;
      await wait(80);
      expect(running.fake.mirrorStatusCalls).toBe(after);
    });

    it('R-UI-021 a changed refresh input reads the status at once', async () => {
      const { r, fake } = await render([sampleMirrorStatus(), sampleMirrorStatus({ state: 'paused' })]);
      expect(stateOf(r.root).getAttribute('data-state')).toBe('scheduled');
      r.fixture.componentRef.setInput('refresh', 1);
      await settle(r.fixture);
      expect(fake.mirrorStatusCalls).toBe(2);
      expect(stateOf(r.root).getAttribute('data-state')).toBe('paused');
    });
  });

  describe('the buttons (Q-UI-105, Q-UI-120)', () => {
    it('R-UI-021 a caller who may change the repository sees Pause and Run; a paused mirror shows Resume instead of Pause', async () => {
      const running = await render([sampleMirrorStatus()]);
      expect(button(running.r.root, 'Pause')).toBeDefined();
      expect(button(running.r.root, 'Run a pass now')).toBeDefined();
      expect(() => button(running.r.root, 'Resume')).toThrow();

      const paused = await render([sampleMirrorStatus({ state: 'paused' })]);
      expect(button(paused.r.root, 'Resume')).toBeDefined();
      expect(() => button(paused.r.root, 'Pause')).toThrow();
    });

    it('R-UI-021 a caller the API refuses sees the status and no button', async () => {
      const { r } = await render([sampleMirrorStatus()], { canChange: false });
      expect(r.root.querySelectorAll('button')).toHaveLength(0);
      expect(stateOf(r.root)).not.toBeNull();
    });

    it('R-UI-021 Pause and Resume ask the parent, which patches the document with If-Match (Q-UI-105)', async () => {
      const { r } = await render([sampleMirrorStatus()]);
      const pause = firstValueFrom(outputToObservable(r.fixture.componentInstance.setPaused));
      button(r.root, 'Pause').click();
      expect(await pause).toBe(true);

      const paused = await render([sampleMirrorStatus({ state: 'paused' })]);
      const resume = firstValueFrom(outputToObservable(paused.r.fixture.componentInstance.setPaused));
      button(paused.r.root, 'Resume').click();
      expect(await resume).toBe(false);
    });

    it('R-UI-021 Pause is disabled while the document is being written', async () => {
      const { r } = await render([sampleMirrorStatus()], { busy: true });
      expect(button(r.root, 'Pause').disabled).toBe(true);
    });
  });

  describe('run a pass (R-REPO-056, Q-UI-121)', () => {
    it('R-UI-021 R-REPO-056 "Run a pass now" posts {"full": false} with an idempotency key, announces the queued job with its link and reads the status again', async () => {
      const { r, fake } = await render([sampleMirrorStatus(), sampleMirrorStatus({ state: 'running', pass: PASS })]);
      button(r.root, 'Run a pass now').click();
      await settle(r.fixture);
      expect(fake.mirrorRuns).toHaveLength(1);
      expect(fake.mirrorRuns[0]).toMatchObject({ key: 'npm-remote', full: false });
      expect(fake.mirrorRuns[0]!.idempotencyKey).toMatch(/^[0-9a-f-]{36}$/);
      const notice = r.root.querySelector('.notices')!;
      expect(text(notice)).toContain('A pass is queued.');
      expect(text(notice)).toContain(fake.runMirrorResult.id);
      expect(notice.querySelector('a')?.getAttribute('href')).toBe('/admin/system/jobs?kind=mirror-pass');
      expect(stateOf(r.root).getAttribute('data-state')).toBe('running');
    });

    it('R-UI-021 "Run a full pass" is in the menu of the run button and posts {"full": true}', async () => {
      const { r, fake } = await render([sampleMirrorStatus()]);
      r.root.querySelector<HTMLButtonElement>('button[aria-label="More ways to run a pass"]')!.click();
      await settle(r.fixture);
      const item = [...r.root.ownerDocument.body.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find((b) =>
        b.textContent?.includes('Run a full pass'),
      )!;
      item.click();
      await settle(r.fixture);
      expect(fake.mirrorRuns[0]).toMatchObject({ full: true });
      expect(text(r.root.querySelector('.notices')!)).toContain('A full pass is queued.');
    });

    it('R-UI-021 R-REPO-056 a 409 mirror_pass_running shows the running job with a link instead of an error', async () => {
      const { r, fake } = await render([sampleMirrorStatus({ state: 'running', pass: PASS })]);
      fake.runMirrorError = new ApiError(409, { code: 'mirror_pass_running', job: JOB }, null);
      button(r.root, 'Run a pass now').click();
      await settle(r.fixture);
      const notice = r.root.querySelector('.notices')!;
      expect(text(notice)).toContain('A pass is already queued or running.');
      expect(text(notice)).toContain(JOB.id);
      expect(notice.querySelector('a')?.getAttribute('href')).toBe('/admin/system/jobs?kind=mirror-pass');
      expect(notice.querySelector('[role="alert"]')).toBeNull();
    });

    it('R-UI-021 R-REPO-056 mirror_off and mirror_paused are shown inline, in words', async () => {
      const { r, fake } = await render([sampleMirrorStatus()]);
      fake.runMirrorError = new ApiError(409, { code: 'mirror_off' }, null);
      button(r.root, 'Run a pass now').click();
      await settle(r.fixture);
      expect(text(r.root.querySelector('.notices')!)).toContain('pull-through mode; there is no mirror to run');

      fake.runMirrorError = new ApiError(409, { code: 'mirror_paused' }, null);
      button(r.root, 'Run a pass now').click();
      await settle(r.fixture);
      expect(text(r.root.querySelector('.notices')!)).toContain('The mirror is paused. Resume it to run a pass.');
    });

    it('R-UI-021 a 400 not_applicable is shown inline; another failure is an error notice', async () => {
      const { r, fake } = await render([sampleMirrorStatus()]);
      fake.runMirrorError = new ApiError(400, { code: 'not_applicable' }, null);
      button(r.root, 'Run a pass now').click();
      await settle(r.fixture);
      expect(text(r.root.querySelector('.notices')!)).toContain('Only a remote repository has a mirror.');

      fake.runMirrorError = new ApiError(403, { code: 'forbidden' }, null);
      button(r.root, 'Run a pass now').click();
      await settle(r.fixture);
      expect(r.root.querySelector('.notices [role="alert"]')?.textContent).toContain('permission');
    });
  });

  it('R-UI-021 R-UI-044 the panel passes the accessibility check in a running and in a failing state', async () => {
    const running = await render([sampleMirrorStatus({ state: 'running', pass: PASS })]);
    await expectAccessible(running.r.root);
    const failing = await render([
      sampleMirrorStatus({
        state: 'failing',
        errors: { consecutiveFailures: 3, lastAt: '2026-11-04T10:30:00Z', lastCode: 'x', lastMessage: 'y' },
        bound: { reached: 'bytes', at: '2026-11-04T10:30:00Z', maxBytes: 1048576, usedBytes: 2000000 },
      }),
    ]);
    await expectAccessible(failing.r.root);
  });
});
