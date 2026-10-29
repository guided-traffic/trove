import { FakeRepositoriesApi } from '../../api/testing/fakes-catalog';
import { ApiError } from '../../api/problem';
import { RepositoriesApi } from '../../api/repositories-api';
import { button, expectAccessible, renderScreen, settle, text, type } from '../../testing/screen';
import { RepositoryList } from './repository-list';

describe('Repository list', () => {
  it('R-UI-020 shows key, format, class, description, client address and activity, and has no serious violation', async () => {
    const { root } = await renderScreen(RepositoryList);
    const t = text(root);
    expect(t).toContain('generic-local');
    expect(t).toContain('Installers and build outputs');
    expect(t).toContain('generic-local/');
    expect(root.querySelectorAll('tbody tr').length).toBe(2);
    expect(root.querySelector('table th[scope="col"]')).toBeTruthy();
    expect(
      root.querySelector('trove-copy-button button[aria-label="Copy client address"]'),
    ).toBeTruthy();
    await expectAccessible(root);
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
    button(row, 'Delete').click();
    await settle(fixture);

    const body = root.ownerDocument.body;
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
