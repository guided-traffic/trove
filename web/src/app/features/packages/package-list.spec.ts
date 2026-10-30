import { TestBed } from '@angular/core/testing';
import { Router } from '@angular/router';
import { PackageSummary, PackagesApi } from '../../api/packages-api';
import { ApiError } from '../../api/problem';
import { RepositoriesApi } from '../../api/repositories-api';
import { FakeRepositoriesApi, GENERIC_FORMAT, PACKAGE_FORMAT } from '../../api/testing/fakes-catalog';
import { FakePackagesApi, NPM_REPOSITORIES } from '../../api/testing/fakes-packages';
import { BrowserStorage } from '../../core/storage';
import {
  button,
  buttonByText,
  expectAccessible,
  openFilters,
  renderScreen,
  settle,
  text,
  type,
} from '../../testing/screen';
import { PackageList } from './package-list';

type Opened = Awaited<ReturnType<typeof open>>;

const API = '/api';
const NUGET ={ ...PACKAGE_FORMAT, name: 'nuget', displayName: 'NuGet', protocolBasePath: API + '/nuget/{key}/' };

async function open(
  inputs: Record<string, unknown> = {},
  setup?: (packages: FakePackagesApi, repositories: FakeRepositoriesApi) => void,
) {
  new BrowserStorage().clear('local');
  const r = await renderScreen(PackageList, {
    inputs,
    setup: (get) => {
      const repositories = get(RepositoriesApi) as FakeRepositoriesApi;
      repositories.formatsResult = [GENERIC_FORMAT, PACKAGE_FORMAT, NUGET];
      repositories.repositories = [...repositories.repositories, ...NPM_REPOSITORIES];
      setup?.(get(PackagesApi) as FakePackagesApi, repositories);
    },
  });
  return { ...r, fake: r.get(PackagesApi) as FakePackagesApi };
}

const rows = (r: Opened) => [...r.root.querySelectorAll('tbody tr')];
const names = (r: Opened) => rows(r).map((row) => text(row.querySelector('th')!));
const navigateSpy = () => vi.spyOn(TestBed.inject(Router), 'navigate').mockResolvedValue(true);

function many(n: number): PackageSummary[] {
  return Array.from({ length: n }, (_, i) => ({
    id: `pkg_${String(i).padStart(4, '0')}`,
    format: 'npm',
    name: `pkg-${String(i).padStart(3, '0')}`,
    description: null,
    latestVersion: '1.0.0',
    keywords: [],
    repositories: ['npm-local'],
    createdAt: '2026-10-01T00:00:00.000Z',
    updatedAt: '2026-10-01T00:00:00.000Z',
    versionCount: 1,
    downloadCount: 0,
  }));
}

describe('Packages list', () => {
  it('R-UI-006 lists the packages with type, name, latest version, description, created, updated, versions, repositories and downloads, and has no serious violation', async () => {
    const r = await open();
    expect(r.root.querySelector('h1')?.textContent).toBe('Packages');
    const headers = [...r.root.querySelectorAll('thead th')].map((th) => text(th.querySelector('.th-sort, .th-inner > span:first-child') ?? th));
    expect(headers).toEqual([
      'Type',
      expect.stringContaining('Name'),
      'Latest version',
      'Description',
      expect.stringContaining('First created'),
      expect.stringContaining('Updated'),
      'Versions',
      'Repositories',
      'Downloads',
    ]);
    expect(names(r)).toHaveLength(3);
    const ui = rows(r).find((row) => text(row).includes('@team-a/ui'))!;
    const cells = [...ui.children].map((c) => text(c));
    expect(cells[0]).toContain('npm');
    expect(cells[2]).toBe('1.4.0');
    expect(cells[3]).toBe('Shared UI components');
    expect(cells[6]).toBe('3');
    expect(cells[7]).toBe('2');
    expect(cells[8]).toBe('1,832');
    expect(ui.querySelector('time')?.getAttribute('datetime')).toBe('2026-09-02T08:00:12.201Z');
    // Updated is relative, with the absolute time in the element's datetime and title (R-UI-006).
    const updated = ui.querySelectorAll('time')[1]!;
    expect(updated.getAttribute('datetime')).toBe('2026-10-20T09:12:44.512Z');
    expect(updated.getAttribute('title')).toContain('2026');
    expect(text(updated)).not.toBe('');
    expect(text(updated)).not.toContain('2026');
    await expectAccessible(r.root);
  });

  it('R-UI-006 every row is a link to the package page; a scoped name keeps its slash as a path separator', async () => {
    const r = await open();
    const link = rows(r)
      .map((row) => row.querySelector<HTMLAnchorElement>('th a')!)
      .find((a) => a.textContent === '@team-a/ui')!;
    expect(link.getAttribute('href')).toBe('/packages/npm/@team-a/ui');
  });

  it('R-UI-006 a format is shown as icon and text, never as icon alone', async () => {
    const r = await open();
    const type = rows(r)[0]!.querySelector('.type-cell')!;
    expect(type.querySelector('.material-icons')?.getAttribute('aria-hidden')).toBe('true');
    expect(text(type.querySelector('span:not(.material-icons)')!)).not.toBe('');
  });

  it('R-UI-006 the sort is the one the API offers: name, first created and updated are sortable, the rest carry a help text instead', async () => {
    const r = await open();
    const sortable = [...r.root.querySelectorAll('thead th[aria-sort] .th-sort')].map((b) =>
      text(b.querySelector('span:first-child')!),
    );
    expect(sortable).toEqual(['Name', 'First created', 'Updated']);
    expect(r.root.querySelectorAll('thead .th-help').length).toBeGreaterThanOrEqual(5);
    expect(r.root.querySelector('th[aria-sort="ascending"]')?.textContent).toContain('Name');
    expect(r.fake.listFilters.at(-1)?.sort).toBe('name');
  });

  it('R-UI-006 R-API-029 a column header asks the server for the sort: clicking Name again sorts descending, First created starts ascending', async () => {
    const r = await open();
    const navigate = navigateSpy();
    r.root.querySelector<HTMLButtonElement>('thead th:nth-child(2) .th-sort')!.click();
    expect(navigate).toHaveBeenLastCalledWith([], { queryParams: { sort: '-name' }, queryParamsHandling: 'merge' });
    r.root.querySelector<HTMLButtonElement>('thead th:nth-child(5) .th-sort')!.click();
    expect(navigate).toHaveBeenLastCalledWith([], { queryParams: { sort: 'createdAt' }, queryParamsHandling: 'merge' });
  });

  it('R-UI-006 the sort in the URL reaches the API, restarts the list and marks the column', async () => {
    const r = await open({ sort: '-updatedAt' });
    expect(r.fake.listFilters.at(-1)?.sort).toBe('-updatedAt');
    expect(names(r)[0]).toBe('Contoso.Logging');
    expect(r.root.querySelector('th[aria-sort="descending"]')?.textContent).toContain('Updated');
    const unknown = await open({ sort: 'downloads' });
    expect(unknown.fake.listFilters.at(-1)?.sort).toBe('name');
  });

  it('R-UI-006 the filter panel offers package type, repository, name or description, keyword and created', async () => {
    const r = await open();
    await openFilters(r.fixture, r.root);
    const categories = [...r.root.querySelectorAll('trove-filter-category .category-label')].map((c) => text(c));
    expect(categories).toEqual(['Package type', 'Repository', 'Name or description', 'Keyword', 'Created']);
    await expectAccessible(r.root);
  });

  it('R-UI-006 choosing a type and searching sends all staged values to the query at once', async () => {
    const r = await open();
    const navigate = navigateSpy();
    await openFilters(r.fixture, r.root);
    for (const label of ['Package type', 'Name or description', 'Keyword']) buttonByText(r.root, label).click();
    await settle(r.fixture);
    const check = [...r.root.querySelectorAll<HTMLInputElement>('trove-filter-category input[type="checkbox"]')].find(
      (i) => text(i.parentElement!) === 'NuGet',
    )!;
    check.checked = true;
    check.dispatchEvent(new Event('change', { bubbles: true }));
    type(r.root.querySelector<HTMLInputElement>('input[aria-label="Name or description"]')!, ' logging ');
    type(r.root.querySelector<HTMLInputElement>('input[aria-label="Keyword"]')!, 'log');
    await settle(r.fixture);
    button(r.root, 'Search').click();
    expect(navigate).toHaveBeenLastCalledWith([], {
      queryParams: { format: ['nuget'], repository: [], q: 'logging', keyword: 'log', period: null, from: null, to: null },
      queryParamsHandling: 'merge',
    });
  });

  it('R-UI-006 acceptance: with format=nuget the list shows one row; the filter is a chip and the panel opens with it', async () => {
    const r = await open({ format: 'nuget' });
    expect(names(r)).toEqual(['Contoso.Logging']);
    expect(r.fake.listFilters.at(-1)?.format).toEqual(['nuget']);
    expect(r.root.querySelector('.chip')?.textContent).toContain('Package type: NuGet');
    expect(r.root.querySelector('trove-filter-panel form')).not.toBeNull();
  });

  it('R-UI-006 several formats and repositories repeat the parameter (OR within a field)', async () => {
    const r = await open({ format: ['npm', 'nuget'], repository: ['npm-remote', 'nuget-local'] });
    const sent = r.fake.listFilters.at(-1)!;
    expect(sent.format).toEqual(['npm', 'nuget']);
    expect(sent.repository).toEqual(['npm-remote', 'nuget-local']);
    expect(names(r).sort()).toEqual(['Contoso.Logging', 'left-pad']);
  });

  it('R-UI-006 every applied filter is a removable chip with an accessible name; removing one changes only that filter', async () => {
    const r = await open({ format: ['npm', 'nuget'], q: 'team', keyword: 'ui' });
    const chips = [...r.root.querySelectorAll('.chip')].map((c) => text(c));
    expect(chips).toEqual([
      'Package type: npmclose',
      'Package type: NuGetclose',
      'Name or description: teamclose',
      'Keyword: uiclose',
    ]);
    const navigate = navigateSpy();
    expect(r.root.querySelector('.chip button')?.getAttribute('aria-label')).toBe('Remove the filter Package type: npm');
    r.root.querySelector<HTMLButtonElement>('.chip button')!.click();
    expect(navigate).toHaveBeenLastCalledWith([], { queryParams: { format: ['nuget'] }, queryParamsHandling: 'merge' });
    const last = [...r.root.querySelectorAll<HTMLButtonElement>('.chip button')].at(-1)!;
    last.click();
    expect(navigate).toHaveBeenLastCalledWith([], { queryParams: { keyword: null }, queryParamsHandling: 'merge' });
    await expectAccessible(r.root);
  });

  it('R-UI-006 the text filter and the keyword reach the server; the screen filters nothing itself', async () => {
    const r = await open({ q: 'team', keyword: 'components' });
    const sent = r.fake.listFilters.at(-1)!;
    expect(sent.q).toBe('team');
    expect(sent.keyword).toBe('components');
    expect(names(r)).toEqual(['@team-a/ui']);
  });

  it('R-UI-006 the created period: last 7, 30 or 90 days becomes createdFrom; a custom period includes its end day', async () => {
    const week = await open({ period: '7' });
    const from = Date.parse(week.fake.listFilters.at(-1)!.createdFrom!);
    expect(Date.now() - from).toBeGreaterThan(6.9 * 86_400_000);
    expect(Date.now() - from).toBeLessThan(7.1 * 86_400_000);
    expect(week.fake.listFilters.at(-1)?.createdTo).toBeUndefined();
    expect(week.root.querySelector('.chip')?.textContent).toContain('Created: Last 7 days');

    const custom = await open({ period: 'custom', from: '2026-09-01', to: '2026-09-30' });
    const sent = custom.fake.listFilters.at(-1)!;
    expect(sent.createdFrom).toBe('2026-09-01T00:00:00.000Z');
    expect(sent.createdTo).toBe('2026-10-01T00:00:00.000Z');
    expect(names(custom)).toEqual(['@team-a/ui']);
  });

  it('R-UI-006 an empty installation offers the client setup; a filter without a match offers to clear the filters', async () => {
    const empty = await open({}, (fake) => (fake.packages = []));
    expect(text(empty.root)).toContain('No package has been published yet.');
    const setup = [...empty.root.querySelectorAll('a')].find((a) => text(a).includes('Set up a client to publish the first package'));
    expect(setup?.getAttribute('href')).toBe('/setup');

    const none = await open({ q: 'no-such-package' });
    expect(text(none.root)).toContain('No package matches the filters.');
    const navigate = navigateSpy();
    button(none.root, 'Clear all filters').click();
    expect(navigate).toHaveBeenLastCalledWith([], {
      queryParams: { format: null, repository: null, q: null, keyword: null, period: null, from: null, to: null },
      queryParamsHandling: 'merge',
    });
    await expectAccessible(none.root);
  });

  it('R-UI-006 "Load more" continues the list with an explicit button, not by scrolling', async () => {
    const r = await open({}, (fake) => (fake.packages = many(60)));
    expect(rows(r)).toHaveLength(50);
    button(r.root, 'Load more').click();
    await settle(r.fixture);
    expect(rows(r)).toHaveLength(60);
    expect(r.root.textContent).not.toContain('Load more');
  });

  it('R-UI-006 a failing list shows the error with a retry', async () => {
    const r = await open({}, (fake) => (fake.listError = new ApiError(503, { code: 'unavailable' }, null)));
    expect(text(r.root.querySelector('[role="alert"]')!)).toContain('temporarily unavailable');
    expect(rows(r)).toHaveLength(0);
    button(r.root, 'Retry').click();
    await settle(r.fixture);
    expect(rows(r)).toHaveLength(3);
  });

  describe('recently viewed and saved views', () => {
    it('R-UI-006 offers three views of the list: all, recently viewed and saved views', async () => {
      const r = await open();
      const links = [...r.root.querySelectorAll('.view-tabs a')];
      expect(links.map((a) => text(a))).toEqual(['All packages', 'Recently viewed', 'Saved views']);
      expect(links[0]?.getAttribute('aria-current')).toBe('page');
      await expectAccessible(r.root);
    });

    it('R-UI-006 "Recently viewed" lists the packages opened, newest first, as links', async () => {
      const r = await open({ tab: 'recent' });
      expect(text(r.root)).toContain('You have not opened a package yet.');
      const memory = (await import('./package-memory')).PackageMemory;
      const m = r.get(memory);
      m.addRecent({ format: 'npm', name: 'left-pad' });
      m.addRecent({ format: 'npm', name: '@team-a/ui' });
      await settle(r.fixture);
      const items = [...r.root.querySelectorAll('.plain-list a')];
      expect(items.map((a) => a.getAttribute('href'))).toEqual(['/packages/npm/@team-a/ui', '/packages/npm/left-pad']);
      expect(r.root.querySelector('.view-tabs a[aria-current="page"]')?.textContent).toContain('Recently viewed');
      await expectAccessible(r.root);
    });

    it('R-UI-006 saves the current filters and sort under a name, renames, sets a default and deletes a view', async () => {
      const r = await open({ tab: 'views', format: 'npm', sort: '-updatedAt' });
      const memory = r.get((await import('./package-memory')).PackageMemory);
      type(r.root.querySelector<HTMLInputElement>('.save-view input')!, 'Recent npm');
      await settle(r.fixture);
      button(r.root, 'Save the current filters and sort').click();
      await settle(r.fixture);
      expect(memory.views()).toHaveLength(1);
      expect(memory.views()[0]).toMatchObject({ name: 'Recent npm', query: { format: 'npm', sort: '-updatedAt' } });
      const view = r.root.querySelector('.plain-list a')!;
      expect(view.getAttribute('href')).toBe('/packages?format=npm&sort=-updatedAt');

      button(r.root, 'Rename').click();
      await settle(r.fixture);
      const field = r.root.querySelector<HTMLInputElement>('.plain-list input')!;
      type(field, 'Newest npm');
      await settle(r.fixture);
      button(r.root, 'Save name').click();
      await settle(r.fixture);
      expect(memory.views()[0]?.name).toBe('Newest npm');

      button(r.root, 'Make default').click();
      await settle(r.fixture);
      expect(memory.defaultView()?.name).toBe('Newest npm');
      expect(text(r.root)).toContain('default');
      button(r.root, 'Remove default').click();
      await settle(r.fixture);
      expect(memory.defaultView()).toBeNull();

      button(r.root, 'Delete').click();
      await settle(r.fixture);
      expect(memory.views()).toEqual([]);
      expect(text(r.root)).toContain('You have no saved views.');
    });

    it('R-UI-006 at most eight views: the save button is disabled and says why', async () => {
      const r = await open({ tab: 'views' });
      const memory = r.get((await import('./package-memory')).PackageMemory);
      for (let i = 0; i < 8; i++) memory.saveView(`view ${i}`, {});
      type(r.root.querySelector<HTMLInputElement>('.save-view input')!, 'ninth');
      await settle(r.fixture);
      expect(button(r.root, 'Save the current filters and sort').disabled).toBe(true);
      expect(text(r.root)).toContain('at most 8 saved views');
    });

    it('R-UI-006 a list opened without a query lands on the default saved view once', async () => {
      new BrowserStorage().set(
        'local',
        'trove.packages.alice',
        JSON.stringify({ recent: [], views: [{ id: 'v1', name: 'NuGet only', query: { format: 'nuget' } }], defaultView: 'v1' }),
      );
      let navigate: ReturnType<typeof navigateSpy> | undefined;
      await renderScreen(PackageList, {
        setup: (get) => {
          navigate = vi.spyOn(get(Router), 'navigate').mockResolvedValue(true);
        },
      });
      expect(navigate).toHaveBeenCalledWith([], { queryParams: { format: 'nuget' }, replaceUrl: true });
      expect(navigate).toHaveBeenCalledTimes(1);
    });

    it('R-UI-006 acceptance: with browser storage blocked the page works and the two storage tabs are hidden', async () => {
      const spy = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
        throw new DOMException('blocked', 'SecurityError');
      });
      try {
        const r = await renderScreen(PackageList);
        expect(r.root.querySelector('.view-tabs')).toBeNull();
        expect(rows({ root: r.root } as Opened)).toHaveLength(3);
        const forced = await renderScreen(PackageList, { inputs: { tab: 'views' } });
        expect(forced.root.querySelector('.save-view')).toBeNull();
        expect(rows({ root: forced.root } as Opened)).toHaveLength(3);
      } finally {
        spy.mockRestore();
      }
    });
  });
});
