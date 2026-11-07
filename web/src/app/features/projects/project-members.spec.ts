import { ProjectsApi } from '../../api/projects-api';
import { SessionApi } from '../../api/session-api';
import { FakeSessionApi } from '../../api/testing/fakes';
import { FakeProjectsApi } from '../../api/testing/fakes-projects';
import { buttonByText, renderScreen, settle, sleep, text, type, typeInto } from '../../testing/screen';
import { ProjectMembersPage } from './project-members-page';

type Get = <A>(token: abstract new (...args: never[]) => A) => A;

/** A caller without any capability that is a member of `web` with `role` (Q-AUTHZ-207). */
function asMember(role: 'reader' | 'developer' | 'maintainer' | 'owner') {
  return (get: Get): void => {
    (get(SessionApi) as FakeSessionApi).meResult = {
      roles: [],
      capabilities: [],
      principals: ['user:alice'],
      projects: [{ key: 'web', role }],
    };
  };
}

describe('Project members (R-UI-041, Q-AUTHZ-207, Q-AUTHZ-217)', () => {
  it('Q-AUTHZ-207 an Owner adds a member with a role, changes it and removes it', async () => {
    const { fixture, root, get } = await renderScreen(ProjectMembersPage, {
      inputs: { project: 'web' },
      capabilities: [],
      setup: asMember('owner'),
    });
    const members = root.querySelector('trove-project-members') as HTMLElement;
    expect(text(members)).toContain('group:web-leads');
    typeInto(members, 'Find a user or group', 'bo');
    await sleep(fixture, 400);
    buttonByText(members, 'user:bob').click();
    await settle(fixture);
    type(members.querySelector<HTMLSelectElement>('select[name="new-role"]')!, 'maintainer');
    await settle(fixture);
    buttonByText(members, 'Add member').click();
    await sleep(fixture, 100);
    const api = get(ProjectsApi) as FakeProjectsApi;
    expect(api.calls).toEqual(['setMember web user:bob maintainer']);
    expect(text(members)).toContain('user:bob');
    type(members.querySelector<HTMLSelectElement>('select[name="role-user:bob"]')!, 'reader');
    await sleep(fixture, 100);
    expect(api.calls).toContain('setMember web user:bob reader');
    buttonByText(members, 'Remove user:bob').click();
    await sleep(fixture, 100);
    expect(api.calls).toContain('removeMember web user:bob');
    expect(text(members)).not.toContain('user:bob');
  });

  it('Q-AUTHZ-217 removing or demoting the last Owner is refused and the refusal is shown', async () => {
    const { fixture, root, get } = await renderScreen(ProjectMembersPage, {
      inputs: { project: 'web' },
      capabilities: [],
      setup: asMember('owner'),
    });
    const members = root.querySelector('trove-project-members') as HTMLElement;
    buttonByText(members, 'Remove group:web-leads').click();
    await sleep(fixture, 100);
    expect(text(members)).toContain('A project keeps at least one Owner');
    expect((get(ProjectsApi) as FakeProjectsApi).memberLists['web']).toHaveLength(1);
    type(members.querySelector<HTMLSelectElement>('select[name="role-group:web-leads"]')!, 'reader');
    await sleep(fixture, 100);
    expect(text(members)).toContain('A project keeps at least one Owner');
    expect((get(ProjectsApi) as FakeProjectsApi).memberLists['web']![0]!.role).toBe('owner');
  });

  it('Q-AUTHZ-207 a Reader sees the members and the roles as text', async () => {
    const { root } = await renderScreen(ProjectMembersPage, {
      inputs: { project: 'web' },
      capabilities: [],
      setup: asMember('reader'),
    });
    const members = root.querySelector('trove-project-members') as HTMLElement;
    expect(text(members)).toContain('group:web-leads');
    expect(text(members)).toContain('Owner');
    expect(members.querySelector('select')).toBeNull();
    expect(members.querySelector('button')).toBeNull();
  });
});
