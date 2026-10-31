import { FieldProblem } from '../../api/problem';
import { RICH_SETTINGS_SCHEMA } from '../../api/testing/fakes-catalog';
import { JsonObject } from '../../shared/merge-patch';
import { expectAccessible, renderScreen, settle, text, type } from '../../testing/screen';
import { FormatSettings } from './format-settings';
import { SettingSchema, settingNodes } from './settings-schema';

async function render(
  cls: string,
  value: JsonObject = {},
  errors = new Map<string, FieldProblem>(),
) {
  return renderScreen(FormatSettings, {
    inputs: {
      nodes: settingNodes(RICH_SETTINGS_SCHEMA as SettingSchema, cls, 'npm'),
      pointer: '/npm',
      value,
      fieldErrors: errors,
    },
  });
}

const el = <T extends Element>(root: Element, selector: string): T => {
  const found = root.querySelector<T>(selector);
  if (!found) throw new Error(`no ${selector}`);
  return found;
};

describe('Format settings rendered from the schema', () => {
  it('R-UI-021 R-ARCH-004 a nested object is a fieldset; booleans, enums, strings and integers get native controls with the schema default and description; the form passes axe', async () => {
    const r = await render('remote');
    const root = r.root;

    const removed = el<HTMLInputElement>(root, '#f-npm-serveRemovedVersions');
    expect(removed.type).toBe('checkbox');
    expect(removed.checked).toBe(false);
    expect(el(root, 'label[for="f-npm-serveRemovedVersions"]').textContent).toContain(
      'serveRemovedVersions',
    );
    expect(el(root, '#f-npm-serveRemovedVersions-v').textContent).toContain('Default: off');
    expect(root.querySelector('#f-npm-latestGuard')).toBeNull();

    const group = el<HTMLFieldSetElement>(root, 'fieldset#f-npm-audit');
    expect(el(group, 'legend').textContent).toContain('audit');
    expect(el(group, '#f-npm-audit-d').getAttribute('lang')).toBe('en');

    const mode = el<HTMLSelectElement>(root, '#f-npm-audit-mode');
    expect([...mode.options].map((o) => o.textContent?.trim())).toEqual(['forward', 'disabled']);
    expect(mode.options[mode.selectedIndex]?.textContent?.trim()).toBe('forward');

    expect(el<HTMLInputElement>(root, '#f-npm-audit-providerURL').type).toBe('url');
    expect(root.querySelector('#f-npm-audit-providerURL-v')).toBeNull();
    const timeout = el<HTMLInputElement>(root, '#f-npm-audit-timeout');
    expect(timeout.value).toBe('');
    expect(timeout.getAttribute('aria-describedby')).toBe(
      'f-npm-audit-timeout-d f-npm-audit-timeout-v',
    );
    expect(text(el(root, '#f-npm-audit-timeout-v'))).toBe('Default: 30s');
    expect(el(root, 'label[for="f-npm-audit-retries"]').textContent).toContain('Retries');
    expect(el<HTMLInputElement>(root, '#f-npm-audit-retries').getAttribute('inputmode')).toBe(
      'numeric',
    );
    expect(text(group)).toContain('This setting cannot be changed here. Set it through the API.');

    await expectAccessible(root);
  });

  it('R-UI-021 the controls edit the member in place: a boolean, an enum, a duration and an integer', async () => {
    const r = await render('remote');
    const settings = r.fixture.componentInstance;

    const removed = el<HTMLInputElement>(r.root, '#f-npm-serveRemovedVersions');
    removed.checked = true;
    removed.dispatchEvent(new Event('change', { bubbles: true }));
    const mode = el<HTMLSelectElement>(r.root, '#f-npm-audit-mode');
    type(mode, '1');
    type(el<HTMLInputElement>(r.root, '#f-npm-audit-timeout'), '45s');
    type(el<HTMLInputElement>(r.root, '#f-npm-audit-retries'), '5');
    await settle(r.fixture);

    expect(settings.value()).toEqual({
      serveRemovedVersions: true,
      audit: { mode: 'disabled', timeout: '45s', retries: 5 },
    });

    type(el<HTMLInputElement>(r.root, '#f-npm-audit-timeout'), '');
    await settle(r.fixture);
    expect(settings.value()).toEqual({
      serveRemovedVersions: true,
      audit: { mode: 'disabled', retries: 5 },
    });
  });

  it('R-UI-021 a stored value is shown, and an enum without a default offers "Not set"', async () => {
    const r = await render('remote', {
      serveRemovedVersions: true,
      audit: { timeout: '1m', hosts: ['a'] },
    });
    expect(el<HTMLInputElement>(r.root, '#f-npm-serveRemovedVersions').checked).toBe(true);
    expect(el<HTMLInputElement>(r.root, '#f-npm-audit-timeout').value).toBe('1m');
    expect(text(r.root)).toContain('["a"]');

    const bare = await renderScreen(FormatSettings, {
      inputs: {
        nodes: settingNodes(
          {
            type: 'object',
            properties: { tier: { type: 'string', enum: ['a', 'b'] } },
          } as SettingSchema,
          'local',
          'npm',
        ),
        pointer: '/npm',
        value: {},
      },
    });
    const tier = el<HTMLSelectElement>(bare.root, '#f-npm-tier');
    expect([...tier.options].map((o) => o.textContent?.trim())).toEqual(['Not set', 'a', 'b']);
    expect(tier.selectedIndex).toBe(0);
  });

  it('R-UI-021 R-UI-044 server field errors appear at their control, at the nearest group for an unknown member, and at the section for the member as a whole', async () => {
    const r = await render(
      'remote',
      { audit: { timeout: 'soon' } },
      new Map<string, FieldProblem>([
        [
          '/npm/audit/timeout',
          {
            pointer: '/npm/audit/timeout',
            code: 'invalid_value',
            detail: 'does not match the pattern',
          },
        ],
        [
          '/npm/audit/bogus',
          { pointer: '/npm/audit/bogus', code: 'invalid_value', detail: 'unknown member bogus' },
        ],
        ['/npm', { pointer: '/npm', code: 'invalid_value', detail: 'must be an object' }],
      ]),
    );
    const timeout = el<HTMLInputElement>(r.root, '#f-npm-audit-timeout');
    expect(timeout.getAttribute('aria-invalid')).toBe('true');
    expect(timeout.getAttribute('aria-describedby')).toContain('f-npm-audit-timeout-e');
    expect(el(r.root, '#f-npm-audit-timeout-e').textContent).toBeTruthy();
    expect(el(r.root, '#f-npm-audit-e').textContent).toBeTruthy();
    expect(el(r.root, '#f-npm').querySelectorAll(':scope > .field-error')).toHaveLength(1);
    await expectAccessible(r.root);
  });
});
