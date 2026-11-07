import { SessionApi } from '../../api/session-api';
import { FakeSessionApi } from '../../api/testing/fakes';
import { FakeProjectsApi, sampleProject } from '../../api/testing/fakes-projects';
import { ProjectsApi } from '../../api/projects-api';
import { seriousViolations } from '../../testing/axe';
import { buttonByText, renderScreen, settle, sleep, text, type, typeInto } from '../../testing/screen';
import { ProjectSettings } from './project-settings';
import { ProjectPoliciesPage, ProjectServiceAccountsPage } from './project-list-pages';
import { ProjectList } from './project-list';

const overlay = (): HTMLElement => document.querySelector('.cdk-overlay-container') as HTMLElement;

/** A caller without any capability that is Owner of `web` (Q-AUTHZ-181). */
function asProjectAdministrator() {
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
  it('R-UI-041 lists key, display name, Owners, members, repositories and the own role, and passes axe', async () => {
    const { root } = await renderScreen(ProjectList, {
      setup: (get) => {
        (get(ProjectsApi) as FakeProjectsApi).projects = [
          sampleProject({ repositories: 2, role: 'owner' }),
          sampleProject({ key: 'data', displayName: 'Data', owners: [], members: 0 }),
        ];
      },
    });
    const rows = root.querySelectorAll('tbody tr');
    expect(rows).toHaveLength(2);
    expect(text(rows[0]!)).toContain('data');
    expect(text(rows[1]!)).toContain('web');
    expect(text(rows[1]!)).toContain('Web platform');
    expect(text(rows[1]!)).toContain('group:web-leads');
    expect(text(rows[1]!)).toContain('Owner');
    expect(text(rows[1]!)).not.toContain('Policies: on');
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-STOR-049 shows the logical size each project is charged in a column of its own', async () => {
    const { root } = await renderScreen(ProjectList, {
      setup: (get) => {
        (get(ProjectsApi) as FakeProjectsApi).projects = [
          sampleProject({ logicalBytes: 3 * 1024 ** 3 }),
          sampleProject({ key: 'data', displayName: 'Data', logicalBytes: 0 }),
        ];
      },
    });
    const headers = [...root.querySelectorAll('th[scope="col"]')].map((h) => h.textContent?.trim());
    const column = headers.findIndex((h) => h?.startsWith('Storage'));
    expect(column).toBeGreaterThan(-1);
    const cells = (row: Element): string[] => [...row.querySelectorAll('th, td')].map((c) => text(c));
    const rows = root.querySelectorAll('tbody tr');
    expect(cells(rows[0]!)[column]).toBe('0 B');
    expect(cells(rows[1]!)[column]).toBe('3.0 GiB');
    expect(rows[1]!.querySelectorAll('td')[column - 1]!.getAttribute('title')).toBe(`${3 * 1024 ** 3} B`);
  });

  it('R-UI-041 an Owner reads the overview and edits its project, but creates none, with the reason', async () => {
    const { root } = await renderScreen(ProjectList, { capabilities: [], setup: asProjectAdministrator() });
    expect((buttonByText(root, 'New project') as HTMLButtonElement).disabled).toBe(true);
    expect(text(root)).toContain('Read only: changing this needs the permission projects:write');
    expect(root.textContent).toContain('Edit web');
  });

  it('R-UI-041 R-AUTHZ-026 creates a project with a first Owner group, then opens it', async () => {
    const { fixture, root, get } = await renderScreen(ProjectList);
    buttonByText(root, 'New project').click();
    await settle(fixture);
    const dialog = overlay().querySelector('trove-project-dialog') as HTMLElement;
    typeInto(dialog, 'Key', 'team-a');
    typeInto(dialog, 'Display name', 'Team A');
    typeInto(dialog, 'Find a user or group', 'team');
    await sleep(fixture, 400);
    buttonByText(dialog, 'group:team-web').click();
    await settle(fixture);
    buttonByText(dialog, 'Create project').click();
    await sleep(fixture, 100);
    const api = get(ProjectsApi) as FakeProjectsApi;
    expect(api.calls).toEqual(['create team-a']);
    expect(api.keys).toHaveLength(1);
    expect(api.projects.find((x) => x.key === 'team-a')?.owners).toEqual(['group:team-web']);
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
});

describe('Project settings (R-UI-041, Q-UI-129)', () => {
  it('R-UI-041 shows the facts of the project and passes axe', async () => {
    const { root } = await renderScreen(ProjectSettings, { inputs: { project: 'web' } });
    expect(text(root)).toContain('Web platform');
    expect(text(root)).toContain('group:web-leads');
    expect([...root.querySelectorAll('h2')].some((h) => text(h) === 'General')).toBe(true);
    // The members, repositories, policies and service accounts have screens of their own (Q-UI-129).
    for (const heading of ['Members', 'Repositories', 'Permission policies', 'Service accounts']) {
      expect([...root.querySelectorAll('h2')].some((h) => text(h) === heading)).toBe(false);
    }
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-STOR-049 shows the logical size of the project with the exact bytes and what they count', async () => {
    const { root } = await renderScreen(ProjectSettings, {
      inputs: { project: 'web' },
      setup: (g) => {
        (g(ProjectsApi) as FakeProjectsApi).projects = [sampleProject({ logicalBytes: 1_610_612_736 })];
      },
    });
    const terms = [...root.querySelectorAll('dt')];
    const storage = terms.find((t) => text(t) === 'Storage');
    expect(storage).toBeTruthy();
    const value = storage!.nextElementSibling!;
    expect(text(value)).toBe('1.5 GiB');
    expect(value.getAttribute('title')).toContain('1,610,612,736 bytes');
    expect(value.getAttribute('title')).toContain('also where another project holds the same content');
  });

  it('R-UI-041 delete is offered only for an empty project, with the reason otherwise', async () => {
    const { root } = await renderScreen(ProjectSettings, {
      inputs: { project: 'web' },
      setup: (g) => {
        (g(ProjectsApi) as FakeProjectsApi).projects = [sampleProject({ serviceAccounts: 1 })];
      },
    });
    expect((buttonByText(root, 'Delete project') as HTMLButtonElement).disabled).toBe(true);
    expect(text(root)).toContain('no repository and no service account belongs to it');
  });

  it('R-UI-041 deletes an empty project after the key is typed', async () => {
    const { fixture, root, get } = await renderScreen(ProjectSettings, { inputs: { project: 'web' } });
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
    const { fixture, root, get } = await renderScreen(ProjectSettings, { inputs: { project: 'web' } });
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

  it('R-UI-041 R-AUTHZ-026 an Owner edits the project but cannot delete it', async () => {
    const { root } = await renderScreen(ProjectSettings, {
      inputs: { project: 'web' },
      capabilities: [],
      setup: asProjectAdministrator(),
    });
    expect(root.textContent).toContain('Edit project');
    expect(root.textContent).not.toContain('Delete project');
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-UI-041 the auditor reads the project and the edit action says what is missing', async () => {
    const { root } = await renderScreen(ProjectSettings, {
      inputs: { project: 'web' },
      capabilities: ['projects:read'],
      setup: (g) => {
        (g(SessionApi) as FakeSessionApi).meResult = { roles: ['auditor'], capabilities: ['projects:read'] };
      },
    });
    expect(text(root)).toContain('Read only: changing this needs the permission projects:write');
    expect(root.textContent).not.toContain('Edit project');
  });

  it('R-UI-041 a project that is not visible answers 404 and says so', async () => {
    const { root } = await renderScreen(ProjectSettings, { inputs: { project: 'nope' } });
    expect(text(root)).toContain('does not exist, or you may not see it');
  });
});

describe('The lists of a project (R-UI-041, R-AUTHZ-026, Q-UI-129)', () => {
  it('Q-UI-129 an Owner creates policies in the project and service accounts', async () => {
    const { root } = await renderScreen(ProjectPoliciesPage, {
      inputs: { project: 'web' },
      capabilities: [],
      setup: asProjectAdministrator(),
    });
    expect(buttonByText(root, 'Create policy').getAttribute('href')).toBe('/projects/web/policies/new');
    expect(text(root)).toContain('The project has no permission policy yet.');
    expect(await seriousViolations(root)).toEqual([]);
    const accounts = await renderScreen(ProjectServiceAccountsPage, {
      inputs: { project: 'web' },
      capabilities: [],
      setup: asProjectAdministrator(),
    });
    expect((buttonByText(accounts.root, 'Create service account') as HTMLButtonElement).disabled).toBe(false);
    expect(text(accounts.root)).toContain('The project has no service account yet.');
  });

  it('Q-UI-129 the auditor reads and every create action says what is missing', async () => {
    const { root } = await renderScreen(ProjectPoliciesPage, {
      inputs: { project: 'web' },
      capabilities: ['projects:read', 'policies:read'],
      setup: (g) => {
        (g(SessionApi) as FakeSessionApi).meResult = { roles: ['auditor'], capabilities: ['projects:read', 'policies:read'] };
      },
    });
    expect(text(root)).toContain('Read only: changing this needs the permission policies:write');
  });
});
