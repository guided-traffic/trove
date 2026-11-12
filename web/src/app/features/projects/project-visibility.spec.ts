import { ProjectsApi } from '../../api/projects-api';
import { RepositoriesApi } from '../../api/repositories-api';
import { SessionApi } from '../../api/session-api';
import { FakeSessionApi } from '../../api/testing/fakes';
import { FakeRepositoriesApi } from '../../api/testing/fakes-catalog';
import { FakeProjectsApi, FakeVisibilityApi, sampleProject } from '../../api/testing/fakes-projects';
import { VisibilityApi } from '../../api/visibility-api';
import { button, buttonByText, expectAccessible, renderScreen, settle, text } from '../../testing/screen';
import { ProjectDialog } from './project-dialog';
import { ProjectSettings } from './project-settings';
import { ProjectsHome } from './projects-home';
import { TroveDialog } from '../../shared/ui/dialog';
import { TestBed } from '@angular/core/testing';

const overlay = (): HTMLElement => document.querySelector('.cdk-overlay-container') as HTMLElement;

type Level = 'private' | 'internal' | 'public';

async function openSettings(level: Level, capabilities?: readonly string[], repositories: FakeRepositoriesApi['repositories'] = []) {
  const r = await renderScreen(ProjectSettings, {
    inputs: { project: 'web' },
    ...(capabilities ? { capabilities } : {}),
    setup: (get) => {
      (get(ProjectsApi) as FakeProjectsApi).projects = [sampleProject({ visibility: level })];
      (get(RepositoriesApi) as FakeRepositoriesApi).repositories = repositories;
      if (capabilities && capabilities.length === 0) {
        (get(SessionApi) as FakeSessionApi).meResult = {
          roles: [],
          capabilities: [],
          principals: ['user:alice'],
          projects: [{ key: 'web', role: 'owner' }],
        };
      }
    },
  });
  const radios = () => [...r.root.querySelectorAll<HTMLInputElement>('trove-visibility-level input[type="radio"]')];
  return { ...r, radios, api: r.get(ProjectsApi) as FakeProjectsApi };
}

describe('The visibility of a project (R-UI-054)', () => {
  it('R-UI-054 the settings page has the section Visibility with the level in its summary, and passes axe', async () => {
    const r = await openSettings('internal');
    expect(text(r.root)).toContain('This project is internal.');
    expect(r.radios().map((x) => x.checked)).toEqual([false, true, false]);
    buttonByText(r.root, 'Visibility').click();
    await expectAccessible(r.root);
  });

  it('R-UI-054 raising the level asks nothing and sends the new level through its own operation with the loaded ETag', async () => {
    const r = await openSettings('private');
    r.radios()[1]!.click();
    await settle(r.fixture);
    button(r.root, /Save changes\s+\(visibility\)/).click();
    await settle(r.fixture);
    expect(overlay()?.querySelector('trove-confirm-dialog') ?? null).toBeNull();
    expect(r.api.calls).toContain('setVisibility web internal');
    expect(r.api.ifMatch).toEqual(['"1"']);
    expect(text(r.root)).toContain('This project is internal.');
  });

  it('R-UI-054 lowering lists the repositories it lowers, asks first, and names the count after the answer', async () => {
    const r = await openSettings('public', undefined, [
      { key: 'npm-public', project: 'web', class: 'local', format: 'npm', visibility: { artifacts: 'public', wiki: 'internal' } },
      { key: 'npm-team', project: 'web', class: 'local', format: 'npm', visibility: { artifacts: 'internal', wiki: 'internal' } },
      { key: 'npm-private', project: 'web', class: 'local', format: 'npm', visibility: { artifacts: 'private', wiki: 'private' } },
    ]);
    r.api.loweredNext = [{ repository: 'web/npm-public', before: { artifacts: 'public', wiki: 'internal' } }];
    r.radios()[1]!.click();
    await settle(r.fixture);
    button(r.root, /Save changes\s+\(visibility\)/).click();
    await settle(r.fixture);
    const dialog = overlay().querySelector('trove-confirm-dialog') as HTMLElement;
    expect(dialog).toBeTruthy();
    expect(text(dialog)).toContain('Anyone who reads them only through their visibility loses access.');
    expect([...dialog.querySelectorAll('li')].map((li) => text(li))).toEqual(['npm-public: Public / Internal → Internal']);
    expect(r.api.calls).not.toContain('setVisibility web internal');
    buttonByText(dialog, 'Lower the visibility').click();
    await settle(r.fixture);
    expect(r.api.calls).toContain('setVisibility web internal');
  });

  it('R-UI-054 cancelling the confirmation changes nothing', async () => {
    const r = await openSettings('public');
    r.radios()[0]!.click();
    await settle(r.fixture);
    button(r.root, /Save changes\s+\(visibility\)/).click();
    await settle(r.fixture);
    buttonByText(overlay().querySelector('trove-confirm-dialog') as HTMLElement, 'Cancel').click();
    await settle(r.fixture);
    expect(r.api.calls.some((c) => c.startsWith('setVisibility'))).toBe(false);
  });

  it('R-UI-054 an Owner who is no administrator sees the level read-only with the reason', async () => {
    const r = await openSettings('internal', []);
    expect(r.radios().every((x) => x.closest('fieldset')!.disabled)).toBe(true);
    expect(text(r.root)).toContain('Only an administrator of trove sets the visibility of a project.');
  });

  it('R-UI-054 the create dialog has the group, preset to the installation default, and creates the project at the chosen level', async () => {
    const r = await renderScreen(ProjectsHome, {
      setup: (get) => ((get(VisibilityApi) as FakeVisibilityApi).stored.settings.defaultLevel = 'internal'),
    });
    TestBed.inject(TroveDialog).open(ProjectDialog, { data: {}, maxWidth: '40rem' });
    await settle(r.fixture);
    const dialog = overlay().querySelector('trove-project-dialog') as HTMLElement;
    const radios = () => [...dialog.querySelectorAll<HTMLInputElement>('input[type="radio"]')];
    expect(radios().map((x) => x.checked)).toEqual([false, true, false]);
    radios()[2]!.click();
    await settle(r.fixture);
    const key = dialog.querySelector<HTMLInputElement>('input[name="key"]')!;
    key.value = 'team';
    key.dispatchEvent(new Event('input', { bubbles: true }));
    const name = dialog.querySelector<HTMLInputElement>('input[name="displayName"]')!;
    name.value = 'Team';
    name.dispatchEvent(new Event('input', { bubbles: true }));
    await settle(r.fixture);
    buttonByText(dialog, 'Create project').click();
    await settle(r.fixture);
    const api = r.get(ProjectsApi) as FakeProjectsApi;
    expect(api.projects.find((p) => p.key === 'team')?.visibility).toBe('public');
  });
});

describe('The start page lists the projects of the caller first (R-UI-054)', () => {
  it('R-UI-054 the memberships come first with their role, the other projects after them with none, each with its level icon', async () => {
    const r = await renderScreen(ProjectsHome, {
      setup: (get) => {
        (get(ProjectsApi) as FakeProjectsApi).projects = [
          sampleProject({ key: 'alpha', displayName: 'Alpha', visibility: 'internal' }),
          sampleProject({ key: 'beta', displayName: 'Beta', visibility: 'public', role: 'developer' }),
        ];
      },
    });
    const rows = [...r.root.querySelectorAll('tbody tr')];
    expect(rows.map((row) => text(row.querySelector('th a')!))).toEqual(['Beta', 'Alpha']);
    expect(text(rows[0]!.children[3]!)).toBe('Developer');
    expect(text(rows[1]!.children[3]!)).toBe('');
    expect(rows.map((row) => row.querySelector('[role="img"]')!.getAttribute('aria-label'))).toEqual(['Public', 'Internal']);
  });

  it('R-UI-054 the filter "Your projects" asks for membership=true and shows the first group alone', async () => {
    const r = await renderScreen(ProjectsHome, {
      setup: (get) => {
        (get(ProjectsApi) as FakeProjectsApi).projects = [
          sampleProject({ key: 'alpha', displayName: 'Alpha', visibility: 'internal' }),
          sampleProject({ key: 'beta', displayName: 'Beta', visibility: 'public', role: 'developer' }),
        ];
      },
    });
    const box = [...r.root.querySelectorAll<HTMLInputElement>('trove-checkbox input')].find((i) => i.labels?.[0]?.textContent?.includes('Your projects'))!;
    box.click();
    await settle(r.fixture);
    expect([...r.root.querySelectorAll('tbody tr th a')].map((a) => text(a))).toEqual(['Beta']);
  });
});
