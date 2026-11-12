import { AuthSettingsApi } from '../../api/auth-settings-api';
import { FakeAuthSettingsApi, problem, sampleAuthSettings } from '../../api/testing/access-fakes';
import { browserStorage, button, buttonByText, renderScreen, settle, text, typeInto, sleep } from '../../testing/screen';
import { seriousViolations } from '../../testing/axe';
import { AuthSettings } from './auth-settings';

const overlay = (): HTMLElement => document.querySelector('.cdk-overlay-container') as HTMLElement;

/** The "Save changes" button of one group; its accessible name carries the group (Q-UI-222). */
const saveButton = (root: ParentNode, group: string): HTMLButtonElement =>
  button(root, new RegExp(`^\\s*Save changes\\s+\\(${group}\\)\\s*$`));
const saveTokens = (root: ParentNode): HTMLButtonElement => saveButton(root, 'token lifetimes');

describe('Authentication settings (R-UI-031)', () => {
  beforeEach(() => browserStorage.clear('session'));

  it('R-UI-031 shows the runtime values with their ceilings and the read-only status, and passes axe', async () => {
    const { root } = await renderScreen(AuthSettings);
    const t = text(root);
    expect(t).toContain('New users are external');
    expect(t).not.toContain('without a credential');
    expect(t).toContain('Ceiling from the configuration: 365 days');
    expect(t).toContain('Corporate SSO');
    expect(t).toContain('discovery document loaded');
    expect(t).toContain('Idle timeout');
    expect(t).toContain('1 hour');
    expect(t).toContain('HTTP Basic');
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-AUTH-040 the break-glass status names a missing or unusable password instead of saying only Enabled', async () => {
    const withPassword = (passwordStatus: 'ok' | 'not_configured' | 'unusable' | 'not_applicable', enabled = true) => ({
      setup: (get: <A>(token: abstract new (...args: never[]) => A) => A) => {
        const api = get(AuthSettingsApi) as FakeAuthSettingsApi;
        const s = sampleAuthSettings();
        api.stored = {
          ...s,
          settings: {
            ...s.settings,
            status: { ...s.settings.status, breakGlass: { enabled, passwordStatus, lockedUntil: null } },
          },
        };
      },
    });
    const ok = await renderScreen(AuthSettings, withPassword('ok'));
    expect(text(ok.root)).toContain('Enabled.');
    expect(text(ok.root)).not.toContain('No password');

    const missing = await renderScreen(AuthSettings, withPassword('not_configured'));
    expect(text(missing.root)).toContain('No password is configured');
    expect(text(missing.root)).toContain('auth.break_glass.password_file');
    expect(text(missing.root)).toContain('Every local sign-in is refused');

    const unusable = await renderScreen(AuthSettings, withPassword('unusable'));
    expect(text(unusable.root)).toContain('password file is not usable');
    expect(text(unusable.root)).toContain('Every local sign-in is refused');

    const off = await renderScreen(AuthSettings, withPassword('not_applicable', false));
    expect(text(off.root)).toContain('Disabled.');
    expect(text(off.root)).not.toContain('Every local sign-in is refused');
  });

  it('R-API-013 saves a merge patch of the changed members with the loaded ETag', async () => {
    const { fixture, root, get } = await renderScreen(AuthSettings);
    typeInto(root, 'Default lifetime (days)', '30');
    await settle(fixture);
    saveTokens(root).click();
    await settle(fixture);
    const api = get(AuthSettingsApi) as FakeAuthSettingsApi;
    expect(api.patches).toHaveLength(1);
    expect(api.patches[0]).toEqual({ patch: { tokens: { pat: { defaultTTLSeconds: 2_592_000 } } }, etag: '"100"' });
    expect(api.stored.etag).toBe('"101"');
  });

  it('Q-UI-222 each group has its own Save changes: saving one sends only its members and keeps the other group\'s edits', async () => {
    const { fixture, root, get } = await renderScreen(AuthSettings);
    const api = get(AuthSettingsApi) as FakeAuthSettingsApi;
    expect(saveButton(root, 'new users are external').disabled).toBe(true);
    expect(saveTokens(root).disabled).toBe(true);
    root.querySelector<HTMLInputElement>('trove-checkbox input')!.click();
    typeInto(root, 'Default lifetime (days)', '30');
    await settle(fixture);
    expect(saveButton(root, 'new users are external').disabled).toBe(false);
    expect(saveTokens(root).disabled).toBe(false);

    saveButton(root, 'new users are external').click();
    await settle(fixture);
    expect(api.patches).toHaveLength(1);
    expect(api.patches[0]).toEqual({ patch: { newUsersExternal: true }, etag: '"100"' });
    expect(saveButton(root, 'new users are external').disabled).toBe(true);
    expect(saveTokens(root).disabled).toBe(false);
    expect(root.querySelector<HTMLInputElement>('input[name="patDefault"]')!.value).toBe('30');

    saveTokens(root).click();
    await settle(fixture);
    expect(api.patches[1]).toEqual({ patch: { tokens: { pat: { defaultTTLSeconds: 2_592_000 } } }, etag: '"101"' });
  });

  it('R-AUTH-044 without settings:write the flag for new users is shown but cannot be changed', async () => {
    const { root } = await renderScreen(AuthSettings, { capabilities: ['settings:read'] });
    expect(root.querySelector<HTMLInputElement>('trove-checkbox input')!.disabled).toBe(true);
    expect(text(root)).toContain('existing users keep their flag');
  });

  it('R-UI-042 a 412 opens the conflict dialog; Overwrite sends my patch with the ETag read now', async () => {
    const { fixture, root, get } = await renderScreen(AuthSettings);
    const api = get(AuthSettingsApi) as FakeAuthSettingsApi;
    // Someone else changed the settings after this screen loaded.
    await api.patch({ tokens: { expiryNoticeSeconds: 86_400 } }, '"100"');
    typeInto(root, 'Default lifetime (days)', '30');
    await settle(fixture);
    saveTokens(root).click();
    await settle(fixture);
    const dialog = overlay().querySelector('trove-conflict-dialog') as HTMLElement;
    expect(dialog).toBeTruthy();
    buttonByText(dialog, 'Overwrite').click();
    await sleep(fixture, 300);
    expect(api.stored.settings.tokens.pat.defaultTTLSeconds).toBe(2_592_000);
    expect(api.stored.etag).toBe('"102"');
  });

  it('R-UI-042 a field problem of the answer shows next to the field', async () => {
    const { fixture, root, get } = await renderScreen(AuthSettings);
    (get(AuthSettingsApi) as FakeAuthSettingsApi).failure = problem(422, 'validation_failed', {
      errors: [{ pointer: '/tokens/pat/maxTTLSeconds', code: 'exceeds_ceiling' }],
    });
    typeInto(root, 'Maximum lifetime (days)', '9999');
    await settle(fixture);
    saveTokens(root).click();
    await settle(fixture);
    expect(text(root.querySelector('.field-error')!)).toContain('above the ceiling');
  });

  it('R-UI-042 recent authentication keeps the entries as a draft and restores them after sign-in', async () => {
    const { fixture, root, get } = await renderScreen(AuthSettings);
    (get(AuthSettingsApi) as FakeAuthSettingsApi).failure = problem(401, 'reauthentication_required');
    typeInto(root, 'Default lifetime (days)', '30');
    await settle(fixture);
    saveTokens(root).click();
    await settle(fixture);
    const drafts = browserStorage.entries('session').filter(([k]) => k.startsWith('trove.draft:'));
    expect(drafts).toHaveLength(1);

    // The sign-in returns to the same route: a new instance picks the draft up.
    const again = await renderScreen(AuthSettings);
    expect(
      again.root.querySelector<HTMLInputElement>('input[name="patDefault"]')!.value,
    ).toBe('30');
    expect(text(again.root)).toBeDefined();
  });

  it('R-UI-005 without settings:write the values are read only and there is no save button', async () => {
    const { root } = await renderScreen(AuthSettings, { capabilities: ['settings:read'] });
    expect(root.textContent).not.toContain('Save changes');
    expect(root.querySelector<HTMLInputElement>('input[name="patDefault"]')!.readOnly).toBe(true);
  });
});
