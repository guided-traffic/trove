import { DebApi, DebIndexState } from '../../api/deb-api';
import { ApiError } from '../../api/problem';
import { FakeDebApi } from '../../api/testing/fakes-deb';
import { buttonByText, expectAccessible, renderScreen, settle, text } from '../../testing/screen';
import { DebIndexStatePanel, behindBy, failureReasonText, suiteStateText } from './deb-index-state';

const JOB = { id: '0193f2c4-7a7e-7b51-9b1e-2f0d6c9e1a10', kind: 'reindex', state: 'queued' as const, href: '/api/v1/system/jobs/x' };
const wait = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

async function render(index?: DebIndexState, options: { canManage?: boolean; intervalMs?: number } = {}) {
  const r = await renderScreen(DebIndexStatePanel, {
    inputs: { key: 'web/deb-local', canManage: options.canManage ?? true, intervalMs: options.intervalMs ?? 60_000 },
    setup: (get) => {
      const fake = get(DebApi) as FakeDebApi;
      if (index) fake.index = index;
    },
  });
  return { r, fake: r.get(DebApi) as FakeDebApi, body: r.root.ownerDocument.body };
}

const rows = (root: Element) => [...root.querySelectorAll('tbody tr')];

describe('Debian index state panel', () => {
  it('R-DEB-014 shows for each suite its state in words, the change and published numbers, the generation, the release date and the signing key', async () => {
    const { r } = await render();
    const [stable, testing] = rows(r.root);
    expect(text(stable!)).toContain('stable');
    expect(text(stable!)).toContain('Current');
    expect(stable!.querySelector('.badge')?.classList.contains('ok')).toBe(true);
    expect(stable!.querySelectorAll('td')[1]!.textContent).toContain('42');
    expect(text(stable!.querySelector('code')!)).toBe('00000000000090C0');
    expect(text(testing!)).toContain('Waiting for a build');
    expect(text(testing!)).toContain('behind by 1');
    expect(testing!.querySelector('.badge .material-icons')?.getAttribute('aria-hidden')).toBe('true');
    await expectAccessible(r.root);
  });

  it('R-DEB-014 a failed suite names the reason in words, the member codes of a virtual repository included', async () => {
    const { r } = await render({
      suites: [
        { distribution: 'stable', changeSeq: 3, publishedSeq: 2, generation: 1, state: 'failed', reason: 'no_signing_key' },
        { distribution: 'testing', changeSeq: 3, publishedSeq: 2, generation: 1, state: 'failed', reason: 'member_unverified: web/deb-remote' },
        { distribution: 'old', changeSeq: 3, publishedSeq: 2, generation: 1, state: 'failed', reason: 'something_new' },
      ],
    });
    const reasons = [...r.root.querySelectorAll('[data-testid="deb-index-reason"]')].map((e) => text(e));
    expect(reasons[0]).toContain('no primary signing key');
    expect(reasons[1]).toContain('could not be verified against its upstream keys: web/deb-remote.');
    expect(reasons[2]).toBe('something_new');
    expect(rows(r.root)[0]!.querySelector('.badge')?.classList.contains('bad')).toBe(true);
  });

  it('R-DEB-014 the member reasons of R-DEB-029 have a sentence each', () => {
    expect(failureReasonText('member_expired: m')).toContain('has expired: m.');
    expect(failureReasonText('member_rollback: m')).toContain('Take the member out and add it back');
    expect(failureReasonText('deb_members_unreadable: m')).toContain('cannot be read with your permissions: m.');
    expect(failureReasonText('plain')).toBe('plain');
    expect(suiteStateText('failed')).toBe('Failed');
    expect(behindBy({ distribution: 's', changeSeq: 5, publishedSeq: 9, generation: 1, state: 'current' })).toBe(0);
  });

  it('R-DEB-014 a repository without suites says so', async () => {
    const { r } = await render({ suites: [] });
    expect(text(r.root)).toContain('No suite has packages yet');
  });

  it('R-DEB-014 while a suite waits for its build the state is read again, and the polling ends once every suite is current', async () => {
    const { r, fake } = await render(undefined, { intervalMs: 10 });
    const reads = () => fake.calls.filter((c) => c === 'indexState').length;
    const first = reads();
    expect(first).toBeGreaterThanOrEqual(1);
    await wait(40);
    expect(reads()).toBeGreaterThan(first);
    fake.index = { suites: fake.index.suites.map((s) => ({ ...s, publishedSeq: s.changeSeq, state: 'current' as const })) };
    await wait(40);
    await settle(r.fixture);
    expect(text(r.root)).not.toContain('Waiting for a build');
    const settled = reads();
    await wait(60);
    expect(reads()).toBe(settled);
  });

  it('R-DEB-014 a failed read shows the error with a retry', async () => {
    const r = await renderScreen(DebIndexStatePanel, {
      inputs: { key: 'web/deb-local', canManage: true },
      setup: (get) => {
        (get(DebApi) as FakeDebApi).failure = new ApiError(503, { code: 'unavailable', detail: 'down' }, null);
      },
    });
    expect(r.root.querySelector('[role="alert"]')).not.toBeNull();
    buttonByText(r.root, 'Retry').click();
    await settle(r.fixture);
    expect(rows(r.root).length).toBe(2);
  });

  it('R-DEB-014 the rebuild is offered to those who manage, asks first, and links the queued job', async () => {
    const { r, fake, body } = await render();
    fake.reindexJob = JOB;
    buttonByText(r.root, 'Rebuild all indexes').click();
    await settle(r.fixture);
    expect(fake.calls).not.toContain('reindex');
    const confirm = [...body.querySelectorAll<HTMLButtonElement>('.dialog-actions button')].find((b) => text(b) === 'Rebuild')!;
    confirm.click();
    await settle(r.fixture);
    expect(fake.calls).toContain('reindex');
    const notice = text(r.root.querySelector('[data-testid="deb-index-notice"]')!);
    expect(notice).toContain('The rebuild is queued.');
    expect(notice).toContain(JOB.id);
  });

  it('R-DEB-014 a rebuild that meets a queued or running one says so instead of failing', async () => {
    const { r, fake, body } = await render();
    fake.reindexJob = null;
    buttonByText(r.root, 'Rebuild all indexes').click();
    await settle(r.fixture);
    [...body.querySelectorAll<HTMLButtonElement>('.dialog-actions button')].find((b) => text(b) === 'Rebuild')!.click();
    await settle(r.fixture);
    expect(text(r.root.querySelector('[data-testid="deb-index-notice"]')!)).toContain('already queued or running');
    expect(r.root.querySelector('[role="alert"]')).toBeNull();
  });

  it('R-DEB-014 without manage there is no rebuild', async () => {
    const { r } = await render(undefined, { canManage: false });
    expect(r.root.querySelector('[data-testid="deb-reindex"]')).toBeNull();
    expect(rows(r.root).length).toBe(2);
  });
});
