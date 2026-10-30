import { EndpointsApi } from '../../api/endpoints-api';
import { FakeEndpointsApi } from '../../api/testing/fakes-catalog';
import { renderScreen, settle } from '../../testing/screen';
import { EndpointList } from './endpoint-list';

const names = (root: HTMLElement): (string | undefined)[] =>
  [...root.querySelectorAll('tbody tr th a')].map((a) => a.textContent?.trim());

describe('Endpoint list table (R-EP-017)', () => {
  it('R-EP-017 the name and rule-count headers sort the complete list in the browser', async () => {
    const { fixture, root } = await renderScreen(EndpointList, {
      setup: (get) => {
        (get(EndpointsApi) as FakeEndpointsApi).endpoints = [
          { name: 'zeta', inert: true, ruleCount: 1, enabledRuleCount: 1, configuration: null },
          { name: 'alpha', inert: true, ruleCount: 5, enabledRuleCount: 0, configuration: null },
          { name: 'mid', inert: true, ruleCount: 3, enabledRuleCount: 3, configuration: null },
        ];
      },
    });
    expect(names(root)).toEqual(['alpha', 'mid', 'zeta']);

    const sortButtons = root.querySelectorAll<HTMLButtonElement>('th button.th-sort');
    expect(sortButtons).toHaveLength(2);
    sortButtons[0]!.click();
    await settle(fixture);
    expect(names(root)).toEqual(['zeta', 'mid', 'alpha']);

    root.querySelectorAll<HTMLButtonElement>('th button.th-sort')[1]!.click();
    await settle(fixture);
    expect(names(root)).toEqual(['zeta', 'mid', 'alpha']);
    expect(root.querySelector('th[aria-sort="ascending"]')?.textContent).toContain('Rules');
  });
});
