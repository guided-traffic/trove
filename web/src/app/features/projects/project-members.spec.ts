import { ComponentFixture } from '@angular/core/testing';
import { ProjectsApi } from '../../api/projects-api';
import { SessionApi } from '../../api/session-api';
import { FakeSessionApi } from '../../api/testing/fakes';
import { FakeProjectsApi } from '../../api/testing/fakes-projects';
import { buttonByText, renderScreen, settle, sleep, text, type, typeInto } from '../../testing/screen';
import { ProjectMembersPage } from './project-members-page';

const overlay = (): HTMLElement => document.querySelector('.cdk-overlay-container') as HTMLElement;

async function removeMember(fixture: ComponentFixture<unknown>, members: HTMLElement, principal: string): Promise<void> {
  members.querySelector<HTMLButtonElement>(`button[aria-label="Actions for ${principal}"]`)!.click();
  await settle(fixture);
  buttonByText(overlay(), 'Remove member').click();
  await sleep(fixture, 100);
}

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
    expect(text(members)).toContain('web-leads');
    buttonByText(root, 'Add member').click();
    await settle(fixture);
    const dialog = overlay().querySelector('trove-project-member-dialog') as HTMLElement;
    typeInto(dialog, 'Find a user or group', 'bo');
    await sleep(fixture, 400);
    buttonByText(dialog, 'user:bob').click();
    await settle(fixture);
    type(dialog.querySelector<HTMLSelectElement>('select[name="new-role"]')!, 'maintainer');
    await settle(fixture);
    buttonByText(dialog, 'Add member').click();
    await sleep(fixture, 100);
    const api = get(ProjectsApi) as FakeProjectsApi;
    expect(api.calls).toEqual(['setMember web user:bob maintainer']);
    expect(text(members)).toContain('bob');
    type(members.querySelector<HTMLSelectElement>('select[name="role-user:bob"]')!, 'reader');
    await sleep(fixture, 100);
    expect(api.calls).toContain('setMember web user:bob reader');
    await removeMember(fixture, members, 'user:bob');
    expect(api.calls).toContain('removeMember web user:bob');
    expect(text(members)).not.toContain('bob');
  });

  it('R-UI-041 Q-UI-214 a member shows its display name, its user name, its source and who the caller is', async () => {
    const { root } = await renderScreen(ProjectMembersPage, {
      inputs: { project: 'web' },
      capabilities: [],
      setup: (get) => {
        asMember('owner')(get);
        (get(ProjectsApi) as FakeProjectsApi).memberLists['web'] = [
          { project: 'web', principal: 'user:alice', kind: 'user', name: 'alice', id: 'a1', displayName: 'Alice Doe', avatarUrl: '/api/v1/avatars/abc', role: 'owner' },
          { project: 'web', principal: 'group:web-leads', kind: 'group', name: 'web-leads', role: 'developer' },
        ];
      },
    });
    const members = root.querySelector('trove-project-members') as HTMLElement;
    const row = members.querySelector('tbody tr') as HTMLElement;
    expect(text(row)).toContain('Alice Doe');
    expect(text(row)).toContain('@alice');
    expect(text(row)).toContain("It's you");
    expect(text(row)).toContain('Direct member');
    expect(row.querySelector('trove-avatar img')?.getAttribute('src')).toBe('/api/v1/avatars/abc');
    expect(text(members.querySelector('.tab-link') as HTMLElement)).toContain('2');
  });

  it('R-UI-041 Q-UI-214 the filter narrows the list by name and the order can be changed', async () => {
    const { fixture, root } = await renderScreen(ProjectMembersPage, {
      inputs: { project: 'web' },
      capabilities: [],
      setup: (get) => {
        asMember('owner')(get);
        (get(ProjectsApi) as FakeProjectsApi).memberLists['web'] = [
          { project: 'web', principal: 'user:bob', kind: 'user', name: 'bob', role: 'owner' },
          { project: 'web', principal: 'user:alice', kind: 'user', name: 'alice', role: 'reader' },
        ];
      },
    });
    const members = root.querySelector('trove-project-members') as HTMLElement;
    const names = (): (string | undefined)[] => Array.from(members.querySelectorAll('tbody .name')).map((n) => n.textContent?.trim());
    expect(names()).toEqual(['alice', 'bob']);
    members.querySelector<HTMLButtonElement>('button[aria-label="Reverse the order"]')!.click();
    await settle(fixture);
    expect(names()).toEqual(['bob', 'alice']);
    const filter = members.querySelector<HTMLInputElement>('input[name="member-filter"]')!;
    filter.value = 'ali';
    filter.dispatchEvent(new Event('input'));
    await settle(fixture);
    expect(names()).toEqual(['alice']);
    filter.value = 'nobody';
    filter.dispatchEvent(new Event('input'));
    await settle(fixture);
    expect(text(members)).toContain('No member matches.');
  });

  it('Q-AUTHZ-217 removing or demoting the last Owner is refused and the refusal is shown', async () => {
    const { fixture, root, get } = await renderScreen(ProjectMembersPage, {
      inputs: { project: 'web' },
      capabilities: [],
      setup: asMember('owner'),
    });
    const members = root.querySelector('trove-project-members') as HTMLElement;
    await removeMember(fixture, members, 'group:web-leads');
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
    expect(text(members)).toContain('web-leads');
    expect(text(members)).toContain('Owner');
    expect(members.querySelector('select')).toBeNull();
    expect(members.querySelector('button[aria-label^="Actions for"]')).toBeNull();
    expect(root.textContent).not.toContain('Add member');
  });
});
