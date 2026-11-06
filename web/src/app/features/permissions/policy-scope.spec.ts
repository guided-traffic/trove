import { SessionApi } from '../../api/session-api';
import { FakeSessionApi } from '../../api/testing/fakes';
import { renderScreen, text } from '../../testing/screen';
import { PolicyEditor } from './policy-editor';
import { PolicyList } from './policy-list';

const newLink = (root: HTMLElement): HTMLAnchorElement =>
  [...root.querySelectorAll<HTMLAnchorElement>('a')].find((a) => text(a) === 'New policy')!;

describe('A policy is global unless it says otherwise (R-UI-041, R-AUTHZ-026)', () => {
  it('R-UI-041 the instance-wide list creates a global policy, also when it is narrowed to a project', async () => {
    const { root } = await renderScreen(PolicyList, { inputs: { project: 'web' } });
    expect(newLink(root).getAttribute('href')).toBe('/admin/permissions/new');
  });

  it('R-UI-041 Q-UI-129 the list names the project of each policy in a column and links to the project\'s screen', async () => {
    const { root } = await renderScreen(PolicyList);
    expect([...root.querySelectorAll('th[scope="col"]')].map((h) => text(h))).toContainEqual(expect.stringMatching(/^Project/));
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
          projects: [{ key: 'web', role: 'owner' }],
        };
      },
    });
    expect(text(root)).not.toContain('Global policy');
    expect(text(root)).toContain('Project policy of web');
  });
});
