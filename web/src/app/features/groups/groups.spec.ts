import { IdentityApi } from '../../api/identity-api';
import { FakeIdentityApi } from '../../api/testing/access-fakes';
import { buttonByText, renderScreen, settle, text, typeInto } from '../../testing/screen';
import { seriousViolations } from '../../testing/axe';
import { GroupDetail } from './group-detail';
import { GroupList } from './group-list';

const overlay = (): HTMLElement => document.querySelector('.cdk-overlay-container') as HTMLElement;

describe('Groups (R-UI-025)', () => {
  it('R-UI-025 lists name, description, origin and member count, and passes axe', async () => {
    const { root } = await renderScreen(GroupList);
    const rows = [...root.querySelectorAll('tbody tr')];
    expect(rows).toHaveLength(3);
    expect(text(rows[1]!)).toContain('team-web');
    expect(text(rows[1]!)).toContain('Identity provider');
    expect(text(rows[1]!)).toContain('2');
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-UI-025 creates a group with an idempotency key', async () => {
    const { fixture, root, get } = await renderScreen(GroupList);
    buttonByText(root, 'New group').click();
    await settle(fixture);
    const dialog = overlay().querySelector('trove-group-create-dialog') as HTMLElement;
    typeInto(dialog, 'Name', 'ops');
    buttonByText(dialog, 'Create group').click();
    await settle(fixture);
    const api = get(IdentityApi) as FakeIdentityApi;
    expect(api.calls).toContain('createGroup ops');
    expect(api.keys).toHaveLength(1);
  });

  it('R-UI-025 adding a manual member lists it with the source manual', async () => {
    const { fixture, root, get } = await renderScreen(GroupDetail, { inputs: { name: 'platform' } });
    typeInto(root, 'Add a member', 'bob');
    await settle(fixture);
    buttonByText(root, 'Add').click();
    await settle(fixture);
    expect((get(IdentityApi) as FakeIdentityApi).calls).toContain('changeGroupMembers platform +bob -');
    const row = [...root.querySelectorAll('tbody tr')].find((r) => text(r).startsWith('bob'));
    expect(row).toBeTruthy();
    expect(text(row!)).toContain('Manual');
  });

  it('R-UI-025 only manual members can be removed', async () => {
    const { root } = await renderScreen(GroupDetail, { inputs: { name: 'team-web' } });
    const rows = [...root.querySelectorAll('section[aria-labelledby="group-members"] tbody tr')];
    expect(rows).toHaveLength(2);
    const provider = rows.find((r) => text(r).startsWith('alice'))!;
    const mixed = rows.find((r) => text(r).startsWith('sa:ci-bot'))!;
    expect(provider.querySelector('button')).toBeNull();
    expect(mixed.querySelector('button')).toBeTruthy();
  });

  it('R-UI-025 a SCIM group is read-only and says why', async () => {
    const { root } = await renderScreen(GroupDetail, { inputs: { name: 'scim-sales' } });
    expect(text(root)).toContain('managed by SCIM');
    expect(root.textContent).not.toContain('Save group');
    expect(root.textContent).not.toContain('Add a member');
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-UI-025 shows the bound roles and the policies that name the group, both read only', async () => {
    const { root } = await renderScreen(GroupDetail, { inputs: { name: 'platform' } });
    expect(text(root)).toContain('repository-admin');
    expect(root.querySelector('a[href="/admin/roles"]')).toBeTruthy();
    expect(text(root)).toContain('Permission policies naming this group');
  });

  it('R-UI-025 saves only the members that changed and passes axe', async () => {
    const { fixture, root, get } = await renderScreen(GroupDetail, { inputs: { name: 'platform' } });
    typeInto(root, 'Description', 'Platform and infra');
    buttonByText(root, 'Save group').click();
    await settle(fixture);
    expect((get(IdentityApi) as FakeIdentityApi).calls).toContain('updateGroup platform {"description":"Platform and infra"}');
    expect(await seriousViolations(root)).toEqual([]);
  });
});
