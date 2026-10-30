import { seriousViolations } from '../../testing/axe';
import { buttonByText, openFilters, renderScreen, settle, text, typeInto } from '../../testing/screen';
import { UserList } from './user-list';

describe('Users list table and filter panel (R-UI-024, R-UI-020)', () => {
  it('R-UI-024 the user name header sorts through the API and flips the order', async () => {
    const { fixture, root } = await renderScreen(UserList);
    const header = root.querySelector('th[aria-sort]')!;
    expect(header.getAttribute('aria-sort')).toBe('ascending');
    expect(text(root.querySelector('tbody tr')!)).toContain('alice');

    header.querySelector<HTMLButtonElement>('button.th-sort')!.click();
    await settle(fixture);
    expect(root.querySelector('th[aria-sort]')!.getAttribute('aria-sort')).toBe('descending');
    expect(text(root.querySelector('tbody tr')!)).toContain('bob');
  });

  it('R-UI-024 only the column the API can sort is a sort control', async () => {
    const { root } = await renderScreen(UserList);
    expect(root.querySelectorAll('th button.th-sort')).toHaveLength(1);
  });

  it('R-UI-024 the filter panel counts the applied filters; "Clear all" resets them and the list', async () => {
    const { fixture, root } = await renderScreen(UserList);
    await openFilters(fixture, root);
    typeInto(root, 'Name or email', 'bob');
    buttonByText(root, 'Apply filters').click();
    await settle(fixture);
    expect(root.querySelector('button.filter-toggle .filter-count')?.textContent).toBe('1');
    expect(root.querySelectorAll('tbody tr')).toHaveLength(1);

    buttonByText(root, 'Clear all').click();
    await settle(fixture);
    expect(root.querySelector('button.filter-toggle .filter-count')).toBeNull();
    expect(root.querySelectorAll('tbody tr')).toHaveLength(2);
  });

  it('R-UI-044 the open filter panel passes the accessibility check', async () => {
    const { fixture, root } = await renderScreen(UserList);
    await openFilters(fixture, root);
    expect(await seriousViolations(root)).toEqual([]);
  });
});
