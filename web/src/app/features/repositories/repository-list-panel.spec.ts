import { Router } from '@angular/router';
import { addressValues, button, expectAccessible, openFilters, renderScreen, settle, type } from '../../testing/screen';
import { RepositoryList } from './repository-list';
import { BrowserStorage } from '../../core/storage';

async function open(inputs: Record<string, unknown> = {}) {
  const rendered = await renderScreen(RepositoryList, {
    inputs,
    setup: (get) => void vi.spyOn(get(Router), 'navigate').mockResolvedValue(true),
  });
  return { ...rendered, router: rendered.get(Router) };
}

describe('Repository list filter panel (R-UI-020)', () => {
  it('R-UI-020 the panel is closed until the filter button opens it, and it passes the accessibility check open', async () => {
    const { fixture, root } = await open();
    expect(root.querySelector('trove-filter-panel form')).toBeNull();
    await openFilters(fixture, root);
    const categories = [...root.querySelectorAll('button.category-toggle .category-label')].map((l) => l.textContent);
    expect(categories).toEqual(['Key or description', 'Project', 'Class', 'Format', 'State']);
    await expectAccessible(root);
  });

  it('R-UI-002 the panel follows the Filters button in the page, before the list, and no "Recently opened" stands above them', async () => {
    const { fixture, root } = await renderScreen(RepositoryList, {
      setup: (get) => {
        vi.spyOn(get(Router), 'navigate').mockResolvedValue(true);
        new BrowserStorage().set(
          'local',
          'trove.frequent.00000000-0000-0000-0000-000000000001',
          JSON.stringify({ repositories: [{ id: 'web/generic-local', visits: 1, last: Date.now() }], projects: [] }),
        );
      },
    });
    await openFilters(fixture, root);
    const layout = root.querySelector('.list-layout')!;
    const order = [...layout.children].map((c) => (c.classList.contains('list-side') ? 'panel' : c.className.split(' ')[0]));
    expect(order).toEqual(['list-toolbar', 'panel', 'list-main']);
    expect(layout.querySelector('.list-toolbar button.filter-toggle')).toBeTruthy();
    expect(root.textContent).not.toContain('Recently opened');
  });

  it('R-UI-020 "Search" in the panel puts every staged value into the query parameters at once', async () => {
    const { fixture, root, router } = await open();
    await openFilters(fixture, root);
    type(root.querySelector<HTMLSelectElement>('select[aria-label="Class"]')!, 'remote');
    type(root.querySelector<HTMLInputElement>('input[aria-label="Key or description"]')!, ' npm ');
    root.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click();
    await settle(fixture);
    button(root, 'Search').click();
    await settle(fixture);
    const [, extras] = (router.navigate as ReturnType<typeof vi.fn>).mock.calls.at(-1)!;
    expect(extras.queryParams).toEqual({ q: 'npm', class: 'remote', format: null, state: 'inactive', project: null });
    expect(extras.queryParamsHandling).toBe('merge');
  });

  it('R-UI-020 an applied filter opens the panel with its category open, counted on the button', async () => {
    const { root } = await open({ class: 'local', format: 'generic' });
    expect(root.querySelector('trove-filter-panel form')).toBeTruthy();
    expect(root.querySelector('button.filter-toggle .filter-count')?.textContent).toBe('2');
    const expanded = [...root.querySelectorAll('button.category-toggle[aria-expanded="true"] .category-label')].map((l) => l.textContent);
    expect(expanded).toEqual(['Class', 'Format']);
  });

  it('R-UI-020 "Clear all" removes every filter from the query', async () => {
    const { fixture, root, router } = await open({ class: 'local' });
    button(root, 'Clear all').click();
    await settle(fixture);
    const [, extras] = (router.navigate as ReturnType<typeof vi.fn>).mock.calls.at(-1)!;
    expect(extras.queryParams).toEqual({ q: null, class: null, format: null, state: null, project: null });
  });

  it('R-UI-020 the repository table has no sort control because the API takes no sort parameter, and explains the class', async () => {
    const { root } = await open();
    expect(root.querySelectorAll('th button.th-sort')).toHaveLength(0);
    // The admin area's list has no address column (Q-UI-330): the class is the column to explain.
    expect([...root.querySelectorAll('th button.th-help')].map((b) => b.getAttribute('aria-label'))).toEqual([
      'About the column Class',
    ]);
  });

  it('R-REPO-002 R-UI-020 R-UI-066 an absolute client address is copied unchanged, without the origin of the page in front', async () => {
    const { root } = await open();
    expect(addressValues(root)).toEqual([]);
    const write = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: write }, configurable: true });
    const copy = root.querySelector<HTMLButtonElement>('td.actions trove-copy-button button')!;
    expect(copy.getAttribute('aria-label')).toBe('Copy client address https://trove.example.net/files/web/generic-local/');
    copy.click();
    await Promise.resolve();
    expect(write).toHaveBeenCalledWith('https://trove.example.net/files/web/generic-local/');
  });
});
