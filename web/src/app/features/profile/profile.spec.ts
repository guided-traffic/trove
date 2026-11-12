import { IdentityApi } from '../../api/identity-api';
import { PreferencesApi } from '../../api/preferences-api';
import { SessionsApi } from '../../api/sessions-api';
import { TokensApi } from '../../api/tokens-api';
import {
  FakeIdentityApi,
  FakeSessionsApi,
  FakeTokensApi,
  SECRET_VALUE,
  problem,
  sampleToken,
  sampleUser,
} from '../../api/testing/access-fakes';
import { FakePreferencesApi, FakeSessionApi } from '../../api/testing/fakes';
import { SessionApi } from '../../api/session-api';
import { buttonByText, renderScreen, settle, text, typeInto, browserStorage, storedText, sleep } from '../../testing/screen';
import { seriousViolations } from '../../testing/axe';
import { Profile } from './profile';
import { ProfileSessions } from './profile-sessions';
import { ProfileTokens } from './profile-tokens';

const scopeLabels = (form: HTMLElement): (string | undefined)[] =>
  Array.from(form.querySelectorAll('.scope > .radio')).map((l) => l.textContent?.trim());

function chooseScope(form: HTMLElement, label: string): void {
  const option = Array.from(form.querySelectorAll('.scope > .radio')).find((l) => l.textContent?.includes(label));
  option!.querySelector('input')!.dispatchEvent(new Event('change'));
}

function noAdministrator(get: <A>(token: abstract new (...args: never[]) => A) => A): void {
  (get(SessionApi) as FakeSessionApi).meResult = { roles: [], capabilities: [] };
}

const overlay = (): HTMLElement => document.querySelector('.cdk-overlay-container') as HTMLElement;

describe('Profile (R-UI-029)', () => {
  beforeEach(() => {
    browserStorage.clear('local');
    browserStorage.clear('session');
  });

  it('R-UI-029 shows identity, groups with source and roles; it has no password, unlock or editable email', async () => {
    const { root } = await renderScreen(Profile);
    const t = text(root);
    expect(root.querySelector<HTMLInputElement>('input[name="displayName"]')?.value).toBe('Alice Example');
    expect(t).toContain('team-web');
    expect(t).toContain('oidc:corp');
    expect(t).toContain('admin');
    expect(root.querySelector('input[type="password"]')).toBeNull();
    expect(t).not.toContain('Unlock');
    // The email is a read-only field: it comes from the identity provider (Q-UI-333).
    expect(root.querySelector<HTMLInputElement>('input[name="email"]')?.readOnly).toBe(true);
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('Q-UI-333 the profile is one column: identity as read-only fields, then quiet sections, Preferences open and Groups open only with a claim notice', async () => {
    const { root } = await renderScreen(Profile);
    expect(root.querySelector('section.card, dl.facts')).toBeNull();
    const sections = [...root.querySelectorAll('trove-disclosure')].map((d) => ({
      heading: d.querySelector('.heading')?.textContent?.replace('chevron_right', '').trim(),
      open: d.querySelector('.toggle')?.getAttribute('aria-expanded'),
    }));
    expect(sections).toEqual([
      { heading: 'Preferences', open: 'true' },
      { heading: 'Groups', open: 'false' },
      { heading: 'System roles', open: 'false' },
    ]);
    expect(root.querySelector<HTMLInputElement>('input[name="username"]')?.readOnly).toBe(true);
  });

  it('R-AUTH-008 a missing group claim is a status notice that names the provider and the reason', async () => {
    const { root } = await renderScreen(Profile, {
      setup: (get) =>
        ((get(IdentityApi) as FakeIdentityApi).meResult = sampleUser({
          groupClaimMissing: { provider: 'corp', reason: 'absent' },
        })),
    });
    const notice = root.querySelector('p[role="status"]') as HTMLElement;
    expect(text(notice)).toContain('corp');
    expect(text(notice)).toContain('carried no group claim');
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-AUTH-008 no notice without the missing claim', async () => {
    const { root } = await renderScreen(Profile);
    expect(root.querySelector('p[role="status"]')).toBeNull();
  });

  it('R-API-012 saves the preferences with the loaded ETag', async () => {
    const { fixture, root, get } = await renderScreen(Profile);
    typeInto(root, 'Theme', 'dark');
    await settle(fixture);
    buttonByText(root, 'Save preferences').click();
    await settle(fixture);
    const prefs = get(PreferencesApi) as FakePreferencesApi;
    expect(prefs.stored.theme).toBe('dark');
    expect(prefs.stored.revision).toBe(2);
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark');
    document.documentElement.removeAttribute('data-theme');
  });

  it('R-UI-042 a 412 on the preferences opens the conflict dialog and Overwrite stores mine', async () => {
    const { fixture, root, get } = await renderScreen(Profile);
    const prefs = get(PreferencesApi) as FakePreferencesApi;
    // Someone else saved in between.
    await prefs.patch({ keyboardShortcuts: true }, '"1"');
    typeInto(root, 'Theme', 'light');
    await settle(fixture);
    buttonByText(root, 'Save preferences').click();
    await settle(fixture);
    const dialog = overlay().querySelector('trove-conflict-dialog') as HTMLElement;
    expect(dialog).toBeTruthy();
    expect(text(dialog)).toContain('Changed by someone else');
    buttonByText(dialog, 'Overwrite').click();
    await sleep(fixture, 300);
    expect(prefs.stored.theme).toBe('light');
    expect(prefs.stored.revision).toBe(3);
    document.documentElement.removeAttribute('data-theme');
  });
});

describe('Personal tokens (R-UI-029)', () => {
  beforeEach(() => {
    browserStorage.clear('local');
    browserStorage.clear('session');
  });

  it('R-UI-029 lists own tokens and marks the ones that expire within seven days', async () => {
    const soon = new Date(Date.now() + 3 * 86_400_000).toISOString();
    const { root } = await renderScreen(ProfileTokens, {
      setup: (get) => {
        (get(TokensApi) as FakeTokensApi).tokenList = [sampleToken({ description: 'laptop', expiresAt: soon })];
      },
    });
    expect(text(root)).toContain('laptop');
    expect(text(root)).toContain('Expires soon');
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-UI-029 revoking all tokens is a danger button last on the page, in a closed section Advanced, and asks first', async () => {
    const { fixture, root, get } = await renderScreen(ProfileTokens);
    // Not in the toolbar above the list: a destructive bulk action comes last (ui-style-guide.md).
    expect([...root.querySelectorAll('.toolbar button')].map((b) => b.textContent?.trim())).not.toContain('Revoke all tokens');
    const advanced = [...root.querySelectorAll('trove-disclosure')].at(-1)!;
    expect(text(advanced)).toContain('Advanced');
    const revoke = [...advanced.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim() === 'Revoke all tokens')!;
    expect(revoke.classList.contains('btn-danger')).toBe(true);
    expect(text(advanced)).toContain('This cannot be undone.');
    revoke.click();
    await settle(fixture);
    const confirm = overlay().querySelector('trove-confirm-dialog') as HTMLElement;
    expect(confirm.textContent).toContain('Revoke all tokens?');
    buttonByText(confirm, 'Revoke all').click();
    await sleep(fixture, 300);
    expect((get(TokensApi) as FakeTokensApi).calls).toContain('revokeAllOwn');
  });

  it('R-EP-011 the personal token form offers only the endpoints that accept personal tokens', async () => {
    // The fake "customers" endpoint accepts service-account tokens only (Q-EP-10): a personal token bound
    // to it would work nowhere, and the server refuses it with 400 invalid_scope.
    const { fixture, root, get } = await renderScreen(ProfileTokens);
    buttonByText(root, 'Create token').click();
    await settle(fixture);
    const form = root.querySelector('trove-token-form') as HTMLElement;
    const names = Array.from(form.querySelectorAll('.endpoints trove-checkbox')).map((e) => e.textContent?.trim());
    expect(names).toEqual(['default']);
    typeInto(form, 'Description', 'bound pat');
    form.querySelector<HTMLInputElement>('.endpoints input[type="checkbox"]')!.click();
    await settle(fixture);
    buttonByText(form, 'Create token').click();
    await settle(fixture);
    expect((get(TokensApi) as FakeTokensApi).lastRequest?.scope).toEqual({ inherit: true, endpoints: ['default'] });
  });

  it('R-UI-029 creates a token, shows its value once with copy and warning, and keeps it out of storage', async () => {
    const { fixture, root, get } = await renderScreen(ProfileTokens);
    buttonByText(root, 'Create token').click();
    await settle(fixture);
    expect(await seriousViolations(root)).toEqual([]);
    typeInto(root.querySelector('trove-token-form')!, 'Description', 'laptop npm');
    buttonByText(root.querySelector('trove-token-form')!, 'Create token').click();
    await settle(fixture);
    const tokens = get(TokensApi) as FakeTokensApi;
    expect(tokens.calls).toContain('create laptop npm');
    expect(tokens.lastRequest?.scope).toEqual({ inherit: true });
    expect(tokens.keys[0]).toMatch(/^[0-9a-f-]{36}$/);

    const secret = root.querySelector('trove-token-secret') as HTMLElement;
    const input = secret.querySelector('input') as HTMLInputElement;
    expect(input.type).toBe('password');
    expect(input.getAttribute('autocomplete')).toBe('off');
    expect(input.getAttribute('spellcheck')).toBe('false');
    expect(input.value).toBe(SECRET_VALUE);
    expect(text(secret)).toContain('cannot be retrieved again');
    expect(await seriousViolations(root)).toEqual([]);

    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    buttonByText(secret, 'Copy').click();
    await settle(fixture);
    expect(writeText).toHaveBeenCalledWith(SECRET_VALUE);

    expect(storedText()).not.toContain('trv_');
    buttonByText(secret, 'I have copied it').click();
    await settle(fixture);
    expect(root.innerHTML).not.toContain(SECRET_VALUE);
  });

  it('R-UI-029 R-AUTH-024 an administrator chooses administrator rights, and the default stays without them', async () => {
    const { fixture, root, get } = await renderScreen(ProfileTokens);
    buttonByText(root, 'Create token').click();
    await settle(fixture);
    const form = root.querySelector('trove-token-form') as HTMLElement;
    expect(scopeLabels(form)).toEqual([
      'Administrator: everything on this installation',
      'All repository permissions of the owner, without administration',
      'Only the repositories and actions below',
    ]);
    expect(form.querySelectorAll<HTMLInputElement>('.scope > .radio input')[1]!.checked).toBe(true);
    typeInto(form, 'Description', 'docker');
    chooseScope(form, 'Administrator');
    await settle(fixture);
    buttonByText(form, 'Create token').click();
    await settle(fixture);
    expect((get(TokensApi) as FakeTokensApi).lastRequest?.scope).toEqual({ inherit: true, admin: true });
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-UI-029 R-AUTH-024 a caller without the administrator role is not offered administrator rights', async () => {
    const { fixture, root } = await renderScreen(ProfileTokens, { setup: noAdministrator });
    buttonByText(root, 'Create token').click();
    await settle(fixture);
    expect(scopeLabels(root.querySelector('trove-token-form') as HTMLElement)).toEqual([
      'All repository permissions of the owner, without administration',
      'Only the repositories and actions below',
    ]);
  });

  it('R-UI-029 R-AUTH-024 an admin token of an owner who lost the role says that its administrator rights are inactive', async () => {
    const admin = sampleToken({ description: 'old admin', scope: { inherit: true, admin: true, repositories: [], system: [] } });
    const { root } = await renderScreen(ProfileTokens, {
      setup: (get) => {
        noAdministrator(get);
        (get(TokensApi) as FakeTokensApi).tokenList = [admin];
      },
    });
    expect(text(root)).toContain('Administrator rights inactive');
  });

  it('R-UI-029 an administrator\'s own admin token shows no such notice', async () => {
    const admin = sampleToken({ description: 'admin', ownerIsAdmin: true, scope: { inherit: true, admin: true, repositories: [], system: [] } });
    const { root } = await renderScreen(ProfileTokens, {
      setup: (get) => {
        (get(TokensApi) as FakeTokensApi).tokenList = [admin];
      },
    });
    expect(text(root)).toContain('Administrator: everything on this installation');
    expect(text(root)).not.toContain('Administrator rights inactive');
  });

  it('R-UI-029 a restricted scope sends the repositories and actions', async () => {
    const { fixture, root, get } = await renderScreen(ProfileTokens);
    buttonByText(root, 'Create token').click();
    await settle(fixture);
    const form = root.querySelector('trove-token-form') as HTMLElement;
    typeInto(form, 'Description', 'ci');
    chooseScope(form, 'Only the repositories');
    await settle(fixture);
    typeInto(form, 'Repositories', 'npm-internal\nnpm-team-*');
    await settle(fixture);
    (form.querySelectorAll('trove-checkbox input')[2] as HTMLInputElement).click();
    await settle(fixture);
    buttonByText(form, 'Create token').click();
    await settle(fixture);
    const scope = (get(TokensApi) as FakeTokensApi).lastRequest?.scope;
    expect(scope?.repositories?.[0]?.repositories).toEqual(['npm-internal', 'npm-team-*']);
    expect(scope?.repositories?.[0]?.actions).toContain('read');
  });

  it('R-UI-029 refuses to submit without a description', async () => {
    const { fixture, root, get } = await renderScreen(ProfileTokens);
    buttonByText(root, 'Create token').click();
    await settle(fixture);
    buttonByText(root.querySelector('trove-token-form')!, 'Create token').click();
    await settle(fixture);
    expect(text(root)).toContain('Enter a description');
    expect((get(TokensApi) as FakeTokensApi).calls).toHaveLength(0);
  });

  it('R-UI-042 recent authentication keeps the non-secret entries as a draft, never a token', async () => {
    const { fixture, root, get } = await renderScreen(ProfileTokens);
    (get(TokensApi) as FakeTokensApi).failure = problem(401, 'reauthentication_required');
    buttonByText(root, 'Create token').click();
    await settle(fixture);
    typeInto(root.querySelector('trove-token-form')!, 'Description', 'laptop npm');
    buttonByText(root.querySelector('trove-token-form')!, 'Create token').click();
    await settle(fixture);
    const stored = browserStorage.entries('session').filter(([k]) => k.startsWith('trove.draft:'));
    expect(stored).toHaveLength(1);
    expect(stored[0]![1]).toContain('laptop npm');
    expect(storedText()).not.toContain('trv_');
  });

  it('R-UI-042 a retried create that answers token_already_issued revokes the lost token and creates again', async () => {
    const { fixture, root, get } = await renderScreen(ProfileTokens);
    const tokens = get(TokensApi) as FakeTokensApi;
    tokens.failure = problem(409, 'token_already_issued', { tokenId: 'lost-id' });
    buttonByText(root, 'Create token').click();
    await settle(fixture);
    typeInto(root.querySelector('trove-token-form')!, 'Description', 'laptop npm');
    buttonByText(root.querySelector('trove-token-form')!, 'Create token').click();
    await settle(fixture);
    expect(tokens.calls).toContain('revoke lost-id');
    expect(tokens.keys).toHaveLength(1);
    expect(root.querySelector('trove-token-secret')).toBeTruthy();
  });

  it('R-UI-029 Q-UI-332 an owner\'s token list shows token, scope, expiry, last use, created and created via (narrow), and the row\'s actions', async () => {
    const { root } = await renderScreen(ProfileTokens);
    const table = root.querySelector('trove-token-table trove-data-table')!;
    const heads = [...table.querySelectorAll<HTMLElement>('thead th')];
    expect(heads.map((th) => text(th).replace(/help_outline/g, ''))).toEqual([
      'Token', 'Scope', 'Expires', 'Last used', 'Created', 'Created via', 'Actions',
    ]);
    expect(heads.map((th) => th.classList.contains('narrow-drop'))).toEqual([false, false, false, false, true, true, false]);
    expect(table.classList.contains('stack-wide')).toBe(false);
    // Rotate is a quiet icon named for the row, Revoke a quiet button; a date shows the day, its title the time.
    const row = table.querySelector('tbody tr')!;
    const rotate = [...row.querySelectorAll('td.actions button')].find((b) => text(b).includes('Rotate'))!;
    expect(rotate.classList.contains('btn-icon')).toBe(true);
    expect(row.querySelector('td time')?.getAttribute('title')).toBeTruthy();
  });

  it('R-UI-029 revokes a token after confirmation', async () => {
    const { fixture, root, get } = await renderScreen(ProfileTokens);
    buttonByText(root.querySelector('trove-token-table')!, 'Revoke').click();
    await settle(fixture);
    buttonByText(overlay().querySelector('trove-confirm-dialog') as HTMLElement, 'Revoke').click();
    await sleep(fixture, 300);
    expect((get(TokensApi) as FakeTokensApi).calls[0]).toMatch(/^revoke /);
  });

  it('R-UI-029 rotates a token, asks for the overlap and shows the new value once', async () => {
    const { fixture, root, get } = await renderScreen(ProfileTokens);
    buttonByText(root.querySelector('trove-token-table')!, 'Rotate').click();
    await settle(fixture);
    const dialog = overlay().querySelector('trove-rotate-dialog') as HTMLElement;
    expect(await seriousViolations(dialog)).toEqual([]);
    buttonByText(dialog, 'Rotate').click();
    await sleep(fixture, 300);
    expect((get(TokensApi) as FakeTokensApi).calls.some((c) => c.startsWith('rotate') && c.endsWith('overlap=0'))).toBe(true);
    expect(root.querySelector('trove-token-secret')).toBeTruthy();
  });
});

describe('Sessions (R-UI-029)', () => {
  it('R-UI-029 lists sessions, marks this one and ends another', async () => {
    const { fixture, root, get } = await renderScreen(ProfileSessions);
    const rows = [...root.querySelectorAll('tbody tr')];
    expect(rows).toHaveLength(2);
    expect(text(rows[0]!)).toContain('This session');
    expect(rows[0]!.querySelector('button')).toBeNull();
    expect(await seriousViolations(root)).toEqual([]);
    rows[1]!.querySelector('button')!.click();
    await settle(fixture);
    expect((get(SessionsApi) as FakeSessionsApi).calls[0]).toMatch(/^endOwn /);
    expect(root.querySelectorAll('tbody tr')).toHaveLength(1);
  });

  it('R-UI-029 ends all other sessions after confirmation and keeps the current one', async () => {
    const { fixture, root, get } = await renderScreen(ProfileSessions);
    buttonByText(root, 'End all other sessions').click();
    await settle(fixture);
    buttonByText(overlay().querySelector('trove-confirm-dialog') as HTMLElement, 'End other sessions').click();
    await sleep(fixture, 300);
    expect((get(SessionsApi) as FakeSessionsApi).calls).toContain('endOwnAll keepCurrent=true');
    expect(root.textContent).not.toContain('End all other sessions');
  });
});
