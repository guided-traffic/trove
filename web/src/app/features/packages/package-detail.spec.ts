import { TestBed } from '@angular/core/testing';
import { Router } from '@angular/router';
import { PackagesApi } from '../../api/packages-api';
import { FakePackagesApi } from '../../api/testing/fakes-packages';
import { expectAccessible, renderScreen, settle, text } from '../../testing/screen';
import { PackageDetail } from './package-detail';
import { PackageMemory } from './package-memory';

type Opened = Awaited<ReturnType<typeof open>>;

async function open(
  inputs: Record<string, unknown> = {},
  setup?: (fake: FakePackagesApi) => void,
  name = '@team-a/ui',
) {
  const r = await renderScreen(PackageDetail, {
    inputs: { format: 'npm', name, ...inputs },
    setup: (get) => setup?.(get(PackagesApi) as FakePackagesApi),
  });
  return { ...r, fake: r.get(PackagesApi) as FakePackagesApi };
}

async function set(r: Opened, inputs: Record<string, unknown>) {
  for (const [k, v] of Object.entries(inputs)) r.fixture.componentRef.setInput(k, v);
  await settle(r.fixture);
}

const versionInput = (r: Opened) => r.root.querySelector<HTMLInputElement>('input[list="pkg-versions"]')!;
const options = (r: Opened) => [...r.root.querySelectorAll<HTMLOptionElement>('#pkg-versions option')].map((o) => o.value);
const tabLabels = (r: Opened) => [...r.root.querySelectorAll('[role="tab"]')].map((t) => text(t));

describe('Package details', () => {
  it('R-UI-007 opens on the latest stable version even though a newer pre-release exists, and shows the facts of the header', async () => {
    const r = await open();
    expect(versionInput(r).value).toBe('1.4.0');
    const t = text(r.root);
    expect(t).toContain('@team-a/ui');
    expect(t).toContain('MIT');
    expect(t).toContain('410');
    expect(r.root.querySelector('.chips')?.textContent).toContain('npm-local');
    expect(r.root.querySelector('.chips')?.textContent).toContain('npm-all');
    expect(r.root.querySelector('time')?.getAttribute('datetime')).toBe('2026-10-20T09:12:44.512Z');
    await expectAccessible(r.root);
  });

  it('R-UI-007 the version selector offers stable versions only until pre-releases are switched on; the switch is a query parameter', async () => {
    const r = await open();
    expect(options(r)).toEqual(['1.4.0', '1.3.0']);
    const navigate = vi.spyOn(TestBed.inject(Router), 'navigate').mockResolvedValue(true);
    const box = r.root.querySelector<HTMLInputElement>('input[type="checkbox"]')!;
    box.checked = true;
    box.dispatchEvent(new Event('change', { bubbles: true }));
    expect(navigate).toHaveBeenCalledWith([], { queryParams: { prerelease: 'true' }, queryParamsHandling: 'merge' });
    await set(r, { prerelease: 'true' });
    expect(options(r)).toEqual(['1.5.0-beta.1', '1.4.0', '1.3.0']);
  });

  it('R-UI-007 choosing a version navigates with ?version=; a value that is no version is ignored', async () => {
    const r = await open();
    const navigate = vi.spyOn(TestBed.inject(Router), 'navigate').mockResolvedValue(true);
    const input = versionInput(r);
    input.value = '1.3.0';
    input.dispatchEvent(new Event('change', { bubbles: true }));
    expect(navigate).toHaveBeenCalledWith([], { queryParams: { version: '1.3.0' }, queryParamsHandling: 'merge' });
    navigate.mockClear();
    input.value = '9.9.9';
    input.dispatchEvent(new Event('change', { bubbles: true }));
    expect(navigate).not.toHaveBeenCalled();
  });

  it('R-UI-007 with only pre-releases the latest pre-release is the default', async () => {
    const r = await open({}, (fake) => {
      fake.versionList = fake.versionList.filter((v) => v.version === '1.5.0-beta.1');
    });
    expect(versionInput(r).value).toBe('1.5.0-beta.1');
  });

  it('R-UI-007 R-UI-008 the README tab is the default when the version has a README and shows the rendered HTML', async () => {
    const r = await open();
    expect(tabLabels(r)).toEqual(['README', 'Metadata', 'Files', 'Repositories', 'All versions']);
    expect(r.root.querySelector('trove-package-readme .readme h1')?.textContent).toBe('@team-a/ui');
    expect(r.fake.readmeCalls).toEqual(['pkg_01JBQ6V7S3K9@1.4.0']);
  });

  it('R-UI-007 a version without a README has no README tab, asks for none and opens on Metadata', async () => {
    const r = await open({ version: '1.5.0-beta.1' });
    expect(tabLabels(r)).toEqual(['Metadata', 'Files', 'Repositories', 'All versions']);
    expect(r.fake.readmeCalls).toEqual([]);
    expect(text(r.root)).toContain('Dependencies');
  });

  it('R-UI-007 the Metadata tab lists distribution tags, dependency kinds and links', async () => {
    const r = await open({ tab: 'metadata' });
    const t = text(r.root);
    expect(t).toContain('Distribution tags');
    const pairs = [...r.root.querySelectorAll('.pkg-tabs dl.facts')].flatMap((dl) =>
      [...dl.querySelectorAll('dt')].map((dt) => `${text(dt)} ${text(dt.nextElementSibling!)}`),
    );
    expect(pairs).toEqual(
      expect.arrayContaining(['latest 1.4.0', 'lit ^3.1.0', 'vitest ^3.0.0', 'node >=22']),
    );
    expect(t).toContain('Dependencies');
    expect(t).toContain('Development dependencies');
    expect(t).toContain('Team A <team-a@example.com>');
    const home = [...r.root.querySelectorAll('a')].find((a) => a.textContent === 'https://example.com/team-a/ui')!;
    expect(home.getAttribute('rel')).toBe('nofollow noopener noreferrer');
    await expectAccessible(r.root);
  });

  it('R-UI-007 a homepage of javascript:alert(1) is shown as text and no element carries it as a link', async () => {
    const r = await open({ tab: 'metadata' }, (fake) => {
      for (const v of fake.versionList) {
        v.metadata = { ...v.metadata, homepage: 'javascript:alert(1)', bugsUrl: 'data:text/html,x', repositoryUrl: 'git+ssh://example.com/x.git' };
      }
    });
    expect(text(r.root)).toContain('javascript:alert(1)');
    expect(text(r.root)).toContain('git+ssh://example.com/x.git');
    for (const a of r.root.querySelectorAll('a')) {
      const href = a.getAttribute('href') ?? '';
      expect(href, href).not.toMatch(/javascript:|data:|git\+ssh:/i);
    }
    expect(r.root.querySelector('a[href*="javascript"]')).toBeNull();
  });

  it('R-UI-007 the Files tab shows the SHA-256 with a copy button and the download link of the API', async () => {
    const r = await open({ tab: 'files' });
    const sha = '0'.repeat(56) + '5be0c1d2';
    expect(text(r.root)).toContain(sha);
    expect(r.root.querySelector('.pkg-tabs trove-copy-button button')?.getAttribute('aria-label')).toBe('Copy SHA-256');
    const download = [...r.root.querySelectorAll('a')].find((a) => a.textContent?.trim() === 'Download')!;
    expect(download.getAttribute('href')).toBe('https://trove.example.net/api/npm/npm-local/@team-a/ui/-/ui-1.4.0.tgz');
    expect(download.hasAttribute('download')).toBe(true);
    await expectAccessible(r.root);
  });

  it('R-UI-007 the Repositories tab lists each repository with its class and a link that shows it in the browser', async () => {
    const r = await open({ tab: 'repositories' });
    const rows = [...r.root.querySelectorAll('tbody tr')];
    expect(rows.map((row) => text(row))).toEqual([
      expect.stringContaining('npm-localLocal'),
      expect.stringContaining('npm-allVirtual'),
    ]);
    expect(rows[0]?.querySelector('a')?.getAttribute('href')).toBe('/browse/npm-local');
    await expectAccessible(r.root);
  });

  it('R-UI-007 the All versions tab lists every version with published time, size, repositories and downloads', async () => {
    const r = await open({ tab: 'versions' });
    const rows = [...r.root.querySelectorAll('tbody tr')].map((row) => text(row));
    expect(rows).toHaveLength(3);
    expect(rows[0]).toContain('1.5.0-beta.1pre-release');
    expect(rows[1]).toMatch(/^1\.4\.0.*47\.1 KiBnpm-local, npm-all410$/);
    await expectAccessible(r.root);
  });

  it('R-UI-007 the open tab is a query parameter: selecting a tab navigates with ?tab=', async () => {
    const r = await open();
    const navigate = vi.spyOn(TestBed.inject(Router), 'navigate').mockResolvedValue(true);
    [...r.root.querySelectorAll<HTMLElement>('[role="tab"]')].find((t) => text(t) === 'Files')!.click();
    await settle(r.fixture);
    expect(navigate).toHaveBeenCalledWith([], { queryParams: { tab: 'files' }, queryParamsHandling: 'merge' });
  });

  it('R-UI-007 opening the page records the package in "recently viewed"', async () => {
    const r = await open();
    expect(r.get(PackageMemory).recent()).toEqual([{ format: 'npm', name: '@team-a/ui' }]);
  });

  it('R-UI-007 R-UI-009 the install panel is part of the page and shows the command of the selected version', async () => {
    const r = await open();
    expect(r.root.querySelector('trove-install-panel pre')?.textContent).toBe('npm install @team-a/ui@1.4.0');
    await set(r, { version: '1.3.0' });
    expect(r.root.querySelector('trove-install-panel pre')?.textContent).toBe('npm install @team-a/ui@1.3.0');
  });

  it('R-UI-007 the install panel comes before the tabs in the document, so on a narrow content area the command is not below the README', async () => {
    const r = await open();
    const panel = r.root.querySelector('.pkg-install')!;
    const tabs = r.root.querySelector('.pkg-tabs')!;
    expect(panel.compareDocumentPosition(tabs) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it('R-UI-007 an unknown package shows the not-found error', async () => {
    const r = await open({}, undefined, 'no-such-package');
    expect(text(r.root)).toContain('does not exist');
    await expectAccessible(r.root);
  });

  it('R-UI-007 a deprecated version is flagged with its reason', async () => {
    const r = await open({}, (fake) => {
      for (const v of fake.versionList) v.metadata = { ...v.metadata, deprecated: 'Use @team-a/ui2' };
    });
    expect(text(r.root)).toContain('Deprecated.');
    expect(text(r.root)).toContain('Use @team-a/ui2');
  });
});
