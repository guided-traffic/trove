import { RolesApi } from '../../api/authz-api';
import { FakeRolesApi, problem } from '../../api/testing/access-fakes';
import { SessionApi } from '../../api/session-api';
import { FakeSessionApi } from '../../api/testing/fakes';
import { buttonByText, renderScreen, settle, textOf, typeInto } from '../../shared/access/testing';
import { seriousViolations } from '../../testing/axe';
import { RoleBindings } from './role-bindings';

const overlay = (): HTMLElement => document.querySelector('.cdk-overlay-container') as HTMLElement;

describe('System roles (R-UI-024)', () => {
  it('R-UI-024 lists the five fixed roles with capabilities and holders, and passes axe', async () => {
    const { root } = await renderScreen(RoleBindings);
    const sections = root.querySelectorAll('section.card');
    expect(sections).toHaveLength(5);
    expect(textOf(sections[0]!)).toContain('admin');
    expect(textOf(sections[0]!)).toContain('group:platform');
    expect(textOf(sections[2]!)).toContain('user:bob');
    expect(textOf(sections[3]!)).toContain('Nobody holds this role');
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-UI-005 the last administrator binding cannot be removed: the button is disabled and says why', async () => {
    const { root } = await renderScreen(RoleBindings);
    const adminRow = root.querySelector('section.card tbody tr') as HTMLElement;
    const unbind = adminRow.querySelector('button') as HTMLButtonElement;
    expect(unbind.disabled).toBe(true);
    expect(unbind.getAttribute('aria-describedby')).toBe('last-admin-guard');
    expect(textOf(root.querySelector('#last-admin-guard')!)).toContain('last administrator binding cannot be removed');
  });

  it('R-AUTHZ-022 binds a role, reads the lists again and refreshes the caller\'s capabilities', async () => {
    const { fixture, root, get } = await renderScreen(RoleBindings);
    const me = get(SessionApi) as FakeSessionApi;
    let refreshed = 0;
    const original = me.authzMe.bind(me);
    me.authzMe = async () => {
      refreshed++;
      return original();
    };
    const auditor = root.querySelectorAll('section.card')[2] as HTMLElement;
    typeInto(auditor, 'Bind to', 'group:security');
    await settle(fixture);
    buttonByText(auditor, 'Bind').click();
    await settle(fixture);
    expect((get(RolesApi) as FakeRolesApi).calls).toContain('bind auditor group:security');
    expect(textOf(root.querySelectorAll('section.card')[2]!)).toContain('group:security');
    expect(refreshed).toBe(1);
  });

  it('R-AUTHZ-022 unbinds after confirmation', async () => {
    const { fixture, root, get } = await renderScreen(RoleBindings);
    const auditor = root.querySelectorAll('section.card')[2] as HTMLElement;
    auditor.querySelector('tbody button')!.dispatchEvent(new Event('click'));
    await settle(fixture);
    buttonByText(overlay().querySelector('trove-confirm-dialog') as HTMLElement, 'Unbind').click();
    await settle(fixture, 300);
    expect((get(RolesApi) as FakeRolesApi).calls).toContain('unbind auditor user:bob');
  });

  it('R-UI-042 a refused bind shows the translated reason', async () => {
    const { fixture, root, get } = await renderScreen(RoleBindings);
    (get(RolesApi) as FakeRolesApi).failure = problem(409, 'auto_join_admin_conflict');
    const admin = root.querySelectorAll('section.card')[0] as HTMLElement;
    typeInto(admin, 'Bind to', 'group:everyone');
    await settle(fixture);
    buttonByText(admin, 'Bind').click();
    await settle(fixture);
    expect(textOf(root)).toContain('An auto-join group cannot hold the administrator role');
  });

  it('R-UI-005 without roles:write there is no bind or unbind', async () => {
    const { root } = await renderScreen(RoleBindings, { capabilities: ['roles:read'] });
    expect(root.querySelector('tbody button')).toBeNull();
    expect(root.querySelector('form')).toBeNull();
  });
});
