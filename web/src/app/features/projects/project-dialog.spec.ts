import { ProjectsApi } from '../../api/projects-api';
import { RepositoriesApi } from '../../api/repositories-api';
import { FakeRepositoriesApi } from '../../api/testing/fakes-catalog';
import { FakeProjectsApi, sampleProject } from '../../api/testing/fakes-projects';
import { buttonByText, renderScreen, settle, sleep, text, type, typeInto } from '../../testing/screen';
import { ProjectList } from './project-list';

const overlay = (): HTMLElement => document.querySelector('.cdk-overlay-container') as HTMLElement;
const dialogOf = (): HTMLElement => overlay().querySelector('trove-project-dialog') as HTMLElement;

describe('The project dialog saves a sequence of calls (R-UI-041, R-UI-042, R-API-014)', () => {
  it('R-UI-041 R-API-014 a corrected retry after a refusal carries a new idempotency key', async () => {
    const { fixture, root, get } = await renderScreen(ProjectList);
    buttonByText(root, 'New project').click();
    await settle(fixture);
    const dialog = dialogOf();
    // "web-x" overlaps the existing project "web": 409, stored under the key.
    typeInto(dialog, 'Key', 'web-x');
    typeInto(dialog, 'Display name', 'Web X');
    buttonByText(dialog, 'Create project').click();
    await sleep(fixture, 100);
    typeInto(dialog, 'Key', 'team-a');
    buttonByText(dialog, 'Create project').click();
    await sleep(fixture, 100);
    const api = get(ProjectsApi) as FakeProjectsApi;
    expect(api.calls).toEqual(['create web-x', 'create team-a']);
    expect(new Set(api.keys).size).toBe(2);
    expect(api.projects.some((p) => p.key === 'team-a')).toBe(true);
  });

  it('R-UI-041 a blank or over-long display name is refused before any request', async () => {
    const { fixture, root, get } = await renderScreen(ProjectList);
    buttonByText(root, 'New project').click();
    await settle(fixture);
    const dialog = dialogOf();
    typeInto(dialog, 'Key', 'team-a');
    typeInto(dialog, 'Display name', '   ');
    buttonByText(dialog, 'Create project').click();
    await settle(fixture);
    expect(text(dialog)).toContain('The display name is required, 1 to 128 characters.');
    typeInto(dialog, 'Display name', 'x'.repeat(129));
    buttonByText(dialog, 'Create project').click();
    await settle(fixture);
    expect(text(dialog)).toContain('The display name is required, 1 to 128 characters.');
    expect((get(ProjectsApi) as FakeProjectsApi).calls).toEqual([]);
    typeInto(dialog, 'Display name', 'Team A');
    buttonByText(dialog, 'Create project').click();
    await sleep(fixture, 100);
    expect((get(ProjectsApi) as FakeProjectsApi).calls).toEqual(['create team-a']);
  });

  it('R-UI-041 an administrator or repository that is already gone (404) counts as removed', async () => {
    const { fixture, root, get } = await renderScreen(ProjectList, {
      setup: (g) => {
        (g(ProjectsApi) as FakeProjectsApi).projects = [sampleProject({ repositories: 1, admins: ['group:web-leads', 'user:alice'] })];
        (g(RepositoriesApi) as FakeRepositoriesApi).repositories[0]!.project = 'web';
      },
    });
    buttonByText(root, 'Edit web').click();
    await settle(fixture);
    const dialog = dialogOf();
    buttonByText(dialog, 'Remove administrator user:alice').click();
    buttonByText(dialog, 'Remove repository generic-local').click();
    await settle(fixture);
    // Someone else removed both while the dialog was open.
    const api = get(ProjectsApi) as FakeProjectsApi;
    api.projects = [sampleProject({ admins: ['group:web-leads'] })];
    (get(RepositoriesApi) as FakeRepositoriesApi).repositories[0]!.project = null;
    buttonByText(dialog, 'Save project').click();
    await sleep(fixture, 100);
    expect(api.calls).toEqual(['removeAdmin web user:alice', 'unassign web generic-local']);
    expect(overlay().querySelector('trove-project-dialog')).toBeNull();
  });

  it('R-UI-041 a repository that moved from the owner the dialog showed to another one is asked about, never moved silently', async () => {
    const { fixture, root, get } = await renderScreen(ProjectList, {
      setup: (g) => {
        (g(ProjectsApi) as FakeProjectsApi).projects = [
          sampleProject(),
          sampleProject({ key: 'data', displayName: 'Data' }),
          sampleProject({ key: 'ops', displayName: 'Ops' }),
        ];
        (g(RepositoriesApi) as FakeRepositoriesApi).repositories[0]!.project = 'data';
      },
    });
    buttonByText(root, 'Edit web').click();
    await settle(fixture);
    const dialog = dialogOf();
    type(dialog.querySelector<HTMLSelectElement>('select[name="add-repository"]')!, 'generic-local');
    await settle(fixture);
    expect(text(dialog)).toContain('moves from data');
    // The person saw "moves from data"; meanwhile the repository went to "ops".
    (get(RepositoriesApi) as FakeRepositoriesApi).repositories[0]!.project = 'ops';
    buttonByText(dialog, 'Save project').click();
    await sleep(fixture, 100);
    const api = get(ProjectsApi) as FakeProjectsApi;
    expect(api.calls).not.toContain('assign web generic-local move');
    const ask = overlay().querySelector('trove-confirm-dialog') as HTMLElement;
    expect(text(ask)).toContain('ops');
    buttonByText(ask, 'Move repository').click();
    await sleep(fixture, 100);
    expect(api.calls).toContain('assign web generic-local move');
  });

  it('R-UI-041 the owner the person saw is moved from without a second question', async () => {
    const { fixture, root, get } = await renderScreen(ProjectList, {
      setup: (g) => {
        (g(ProjectsApi) as FakeProjectsApi).projects = [sampleProject(), sampleProject({ key: 'data', displayName: 'Data' })];
        (g(RepositoriesApi) as FakeRepositoriesApi).repositories[0]!.project = 'data';
      },
    });
    buttonByText(root, 'Edit web').click();
    await settle(fixture);
    const dialog = dialogOf();
    type(dialog.querySelector<HTMLSelectElement>('select[name="add-repository"]')!, 'generic-local');
    await settle(fixture);
    buttonByText(dialog, 'Save project').click();
    await sleep(fixture, 100);
    expect(overlay().querySelector('trove-confirm-dialog')).toBeNull();
    expect((get(ProjectsApi) as FakeProjectsApi).calls).toContain('assign web generic-local move');
  });

  it('R-UI-041 a 409 inside the window after the refresh still asks', async () => {
    const { fixture, root, get } = await renderScreen(ProjectList, {
      setup: (g) => {
        (g(ProjectsApi) as FakeProjectsApi).projects = [sampleProject(), sampleProject({ key: 'data', displayName: 'Data' })];
      },
    });
    buttonByText(root, 'Edit web').click();
    await settle(fixture);
    const dialog = dialogOf();
    type(dialog.querySelector<HTMLSelectElement>('select[name="add-repository"]')!, 'generic-local');
    await settle(fixture);
    // The refresh before the assignment still reads "unassigned"; the assignment then finds it taken.
    const repos = get(RepositoriesApi) as FakeRepositoriesApi;
    const listAll = repos.listAll.bind(repos);
    let reads = 0;
    repos.listAll = async () => {
      const real = await listAll();
      if (++reads > 1) return real;
      repos.repositories[0]!.project = 'data';
      return real.map((r) => ({ ...r, project: null }));
    };
    buttonByText(dialog, 'Save project').click();
    await sleep(fixture, 100);
    const ask = overlay().querySelector('trove-confirm-dialog') as HTMLElement;
    expect(text(ask)).toContain('data');
    buttonByText(ask, 'Move repository').click();
    await sleep(fixture, 100);
    expect((get(ProjectsApi) as FakeProjectsApi).calls).toContain('assign web generic-local move');
  });

  async function openWithRepositoryAssignedElsewhere() {
    const screen = await renderScreen(ProjectList, {
      setup: (g) => {
        (g(ProjectsApi) as FakeProjectsApi).projects = [sampleProject(), sampleProject({ key: 'data', displayName: 'Data' })];
      },
    });
    buttonByText(screen.root, 'Edit web').click();
    await settle(screen.fixture);
    const dialog = dialogOf();
    type(dialog.querySelector<HTMLSelectElement>('select[name="add-repository"]')!, 'generic-local');
    await settle(screen.fixture);
    // Assigned to "data" after the dialog loaded its summaries.
    (screen.get(RepositoriesApi) as FakeRepositoriesApi).repositories[0]!.project = 'data';
    return { ...screen, dialog };
  }

  it('R-UI-041 a 409 repository_assigned reloads the repository and asks whether to move it', async () => {
    const { fixture, dialog, get } = await openWithRepositoryAssignedElsewhere();
    buttonByText(dialog, 'Save project').click();
    await sleep(fixture, 100);
    const ask = overlay().querySelector('trove-confirm-dialog') as HTMLElement;
    expect(ask).toBeTruthy();
    expect(text(ask)).toContain('generic-local');
    expect(text(ask)).toContain('data');
    buttonByText(ask, 'Move repository').click();
    await sleep(fixture, 100);
    const api = get(ProjectsApi) as FakeProjectsApi;
    expect(api.calls).toContain('assign web generic-local move');
    expect((get(RepositoriesApi) as FakeRepositoriesApi).repositories[0]!.project).toBe('web');
    expect(overlay().querySelector('trove-project-dialog')).toBeNull();
  });

  it('R-UI-041 declining the move leaves the dialog open and the repository where it is', async () => {
    const { fixture, dialog, get } = await openWithRepositoryAssignedElsewhere();
    buttonByText(dialog, 'Save project').click();
    await sleep(fixture, 100);
    buttonByText(overlay().querySelector('trove-confirm-dialog') as HTMLElement, 'Cancel').click();
    await sleep(fixture, 100);
    expect(overlay().querySelector('trove-project-dialog')).toBeTruthy();
    expect((get(RepositoriesApi) as FakeRepositoriesApi).repositories[0]!.project).toBe('data');
    expect((get(ProjectsApi) as FakeProjectsApi).calls).not.toContain('assign web generic-local move');
  });

  it('R-UI-041 the baseline that arrives late does not overwrite a repository added early', async () => {
    const { fixture, root, get } = await renderScreen(ProjectList, {
      setup: (g) => {
        (g(ProjectsApi) as FakeProjectsApi).projects = [sampleProject({ repositories: 1 })];
        const repos = g(RepositoriesApi) as FakeRepositoriesApi;
        repos.repositories[0]!.project = null;
        repos.repositories[1]!.project = 'web';
        const list = repos.list.bind(repos);
        repos.list = (filters) => {
          const fetch = list(filters);
          return async (cursor, limit) => {
            await new Promise((resolve) => setTimeout(resolve, 150));
            return fetch(cursor, limit);
          };
        };
      },
    });
    buttonByText(root, 'Edit web').click();
    await settle(fixture);
    const dialog = dialogOf();
    type(dialog.querySelector<HTMLSelectElement>('select[name="add-repository"]')!, 'generic-local');
    await sleep(fixture, 300);
    expect(text(dialog)).toContain('generic-local');
    expect(text(dialog)).toContain('releases');
    buttonByText(dialog, 'Save project').click();
    await sleep(fixture, 100);
    expect((get(ProjectsApi) as FakeProjectsApi).calls).toEqual(['assign web generic-local']);
  });
});
