import { RepositoriesApi } from '../../api/repositories-api';
import { FakeRepositoriesApi, PACKAGE_FORMAT } from '../../api/testing/fakes-catalog';
import { addressValues, expectAccessible, renderScreen, settle, text } from '../../testing/screen';
import { SearchResults } from './search-results';

async function open(q: string | undefined) {
  const r = await renderScreen(SearchResults, {
    inputs: { q },
    setup: (get) => {
      const fake = get(RepositoriesApi) as FakeRepositoriesApi;
      fake.formatsResult = [PACKAGE_FORMAT];
    },
  });
  await settle(r.fixture);
  return { ...r, fake: r.get(RepositoriesApi) as FakeRepositoriesApi };
}

/** Every link of the page: none may lead into the admin area (Q-UI-390). */
const hrefs = (root: HTMLElement) => [...root.querySelectorAll<HTMLAnchorElement>('a[href]')].map((a) => a.getAttribute('href') ?? '');

describe('Search results (R-UI-002)', () => {
  it('R-UI-002 Q-UI-330 lists the matching repositories with key, project, format and class as columns and the address as a copy button, each linked into its project, and passes axe', async () => {
    const r = await open('generic');
    const rows = [...r.root.querySelectorAll('tbody tr')];
    expect(rows.length).toBeGreaterThan(0);
    expect(rows.every((tr) => text(tr).toLowerCase().includes('generic'))).toBe(true);
    const first = rows[0]!;
    const key = first.querySelector('th a')!;
    const project = first.querySelector('td a')!;
    expect(key.getAttribute('href')).toMatch(/^\/projects\/[^/]+\/repositories\/[^/]+$/);
    expect(project.getAttribute('href')).toMatch(/^\/projects\/[^/]+$/);
    const heads = [...r.root.querySelectorAll('thead th')].map((th) => text(th).replace('help_outline', ''));
    expect(heads.map((h) => h.split(' ')[0])).toEqual(['Repository', 'Project', 'Format', 'Class', 'Actions']);
    expect(first.querySelector('td.format-cell .format-name trove-format-icon')).not.toBeNull();
    expect(text(first.querySelector('td.class-cell')!)).not.toBe('');
    // Who searches opens the repository: the address is no field, its copy button carries it in its name and tooltip.
    expect(addressValues(r.root)).toEqual([]);
    const copy = first.querySelector<HTMLButtonElement>('td.actions trove-copy-button button')!;
    expect(copy.getAttribute('aria-label')).toMatch(/^Copy client address https?:\/\//);
    expect(copy.getAttribute('title')).toBe(copy.getAttribute('aria-label'));
    expect(copy.classList.contains('btn-tertiary')).toBe(true);
    expect(text(r.root.querySelector('h1')!)).toBe('Search results');
    expect(text(r.root)).toContain('Repositories whose key or description matches “generic”.');
    await expectAccessible(r.root);
  });

  it('R-UI-002 offers the start page\'s package search for the same text, and no link of the page leads into the admin area', async () => {
    const r = await open('generic');
    const packages = [...r.root.querySelectorAll<HTMLAnchorElement>('a')].find((a) => text(a).startsWith('Find packages matching'))!;
    expect(packages.getAttribute('href')).toBe('/projects?find=generic');
    expect(hrefs(r.root).length).toBeGreaterThan(1);
    expect(hrefs(r.root).filter((h) => h.startsWith('/admin'))).toEqual([]);
  });

  it('R-UI-002 says when nothing matches, and asks for a word without one', async () => {
    const none = await open('no-such-repository-anywhere');
    expect(text(none.root)).toContain('No repository matches “no-such-repository-anywhere”.');
    const empty = await open(undefined);
    expect(text(empty.root)).toContain('Type a word into the search field to find a repository.');
    expect(empty.root.querySelector('tbody')).toBeNull();
  });
});
