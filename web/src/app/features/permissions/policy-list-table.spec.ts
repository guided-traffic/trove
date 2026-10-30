import { PolicyApi } from '../../api/authz-api';
import { FakePolicyApi, samplePolicy } from '../../api/testing/access-fakes';
import { renderScreen, settle, text } from '../../testing/screen';
import { PolicyList } from './policy-list';

describe('Policy list table and filter panel (R-UI-027, R-UI-020)', () => {
  it('R-UI-027 the name header sorts through the API', async () => {
    const { fixture, root } = await renderScreen(PolicyList, {
      setup: (get) => {
        (get(PolicyApi) as FakePolicyApi).policies = [samplePolicy({ name: 'alpha' }), samplePolicy({ name: 'omega' })];
      },
    });
    expect(text(root.querySelector('tbody tr')!)).toContain('alpha');
    root.querySelector<HTMLButtonElement>('th button.th-sort')!.click();
    await settle(fixture);
    expect(text(root.querySelector('tbody tr')!)).toContain('omega');
    expect(root.querySelector('th[aria-sort]')!.getAttribute('aria-sort')).toBe('descending');
  });

  it('R-UI-027 a link with a principal opens the filter panel with the filter shown and counted', async () => {
    const { root } = await renderScreen(PolicyList, { inputs: { principal: 'anonymous' } });
    expect(root.querySelector('trove-filter-panel form')).toBeTruthy();
    expect(root.querySelector('button.filter-toggle .filter-count')?.textContent).toBe('1');
  });
});
