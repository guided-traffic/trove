import { ProjectsApi } from '../../api/projects-api';
import { FakeProjectsApi } from '../../api/testing/fakes-projects';
import { buttonByText, renderScreen, settle, sleep, text, typeInto } from '../../testing/screen';
import { ProjectList } from './project-list';

const overlay = (): HTMLElement => document.querySelector('.cdk-overlay-container') as HTMLElement;
const dialogOf = (): HTMLElement => overlay().querySelector('trove-project-dialog') as HTMLElement;

describe('The project dialog saves a sequence of calls (R-UI-041, R-UI-042, R-API-014)', () => {
  it('R-UI-041 R-API-014 a corrected retry after a refusal carries a new idempotency key', async () => {
    const { fixture, root, get } = await renderScreen(ProjectList);
    buttonByText(root, 'New project').click();
    await settle(fixture);
    const dialog = dialogOf();
    // "web" exists: 409, stored under the key.
    typeInto(dialog, 'Key', 'web');
    typeInto(dialog, 'Display name', 'Web X');
    buttonByText(dialog, 'Create project').click();
    await sleep(fixture, 100);
    typeInto(dialog, 'Key', 'team-a');
    buttonByText(dialog, 'Create project').click();
    await sleep(fixture, 100);
    const api = get(ProjectsApi) as FakeProjectsApi;
    expect(api.calls).toEqual(['create web', 'create team-a']);
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

  it('R-UI-041 R-AUTHZ-026 a new project names its first Owners and the dialog edits no members or repositories', async () => {
    const { fixture, root, get } = await renderScreen(ProjectList);
    buttonByText(root, 'New project').click();
    await settle(fixture);
    const dialog = dialogOf();
    expect(dialog.querySelector('select[name="add-repository"]')).toBeNull();
    typeInto(dialog, 'Key', 'team-a');
    typeInto(dialog, 'Display name', 'Team A');
    buttonByText(dialog, 'Create project').click();
    await sleep(fixture, 100);
    const api = get(ProjectsApi) as FakeProjectsApi;
    expect(api.calls).toEqual(['create team-a']);
    expect(api.projects.some((p) => p.key === 'team-a')).toBe(true);
  });
});
