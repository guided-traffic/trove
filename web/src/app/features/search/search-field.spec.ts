import { signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { Router } from '@angular/router';
import { afterEach, describe, expect, it } from 'vitest';
import { ApiError } from '../../api/problem';
import { RepositoriesApi } from '../../api/repositories-api';
import { SearchApi } from '../../api/search-api';
import { SessionApi } from '../../api/session-api';
import { FakeSessionApi } from '../../api/testing/fakes';
import { FakeRepositoriesApi } from '../../api/testing/fakes-catalog';
import { FakeSearchApi } from '../../api/testing/fakes-search';
import { BrowserStorage } from '../../core/storage';
import { spokenText } from '../../testing/axe';
import { expectAccessible, renderScreen, settle, sleep, text } from '../../testing/screen';
import { SearchField } from './search-field';
import { SearchLayout } from './search-layout';

type Get = <A>(token: abstract new (...args: never[]) => A) => A;

/** The personas of the plan: ada holds the role admin, sam reads two projects, an anonymous visitor of public content. */
type Persona = 'ada' | 'sam' | 'anonymous';

const SAM = '00000000-0000-0000-0000-00000000005a';

function as(persona: Persona) {
  return (get: Get): void => {
    const session = get(SessionApi) as FakeSessionApi;
    if (persona === 'anonymous') {
      session.whoamiResult = new ApiError(401, null, null);
    } else if (persona === 'sam') {
      session.whoamiResult = { principal: { kind: 'user', name: 'sam', id: SAM } } as never;
      session.meResult = { capabilities: [], roles: [], projects: [{ key: 'web', role: 'reader' }] };
    }
  };
}

async function render(persona: Persona = 'ada', options: { narrow?: boolean; setup?: (get: Get) => void } = {}) {
  const r = await renderScreen(SearchField, {
    providers: [{ provide: SearchLayout, useValue: { narrow: signal(options.narrow ?? false) } }],
    setup: (get) => {
      as(persona)(get);
      options.setup?.(get);
    },
  });
  document.body.appendChild(r.root);
  return { ...r, search: r.get(SearchApi) as FakeSearchApi };
}

afterEach(() => {
  document.body.replaceChildren();
  new BrowserStorage().clear('local');
});

const input = (root: HTMLElement): HTMLInputElement => root.querySelector<HTMLInputElement>('#global-search')!;
function typeText(root: HTMLElement, value: string): void {
  const i = input(root);
  i.value = value;
  i.dispatchEvent(new Event('input', { bubbles: true }));
}
function press(root: HTMLElement, key: string, init: KeyboardEventInit = {}): KeyboardEvent {
  const e = new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true, ...init });
  input(root).dispatchEvent(e);
  return e;
}
const options = (root: ParentNode): HTMLElement[] => [...root.querySelectorAll<HTMLElement>('[role="option"]')];
const groupNames = (root: HTMLElement): string[] =>
  [...root.querySelectorAll('[role="listbox"] [role="group"]')].map((g) => spokenText(g));
/** What a screen reader says for each entry or item. */
const said = (items: readonly Element[]): string[] => items.map((i) => spokenText(i));
const status = (root: HTMLElement): string => text(root.querySelector('.search-status') ?? document.createElement('p'));

async function open(fixture: Parameters<typeof settle>[0], root: HTMLElement): Promise<void> {
  input(root).focus();
  input(root).dispatchEvent(new Event('focus'));
  await settle(fixture);
}

describe('The search field (R-UI-095)', () => {
  it('R-UI-095 a signed-in caller without any role gets the combobox with the slash hint, a filter button and no admin kinds', async () => {
    const { root, fixture } = await render('sam');
    const field = input(root);
    expect(field.getAttribute('role')).toBe('combobox');
    expect(field.placeholder).toBe('Search or go to… (/)');
    expect(root.querySelector('[role="search"]')).not.toBeNull();
    const filter = root.querySelector<HTMLButtonElement>('.search-filter')!;
    expect(filter.getAttribute('aria-label')).toBe('Filter: All');
    filter.click();
    await settle(fixture);
    const items = said([...document.querySelectorAll('.search-filter-menu [role="menuitemradio"]')]);
    expect(items).toEqual(['All', 'Repositories', 'Packages', 'Projects']);
  });

  it('R-UI-095 an anonymous visitor of public content gets the field without the slash hint', async () => {
    const { root } = await render('anonymous');
    expect(input(root).placeholder).toBe('Search or go to…');
  });

  it('R-UI-095 R-UI-099 the filter offers an instance administrator Users and Groups, marked Admin area; the chip shows the kind and Backspace returns to All', async () => {
    const { root, fixture } = await render('ada');
    root.querySelector<HTMLButtonElement>('.search-filter')!.click();
    await settle(fixture);
    const items = [...document.querySelectorAll<HTMLElement>('.search-filter-menu [role="menuitemradio"]')];
    expect(said(items)).toEqual(['All', 'Repositories', 'Packages', 'Projects', 'Users, Admin area', 'Groups, Admin area']);
    items[2]!.click();
    await settle(fixture);
    expect(text(root.querySelector('.search-chip')!)).toContain('Packages');
    expect(root.querySelector('.search-filter')!.getAttribute('aria-label')).toBe('Filter: Packages');
    press(root, 'Backspace');
    await settle(fixture);
    expect(root.querySelector('.search-chip')).toBeNull();
    expect(root.querySelector('.search-filter')!.getAttribute('aria-label')).toBe('Filter: All');
  });

  it('R-UI-095 below 36 rem a button opens the sheet with the focus in its field; Escape closes the list, then the sheet, and the focus is on the button again', async () => {
    const { root, fixture } = await render('sam', { narrow: true });
    expect(input(root)).toBeNull();
    const button = root.querySelector<HTMLButtonElement>('.search-sheet-button')!;
    expect(button.getAttribute('aria-label')).toBe('Search');
    button.click();
    await settle(fixture);
    const sheet = root.querySelector('[role="dialog"]')!;
    expect(sheet.getAttribute('aria-label')).toBe('Search');
    expect(document.activeElement).toBe(input(root));
    input(root).dispatchEvent(new Event('focus'));
    await settle(fixture);
    expect(input(root).getAttribute('aria-expanded')).toBe('true');
    press(root, 'Escape');
    await settle(fixture);
    expect(input(root).getAttribute('aria-expanded')).toBe('false');
    press(root, 'Escape');
    await settle(fixture);
    expect(root.querySelector('[role="dialog"]')).toBeNull();
    expect(document.activeElement).toBe(root.querySelector('.search-sheet-button'));
    await expectAccessible(root);
  });
});

describe('What the opened field offers (R-UI-096)', () => {
  it('R-UI-096 a plain user gets Places, View all my projects and no Admin area group', async () => {
    const { root, fixture } = await render('sam');
    await open(fixture, root);
    expect(groupNames(root)).toEqual(['Places']);
    expect(said(options(root))).toEqual(['Projects', 'Guides', 'Profile', 'Personal tokens', 'View all my projects']);
    await expectAccessible(root);
  });

  it('R-UI-096 R-UI-099 an instance administrator gets the Admin area group last, its heading and every entry marked', async () => {
    const { root, fixture } = await render('ada');
    await open(fixture, root);
    expect(groupNames(root)).toEqual(['Places', 'Admin area']);
    const admin = [...root.querySelectorAll('[role="group"]')].at(-1)!;
    expect(admin.classList).toContain('admin-start');
    expect(said(options(admin))).toEqual([
      'Dashboard, Admin area',
      'Repositories, Admin area',
      'Users, Admin area',
      'Groups, Admin area',
      'Settings, Admin area',
    ]);
    await expectAccessible(root);
  });

  it('R-UI-096 an anonymous visitor gets Projects and Guides only', async () => {
    const { root, fixture } = await render('anonymous');
    await open(fixture, root);
    expect(said(options(root))).toEqual(['Projects', 'Guides']);
  });

  it('R-UI-096 Q-UI-406 the frequently visited repositories show after one name= request, in their order, and only those it returned; the others stay stored', async () => {
    const now = Date.now();
    const stored = {
      repositories: [
        { id: 'web/releases', visits: 3, last: now - 1000 },
        { id: 'platform/collections', visits: 1, last: now - 500 },
        { id: 'web/generic-local', visits: 1, last: now - 2000 },
      ],
      projects: [{ id: 'web', visits: 2, last: now }],
    };
    const { root, fixture, get } = await render('sam', {
      setup: () => new BrowserStorage().set('local', `trove.frequent.${SAM}`, JSON.stringify(stored)),
    });
    const repos = get(RepositoriesApi) as FakeRepositoriesApi;
    const before = repos.listFilters.length;
    await open(fixture, root);
    await settle(fixture);
    const named = repos.listFilters.slice(before).filter((f) => f.name);
    expect(named).toHaveLength(1);
    expect(named[0]!.name).toEqual(['web/releases', 'platform/collections', 'web/generic-local']);
    expect(groupNames(root)).toEqual(['Places', 'Frequently visited repositories', 'Frequently visited projects']);
    const frequent = [...root.querySelectorAll('[role="group"]')][1]!;
    expect(said(options(frequent))).toEqual(['releases · web', 'generic-local · web']);
    expect(new BrowserStorage().get('local', `trove.frequent.${SAM}`)).toContain('platform/collections');
    // Opened again, nothing changed: no second request.
    input(root).dispatchEvent(new Event('blur'));
    await open(fixture, root);
    expect(repos.listFilters.slice(before).filter((f) => f.name)).toHaveLength(1);
  });
});

describe('Live suggestions (R-UI-097)', () => {
  it('R-UI-097 one request 250 ms after the last key, with the kinds of the filter; a superseded text sends nothing', async () => {
    const { root, fixture, search } = await render('sam');
    await open(fixture, root);
    typeText(root, 'ven');
    await sleep(fixture, 50);
    typeText(root, 'vend');
    await sleep(fixture, 50);
    expect(search.requests).toHaveLength(0);
    await sleep(fixture, 300);
    expect(search.requests.map((r) => [r.q, r.kinds?.join(','), r.limit])).toEqual([['vend', 'repository,package,project', 5]]);
    expect(groupNames(root)).toEqual(['Repositories']);
    const first = options(root)[0]!;
    expect(spokenText(first)).toBe('npm-vendor · web-frontend');
    expect(first.querySelector('b')?.textContent).toBe('vend');
    expect(text(root.querySelector('[aria-live="polite"]')!)).toBe('1 suggestion.');
    await expectAccessible(root);
  });

  it('R-UI-097 a newer text cancels the pending request', async () => {
    const { root, fixture, search } = await render('sam');
    search.hold = true;
    await open(fixture, root);
    typeText(root, 'npm');
    await sleep(fixture, 300);
    typeText(root, 'npm-l');
    await sleep(fixture, 300);
    expect(search.requests.map((r) => r.q)).toEqual(['npm', 'npm-l']);
    expect(search.requests[0]!.signal?.aborted).toBe(true);
    expect(status(root)).toBe('Searching…');
    search.hold = false;
    search.release();
    await settle(fixture);
  });

  it('R-UI-097 Q-UI-407 the groups of an instance administrator: the main area first, then Admin pages, Users and Groups behind a line, each marked', async () => {
    const { root, fixture } = await render('ada', {
      setup: (get) => {
        const s = get(SearchApi) as FakeSearchApi;
        s.users = [{ id: '0192f0c4-7a7e-7b51-9b1e-2f0d6c9e1a12', username: 'users-bot', displayName: null, disabled: true }];
        s.groups = [{ name: 'users', description: '' }];
      },
    });
    await open(fixture, root);
    typeText(root, 'users');
    await sleep(fixture, 300);
    expect(groupNames(root)).toEqual(['Admin pages, Admin area', 'Users, Admin area', 'Groups, Admin area']);
    expect(said(options(root))).toEqual([
      'Users, Admin area',
      'users-bot · users-bot, Disabled, Admin area',
      'users, Admin area',
    ]);
    expect(root.querySelector('.search-group.admin-start')).toBe(root.querySelector('[role="group"]'));
  });

  it('R-UI-097 the main area groups in their order, with the All … for line where the answer says more', async () => {
    const { root, fixture } = await render('sam', {
      setup: (get) => {
        const s = get(SearchApi) as FakeSearchApi;
        s.repositories = Array.from({ length: 6 }, (_, i) => ({ project: 'web', key: `web-${i}`, format: 'npm', class: 'local' as const }));
      },
    });
    await open(fixture, root);
    typeText(root, 'web');
    await sleep(fixture, 300);
    expect(groupNames(root)).toEqual(['Repositories', 'Packages', 'Projects']);
    const repos = options(root.querySelector('[role="group"]')!);
    expect(repos).toHaveLength(6);
    expect(spokenText(repos[5]!)).toBe('All repositories for “web”');
    repos[5]!.click();
    await settle(fixture);
    expect(TestBed.inject(Router).url).toBe('/search?q=web&kind=repository');
  });

  it('R-UI-097 a kind chosen asks for that kind alone with ten', async () => {
    const { root, fixture, search } = await render('sam');
    root.querySelector<HTMLButtonElement>('.search-filter')!.click();
    await settle(fixture);
    [...document.querySelectorAll<HTMLElement>('.search-filter-menu [role="menuitemradio"]')][1]!.click();
    await settle(fixture);
    typeText(root, 'npm');
    await sleep(fixture, 300);
    expect(search.requests.at(-1)).toMatchObject({ q: 'npm', kinds: ['repository'], limit: 10 });
  });

  it('R-UI-097 no match says so; a 429 shows its notice and sends nothing before Retry-After; another error says the suggestions are not available', async () => {
    const { root, fixture, search } = await render('sam');
    await open(fixture, root);
    typeText(root, 'zzz');
    await sleep(fixture, 300);
    expect(status(root)).toBe('Nothing you may read matches “zzz”. Press Enter to search everything.');
    search.failure = new ApiError(429, { status: 429, code: 'rate_limited' }, null, 1);
    typeText(root, 'zzzz');
    await sleep(fixture, 300);
    expect(status(root)).toBe('Too many searches. Suggestions return in 1 s.');
    const sent = search.requests.length;
    typeText(root, 'zzzzz');
    await sleep(fixture, 400);
    expect(search.requests).toHaveLength(sent);
    search.failure = new ApiError(500, null, null);
    await sleep(fixture, 900);
    expect(search.requests.length).toBe(sent + 1);
    expect(status(root)).toBe('Suggestions are not available.');
  });

  it('R-UI-097 an anonymous caller waits for three characters and 500 ms, and sends at most four requests in 4 s', async () => {
    const { root, fixture, search } = await render('anonymous');
    await open(fixture, root);
    typeText(root, 'we');
    await sleep(fixture, 600);
    expect(search.requests).toHaveLength(0);
    for (const t of ['web', 'webx', 'weby', 'webz', 'webq']) {
      typeText(root, t);
      await sleep(fixture, 550);
    }
    expect(search.requests.map((r) => r.q)).toEqual(['web', 'webx', 'weby', 'webz']);
  });

  it('R-UI-097 the keyboard: Down enters the list and crosses groups, Up from the first entry returns to the text, nothing wraps, Enter opens, Escape closes and then empties', async () => {
    const { root, fixture } = await render('sam');
    await open(fixture, root);
    typeText(root, 'web');
    await sleep(fixture, 300);
    const field = input(root);
    expect(field.getAttribute('aria-expanded')).toBe('true');
    expect(field.getAttribute('aria-controls')).toBe('search-suggestions');
    expect(field.getAttribute('aria-autocomplete')).toBe('list');
    expect(field.getAttribute('aria-activedescendant')).toBeNull();
    const all = options(root);
    const firstGroupSize = options(root.querySelector('[role="group"]')!).length;
    for (let i = 0; i < firstGroupSize + 1; i++) press(root, 'ArrowDown');
    await settle(fixture);
    expect(field.getAttribute('aria-activedescendant')).toBe(all[firstGroupSize]!.id);
    for (let i = 0; i < all.length + 3; i++) press(root, 'ArrowDown');
    await settle(fixture);
    expect(field.getAttribute('aria-activedescendant')).toBe(options(root).at(-1)!.id);
    for (let i = 0; i < all.length + 3; i++) press(root, 'ArrowUp');
    await settle(fixture);
    expect(field.getAttribute('aria-activedescendant')).toBeNull();
    press(root, 'ArrowDown');
    press(root, 'ArrowDown');
    await settle(fixture);
    expect(spokenText(options(root)[1]!)).toBe('npm-all · web');
    press(root, 'Enter');
    await settle(fixture);
    expect(TestBed.inject(Router).url).toBe('/projects/web/repositories/npm-all');
    await open(fixture, root);
    typeText(root, 'web');
    await sleep(fixture, 300);
    press(root, 'Escape');
    await settle(fixture);
    expect(field.getAttribute('aria-expanded')).toBe('false');
    expect(field.value).toBe('web');
    press(root, 'Escape');
    await settle(fixture);
    expect(field.value).toBe('');
    press(root, 'ArrowDown', { altKey: true });
    await settle(fixture);
    expect(field.getAttribute('aria-expanded')).toBe('true');
    expect(field.getAttribute('aria-activedescendant')).toBeNull();
    press(root, 'Tab');
    await settle(fixture);
    expect(field.getAttribute('aria-expanded')).toBe('false');
  });

  it('R-UI-098 Enter without an active entry opens the results page with the text, never a route under /admin', async () => {
    const { root, fixture } = await render('ada');
    await open(fixture, root);
    typeText(root, 'test');
    await sleep(fixture, 300);
    press(root, 'Enter');
    await settle(fixture);
    expect(TestBed.inject(Router).url).toBe('/search?q=test');
  });
});
