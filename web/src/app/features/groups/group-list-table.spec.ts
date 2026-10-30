import { renderScreen, settle, text } from '../../testing/screen';
import { GroupList } from './group-list';

describe('Groups table (R-UI-025, R-UI-020)', () => {
  it('R-UI-025 the name header sorts through the API; the member count is a right-aligned figure', async () => {
    const { fixture, root } = await renderScreen(GroupList);
    expect(root.querySelector('th[aria-sort]')!.getAttribute('aria-sort')).toBe('ascending');
    expect(text(root.querySelector('tbody tr')!)).toContain('platform');
    root.querySelector<HTMLButtonElement>('th button.th-sort')!.click();
    await settle(fixture);
    expect(text(root.querySelector('tbody tr')!)).toContain('team-web');
    expect(root.querySelector('tbody tr td.num')).toBeTruthy();
  });

  it('R-UI-025 the origin column explains itself in a help tooltip', async () => {
    const { root } = await renderScreen(GroupList);
    expect(root.querySelector('th button.th-help')?.getAttribute('aria-label')).toBe('About the column Origin');
  });
});
