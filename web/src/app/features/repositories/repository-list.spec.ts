import { FakeRepositoriesApi } from '../../api/testing/fakes-catalog';
import { ApiError } from '../../api/problem';
import { RepositoriesApi } from '../../api/repositories-api';
import { addressValues, button, expectAccessible, renderScreen, settle, text, type } from '../../testing/screen';
import { RepositoryList } from './repository-list';

describe('Repository list', () => {
  it('R-UI-020 R-UI-066 shows key, format, class, description, client address and activity, and has no serious violation', async () => {
    const { root } = await renderScreen(RepositoryList);
    const t = text(root);
    expect(t).toContain('generic-local');
    expect(t).toContain('Installers and build outputs');
    expect(addressValues(root)[0]).toContain('generic-local/');
    expect(root.querySelectorAll('tbody tr').length).toBe(2);
    expect(root.querySelector('table th[scope="col"]')).toBeTruthy();
    expect(
      root.querySelector('trove-address-field input[aria-label="Client address of generic-local"]'),
    ).toBeTruthy();
    expect(
      root.querySelector('trove-address-field trove-copy-button button[aria-label="Copy client address of generic-local"]'),
    ).toBeTruthy();
    await expectAccessible(root);
  });

  it('R-UI-065 R-UI-066 every address field of the list has one width: the column is sized to the longest address, no field to its own', async () => {
    const { root } = await renderScreen(RepositoryList);
    const addresses = addressValues(root);
    expect(addresses.length).toBe(2);
    expect(new Set(addresses.map((a) => a.length)).size).toBeGreaterThan(1);
    const table = root.querySelector<HTMLElement>('trove-data-table')!;
    expect(table.style.getPropertyValue('--trove-address-chars')).toBe(String(Math.max(...addresses.map((a) => a.length))));
    for (const input of root.querySelectorAll<HTMLInputElement>('trove-address-field input')) {
      expect(input.closest('td')?.classList.contains('address-cell')).toBe(true);
      expect(input.style.inlineSize).toBe('');
    }
  });

  it('R-UI-020 the description is the second line of the first column, and the row actions are one menu named for the row', async () => {
    const { root, fixture } = await renderScreen(RepositoryList);
    const first = [...root.querySelectorAll('tbody th[scope="row"]')].find((th) => th.textContent?.includes('generic-local'))!;
    const lines = [...first.querySelectorAll('.secondary')].map((s) => s.textContent?.replace(/\s+/g, ' ').trim());
    expect(lines.at(-1)).toContain('Installers and build outputs');
    const heads = [...root.querySelectorAll('thead th')].map((th) => th.textContent?.trim());
    expect(heads).not.toContain('Description');
    const row = first.closest('tr')!;
    // In the admin area the project, the format (its icon and its name, UI-A-24) and the class lead the line under the key,
    // so that the address keeps the room of two columns (Q-UI-325, UI-A-30).
    const kind = first.querySelector('.admin-kind')!;
    expect(kind.querySelector('a.project-link')?.getAttribute('href')).toBe('/projects/web');
    expect(kind.querySelector('.format-name trove-format-icon')).toBeTruthy();
    expect(kind.querySelector('.format-name span[translate="no"]')?.textContent).toBe('Generic');
    expect(kind.textContent).toContain('Local');
    expect(row.querySelector('td.kind-cell')).toBeNull();
    expect(heads.some((h) => h?.startsWith('Format'))).toBe(false);
    // No row has a last activity: the column is left out (a column of dashes says nothing).
    expect(heads).not.toContain('Last activity');
    expect(root.querySelector('trove-data-table')?.classList.contains('stack')).toBe(true);
    expect(row.querySelectorAll('td.actions button')).toHaveLength(1);
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
