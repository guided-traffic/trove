import { afterEach } from 'vitest';
import { ApiError } from '../../api/problem';
import { RepositoriesApi } from '../../api/repositories-api';
import { SessionApi } from '../../api/session-api';
import { FakeSessionApi } from '../../api/testing/fakes';
import { FakeRepositoriesApi } from '../../api/testing/fakes-catalog';
import { BrowserStorage } from '../../core/storage';
import { addressValues, expectAccessible, renderScreen, settle, text } from '../../testing/screen';
import { ProjectsHome } from './projects-home';

/** The fakes' signed-in principal (`alice`). */
const ALICE = '00000000-0000-0000-0000-000000000001';

/** Stores visits of repositories as the search's store keeps them, newest last visit first in the array's order of `last`. */
function visited(...refs: string[]): void {
  const now = Date.now();
  const repositories = refs.map((id, i) => ({ id, visits: 1, last: now - i * 1000 }));
  new BrowserStorage().set('local', `trove.frequent.${ALICE}`, JSON.stringify({ repositories, projects: [] }));
}

afterEach(() => new BrowserStorage().clear('local'));

describe('Start page', () => {
  it('R-UI-067 R-UI-041 names each project by its display name with the key under it, once', async () => {
    const { root } = await renderScreen(ProjectsHome);
    const heads = [...root.querySelectorAll('thead th')].map((th) => text(th));
    expect(heads.some((h) => h.startsWith('Key'))).toBe(false);
    expect(heads.some((h) => h.startsWith('Display name'))).toBe(false);
    const first = root.querySelector('tbody th[scope="row"]');
    expect(first?.querySelector('.secondary')?.getAttribute('translate')).toBe('no');
    await expectAccessible(root);
  });

  it('R-UI-067 R-UI-096 shows the repositories opened before, from the search\'s store, with their client address and Set up client', async () => {
    const { root, fixture } = await renderScreen(ProjectsHome, { setup: () => visited('web/generic-local') });
    await settle(fixture);
    expect(text(root.querySelector('.recent')!)).toContain('generic-local');
    expect(addressValues(root).some((a) => a.includes('web/generic-local'))).toBe(true);
    const setup = [...root.querySelectorAll('.recent a.btn')].find((a) => text(a) === 'Set up client');
    expect(setup?.getAttribute('href')).toBe('/setup?repository=web%2Fgeneric-local');
  });

  it('R-UI-096 Q-UI-406 Recently opened asks one name= request for its entries, newest first, and leaves out one the caller cannot read', async () => {
    const { root, fixture, get } = await renderScreen(ProjectsHome, { setup: () => visited('web/releases', 'platform/gone', 'web/generic-local') });
    await settle(fixture);
    const named = (get(RepositoriesApi) as FakeRepositoriesApi).listFilters.filter((f) => f.name);
    expect(named).toEqual([{ name: ['web/releases', 'platform/gone', 'web/generic-local'] }]);
    expect([...root.querySelectorAll('.recent-name a')].map((a) => text(a))).toEqual(['releases', 'generic-local']);
  });

  it('Q-UI-414 the start page has no package search of its own: the top bar\'s field is the one search', async () => {
    const { root } = await renderScreen(ProjectsHome);
    expect(root.querySelector('input[type="search"]')).toBeNull();
    expect(text(root)).not.toContain('Find a package');
  });

  it('R-UI-095 an anonymous visitor sees the public projects without a role column', async () => {
    const { root, fixture } = await renderScreen(ProjectsHome, {
      setup: (get) => ((get(SessionApi) as FakeSessionApi).whoamiResult = new ApiError(401, null, null)),
    });
    await settle(fixture);
    expect(text(root.querySelector('caption')!)).toBe('Public projects');
    const heads = [...root.querySelectorAll('thead th')].map((th) => text(th).replace('help_outline', ''));
    expect(heads.some((h) => h.startsWith('Your role'))).toBe(false);
    expect(root.querySelector('.recent')).toBeNull();
    await expectAccessible(root);
  });
});
