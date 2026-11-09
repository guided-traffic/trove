import { ApiError } from '../../api/problem';
import { SystemApi } from '../../api/system-api';
import { FakeSystemApi } from '../../api/testing/fakes';
import { BannerStore } from '../../core/banner-store';
import { button, buttonByText, expectAccessible, renderScreen, settle, text, typeInto } from '../../testing/screen';
import { GeneralSettings } from './general-settings';

const overlay = (): HTMLElement => document.querySelector('.cdk-overlay-container') as HTMLElement;

async function render(options: { capabilities?: readonly string[]; setup?: (api: FakeSystemApi) => void } = {}) {
  const r = await renderScreen(GeneralSettings, {
    capabilities: options.capabilities,
    setup: (get) => options.setup?.(get(SystemApi) as FakeSystemApi),
  });
  return { ...r, api: r.get(SystemApi) as FakeSystemApi, notices: r.get(BannerStore) };
}

const checkbox = (root: ParentNode, label: string): HTMLInputElement => {
  const l = [...root.querySelectorAll('label.check-row')].find((e) => text(e).startsWith(label));
  const input = l?.querySelector('input');
  if (!input) throw new Error(`no checkbox "${label}"`);
  return input;
};

async function toggle(fixture: Parameters<typeof settle>[0], input: HTMLInputElement): Promise<void> {
  input.click();
  await settle(fixture);
}

describe('General settings (R-UI-033)', () => {
  it('R-UI-033 shows the public base URL with its source, the banner and the maintenance mode, and passes axe', async () => {
    const { root, api } = await render();
    expect(text(root.querySelector('h1')!)).toBe('General settings');
    expect(text(root.querySelector('[data-testid="base-url"]')!)).toBe((await api.info()).baseUrl);
    expect(text(root.querySelector('[data-testid="base-url-source"]')!)).toContain('configuration file');
    expect(text(root)).toContain('Banner');
    expect(text(root.querySelector('[data-testid="mode-state"]')!)).toContain('Normal mode');
    await expectAccessible(root);
  });

  it('R-UI-033 names a base URL that was derived from the request as development only', async () => {
    const { root } = await render({
      setup: (api) => {
        api.infoResult = { ...api.infoResult, baseUrlSource: 'request' };
      },
    });
    expect(text(root.querySelector('[data-testid="base-url-source"]')!)).toContain('development only');
  });

  it('R-UI-033 saves an enabled banner with its severity, title and message, and shows it at once', async () => {
    const { fixture, root, api, notices } = await render();
    expect(button(root, 'Save banner').disabled).toBe(true);
    await toggle(fixture, checkbox(root, 'Show the banner'));
    const severity = root.querySelector<HTMLSelectElement>('select[name="severity"]')!;
    severity.value = 'warning';
    severity.dispatchEvent(new Event('change', { bubbles: true }));
    typeInto(root, 'Title', 'Planned downtime');
    typeInto(root, 'Banner message', 'Read-only on Saturday, see [status](https://status.example.com/).');
    await settle(fixture);
    button(root, 'Save banner').click();
    await settle(fixture);
    expect(api.bannerWrites).toEqual([
      {
        enabled: true,
        severity: 'warning',
        title: 'Planned downtime',
        message: 'Read-only on Saturday, see [status](https://status.example.com/).',
      },
    ]);
    expect(notices.notices().map((n) => [n.kind, n.severity, n.title])).toEqual([['banner', 'warning', 'Planned downtime']]);
    expect(button(root, 'Save banner').disabled).toBe(true);
  });

  it('R-OPS-025 previews the message with https links only: a javascript link stays text', async () => {
    const { fixture, root } = await render();
    typeInto(root, 'Banner message', 'see [a](https://example.com/) and [b](javascript:alert(1))');
    await settle(fixture);
    const preview = root.querySelector('[data-testid="banner-preview"]')!;
    expect([...preview.querySelectorAll('a')].map((a) => a.getAttribute('href'))).toEqual(['https://example.com/']);
    expect(text(preview)).toContain('[b](javascript:alert(1))');
  });

  it('R-UI-033 shows the stored banner, switches it off and keeps its text', async () => {
    const { fixture, root, api } = await render({
      setup: (api) => {
        api.bannerResult = { enabled: true, severity: 'critical', title: 'Outage', message: 'Pushes fail.' };
      },
    });
    expect(checkbox(root, 'Show the banner').checked).toBe(true);
    expect(root.querySelector<HTMLSelectElement>('select[name="severity"]')!.value).toBe('critical');
    await toggle(fixture, checkbox(root, 'Show the banner'));
    button(root, 'Save banner').click();
    await settle(fixture);
    expect(api.bannerWrites[0]).toMatchObject({ enabled: false, severity: 'critical', message: 'Pushes fail.' });
  });

  it('R-UI-033 a refused banner puts the API\'s text next to the field and keeps the entries', async () => {
    const { fixture, root, api } = await render();
    await toggle(fixture, checkbox(root, 'Show the banner'));
    api.writeError = new ApiError(
      422,
      { code: 'validation_failed', errors: [{ pointer: '/message', code: 'required', detail: 'An enabled banner needs a message.' }] },
      null,
    );
    typeInto(root, 'Banner message', 'x');
    await settle(fixture);
    button(root, 'Save banner').click();
    await settle(fixture);
    expect(text(root.querySelector('.field-error')!)).toBe('An enabled banner needs a message.');
    expect(checkbox(root, 'Show the banner').checked).toBe(true);
    expect((root.querySelector('textarea[name="message"]') as HTMLTextAreaElement).value).toBe('x');
  });

  it('R-UI-033 R-OPS-024 switching the maintenance mode on asks first, then sends the message, cache-fill switch and retry hint', async () => {
    const { fixture, root, api, notices } = await render();
    await toggle(fixture, checkbox(root, 'Read-only maintenance mode'));
    typeInto(root, 'Maintenance message', 'Storage migration until 14:00 UTC');
    await settle(fixture);
    await toggle(fixture, checkbox(root, 'Remote repositories keep filling'));
    typeInto(root, 'Retry after (seconds)', '120');
    await settle(fixture);
    button(root, 'Save maintenance mode').click();
    await settle(fixture);
    expect(api.maintenanceWrites).toEqual([]);
    expect(text(overlay())).toContain('Switch to read-only maintenance mode?');
    buttonByText(overlay(), 'Switch on').click();
    await settle(fixture);
    expect(api.maintenanceWrites).toEqual([
      { mode: 'read_only', message: 'Storage migration until 14:00 UTC', cacheFill: false, retryAfterSeconds: 120 },
    ]);
    expect(text(root.querySelector('[data-testid="mode-state"]')!)).toContain('Read-only mode is on.');
    expect(notices.notices().map((n) => n.kind)).toEqual(['maintenance']);
  });

  it('R-OPS-024 declining the confirmation sends nothing', async () => {
    const { fixture, root, api } = await render();
    await toggle(fixture, checkbox(root, 'Read-only maintenance mode'));
    button(root, 'Save maintenance mode').click();
    await settle(fixture);
    buttonByText(overlay(), 'Cancel').click();
    await settle(fixture);
    expect(api.maintenanceWrites).toEqual([]);
    expect(checkbox(root, 'Read-only maintenance mode').checked).toBe(true);
  });

  it('R-OPS-024 switching the mode off needs no confirmation and sends mode normal', async () => {
    const { fixture, root, api } = await render({
      setup: (api) => {
        api.maintenanceResult = { mode: 'read_only', cacheFill: true, message: 'Frozen', retryAfterSeconds: 300, since: '2026-11-09T12:00:00Z', by: 'alice' };
      },
    });
    expect(text(root.querySelector('[data-testid="mode-state"]')!)).toContain('Read-only mode is on. Since');
    expect(text(root.querySelector('[data-testid="mode-state"]')!)).toContain('by alice');
    await toggle(fixture, checkbox(root, 'Read-only maintenance mode'));
    button(root, 'Save maintenance mode').click();
    await settle(fixture);
    expect(api.maintenanceWrites).toEqual([{ mode: 'normal' }]);
    expect(text(root.querySelector('[data-testid="mode-state"]')!)).toContain('Normal mode');
  });

  it('R-UI-033 a caller without settings:write sees the values read-only and no save button', async () => {
    const { root } = await render({ capabilities: ['settings:read'] });
    expect(text(root)).toContain('Read only: changing this needs the permission settings:write');
    expect(root.querySelector('fieldset')!.disabled).toBe(true);
    expect([...root.querySelectorAll('button')].map((b) => text(b))).not.toContain('Save banner');
    expect([...root.querySelectorAll('button')].map((b) => text(b))).not.toContain('Save maintenance mode');
  });

  it('R-UI-033 a load failure is shown with a retry', async () => {
    const { root } = await render({
      setup: (api) => {
        api.bannerResult = new ApiError(503, { code: 'unavailable', detail: 'The system settings are not available.' }, null);
      },
    });
    expect(root.querySelector('trove-error-notice')).toBeTruthy();
  });
});
