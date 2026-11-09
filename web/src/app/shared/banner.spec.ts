import { SystemApi } from '../api/system-api';
import { FakeSystemApi } from '../api/testing/fakes';
import { expectAccessible, renderScreen, settle, text } from '../testing/screen';
import { Banner } from './banner';

async function render(setup?: (api: FakeSystemApi) => void) {
  const r = await renderScreen(Banner, {
    setup: (get) => {
      setup?.(get(SystemApi) as FakeSystemApi);
    },
  });
  await settle(r.fixture);
  return r;
}

describe('Banner', () => {
  it('R-UI-033 renders nothing while there is no notice', async () => {
    const r = await render();
    expect(r.root.querySelector('section')).toBeNull();
  });

  it('R-UI-033 an enabled banner shows its severity word, title and message with an icon, and is accessible', async () => {
    const r = await render((api) => {
      api.bannerResult = { enabled: true, severity: 'warning', title: 'Planned downtime', message: 'Saturday 22:00 UTC.' };
    });
    const region = r.root.querySelector('section[role="region"]')!;
    expect(region.getAttribute('data-severity')).toBe('warning');
    expect(region.getAttribute('aria-label')).toBe('System notice');
    expect(text(region)).toContain('Warning: Planned downtime.');
    expect(text(region)).toContain('Saturday 22:00 UTC.');
    expect(region.querySelector('.material-icons')?.getAttribute('aria-hidden')).toBe('true');
    await expectAccessible(r.root);
  });

  it('R-UI-033 R-OPS-025 an https link is a link that opens safely; any other link is text and nothing is injected', async () => {
    const r = await render((api) => {
      api.bannerResult = {
        enabled: true,
        message: 'Read [status](https://status.example.com/) or [bad](javascript:alert(1)) <img src=x onerror=alert(1)>',
      };
    });
    const links = [...r.root.querySelectorAll('a')];
    expect(links.map((a) => a.getAttribute('href'))).toEqual(['https://status.example.com/']);
    expect(links[0]!.getAttribute('rel')).toBe('noopener noreferrer');
    expect(links[0]!.getAttribute('target')).toBe('_blank');
    expect(text(r.root)).toContain('[bad](javascript:alert(1))');
    expect(r.root.querySelector('img')).toBeNull();
  });

  it('R-UI-033 the read-only maintenance mode shows its message above the banner, for a signed-in caller', async () => {
    const r = await renderScreen(Banner, {
      setup: (get) => {
        const api = get(SystemApi) as FakeSystemApi;
        api.bannerResult = { enabled: true, message: 'Planned work' };
        api.maintenanceResult = { mode: 'read_only', cacheFill: true, message: 'Storage migration until 14:00 UTC' };
      },
    });
    await settle(r.fixture);
    const regions = [...r.root.querySelectorAll('section')];
    expect(regions.map((s) => s.getAttribute('data-kind'))).toEqual(['maintenance', 'banner']);
    expect(text(regions[0]!)).toContain('Read-only maintenance mode.');
    expect(text(regions[0]!)).toContain('Storage migration until 14:00 UTC');
    expect(regions[0]!.getAttribute('aria-label')).toBe('Maintenance notice');
  });
});
