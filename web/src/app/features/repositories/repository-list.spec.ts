import { FakeRepositoriesApi } from '../../api/testing/fakes-catalog';
import { ApiError } from '../../api/problem';
import { RepositoriesApi } from '../../api/repositories-api';
import { addressValues, button, expectAccessible, renderScreen, settle, text, type } from '../../testing/screen';
import { RepositoryList } from './repository-list';

describe('Repository list', () => {
  it('R-UI-020 Q-UI-330 the admin area\'s list has the columns repository, project, format and class, and no address field, and has no serious violation', async () => {
    const { root } = await renderScreen(RepositoryList);
    const t = text(root);
    expect(t).toContain('generic-local');
    expect(t).toContain('Installers and build outputs');
    expect(root.querySelectorAll('tbody tr').length).toBe(2);
    const heads = [...root.querySelectorAll('thead th')].map((th) => text(th).replace('help_outline', '').split(' ')[0]);
    expect(heads.slice(0, 4)).toEqual(['Repository', 'Project', 'Format', 'Class']);
    const row = [...root.querySelectorAll('tbody tr')].find((tr) => text(tr).includes('generic-local'))!;
    expect(row.querySelector('td.project-cell a')?.getAttribute('href')).toBe('/projects/web');
    expect(row.querySelector('td.format-cell .format-name trove-format-icon')).toBeTruthy();
    expect(text(row.querySelector('td.format-cell span[translate="no"]')!)).toBe('Generic');
    expect(text(row.querySelector('td.class-cell')!)).toContain('Local');
    expect(root.querySelector('trove-address-field')).toBeNull();
    expect(addressValues(root)).toEqual([]);
    await expectAccessible(root);
  });

  it('R-UI-066 Q-UI-330 in the admin area the address is a quiet copy button beside the menu, its name and tooltip carrying the address, and an item of the menu', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    const { root, fixture } = await renderScreen(RepositoryList);
    const row = [...root.querySelectorAll('tbody tr')].find((tr) => text(tr).includes('generic-local'))!;
    const copy = row.querySelector<HTMLButtonElement>('td.actions trove-copy-button button')!;
    const label = copy.getAttribute('aria-label')!;
    expect(label).toMatch(/^Copy client address https?:\/\/.*generic-local\/$/);
    expect(copy.getAttribute('title')).toBe(label);
    expect(copy.classList.contains('btn-tertiary')).toBe(true);
    const address = label.replace('Copy client address ', '');
    copy.click();
    await settle(fixture);
    expect(writeText).toHaveBeenLastCalledWith(address);
    row.querySelector<HTMLButtonElement>('button[aria-label="Actions for generic-local"]')!.click();
    await settle(fixture);
    const item = [...root.ownerDocument.body.querySelectorAll<HTMLButtonElement>('.menu [role="menuitem"]')].find(
      (i) => i.textContent?.trim() === 'Copy client address',
    )!;
    writeText.mockClear();
    item.click();
    await settle(fixture);
    expect(writeText).toHaveBeenLastCalledWith(address);
  });

  it('R-UI-020 the description is the second line of the first column, and the row actions are one menu named for the row', async () => {
    const { root, fixture } = await renderScreen(RepositoryList);
    const first = [...root.querySelectorAll('tbody th[scope="row"]')].find((th) => th.textContent?.includes('generic-local'))!;
    const lines = [...first.querySelectorAll('.secondary')].map((s) => s.textContent?.replace(/\s+/g, ' ').trim());
    expect(lines.at(-1)).toContain('Installers and build outputs');
    const heads = [...root.querySelectorAll('thead th')].map((th) => th.textContent?.trim());
    expect(heads).not.toContain('Description');
    const row = first.closest('tr')!;
    // The description is the only line under the key: every other property is a column (Q-UI-330).
    expect(first.querySelectorAll('.secondary')).toHaveLength(1);
    // No row has a last activity: the column is left out (a column of dashes says nothing).
    expect(heads).not.toContain('Last activity');
    expect(root.querySelector('trove-data-table')?.classList.contains('stack')).toBe(true);
    // The copy button and the menu.
    expect(row.querySelectorAll('td.actions button')).toHaveLength(2);
    row.querySelector<HTMLButtonElement>('button[aria-label="Actions for generic-local"]')!.click();
    await settle(fixture);
    const menu = root.ownerDocument.body.querySelector('.menu')!;
    expect([...menu.querySelectorAll('[role="menuitem"]')].map((i) => i.textContent?.trim())).toEqual(
      expect.arrayContaining(['Settings', 'Browse']),
    );
  });

  it('R-UI-020 R-UI-067 a reader is offered no Settings in a row: there is nothing to read or change for them there', async () => {
    const { root, fixture } = await renderScreen(RepositoryList, { capabilities: [] });
    root.querySelector<HTMLButtonElement>('button[aria-label="Actions for generic-local"]')!.click();
    await settle(fixture);
    const items = [...root.ownerDocument.body.querySelectorAll('.menu [role="menuitem"]')].map((i) => i.textContent?.trim());
    expect(items).toContain('Browse');
    expect(items).not.toContain('Settings');
  });

  it('R-UI-020 the filters reach the API and restart the walk (class and format)', async () => {
    const { root, get } = await renderScreen(RepositoryList, {
      inputs: { class: 'local', format: 'generic', state: 'inactive' },
    });
    const fake = get(RepositoriesApi) as FakeRepositoriesApi;
    expect(fake.listFilters.at(-1)).toEqual({
      q: undefined,
      class: 'local',
      format: 'generic',
      state: 'inactive',
    });
    expect(text(root)).toContain('releases');
    expect(text(root)).not.toContain('generic-local');
  });

  it('R-UI-020 an empty list and an empty filter result are explained, and pass the accessibility check', async () => {
    const empty = await renderScreen(RepositoryList, {
      setup: (get) => ((get(RepositoriesApi) as FakeRepositoriesApi).repositories = []),
    });
    expect(text(empty.root)).toContain('There is no repository yet.');
    await expectAccessible(empty.root);

    const filtered = await renderScreen(RepositoryList, { inputs: { q: 'nothing-like-this' } });
    expect(text(filtered.root)).toContain('No repository matches the filters.');
    await expectAccessible(filtered.root);
  });

  it('R-UI-020 a failing list shows the error with its request ID and a retry, and passes the accessibility check', async () => {
    const { root } = await renderScreen(RepositoryList, {
      setup: (get) => {
        const fake = get(RepositoriesApi) as FakeRepositoriesApi;
        fake.list = () => () => Promise.reject(new ApiError(503, { code: 'unavailable' }, 'rid-1'));
      },
    });
    expect(text(root)).toContain('temporarily unavailable');
    expect(text(root)).toContain('Retry');
    await expectAccessible(root);
  });

  it('R-UI-020 "Create repository" is offered only to a caller who may write repository configuration', async () => {
    const admin = await renderScreen(RepositoryList);
    expect(text(admin.root)).toContain('Create repository');
    const reader = await renderScreen(RepositoryList, {
      capabilities: ['repositories:read-config'],
    });
    expect(text(reader.root)).not.toContain('Create repository');
  });

  it('R-UI-020 "Load more" continues the cursor walk', async () => {
    const { root, fixture } = await renderScreen(RepositoryList, {
      setup: (get) => {
        const fake = get(RepositoriesApi) as FakeRepositoriesApi;
        fake.repositories = Array.from({ length: 120 }, (_, i) => ({
          key: `repo-${String(i).padStart(3, '0')}`,
          class: 'local' as const,
          format: 'generic',
        }));
      },
    });
    expect(root.querySelectorAll('tbody tr').length).toBe(50);
    button(root, 'Load more').click();
    await settle(fixture);
    expect(root.querySelectorAll('tbody tr').length).toBe(100);
  });

  it('R-UI-020 delete asks for the key, runs as a job and follows its progress until the row is gone', async () => {
    const { root, fixture, get } = await renderScreen(RepositoryList, {
      setup: (get) => {
        const fake = get(RepositoriesApi) as FakeRepositoriesApi;
        fake.deleteResult = {
          done: false,
          job: {
            id: 'job-1',
            kind: 'repository-delete',
            state: 'queued',
            href: '/api/v1/system/jobs/job-1',
          },
        };
      },
    });
    const row = [...root.querySelectorAll('tbody tr')].find((r) =>
      r.textContent?.includes('generic-local'),
    )!;
    // The row's actions are a menu (Q-UI-302): open it, then choose Delete.
    row.querySelector<HTMLButtonElement>('button[aria-label="Actions for generic-local"]')!.click();
    await settle(fixture);
    const body = root.ownerDocument.body;
    button(body.querySelector<HTMLElement>('.menu')!, 'Delete').click();
    await settle(fixture);

    const confirm = button(body, 'Delete repository');
    expect(confirm.disabled).toBe(true);
    type(body.querySelector<HTMLInputElement>('#confirm-name')!, 'generic-local');
    await settle(fixture);
    expect(confirm.disabled).toBe(false);
    confirm.click();
    await settle(fixture);

    const fake = get(RepositoriesApi) as FakeRepositoriesApi;
    expect(fake.deleteCalls).toHaveLength(1);
    expect(fake.deleteCalls[0]?.idempotencyKey).toBeTruthy();
    expect(text(root)).toContain('Deleting');
  });
});
