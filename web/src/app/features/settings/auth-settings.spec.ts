import { AuthSettingsApi } from '../../api/auth-settings-api';
import { FakeAuthSettingsApi, problem, sampleAuthSettings } from '../../api/testing/access-fakes';
import { browserStorage, buttonByText, renderScreen, settle, textOf, typeInto } from '../../shared/access/testing';
import { seriousViolations } from '../../testing/axe';
import { AuthSettings } from './auth-settings';

const overlay = (): HTMLElement => document.querySelector('.cdk-overlay-container') as HTMLElement;

describe('Authentication settings (R-UI-031)', () => {
  beforeEach(() => browserStorage.clear('session'));

  it('R-UI-031 shows the runtime values with their ceilings and the read-only status, and passes axe', async () => {
    const { root } = await renderScreen(AuthSettings);
    const t = textOf(root);
    expect(t).toContain('Anonymous access');
    expect(t).toContain('Ceiling from the configuration: 365 days');
    expect(t).toContain('Corporate SSO');
    expect(t).toContain('discovery document loaded');
    expect(t).toContain('Idle timeout');
    expect(t).toContain('1 hour');
    expect(t).toContain('HTTP Basic');
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-API-013 saves a merge patch of the changed members with the loaded ETag', async () => {
    const { fixture, root, get } = await renderScreen(AuthSettings);
    typeInto(root, 'Default lifetime (days)', '30');
    await settle(fixture);
    buttonByText(root, 'Save settings').click();
    await settle(fixture);
    const api = get(AuthSettingsApi) as FakeAuthSettingsApi;
    expect(api.patches).toHaveLength(1);
    expect(api.patches[0]).toEqual({ patch: { tokens: { pat: { defaultTTLSeconds: 2_592_000 } } }, etag: '"100"' });
    expect(api.stored.etag).toBe('"101"');
  });

  it('R-AUTH-040 anonymous access cannot be switched on above the ceiling and says why', async () => {
    const { root } = await renderScreen(AuthSettings, {
      setup: (get) => {
        const api = get(AuthSettingsApi) as FakeAuthSettingsApi;
        const s = sampleAuthSettings();
        api.stored = {
          ...s,
          settings: { ...s.settings, ceilings: { ...s.settings.ceilings, anonymousAllowed: false } },
        };
      },
    });
    expect(root.querySelector<HTMLInputElement>('mat-checkbox input')!.disabled).toBe(true);
    expect(textOf(root)).toContain('does not allow anonymous access');
  });

  it('R-UI-042 a 412 opens the conflict dialog; Overwrite sends my patch with the ETag read now', async () => {
    const { fixture, root, get } = await renderScreen(AuthSettings);
    const api = get(AuthSettingsApi) as FakeAuthSettingsApi;
    // Someone else changed the settings after this screen loaded.
    await api.patch({ tokens: { expiryNoticeSeconds: 86_400 } }, '"100"');
    typeInto(root, 'Default lifetime (days)', '30');
    await settle(fixture);
    buttonByText(root, 'Save settings').click();
    await settle(fixture);
    const dialog = overlay().querySelector('trove-conflict-dialog') as HTMLElement;
    expect(dialog).toBeTruthy();
    buttonByText(dialog, 'Overwrite').click();
    await settle(fixture, 300);
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
    buttonByText(root, 'Save settings').click();
    await settle(fixture);
    expect(textOf(root.querySelector('.field-error')!)).toContain('above the ceiling');
  });

  it('R-UI-042 recent authentication keeps the entries as a draft and restores them after sign-in', async () => {
    const { fixture, root, get } = await renderScreen(AuthSettings);
    (get(AuthSettingsApi) as FakeAuthSettingsApi).failure = problem(401, 'reauthentication_required');
    typeInto(root, 'Default lifetime (days)', '30');
    await settle(fixture);
    buttonByText(root, 'Save settings').click();
    await settle(fixture);
    const drafts = browserStorage.entries('session').filter(([k]) => k.startsWith('trove.draft:'));
    expect(drafts).toHaveLength(1);

    // The sign-in returns to the same route: a new instance picks the draft up.
    const again = await renderScreen(AuthSettings);
    expect(
      again.root.querySelector<HTMLInputElement>('input[name="patDefault"]')!.value,
    ).toBe('30');
    expect(textOf(again.root)).toBeDefined();
  });

  it('R-UI-005 without settings:write the values are read only and there is no save button', async () => {
    const { root } = await renderScreen(AuthSettings, { capabilities: ['settings:read'] });
    expect(root.textContent).not.toContain('Save settings');
    expect(root.querySelector<HTMLInputElement>('input[name="patDefault"]')!.readOnly).toBe(true);
  });
});
