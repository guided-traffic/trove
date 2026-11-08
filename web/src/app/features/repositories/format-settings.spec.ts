import { FieldProblem } from '../../api/problem';
import { DEB_SETTINGS_SCHEMA, NUGET_SETTINGS_SCHEMA, RICH_SETTINGS_SCHEMA } from '../../api/testing/fakes-catalog';
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

  it('R-UI-021 R-NUGET-020 the NuGet schema renders deleteMode on a local and forwardSearch on a remote, each with its default, and nothing on a virtual repository', async () => {
    const render = (cls: string, value: JsonObject = {}) =>
      renderScreen(FormatSettings, {
        inputs: {
          nodes: settingNodes(NUGET_SETTINGS_SCHEMA as SettingSchema, cls, 'nuget'),
          pointer: '/nuget',
          value,
          fieldErrors: new Map<string, FieldProblem>(),
        },
      });

    const local = await render('local');
    const mode = el<HTMLSelectElement>(local.root, '#f-nuget-deleteMode');
    expect([...mode.options].map((o) => o.textContent?.trim())).toEqual(['unlist', 'delete']);
    expect(mode.options[mode.selectedIndex]?.textContent?.trim()).toBe('unlist');
    expect(local.root.querySelector('#f-nuget-forwardSearch')).toBeNull();
    type(mode, '1');
    await settle(local.fixture);
    expect(local.fixture.componentInstance.value()).toEqual({ deleteMode: 'delete' });
    await expectAccessible(local.root);

    const remote = await render('remote');
    const forward = el<HTMLInputElement>(remote.root, '#f-nuget-forwardSearch');
    expect(forward.type).toBe('checkbox');
    expect(forward.checked).toBe(true);
    expect(text(el(remote.root, '#f-nuget-forwardSearch-v'))).toContain('Default: on');
    expect(remote.root.querySelector('#f-nuget-deleteMode')).toBeNull();
    await expectAccessible(remote.root);

    const virtual = await render('virtual');
    expect(virtual.root.querySelector('input, select')).toBeNull();
  });

  describe('Debian', () => {
    const render = (cls: string, value: JsonObject = {}, errors = new Map<string, FieldProblem>()) =>
      renderScreen(FormatSettings, {
        inputs: {
          nodes: settingNodes(DEB_SETTINGS_SCHEMA as SettingSchema, cls, 'deb'),
          pointer: '/deb',
          value,
          fieldErrors: errors,
        },
      });

    it('R-DEB-008 a local repository edits architectures one per line with the default shown, compression as a checkbox, origin and label', async () => {
      const r = await render('local');
      const arch = el<HTMLTextAreaElement>(r.root, '#f-deb-architectures');
      expect(arch.value).toBe('');
      expect(text(el(r.root, '#f-deb-architectures-v'))).toBe('Default: amd64, arm64');
      expect(text(el(r.root, '#f-deb-architectures-l'))).toBe('One entry per line.');
      const xz = el<HTMLInputElement>(r.root, '#f-deb-compression-xz');
      expect(xz.type).toBe('checkbox');
      expect(xz.checked).toBe(false);
      expect(r.root.querySelector('#f-deb-compression-v')).toBeNull();
      expect(el<HTMLInputElement>(r.root, '#f-deb-origin').value).toBe('');
      expect(r.root.querySelector('#f-deb-suites')).toBeNull();
      expect(r.root.querySelector('#f-deb-upstreamKeys')).toBeNull();

      type(arch, 'arm64\n\n riscv64 ');
      xz.checked = true;
      xz.dispatchEvent(new Event('change', { bubbles: true }));
      type(el<HTMLInputElement>(r.root, '#f-deb-origin'), 'acme');
      await settle(r.fixture);
      expect(r.fixture.componentInstance.value()).toEqual({
        architectures: ['arm64', 'riscv64'],
        compression: ['xz'],
        origin: 'acme',
      });
      // the blank line the user is typing is not rewritten under the cursor
      expect(el<HTMLTextAreaElement>(r.root, '#f-deb-architectures').value).toBe('arm64\n\n riscv64 ');

      type(el<HTMLTextAreaElement>(r.root, '#f-deb-architectures'), '');
      xz.checked = false;
      xz.dispatchEvent(new Event('change', { bubbles: true }));
      await settle(r.fixture);
      expect(r.fixture.componentInstance.value()).toEqual({ origin: 'acme' });
      await expectAccessible(r.root);
    });

    it('R-DEB-008 a stored list is shown one entry per line, and a virtual repository adds suites and components', async () => {
      const r = await render('virtual', { suites: ['stable', 'testing'], components: ['main', 'contrib'] });
      expect(el<HTMLTextAreaElement>(r.root, '#f-deb-suites').value).toBe('stable\ntesting');
      expect(el<HTMLTextAreaElement>(r.root, '#f-deb-components').value).toBe('main\ncontrib');
      expect(text(el(r.root, '#f-deb-components-v'))).toBe('Default: main');
      expect(r.root.querySelector('#f-deb-suites-v')).toBeNull();
    });

    it('R-DEB-020 R-DEB-024 a remote repository edits the upstream keys in an area and the mirror settings in a fieldset, and a server refusal appears at the member', async () => {
      const keys = new Map<string, FieldProblem>([
        ['/deb/mirror/suites', { pointer: '/deb/mirror/suites', code: 'deb_mirror_suites_required', detail: 'a mirror needs suites' }],
        ['/deb/upstreamKeys', { pointer: '/deb/upstreamKeys', code: 'invalid_value', detail: 'no certificate' }],
      ]);
      const r = await render('remote', { upstreamKeys: 'BLOCK', mirror: { suites: ['stable'], sources: true } }, keys);
      const area = el<HTMLTextAreaElement>(r.root, '#f-deb-upstreamKeys');
      expect(area.tagName).toBe('TEXTAREA');
      expect(area.value).toBe('BLOCK');
      expect(area.getAttribute('aria-invalid')).toBe('true');
      const mirror = el<HTMLFieldSetElement>(r.root, 'fieldset#f-deb-mirror');
      expect(el<HTMLTextAreaElement>(mirror, '#f-deb-mirror-suites').value).toBe('stable');
      expect(el<HTMLTextAreaElement>(mirror, '#f-deb-mirror-suites').getAttribute('aria-invalid')).toBe('true');
      expect(text(el(mirror, '#f-deb-mirror-architectures-v'))).toBe('Default: amd64');
      expect(el<HTMLInputElement>(mirror, '#f-deb-mirror-sources').checked).toBe(true);
      expect(r.root.querySelector('#f-deb-architectures')).toBeNull();

      type(el<HTMLTextAreaElement>(mirror, '#f-deb-mirror-suites'), 'stable\ntesting');
      await settle(r.fixture);
      expect(r.fixture.componentInstance.value()).toEqual({
        upstreamKeys: 'BLOCK',
        mirror: { suites: ['stable', 'testing'], sources: true },
      });
      await expectAccessible(r.root);
    });
  });
});
