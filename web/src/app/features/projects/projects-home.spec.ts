import { vi } from 'vitest';
import { RecentRepositories } from '../repositories/recent-repositories';
import { addressValues, expectAccessible, renderScreen, settle, text, type } from '../../testing/screen';
import { ProjectsHome } from './projects-home';

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

  it('R-UI-067 shows the repositories opened before with their client address and Set up client', async () => {
    const { root, fixture } = await renderScreen(ProjectsHome, {
      setup: (get) => get(RecentRepositories).add('web/generic-local'),
    });
    await settle(fixture);
    expect(text(root.querySelector('.recent')!)).toContain('generic-local');
    expect(addressValues(root).some((a) => a.includes('web/generic-local'))).toBe(true);
    const setup = [...root.querySelectorAll('.recent a.btn')].find((a) => text(a) === 'Set up client');
    expect(setup?.getAttribute('href')).toBe('/setup?repository=web%2Fgeneric-local');
  });

  it('R-UI-067 the package search asks the server from two characters on, after the typing stops', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const { root, fixture } = await renderScreen(ProjectsHome);
      type(root.querySelector<HTMLInputElement>('#find-package')!, 'l');
      await settle(fixture);
      expect(root.querySelector('.found')).toBeNull();
      type(root.querySelector<HTMLInputElement>('#find-package')!, 'left');
      await vi.advanceTimersByTimeAsync(350);
      await settle(fixture);
      expect(root.querySelector('.found')).toBeTruthy();
    } finally {
      vi.useRealTimers();
    }
  });

  it('R-UI-002 the package search takes its text from the address, as the search results page hands it over', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const { root, fixture } = await renderScreen(ProjectsHome, { inputs: { find: 'left' } });
      await vi.advanceTimersByTimeAsync(350);
      await settle(fixture);
      expect(root.querySelector<HTMLInputElement>('#find-package')!.value).toBe('left');
      expect(root.querySelector('.found')).toBeTruthy();
    } finally {
      vi.useRealTimers();
    }
  });
});
