import { TestBed } from '@angular/core/testing';
import { AuthSettingsApi } from '../../api/auth-settings-api';
import { IdentityApi } from '../../api/identity-api';
import { FakeAuthSettingsApi, FakeIdentityApi, sampleUser } from '../../api/testing/access-fakes';
import { TroveDialog } from '../../shared/ui/dialog';
import { seriousViolations } from '../../testing/axe';
import { button, buttonByText, openFilters, renderScreen, settle, text, typeInto } from '../../testing/screen';
import { UserCreateDialog } from './user-create-dialog';
import { UserDetail } from './user-detail';
import { UserList } from './user-list';

const overlay = (): HTMLElement => document.querySelector('.cdk-overlay-container') as HTMLElement;

const externalBox = (root: ParentNode): HTMLInputElement =>
  [...root.querySelectorAll<HTMLInputElement>('trove-checkbox input')].find((i) => i.labels?.[0]?.textContent?.includes('External user'))!;

describe('The external flag of a user (R-UI-056)', () => {
  it('R-UI-056 the user page has the checkbox External user with its sentence and its own save, and passes axe', async () => {
    const { fixture, root, get } = await renderScreen(UserDetail, { inputs: { username: 'alice' } });
    const box = externalBox(root);
    expect(box.checked).toBe(false);
    expect(text(root)).toContain('Sees only the projects it is a member of, what a policy grants it, and what is public.');
    const save = () => button(root, /Save changes\s+\(external user\)/);
    expect(save().disabled).toBe(true);
    box.click();
    await settle(fixture);
    expect(save().disabled).toBe(false);
    save().click();
    await settle(fixture);
    const api = get(IdentityApi) as FakeIdentityApi;
    expect(api.calls).toContain('updateUser alice {"external":true}');
    expect(api.userList.find((u) => u.username === 'alice')?.external).toBe(true);
    expect(save().disabled).toBe(true);
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-UI-056 the break-glass account shows it disabled with the reason', async () => {
    const { fixture, root } = await renderScreen(UserDetail, {
      inputs: { username: 'admin' },
      // without tokens:read the Tokens section is not created: the page of a user without Advanced actions reads the section's offersRevokeAll before its input is set (finding)
      capabilities: ['identities:read', 'identities:write'],
      setup: (get) => {
        (get(IdentityApi) as FakeIdentityApi).userList = [sampleUser({ username: 'admin', origin: 'system' })];
      },
    });
    await settle(fixture);
    expect(externalBox(root).disabled).toBe(true);
    expect(text(root)).toContain('The break-glass account is never external.');
  });

  it('R-UI-056 without identities:write the flag is shown but cannot be changed', async () => {
    const { root } = await renderScreen(UserDetail, { inputs: { username: 'alice' }, capabilities: ['identities:read'] });
    expect(externalBox(root).disabled).toBe(true);
    expect(() => button(root, /Save changes\s+\(external user\)/)).toThrow();
  });

  it('R-UI-056 the list shows the badge External next to an external user and filters by it', async () => {
    const { fixture, root } = await renderScreen(UserList, {
      setup: (get) => {
        const api = get(IdentityApi) as FakeIdentityApi;
        api.userList = api.userList.map((u) => (u.username === 'bob' ? { ...u, external: true } : u));
      },
    });
    const rows = [...root.querySelectorAll('tbody tr')];
    expect(rows.map((r) => r.querySelector('.badge')?.textContent?.trim() ?? null)).toEqual([null, 'External']);

    await openFilters(fixture, root);
    const select = root.querySelector<HTMLSelectElement>('select[name="external"]')!;
    select.value = 'true';
    select.dispatchEvent(new Event('change', { bubbles: true }));
    buttonByText(root, 'Apply filters').click();
    await settle(fixture);
    expect([...root.querySelectorAll('tbody tr th a')].map((a) => text(a))).toEqual(['Bob Example']);
  });

  it('R-UI-056 Add user has the checkbox, preset to newUsersExternal, and sends the flag', async () => {
    const r = await renderScreen(UserList, {
      setup: (get) => {
        const api = get(AuthSettingsApi) as FakeAuthSettingsApi;
        api.stored = { ...api.stored, settings: { ...api.stored.settings, newUsersExternal: true } };
      },
    });
    TestBed.inject(TroveDialog).open(UserCreateDialog, { maxWidth: '36rem' });
    await settle(r.fixture);
    const dialog = overlay().querySelector('trove-user-create-dialog') as HTMLElement;
    expect(externalBox(dialog).checked).toBe(true);
    typeInto(dialog, 'User name', 'erin');
    await settle(r.fixture);
    buttonByText(dialog, 'Add user').click();
    await settle(r.fixture);
    const api = r.get(IdentityApi) as FakeIdentityApi;
    expect(api.userList.find((u) => u.username === 'erin')?.external).toBe(true);
  });
});
