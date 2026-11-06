import { SessionApi } from '../../api/session-api';
import { FakeSessionApi } from '../../api/testing/fakes';
import { FakeProjectsApi, sampleProject } from '../../api/testing/fakes-projects';
import { FakeRepositoriesApi } from '../../api/testing/fakes-catalog';
import { ProjectsApi } from '../../api/projects-api';
import { RepositoriesApi } from '../../api/repositories-api';
import { seriousViolations } from '../../testing/axe';
import { buttonByText, renderScreen, settle, sleep, text, type, typeInto } from '../../testing/screen';
import { ProjectDetail } from './project-detail';
import { ProjectList } from './project-list';

const overlay = (): HTMLElement => document.querySelector('.cdk-overlay-container') as HTMLElement;

/** A caller without any capability that administers `web` (Q-AUTHZ-181). */
function asProjectAdministrator(managePolicies = true, manageRepositories = false) {
  return (get: <A>(token: abstract new (...args: never[]) => A) => A): void => {
    (get(SessionApi) as FakeSessionApi).meResult = {
      roles: [],
      capabilities: [],
      principals: ['user:alice'],
      projects: [{ key: 'web', role: 'owner' }],
    };
  };
}

describe('Projects overview (R-UI-041)', () => {
  it('R-UI-041 lists key, display name, administrators, repositories and the switches, and passes axe', async () => {
    const { root } = await renderScreen(ProjectList, {
      setup: (get) => {
        (get(ProjectsApi) as FakeProjectsApi).projects = [
          sampleProject({ repositories: 2, manageRepositories: true }),
          sampleProject({ key: 'data', displayName: 'Data', admins: [], managePolicies: false }),
        ];
      },
    });
    const rows = root.querySelectorAll('tbody tr');
    expect(rows).toHaveLength(2);
    expect(text(rows[0]!)).toContain('data');
    expect(text(rows[1]!)).toContain('web');
    expect(text(rows[1]!)).toContain('Web platform');
    expect(text(rows[1]!)).toContain('group:web-leads');
    expect(text(rows[1]!)).toContain('Policies: on');
    expect(text(rows[1]!)).toContain('Repositories: on');
    expect(text(rows[0]!)).toContain('Policies: off');
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-UI-041 a project administrator reads the overview: no creating, no editing, with the reason', async () => {
    const { root } = await renderScreen(ProjectList, { capabilities: [], setup: asProjectAdministrator() });
    expect((buttonByText(root, 'New project') as HTMLButtonElement).disabled).toBe(true);
    expect(text(root)).toContain('Read only: changing this needs the permission projects:write');
    expect(root.textContent).not.toContain('Edit web');
  });

  it('R-UI-041 creates a project with an administrator group and a repository, then opens it', async () => {
    const { fixture, root, get } = await renderScreen(ProjectList);
    buttonByText(root, 'New project').click();
    await settle(fixture);
    const dialog = overlay().querySelector('trove-project-dialog') as HTMLElement;
    typeInto(dialog, 'Key', 'team-a');
    typeInto(dialog, 'Display name', 'Team A');
    // The administrator is found through the identity lists (the fake knows the group team-web).
    typeInto(dialog, 'Find a user or group', 'team');
    await sleep(fixture, 400);
    buttonByText(dialog, 'group:team-web').click();
    await settle(fixture);
    type(dialog.querySelector<HTMLSelectElement>('select[name="add-repository"]')!, 'generic-local');
    await settle(fixture);
    expect(text(dialog)).toContain('generic-local');
    buttonByText(dialog, 'Create project').click();
    await sleep(fixture, 100);
    const api = get(ProjectsApi) as FakeProjectsApi;
    expect(api.calls).toEqual(['create team-a', 'addAdmin team-a group:team-web', 'assign team-a generic-local']);
    expect(api.keys).toHaveLength(1);
    expect((get(RepositoriesApi) as FakeRepositoriesApi).repositories.find((r) => r.key === 'generic-local')?.project).toBe('team-a');
  });

  it('R-UI-041 a key that does not match the pattern is refused before any request', async () => {
    const { fixture, root, get } = await renderScreen(ProjectList);
    buttonByText(root, 'New project').click();
    await settle(fixture);
    const dialog = overlay().querySelector('trove-project-dialog') as HTMLElement;
    typeInto(dialog, 'Key', 'Team_A');
    buttonByText(dialog, 'Create project').click();
    await settle(fixture);
    expect(text(dialog)).toContain('The key does not match the pattern.');
    expect((get(ProjectsApi) as FakeProjectsApi).calls).toEqual([]);
  });

  it('R-UI-041 R-UI-042 an edit sends If-Match; a 412 opens the conflict dialog', async () => {
    const { fixture, root, get } = await renderScreen(ProjectList);
    const api = get(ProjectsApi) as FakeProjectsApi;
    buttonByText(root, 'Edit web').click();
    await settle(fixture);
    let dialog = overlay().querySelector('trove-project-dialog') as HTMLElement;
    expect(dialog.querySelector<HTMLInputElement>('input[name="key"]')!.readOnly).toBe(true);
    typeInto(dialog, 'Display name', 'Web team');
    buttonByText(dialog, 'Save project').click();
    await sleep(fixture, 100);
    expect(api.ifMatch).toEqual(['"1"']);
    expect(api.projects[0]?.displayName).toBe('Web team');

    // Someone else changes it while the dialog is open.
    buttonByText(root, 'Edit web').click();
    await settle(fixture);
    dialog = overlay().querySelector('trove-project-dialog') as HTMLElement;
    api.bump('web');
    typeInto(dialog, 'Display name', 'Web crew');
    buttonByText(dialog, 'Save project').click();
    await settle(fixture);
    expect(overlay().querySelector('trove-conflict-dialog')).toBeTruthy();
  });

  it('R-UI-041 removing an administrator and unassigning a repository are separate calls', async () => {
    const { fixture, root, get } = await renderScreen(ProjectList, {
      setup: (g) => {
        (g(ProjectsApi) as FakeProjectsApi).projects = [sampleProject({ repositories: 1, admins: ['group:web-leads', 'user:alice'] })];
        (g(RepositoriesApi) as FakeRepositoriesApi).repositories[0]!.project = 'web';
      },
    });
    buttonByText(root, 'Edit web').click();
    await settle(fixture);
    const dialog = overlay().querySelector('trove-project-dialog') as HTMLElement;
    buttonByText(dialog, 'Remove administrator user:alice').click();
    buttonByText(dialog, 'Remove repository generic-local').click();
    await settle(fixture);
    buttonByText(dialog, 'Save project').click();
    await sleep(fixture, 100);
    expect((get(ProjectsApi) as FakeProjectsApi).calls).toEqual(['removeAdmin web user:alice', 'unassign web generic-local']);
  });

  it('R-UI-041 a repository of another project is listed with it and is moved only on purpose', async () => {
    const { fixture, root, get } = await renderScreen(ProjectList, {
      setup: (g) => {
        (g(ProjectsApi) as FakeProjectsApi).projects = [sampleProject(), sampleProject({ key: 'data', displayName: 'Data', repositories: 1 })];
        (g(RepositoriesApi) as FakeRepositoriesApi).repositories[0]!.project = 'data';
      },
    });
    buttonByText(root, 'Edit web').click();
    await settle(fixture);
    const dialog = overlay().querySelector('trove-project-dialog') as HTMLElement;
    const option = [...dialog.querySelectorAll('option')].find((o) => o.value === 'generic-local')!;
    expect(option.textContent).toContain('belongs to data');
    type(dialog.querySelector<HTMLSelectElement>('select[name="add-repository"]')!, 'generic-local');
    await settle(fixture);
    expect(text(dialog)).toContain('moves from data');
    buttonByText(dialog, 'Save project').click();
    await sleep(fixture, 100);
    expect((get(ProjectsApi) as FakeProjectsApi).calls).toContain('assign web generic-local move');
  });
});

describe('Project detail (R-UI-041)', () => {
  it('R-UI-041 shows the overview and the three sections, and passes axe', async () => {
    const { root } = await renderScreen(ProjectDetail, { inputs: { key: 'web' } });
    expect(text(root)).toContain('Web platform');
    expect(text(root)).toContain('group:web-leads');
    for (const heading of ['General', 'Repositories', 'Permission policies', 'Service accounts']) {
      expect([...root.querySelectorAll('h2')].some((h) => text(h) === heading)).toBe(true);
    }
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-UI-041 delete is offered only for an empty project, with the reason otherwise', async () => {
    const { root } = await renderScreen(ProjectDetail, {
      inputs: { key: 'web' },
      setup: (g) => {
        (g(ProjectsApi) as FakeProjectsApi).projects = [sampleProject({ serviceAccounts: 1 })];
      },
    });
    expect((buttonByText(root, 'Delete project') as HTMLButtonElement).disabled).toBe(true);
    expect(text(root)).toContain('no repository and no service account belongs to it');
  });

  it('R-UI-041 deletes an empty project after the key is typed', async () => {
    const { fixture, root, get } = await renderScreen(ProjectDetail, { inputs: { key: 'web' } });
    buttonByText(root, 'Delete project').click();
    await settle(fixture);
    const confirm = overlay().querySelector('trove-confirm-dialog') as HTMLElement;
    type(confirm.querySelector<HTMLInputElement>('#confirm-name')!, 'web');
    await settle(fixture);
    buttonByText(confirm, 'Delete project').click();
    await sleep(fixture, 100);
    expect((get(ProjectsApi) as FakeProjectsApi).calls).toEqual(['delete web']);
  });

  it('R-UI-041 explains 409 project_not_empty when something arrived since the page was read', async () => {
    const { fixture, root, get } = await renderScreen(ProjectDetail, { inputs: { key: 'web' } });
    const api = get(ProjectsApi) as FakeProjectsApi;
    api.failure = new (await import('../../api/problem')).ApiError(409, { code: 'project_not_empty' }, 'req-1');
    buttonByText(root, 'Delete project').click();
    await settle(fixture);
    const confirm = overlay().querySelector('trove-confirm-dialog') as HTMLElement;
    type(confirm.querySelector<HTMLInputElement>('#confirm-name')!, 'web');
    await settle(fixture);
    buttonByText(confirm, 'Delete project').click();
    await sleep(fixture, 100);
    expect(text(root)).toContain('still has repositories or service accounts');
  });

  it('R-UI-041 R-AUTHZ-026 a project administrator reads the project and acts within its switches', async () => {
    const { root } = await renderScreen(ProjectDetail, {
      inputs: { key: 'web' },
      capabilities: [],
      setup: asProjectAdministrator(true, false),
    });
    // No edit, no delete.
    expect(root.textContent).not.toContain('Edit project');
    expect(root.textContent).not.toContain('Delete project');
    // manageRepositories is off: the create action is disabled and says why.
    const create = buttonByText(root, 'Create repository') as HTMLButtonElement;
    expect(create.disabled).toBe(true);
    expect(text(root)).toContain('Switched off for this project: manageRepositories');
    // managePolicies is on: the policy and the service-account actions work.
    expect(buttonByText(root, 'Create policy').getAttribute('href')).toContain('/admin/permissions/new?project=web');
    expect((buttonByText(root, 'Create service account') as HTMLButtonElement).disabled).toBe(false);
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-UI-041 with manageRepositories on, creating a repository opens the wizard of the project', async () => {
    const { root } = await renderScreen(ProjectDetail, {
      inputs: { key: 'web' },
      capabilities: [],
      setup: asProjectAdministrator(false, true),
    });
    expect(buttonByText(root, 'Create repository').getAttribute('href')).toContain('/admin/repositories/new?project=web');
    // managePolicies is off: policies and service accounts are disabled with the reason.
    expect((buttonByText(root, 'Create service account') as HTMLButtonElement).disabled).toBe(true);
    expect(text(root)).toContain('Switched off for this project: managePolicies');
  });

  it('R-UI-041 the auditor reads every project and every create action says what is missing', async () => {
    const { root } = await renderScreen(ProjectDetail, {
      inputs: { key: 'web' },
      capabilities: ['projects:read', 'policies:read', 'identities:read', 'repositories:read-config'],
      setup: (g) => {
        (g(SessionApi) as FakeSessionApi).meResult = { roles: ['auditor'], capabilities: ['projects:read', 'policies:read', 'identities:read', 'repositories:read-config'] };
      },
    });
    expect(text(root)).toContain('Read only: changing this needs the permission projects:write');
    expect(text(root)).toContain('Read only: changing this needs the permission policies:write');
    expect((buttonByText(root, 'Create repository') as HTMLButtonElement).disabled).toBe(true);
  });

  it('R-UI-041 lists the project\'s repositories, policies and service accounts', async () => {
    const { root } = await renderScreen(ProjectDetail, {
      inputs: { key: 'web' },
      setup: (g) => {
        (g(RepositoriesApi) as FakeRepositoriesApi).repositories[0]!.project = 'web';
      },
    });
    expect(text(root.querySelector('trove-project-repositories')!)).toContain('generic-local');
    expect(text(root.querySelector('trove-project-policies')!)).toContain('The project has no permission policy yet.');
    expect(text(root.querySelector('trove-project-service-accounts')!)).toContain('The project has no service account yet.');
  });

  it('R-UI-041 a project that is not visible answers 404 and says so', async () => {
    const { root } = await renderScreen(ProjectDetail, { inputs: { key: 'nope' } });
    expect(text(root)).toContain('does not exist, or you may not see it');
  });
});
