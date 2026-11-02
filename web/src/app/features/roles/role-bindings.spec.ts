import { RolesApi } from '../../api/authz-api';
import { FakeRolesApi, problem } from '../../api/testing/access-fakes';
import { SessionApi } from '../../api/session-api';
import { FakeSessionApi } from '../../api/testing/fakes';
import { buttonByText, renderScreen, settle, text, typeInto, sleep } from '../../testing/screen';
import { seriousViolations } from '../../testing/axe';
import { RoleBindings } from './role-bindings';

const overlay = (): HTMLElement => document.querySelector('.cdk-overlay-container') as HTMLElement;

describe('System roles (R-UI-024)', () => {
  it('R-UI-024 lists the five fixed roles with capabilities and holders, and passes axe', async () => {
    const { root } = await renderScreen(RoleBindings);
    const sections = root.querySelectorAll('section.card');
    expect(sections).toHaveLength(5);
    expect(text(sections[0]!)).toContain('admin');
    expect(text(sections[0]!)).toContain('group:platform');
    expect(text(sections[2]!)).toContain('user:bob');
    expect(text(sections[3]!)).toContain('Nobody holds this role');
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-UI-005 the last administrator binding cannot be removed: the button is disabled and says why', async () => {
    const { root } = await renderScreen(RoleBindings);
    const adminRow = root.querySelector('section.card tbody tr') as HTMLElement;
    const unbind = adminRow.querySelector('button') as HTMLButtonElement;
    expect(unbind.disabled).toBe(true);
    expect(unbind.getAttribute('aria-describedby')).toBe('last-admin-guard');
    expect(text(root.querySelector('#last-admin-guard')!)).toContain('last administrator binding cannot be removed');
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
    expect(text(root.querySelectorAll('section.card')[2]!)).toContain('group:security');
    expect(refreshed).toBe(1);
  });

  it('R-AUTHZ-022 unbinds after confirmation', async () => {
    const { fixture, root, get } = await renderScreen(RoleBindings);
    const auditor = root.querySelectorAll('section.card')[2] as HTMLElement;
    auditor.querySelector('tbody button')!.dispatchEvent(new Event('click'));
    await settle(fixture);
    buttonByText(overlay().querySelector('trove-confirm-dialog') as HTMLElement, 'Unbind').click();
    await sleep(fixture, 300);
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
    expect(text(root)).toContain('An auto-join group cannot hold the administrator role');
  });

  it('R-AUTHZ-010 shows the source of every binding and no unbind control for one from the configuration', async () => {
    const { root } = await renderScreen(RoleBindings, {
      setup: (get) => {
        const api = get(RolesApi) as FakeRolesApi;
        api.bindingList = [
          ...api.bindingList,
          {
            role: 'admin',
            principal: 'group:gh-wds18-k8s-main-admins',
            grantedAt: '2026-11-02T08:00:00Z',
            grantedBy: '',
            source: 'configuration',
          },
        ];
      },
    });
    const admin = root.querySelectorAll('section.card')[0] as HTMLElement;
    const rows = Array.from(admin.querySelectorAll('tbody tr')) as HTMLElement[];
    const declared = rows.find((r) => text(r).includes('group:gh-wds18-k8s-main-admins'))!;
    const made = rows.find((r) => text(r).includes('group:platform'))!;
    expect(text(declared)).toContain('Configuration');
    expect(text(declared)).toContain('Managed in the configuration');
    expect(declared.querySelector('button')).toBeNull();
    expect(text(made)).toContain('API');
    expect(made.querySelector('button')).not.toBeNull();
    expect(text(admin)).toContain('declared in authz.role_bindings of the system configuration');
    // Two holders of admin: the binding made through the API may go, the configuration's stays.
    expect((made.querySelector('button') as HTMLButtonElement).disabled).toBe(false);
    expect(admin.querySelector('#last-admin-guard')).toBeNull();
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-AUTHZ-010 without a binding from the configuration there is no configuration hint', async () => {
    const { root } = await renderScreen(RoleBindings);
    expect(text(root)).not.toContain('authz.role_bindings');
  });

  it('R-UI-042 a refused unbind of a binding from the configuration shows the translated reason', async () => {
    const { fixture, root, get } = await renderScreen(RoleBindings);
    (get(RolesApi) as FakeRolesApi).failure = problem(409, 'role_binding_managed_by_configuration');
    const auditor = root.querySelectorAll('section.card')[2] as HTMLElement;
    auditor.querySelector('tbody button')!.dispatchEvent(new Event('click'));
    await settle(fixture);
    buttonByText(overlay().querySelector('trove-confirm-dialog') as HTMLElement, 'Unbind').click();
    await sleep(fixture, 300);
    expect(text(root)).toContain('This role binding is declared in the system configuration and can only be removed there.');
  });

  it('R-UI-005 without roles:write there is no bind or unbind', async () => {
    const { root } = await renderScreen(RoleBindings, { capabilities: ['roles:read'] });
    expect(root.querySelector('tbody button')).toBeNull();
    expect(root.querySelector('form')).toBeNull();
  });
});
