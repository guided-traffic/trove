import { TestBed } from '@angular/core/testing';
import { Router } from '@angular/router';
import { afterEach, describe, expect, it } from 'vitest';
import { PackagesApi } from '../../api/packages-api';
import { RepositoriesApi } from '../../api/repositories-api';
import { FakePackagesApi } from '../../api/testing/fakes-packages';
import { FakeRepositoriesApi, PACKAGE_FORMAT } from '../../api/testing/fakes-catalog';
import { addressValues, expectAccessible, renderScreen, settle, text } from '../../testing/screen';
import { SearchResults } from './search-results';

type Get = <A>(token: abstract new (...args: never[]) => A) => A;

async function open(q: string | undefined, kind?: string, setup?: (get: Get) => void) {
  const r = await renderScreen(SearchResults, {
    inputs: { q, kind },
    setup: (get) => {
      (get(RepositoriesApi) as FakeRepositoriesApi).formatsResult = [PACKAGE_FORMAT];
      setup?.(get);
    },
  });
  await settle(r.fixture);
  return { ...r, repos: r.get(RepositoriesApi) as FakeRepositoriesApi, pkgs: r.get(PackagesApi) as FakePackagesApi };
}

/** Every link of the page: none may lead into the admin area (R-UI-099). */
const hrefs = (root: HTMLElement) => [...root.querySelectorAll<HTMLAnchorElement>('a[href]')].map((a) => a.getAttribute('href') ?? '');
const selectedTab = (root: HTMLElement) => text(root.querySelector('[role="tab"][aria-selected="true"]')!);

afterEach(() => history.replaceState(null, ''));

describe('The results page of the search (R-UI-098)', () => {
  it('R-UI-098 the heading says what it shows; the tabs are Repositories, Packages and Projects without counts; Repositories by default', async () => {
    const r = await open('generic');
    expect(text(r.root.querySelector('h1')!)).toBe('Search');
    expect(text(r.root)).toContain('What you may read that matches “generic”.');
    expect([...r.root.querySelectorAll('[role="tab"]')].map((t) => text(t))).toEqual(['Repositories', 'Packages', 'Projects']);
    expect(selectedTab(r.root)).toBe('Repositories');
    expect(r.repos.listFilters.at(-1)).toEqual({ q: 'generic' });
    await expectAccessible(r.root);
  });

  it('R-UI-098 Q-UI-330 the Repositories tab keeps the admin list\'s pattern: key, project, format and class as columns, the address as a copy button', async () => {
    const r = await open('generic');
    const rows = [...r.root.querySelectorAll('tbody tr')];
    expect(rows.length).toBeGreaterThan(0);
    const first = rows[0]!;
    expect(first.querySelector('th a')!.getAttribute('href')).toMatch(/^\/projects\/[^/]+\/repositories\/[^/]+$/);
    expect(first.querySelector('td a')!.getAttribute('href')).toMatch(/^\/projects\/[^/]+$/);
    const heads = [...r.root.querySelectorAll('thead th')].map((th) => text(th).replace('help_outline', ''));
    expect(heads.map((h) => h.split(' ')[0])).toEqual(['Repository', 'Project', 'Format', 'Class', 'Actions']);
    expect(addressValues(r.root)).toEqual([]);
    const copy = first.querySelector<HTMLButtonElement>('td.actions trove-copy-button button')!;
    expect(copy.getAttribute('aria-label')).toMatch(/^Copy client address https?:\/\//);
  });

  it('R-UI-098 kind=package opened directly shows the Packages tab with its project and repository, and Load more after 25 rows', async () => {
    const r = await open('pkg', 'package', (get) => {
      const fake = get(PackagesApi) as FakePackagesApi;
      fake.packages = Array.from({ length: 30 }, (_, i) => ({
        ...fake.packages[0]!,
        id: `pkg_${i}`,
        name: `pkg-${String(i).padStart(2, '0')}`,
        repositories: ['web/npm-local'],
      }));
    });
    expect(selectedTab(r.root)).toBe('Packages');
    expect(r.pkgs.listFilters.at(-1)).toEqual({ q: 'pkg' });
    expect(r.root.querySelectorAll('tbody tr')).toHaveLength(25);
    const first = r.root.querySelector('tbody tr')!;
    expect(first.querySelector('th a')!.getAttribute('href')).toBe('/projects/web/packages/npm/pkg-00');
    expect([...first.querySelectorAll('td a')].map((a) => a.getAttribute('href'))).toEqual(['/projects/web', '/projects/web/repositories/npm-local']);
    const more = [...r.root.querySelectorAll<HTMLButtonElement>('button')].find((b) => text(b) === 'Load more')!;
    more.click();
    await settle(r.fixture);
    expect(r.root.querySelectorAll('tbody tr')).toHaveLength(30);
    await expectAccessible(r.root);
  });

  it('R-UI-098 without kind the tab is the first group that had a suggestion when Enter was pressed', async () => {
    history.replaceState({ firstKind: 'project' }, '');
    const r = await open('web');
    expect(selectedTab(r.root)).toBe('Projects');
    expect(r.root.querySelectorAll('tbody tr').length).toBeGreaterThan(0);
    const users = await open('web', 'user');
    expect(selectedTab(users.root)).toBe('Projects');
  });

  it('R-UI-098 choosing a tab puts its kind into the address', async () => {
    const r = await open('web');
    const tabs = [...r.root.querySelectorAll<HTMLElement>('[role="tab"]')];
    tabs[2]!.click();
    await settle(r.fixture);
    expect(TestBed.inject(Router).url).toBe('/search?q=web&kind=project');
  });

  it('R-UI-098 says when nothing matches, and asks for two characters without asking the server', async () => {
    const none = await open('no-such-repository-anywhere');
    expect(text(none.root)).toContain('Nothing you may read matches “no-such-repository-anywhere”.');
    const short = await open('w');
    expect(text(short.root)).toContain('Type at least two characters.');
    expect(short.repos.listFilters.filter((f) => f.q !== undefined)).toEqual([]);
  });

  it('R-UI-098 R-UI-099 no link of the page leads into the admin area', async () => {
    for (const [kind, q] of [['repository', 'web'], ['package', 'ui'], ['project', 'web']] as const) {
      const r = await open(q, kind);
      expect(hrefs(r.root).length).toBeGreaterThan(0);
      expect(hrefs(r.root).filter((h) => h.startsWith('/admin'))).toEqual([]);
    }
  });
});
