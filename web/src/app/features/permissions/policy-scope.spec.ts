import { SessionApi } from '../../api/session-api';
import { FakeSessionApi } from '../../api/testing/fakes';
import { PreferencesStore } from '../../core/preferences-store';
import { ProjectFilter } from '../../core/project-filter';
import { renderScreen, text } from '../../testing/screen';
import { PolicyEditor } from './policy-editor';
import { PolicyList } from './policy-list';

const newLink = (root: HTMLElement): HTMLAnchorElement =>
  [...root.querySelectorAll<HTMLAnchorElement>('a')].find((a) => text(a) === 'New policy')!;

describe('A policy is global unless it says otherwise (R-UI-041, R-AUTHZ-026)', () => {
  it('R-UI-041 "New policy" of a project-scoped list creates a policy of that project', async () => {
    const { root } = await renderScreen(PolicyList, { inputs: { project: 'web' } });
    expect(newLink(root).getAttribute('href')).toContain('project=web');
  });

  it('R-UI-041 "New policy" follows the project filter of the top bar', async () => {
    const { fixture, root, get } = await renderScreen(PolicyList, { capabilities: ['policies:write', 'policies:read', 'projects:read'] });
    expect(newLink(root).getAttribute('href')).not.toContain('project=');
    await get(ProjectFilter).refresh();
    await get(PreferencesStore).setProjectFilter('web');
    await fixture.whenStable();
    fixture.detectChanges();
    expect(text(root)).toContain('Showing the policies of project web');
    expect(newLink(root).getAttribute('href')).toContain('project=web');
  });

  it('R-UI-041 an unscoped list links to a global policy', async () => {
    const { root } = await renderScreen(PolicyList);
    expect(newLink(root).getAttribute('href')).toBe('/admin/permissions/new');
  });

  it('R-UI-041 the editor says a global policy applies to every repository', async () => {
    const { root } = await renderScreen(PolicyEditor);
    expect(text(root)).toContain('Global policy — applies to every repository');
  });

  it('R-UI-041 a project policy does not claim to be global', async () => {
    const { root } = await renderScreen(PolicyEditor, {
      inputs: { project: 'web' },
      capabilities: [],
      setup: (g) => {
        (g(SessionApi) as FakeSessionApi).meResult = {
          roles: [],
          capabilities: [],
          principals: ['user:alice'],
          projects: [{ key: 'web', managePolicies: true, manageRepositories: true }],
        };
      },
    });
    expect(text(root)).not.toContain('Global policy');
    expect(text(root)).toContain('Project policy of web');
  });
});
