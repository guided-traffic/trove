import { IdentityApi } from '../../api/identity-api';
import { FakeIdentityApi, problem } from '../../api/testing/access-fakes';
import { SessionsApi } from '../../api/sessions-api';
import { FakeSessionsApi } from '../../api/testing/access-fakes';
import { seriousViolations } from '../../testing/axe';
import { buttonByText, renderScreen, settle, text, typeInto, sleep } from '../../testing/screen';
import { UserDetail } from './user-detail';
import { UserList } from './user-list';

const overlay = (): HTMLElement => document.querySelector('.cdk-overlay-container') as HTMLElement;

describe('Users list (R-UI-024)', () => {
  it('R-UI-024 lists username, display name, email, origin, status and last sign-in, and passes axe', async () => {
    const { root } = await renderScreen(UserList);
    const rows = [...root.querySelectorAll('tbody tr')];
    expect(rows).toHaveLength(2);
    expect(text(rows[0]!)).toContain('alice');
    expect(text(rows[0]!)).toContain('Identity provider');
    expect(text(rows[1]!)).toContain('Disabled');
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-UI-005 without identities:write "Add user" is disabled and the reason is next to it', async () => {
    const { root } = await renderScreen(UserList, { capabilities: ['identities:read'] });
    const add = buttonByText(root, 'Add user') as HTMLButtonElement;
    expect(add.disabled).toBe(true);
    expect(add.getAttribute('aria-describedby')).toBe('needs-users');
    expect(text(root.querySelector('#needs-users')!)).toContain('needs the permission identities:write');
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-API-010 pages with "Load more" until the cursor ends', async () => {
    const { fixture, root } = await renderScreen(UserList, {
      setup: (get) => ((get(IdentityApi) as FakeIdentityApi).pageSize = 1),
    });
    expect(root.querySelectorAll('tbody tr')).toHaveLength(1);
    buttonByText(root, 'Load more').click();
    await settle(fixture);
    expect(root.querySelectorAll('tbody tr')).toHaveLength(2);
    expect(root.textContent).not.toContain('Load more');
  });

  it('R-UI-024 a filter starts a new traversal', async () => {
    const { fixture, root } = await renderScreen(UserList);
    typeInto(root, 'Name or email', 'bob');
    buttonByText(root, 'Apply filters').click();
    await settle(fixture);
    const rows = root.querySelectorAll('tbody tr');
    expect(rows).toHaveLength(1);
    expect(text(rows[0]!)).toContain('bob');
  });

  it('R-UI-024 "Add user" pre-provisions a user with an idempotency key and has no password field', async () => {
    const { fixture, root, get } = await renderScreen(UserList);
    buttonByText(root, 'Add user').click();
    await settle(fixture);
    const dialog = overlay().querySelector('trove-user-create-dialog') as HTMLElement;
    expect(dialog.querySelector('input[type="password"]')).toBeNull();
    typeInto(dialog, 'User name', 'carol');
    typeInto(dialog, 'Email', 'carol@example.com');
    buttonByText(dialog, 'Add user').click();
    await settle(fixture);
    const api = get(IdentityApi) as FakeIdentityApi;
    expect(api.calls).toContain('createUser carol');
    expect(api.keys[0]).toMatch(/^[0-9a-f-]{36}$/);
  });

  it('R-UI-042 a refused create shows the field problem next to the field', async () => {
    const { fixture, root, get } = await renderScreen(UserList);
    (get(IdentityApi) as FakeIdentityApi).failure = problem(422, 'validation_failed', {
      errors: [{ pointer: '/username', code: 'username_invalid' }],
    });
    buttonByText(root, 'Add user').click();
    await settle(fixture);
    const dialog = overlay().querySelector('trove-user-create-dialog') as HTMLElement;
    typeInto(dialog, 'User name', 'Bad Name');
    buttonByText(dialog, 'Add user').click();
    await settle(fixture);
    expect(text(dialog.querySelector('.field-error')!)).toContain('lower-case letters');
    expect(await seriousViolations(overlay())).toEqual([]);
  });
});

describe('User detail (R-UI-024)', () => {
  it('R-UI-024 shows attributes, identities, groups, roles, tokens and sessions, and passes axe', async () => {
    const { root } = await renderScreen(UserDetail, { inputs: { username: 'alice' } });
    const t = text(root);
    expect(t).toContain('Alice Example');
    expect(t).toContain('corp');
    expect(t).toContain('team-web');
    expect(t).toContain('System roles');
    expect(t).toContain('Tokens');
    expect(t).toContain('Firefox 140');
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-UI-005 the self-lockout guard disables disable and delete on the own account', async () => {
    const { root } = await renderScreen(UserDetail, { inputs: { username: 'alice' } });
    expect((buttonByText(root, 'Disable') as HTMLButtonElement).disabled).toBe(true);
    expect((buttonByText(root, 'Delete') as HTMLButtonElement).disabled).toBe(true);
    expect(root.textContent).toContain('cannot disable or delete your own account');
  });

  it('R-UI-024 identity-provider memberships are read only; a manual one can be removed', async () => {
    const { fixture, root, get } = await renderScreen(UserDetail, { inputs: { username: 'alice' } });
    const rows = [...root.querySelectorAll('section[aria-labelledby="user-groups"] tbody tr')];
    const web = rows.find((r) => text(r).includes('team-web'))!;
    const platform = rows.find((r) => text(r).includes('platform'))!;
    expect(web.querySelector('button')).toBeNull();
    expect(text(web)).toContain('Identity provider');
    platform.querySelector('button')!.click();
    await settle(fixture);
    expect((get(IdentityApi) as FakeIdentityApi).calls).toContain('changeUserGroups alice + -platform');
  });

  it('R-UI-024 disabling asks whether to revoke the tokens and sends the answer', async () => {
    const { fixture, root, get } = await renderScreen(UserDetail, { inputs: { username: 'alice' } });
    // Another user: the self guard does not apply.
    fixture.componentRef.setInput('username', 'bob');
    await settle(fixture);
    // bob is disabled in the fake: enable first, then disable with revoke.
    buttonByText(root, 'Enable').click();
    await settle(fixture);
    buttonByText(root, 'Disable').click();
    await settle(fixture);
    const confirm = overlay().querySelector('trove-confirm-dialog') as HTMLElement;
    expect(confirm.textContent).toContain('Also revoke all tokens');
    buttonByText(confirm, 'Disable').click();
    await sleep(fixture, 300);
    const calls = (get(IdentityApi) as FakeIdentityApi).calls;
    expect(calls).toContain('enableUser bob');
    expect(calls).toContain('disableUser bob revokeTokens=true');
  });

  it('R-UI-024 ends all sessions of a user after confirmation', async () => {
    const { fixture, root, get } = await renderScreen(UserDetail, { inputs: { username: 'alice' } });
    buttonByText(root, 'End all sessions').click();
    await settle(fixture);
    buttonByText(overlay().querySelector('trove-confirm-dialog') as HTMLElement, 'End sessions').click();
    await sleep(fixture, 300);
    expect((get(SessionsApi) as FakeSessionsApi).calls).toContain('endAllOfUser alice');
  });

  it('R-UI-024 a correction of the display name is a merge patch of that member only', async () => {
    const { fixture, root, get } = await renderScreen(UserDetail, { inputs: { username: 'alice' } });
    typeInto(root, 'Display name', 'Alice B.');
    buttonByText(root, 'Save correction').click();
    await settle(fixture);
    expect((get(IdentityApi) as FakeIdentityApi).calls).toContain('updateUser alice {"displayName":"Alice B."}');
  });

  it('R-API-012 a user PATCH sends If-Match with the ETag of the loaded record', async () => {
    const { fixture, root, get } = await renderScreen(UserDetail, { inputs: { username: 'alice' } });
    const api = get(IdentityApi) as FakeIdentityApi;
    expect(api.etagOf('user:alice')).toBe('"1"');
    typeInto(root, 'Display name', 'Alice B.');
    buttonByText(root, 'Save correction').click();
    await settle(fixture);
    expect(api.ifMatch).toEqual(['"1"']);
    // The answer's ETag is the one for the next write.
    typeInto(root, 'Display name', 'Alice C.');
    buttonByText(root, 'Save correction').click();
    await settle(fixture);
    expect(api.ifMatch).toEqual(['"1"', '"2"']);
  });

  it('R-UI-042 a 412 on a user PATCH opens the conflict dialog; Overwrite sends the patch with the ETag read now', async () => {
    const { fixture, root, get } = await renderScreen(UserDetail, { inputs: { username: 'alice' } });
    const api = get(IdentityApi) as FakeIdentityApi;
    api.bumpRevision('user:alice');
    typeInto(root, 'Display name', 'Alice B.');
    buttonByText(root, 'Save correction').click();
    await settle(fixture);
    const dialog = overlay().querySelector('trove-conflict-dialog') as HTMLElement;
    expect(dialog).toBeTruthy();
    expect(text(dialog)).toContain('displayName');
    buttonByText(dialog, 'Overwrite').click();
    await sleep(fixture, 300);
    // The stale tag was refused (it never reached the guard's bump), then the tag read after the 412 went through.
    expect(api.ifMatch).toEqual(['"1"', '"2"']);
    expect(api.userList[0]!.displayName).toBe('Alice B.');
  });

  it('R-UI-042 "Reload" on the conflict dialog drops the draft and writes nothing', async () => {
    const { fixture, root, get } = await renderScreen(UserDetail, { inputs: { username: 'alice' } });
    const api = get(IdentityApi) as FakeIdentityApi;
    api.bumpRevision('user:alice');
    typeInto(root, 'Display name', 'Alice B.');
    buttonByText(root, 'Save correction').click();
    await settle(fixture);
    buttonByText(overlay().querySelector('trove-conflict-dialog') as HTMLElement, 'Reload (discard mine)').click();
    await sleep(fixture, 300);
    expect(api.userList[0]!.displayName).toBe('Alice Example');
    expect(api.ifMatch).toEqual(['"1"']);
  });

  it('R-UI-005 a user the caller may not see is an error notice, not a blank page', async () => {
    const { root } = await renderScreen(UserDetail, { inputs: { username: 'nobody' } });
    expect(root.querySelector('[role="alert"]')).toBeTruthy();
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-UI-024 an administrator without identities:write sees no edit controls', async () => {
    const { root } = await renderScreen(UserDetail, {
      inputs: { username: 'bob' },
      capabilities: ['identities:read'],
    });
    expect(root.textContent).not.toContain('Save correction');
    expect(root.textContent).not.toContain('Enable');
    expect(root.textContent).not.toContain('Add to group');
  });
});
