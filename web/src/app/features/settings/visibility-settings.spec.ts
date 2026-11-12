import { ApiError } from '../../api/problem';
import { VisibilityApi } from '../../api/visibility-api';
import { FakeVisibilityApi } from '../../api/testing/fakes-projects';
import { button, expectAccessible, renderScreen, settle, text } from '../../testing/screen';
import { GeneralSettings } from './general-settings';

async function render(options: { capabilities?: readonly string[]; setup?: (api: FakeVisibilityApi) => void } = {}) {
  const r = await renderScreen(GeneralSettings, {
    capabilities: options.capabilities,
    setup: (get) => options.setup?.(get(VisibilityApi) as FakeVisibilityApi),
  });
  const api = r.get(VisibilityApi) as FakeVisibilityApi;
  const radios = () => [...r.root.querySelectorAll<HTMLInputElement>('trove-visibility-level input[type="radio"]')];
  const save = () => button(r.root, /^\s*Save changes\s+\(visibility\)\s*$/);
  return { ...r, api, radios, save };
}

describe('General settings: the installation default visibility (R-UI-053)', () => {
  it('R-UI-053 the section Visibility is closed, names the default in its summary and holds the three levels, and passes axe', async () => {
    const { root, radios } = await render();
    const toggle = [...root.querySelectorAll<HTMLButtonElement>('button[aria-expanded]')].find((b) => text(b).endsWith('Visibility'))!;
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
    expect(text(root)).toContain('New projects are private.');
    expect(radios().map((x) => x.checked)).toEqual([true, false, false]);
    expect(text(root)).toContain('The default applies to projects and repositories created later.');
    toggle.click();
    await expectAccessible(root);
  });

  it('R-UI-053 choosing Internal and saving patches the default with the loaded ETag', async () => {
    const { fixture, api, radios, save } = await render();
    expect(save().disabled).toBe(true);
    radios()[1]!.click();
    await settle(fixture);
    save().click();
    await settle(fixture);
    expect(api.patches).toEqual([{ patch: { defaultLevel: 'internal' }, etag: '"1"' }]);
    expect(api.stored.settings.defaultLevel).toBe('internal');
    expect(save().disabled).toBe(true);
  });

  it('R-UI-053 while the installation admits no request without a credential, Public is disabled and says why', async () => {
    const { root, radios } = await render({ setup: (api) => (api.stored.settings.publicAvailable = false) });
    expect(radios()[2]!.disabled).toBe(true);
    expect(text(root)).toContain('endpoints.default.auth.anonymous');
  });

  it('R-UI-053 lists what is public now, each linked to its settings, or says nothing is', async () => {
    const empty = await render();
    expect(text(empty.root.querySelector('[data-testid="public-objects"]')!)).toBe('Nothing is public.');

    const { root } = await render({
      setup: (api) => (api.stored.settings.publicObjects = { projects: ['web'], repositories: ['web/npm-public'], truncated: false }),
    });
    const links = [...root.querySelectorAll<HTMLAnchorElement>('[data-testid="public-objects"] a')];
    expect(links.map((a) => [text(a), a.getAttribute('href')])).toEqual([
      ['web', '/projects/web/settings'],
      ['web/npm-public', '/projects/web/repositories/npm-public/settings'],
    ]);
  });

  it('R-UI-053 a holder of settings:read alone sees the section read-only with the reason', async () => {
    const { root, radios } = await render({ capabilities: ['settings:read'] });
    expect(radios().every((x) => x.closest('fieldset')!.disabled)).toBe(true);
    expect(text(root)).toContain('security-settings:write');
    expect(() => button(root, /Save changes\s+\(visibility\)/)).toThrow();
  });

  it('R-UI-053 a 409 public_not_allowed is shown at the group', async () => {
    const { fixture, root, api, radios, save } = await render();
    api.failure = new ApiError(409, { code: 'public_not_allowed' }, 'req-1');
    radios()[2]!.click();
    await settle(fixture);
    save().click();
    await settle(fixture);
    expect(text(root.querySelector('trove-error-notice')!)).toContain('admits no request without a credential');
  });
});
