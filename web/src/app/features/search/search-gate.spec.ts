import { signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { Router, UrlTree } from '@angular/router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { SearchApi } from '../../api/search-api';
import { SessionApi } from '../../api/session-api';
import { FakeSessionApi } from '../../api/testing/fakes';
import { FakeSearchApi } from '../../api/testing/fakes-search';
import { BrowserStorage } from '../../core/storage';
import { renderScreen, settle, sleep } from '../../testing/screen';
import { SearchField } from './search-field';
import { SearchLayout } from './search-layout';
import { gate } from './search-model';

type Persona = 'ada' | 'priya' | 'sam' | 'security-admin';

const ME: Record<Persona, Partial<Record<'capabilities' | 'roles', string[]>> & { projects?: { key: string; role: 'owner' | 'reader' }[] }> = {
  ada: {},
  priya: { capabilities: [], roles: [], projects: [{ key: 'web', role: 'owner' }] },
  sam: { capabilities: [], roles: [], projects: [{ key: 'web', role: 'reader' }] },
  'security-admin': { capabilities: ['audit:read', 'identities:read', 'settings:read'], roles: ['security-admin'] },
};

interface Opened {
  readonly url: string;
  /** The entry carried the mark Admin area. */
  readonly marked: boolean;
  /** A click, or Enter on an active entry. */
  readonly chosen: boolean;
  readonly how: string;
}

const input = (root: HTMLElement): HTMLInputElement => root.querySelector<HTMLInputElement>('#global-search')!;
const options = (root: ParentNode): HTMLElement[] => [...root.querySelectorAll<HTMLElement>('[role="option"]')];

/** Drives every way out of the field for one persona and records what it opened (R-UI-099). */
async function walk(persona: Persona): Promise<Opened[]> {
  new BrowserStorage().clear('local');
  const { root, fixture } = await renderScreen(SearchField, {
    providers: [{ provide: SearchLayout, useValue: { narrow: signal(false) } }],
    setup: (get) => {
      if (persona !== 'ada') (get(SessionApi) as FakeSessionApi).meResult = { capabilities: [], ...ME[persona] } as never;
      const s = get(SearchApi) as FakeSearchApi;
      s.users = [{ id: '0192f0c4-7a7e-7b51-9b1e-2f0d6c9e1a12', username: 'users-bot', displayName: null, disabled: false }];
      s.groups = [{ name: 'users', description: '' }];
      s.repositories = Array.from({ length: 6 }, (_, i) => ({ project: 'web', key: `users-${i}`, format: 'npm', class: 'local' as const }));
    },
  });
  document.body.appendChild(root);
  const router = TestBed.inject(Router);
  const opened: Opened[] = [];
  let pending: Omit<Opened, 'url'> = { marked: false, chosen: false, how: '' };
  vi.spyOn(router, 'navigateByUrl').mockImplementation(async (url: string | UrlTree) => {
    opened.push({ url: typeof url === 'string' ? url : router.serializeUrl(url), ...pending });
    return true;
  });
  // The first time a text waits for its answer; the field keeps the answer while the list is open, so later starts reuse it.
  const answered = new Set<string>();
  const start = async (q: string): Promise<void> => {
    input(root).dispatchEvent(new Event('focus'));
    const i = input(root);
    i.value = q;
    i.dispatchEvent(new Event('input', { bubbles: true }));
    if (q.length >= 2 && !answered.has(q)) {
      answered.add(q);
      await sleep(fixture, 300);
    } else {
      await settle(fixture);
    }
  };
  const key = (k: string): void => {
    input(root).dispatchEvent(new KeyboardEvent('keydown', { key: k, bubbles: true, cancelable: true }));
  };
  const chooseFilter = async (index: number): Promise<void> => {
    root.querySelector<HTMLButtonElement>('.search-filter')!.click();
    await settle(fixture);
    [...document.querySelectorAll<HTMLElement>('.search-filter-menu [role="menuitemradio"]')][index]!.click();
    await settle(fixture);
  };

  for (const q of ['', 'users', 'web']) {
    await start(q);
    const count = options(root).length;
    for (let i = 0; i < count; i++) {
      // Enter on the active entry.
      await start(q);
      for (let n = 0; n <= i; n++) key('ArrowDown');
      await settle(fixture);
      pending = { marked: !!options(root)[i]!.querySelector('.search-admin-mark'), chosen: true, how: `Enter on ${i} of "${q}"` };
      key('Enter');
      await settle(fixture);
      // A click on it.
      await start(q);
      pending = { marked: !!options(root)[i]!.querySelector('.search-admin-mark'), chosen: true, how: `click on ${i} of "${q}"` };
      options(root)[i]!.click();
      await settle(fixture);
    }
    // Enter without an active entry.
    await start(q);
    pending = { marked: false, chosen: false, how: `Enter without a pick on "${q}"` };
    key('Enter');
    await settle(fixture);
  }
  // Every filter, Enter without an active entry.
  for (let f = 1; f < (persona === 'ada' ? 6 : 4); f++) {
    await chooseFilter(f);
    await start('users');
    pending = { marked: false, chosen: false, how: `filter ${f}, Enter without a pick` };
    key('Enter');
    await settle(fixture);
    await chooseFilter(0);
  }
  return opened;
}

afterEach(() => {
  document.body.replaceChildren();
  vi.restoreAllMocks();
});

describe('The one gate of the search (R-UI-099)', () => {
  it('R-UI-099 a route of the main area opens; a route under /admin only for an admin suggestion an instance administrator chose', () => {
    expect(gate({ url: '/search?q=users', area: 'main', chosen: false }, true)).toBe('/search?q=users');
    expect(gate({ url: '/admin/users', area: 'admin', chosen: true }, true)).toBe('/admin/users');
    expect(gate({ url: '/admin/users', area: 'admin', chosen: false }, true)).toBeNull();
    expect(gate({ url: '/admin/users', area: 'admin', chosen: true }, false)).toBeNull();
    expect(gate({ url: '/admin/users', area: 'main', chosen: true }, true)).toBeNull();
    expect(gate({ url: '/admin', area: 'admin', chosen: true }, false)).toBeNull();
    expect(gate({ url: '/administration', area: 'main', chosen: true }, false)).toBe('/administration');
  });

  for (const persona of ['ada', 'priya', 'sam', 'security-admin'] as const) {
    it(`R-UI-099 every way out of the field as ${persona} reaches /admin ${persona === 'ada' ? 'only by the choice of a marked entry' : 'never'}`, async () => {
      const opened = await walk(persona);
      expect(opened.length).toBeGreaterThan(10);
      const admin = opened.filter((o) => o.url === '/admin' || o.url.startsWith('/admin/') || o.url.startsWith('/admin?'));
      const wrong = admin.filter((o) => persona !== 'ada' || !o.marked || !o.chosen);
      expect(wrong.map((o) => `${o.how} → ${o.url}`)).toEqual([]);
      if (persona === 'ada') {
        expect(admin.map((o) => o.url)).toEqual(expect.arrayContaining(['/admin/users', '/admin/users/users-bot', '/admin/groups/users']));
        // Under the Users and Groups filters, Enter without a pick opened nothing.
        expect(opened.filter((o) => o.how.startsWith('filter 4') || o.how.startsWith('filter 5'))).toEqual([]);
      } else {
        expect(admin).toEqual([]);
      }
      // Enter without a pick always opens the results page.
      for (const o of opened.filter((x) => x.how.startsWith('Enter without a pick'))) expect(o.url.startsWith('/search')).toBe(true);
    });
  }
});
