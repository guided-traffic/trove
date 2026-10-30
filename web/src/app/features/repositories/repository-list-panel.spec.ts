import { Router } from '@angular/router';
import { button, expectAccessible, openFilters, renderScreen, settle, type } from '../../testing/screen';
import { RepositoryList } from './repository-list';

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
    expect(categories).toEqual(['Key or description', 'Class', 'Format', 'State']);
    await expectAccessible(root);
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
    expect(extras.queryParams).toEqual({ q: 'npm', class: 'remote', format: null, state: 'inactive' });
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
    expect(extras.queryParams).toEqual({ q: null, class: null, format: null, state: null });
  });

  it('R-UI-020 the repository table has no sort control because the API takes no sort parameter, and explains three columns', async () => {
    const { root } = await open();
    expect(root.querySelectorAll('th button.th-sort')).toHaveLength(0);
    expect([...root.querySelectorAll('th button.th-help')].map((b) => b.getAttribute('aria-label'))).toEqual([
      'About the column Class',
      'About the column Client address',
      'About the column Last activity',
    ]);
  });

  it('R-REPO-002 R-UI-020 an absolute client address is shown and copied unchanged, without the origin of the page in front', async () => {
    const { root } = await open();
    const code = root.querySelector('tbody tr code')!;
    expect(code.textContent?.trim()).toBe('https://trove.example.net/files/generic-local/');
    expect(code.getAttribute('title')).toBe('https://trove.example.net/files/generic-local/');
    const write = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: write }, configurable: true });
    root.querySelector<HTMLButtonElement>('trove-copy-button button')!.click();
    await Promise.resolve();
    expect(write).toHaveBeenCalledWith('https://trove.example.net/files/generic-local/');
  });
});
