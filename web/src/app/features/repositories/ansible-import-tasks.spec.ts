import { AnsibleApi } from '../../api/ansible-api';
import { ANSIBLE_REPOSITORY, FakeAnsibleApi, ansibleProblem, importTask } from '../../api/testing/fakes-ansible';
import { expectAccessible, renderScreen, settle, text } from '../../testing/screen';
import { AnsibleImportTasks, importStateText } from './ansible-import-tasks';

async function render(tasks = [importTask()], fail?: ReturnType<typeof ansibleProblem>) {
  const r = await renderScreen(AnsibleImportTasks, {
    inputs: { key: ANSIBLE_REPOSITORY },
    setup: (get) => {
      const fake = get(AnsibleApi) as FakeAnsibleApi;
      fake.tasks = [...tasks];
      fake.failure = fail ?? null;
    },
  });
  return { r, fake: r.get(AnsibleApi) as FakeAnsibleApi };
}

const rows = (root: Element) => [...root.querySelectorAll('tbody tr')];

describe('Ansible import tasks', () => {
  it('R-ANS-026 lists each task with its collection, version, state in words, time and messages', async () => {
    const { r } = await render([
      importTask(),
      importTask({
        id: '6f1c0000-0000-4000-8000-000000000002',
        state: 'failed',
        version: '1.1.0',
        error: { code: 'invalid_archive', description: 'MANIFEST.json is missing' },
        messages: [{ level: 'ERROR', message: 'MANIFEST.json is missing', time: '2026-11-09T09:05:00Z' }],
      }),
    ]);
    const [done, failed] = rows(r.root);
    expect(text(done!)).toContain('acme.tools');
    expect(text(done!)).toContain('1.0.0');
    expect(text(done!)).toContain('Completed');
    expect(done!.querySelector('.badge')?.classList.contains('ok')).toBe(true);
    expect(done!.querySelector('details li')?.textContent).toContain('Collection acme.tools 1.0.0 stored');
    expect(text(failed!)).toContain('Failed');
    expect(failed!.querySelector('.badge')?.classList.contains('bad')).toBe(true);
    expect(failed!.querySelector('[data-testid="ansible-import-error"]')?.textContent).toContain('MANIFEST.json is missing');
    expect(failed!.querySelector('.badge .material-icons')?.getAttribute('aria-hidden')).toBe('true');
    await expectAccessible(r.root);
  });

  it('R-ANS-026 says so when nothing was published', async () => {
    const { r } = await render([]);
    expect(text(r.root)).toContain('No collection has been published to this repository yet');
  });

  it('R-ANS-026 pages through the tasks with Load more', async () => {
    const many = Array.from({ length: 60 }, (_, i) =>
      importTask({ id: `6f1c0000-0000-4000-8000-${String(i).padStart(12, '0')}`, version: `1.0.${i}` }),
    );
    const { r } = await render(many);
    expect(rows(r.root)).toHaveLength(50);
    const more = [...r.root.querySelectorAll('button')].find((b) => text(b) === 'Load more')!;
    more.click();
    await settle(r.fixture);
    expect(rows(r.root)).toHaveLength(60);
  });

  it('R-ANS-026 the refusal of the API is shown as it is, the 501 of an unbuilt operation included', async () => {
    const { r } = await render([], ansibleProblem(501, 'ansible_not_implemented', 'The operation is not built in this release yet.'));
    expect(text(r.root)).toContain('not built in this release');
  });

  it('R-ANS-026 the state is named in words', () => {
    expect(importStateText('completed')).toBe('Completed');
    expect(importStateText('failed')).toBe('Failed');
  });
});
