import { provideHttpClient } from '@angular/common/http';
import { signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { Router, provideRouter, withComponentInputBinding } from '@angular/router';
import { App } from './app';
import { routes } from './app.routes';
import { FakeSessionApi, provideFakeApis } from './api/testing/fakes';
import { ApiError } from './api/problem';
import { SessionApi } from './api/session-api';
import { BannerStore } from './core/banner-store';
import { Layout } from './core/layout';
import { NavState } from './core/nav-state';
import { BrowserStorage } from './core/storage';
import { SessionStore } from './core/session-store';
import { seriousViolations } from './testing/axe';

async function render(url = '/', setup?: (api: FakeSessionApi) => void, pinned?: boolean, keepStorage = false) {
  if (!keepStorage) new BrowserStorage().clear('local');
  await TestBed.configureTestingModule({
    imports: [App],
    providers: [
      provideRouter(routes, withComponentInputBinding()),
      provideHttpClient(),
      ...provideFakeApis(),
      // jsdom has no viewport: the specs that need the pinned sidebar say so.
      ...(pinned === undefined ? [] : [{ provide: Layout, useValue: { pinned: signal(pinned) } }]),
    ],
  }).compileComponents();
  setup?.(TestBed.inject(SessionApi) as FakeSessionApi);
  const fixture = TestBed.createComponent(App);
  await TestBed.inject(Router).navigateByUrl(url);
  await fixture.whenStable();
  return { fixture, root: fixture.nativeElement as HTMLElement };
}

describe('App shell', () => {
  it('R-UI-002 renders header, navigation, main region and the first content screen', async () => {
    const { root } = await render();
    expect(root.querySelector('mat-toolbar')?.textContent).toContain('trove');
    expect(root.querySelector('nav[aria-label]')).toBeTruthy();
    expect(root.querySelector('main#main')).toBeTruthy();
    expect(root.querySelector('main h1')?.textContent).toContain('Browse');
  });

  it('R-UI-002 the skip link is the first focusable element and focuses the main heading', async () => {
    const { root } = await render();
    document.body.appendChild(root);
    const first = root.querySelector<HTMLElement>('a, button, [tabindex="0"]');
    expect(first?.classList.contains('skip-link')).toBe(true);
    first?.click();
    expect(document.activeElement?.tagName).toBe('H1');
    root.remove();
  });

  it('R-UI-002 shows the banner area only while a banner text is set', async () => {
    const { fixture, root } = await render();
    expect(root.querySelector('[role="region"]')).toBeNull();
    TestBed.inject(BannerStore).set('Maintenance tonight');
    await fixture.whenStable();
    const banner = root.querySelector('[role="region"]');
    expect(banner?.getAttribute('aria-label')).toBeTruthy();
    expect(banner?.textContent).toContain('Maintenance tonight');
  });

  it('R-UI-001 shows the not-found page for an unknown route', async () => {
    const { fixture, root } = await render();
    await TestBed.inject(Router).navigateByUrl('/no/such/page');
    await fixture.whenStable();
    expect(root.querySelector('main h1')?.textContent).toContain('Page not found');
  });

  it('R-UI-002 an administrator sees the Administration area, a user without capabilities does not', async () => {
    const admin = await render();
    expect(admin.root.querySelector('nav')?.textContent).toContain('Administration');
    expect(admin.root.querySelector('nav')?.textContent).toContain('System status');
    TestBed.resetTestingModule();

    const plain = await render('/', (api) => (api.meResult = { capabilities: [] }));
    expect(plain.root.querySelector('nav')?.textContent).toContain('Packages and files');
    expect(plain.root.querySelector('nav')?.textContent).not.toContain('Administration');
  });

  it('R-UI-002 shows only the entries whose capability the caller holds', async () => {
    const { root } = await render('/', (api) => (api.meResult = { capabilities: ['audit:read'] }));
    const links = [...root.querySelectorAll('nav a .nav-label')].map((a) => a.textContent?.trim());
    expect(links).toEqual(['Packages', 'Browse', 'Guides', 'Audit log']);
  });

  it('R-UI-004 a missing session sends the caller to /login with the route as returnTo', async () => {
    const { root } = await render('/admin/users', (api) => {
      api.whoamiResult = new ApiError(401, { code: 'unauthenticated' }, null);
    });
    const router = TestBed.inject(Router);
    expect(router.url).toBe('/login?returnTo=%2Fadmin%2Fusers');
    expect(root.querySelector('main h1')?.textContent).toContain('Sign in');
    expect(TestBed.inject(SessionStore).state()).toBe('anonymous');
  });

  it('R-UI-044 reports no serious accessibility violation on the shell', async () => {
    const { root } = await render();
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-UI-002 the navigation groups its entries (identity, access, system) with an icon per entry and folds a group', async () => {
    const { fixture, root } = await render('/admin/users');
    const toggles = [...root.querySelectorAll<HTMLButtonElement>('nav button.group-toggle')];
    expect(toggles.map((b) => b.querySelector('.nav-label')?.textContent)).toEqual(['Identity', 'Access', 'System']);
    expect(root.querySelectorAll('nav .nav-icon').length).toBeGreaterThanOrEqual(10);
    expect(root.querySelector('nav a[aria-current="page"] .nav-label')?.textContent).toBe('Users');

    const identity = toggles[0]!;
    const list = root.querySelector<HTMLElement>(`#${identity.getAttribute('aria-controls')}`)!;
    expect(identity.getAttribute('aria-expanded')).toBe('true');
    expect(list.hidden).toBe(false);
    // The group that holds the current page is marked when it is folded.
    identity.click();
    await fixture.whenStable();
    expect(identity.getAttribute('aria-expanded')).toBe('false');
    expect(list.hidden).toBe(true);
    expect(identity.classList.contains('has-active')).toBe(true);
  });

  it('R-UI-002 a folded group stays folded for the same viewer and not for another', async () => {
    const first = await render('/admin/users');
    first.root.querySelector<HTMLButtonElement>('nav button.group-toggle')!.click();
    await first.fixture.whenStable();
    const storage = new BrowserStorage();
    const key = storage.entries('local').find(([k]) => k.startsWith('trove.nav.'))![0];
    expect(JSON.parse(storage.get('local', key)!)).toEqual({ rail: false, closed: ['identity'] });
    // Nothing of the layout is a secret (R-UI-043): the key carries the principal's name only.
    expect(key).toMatch(/^trove\.nav\.[^\s]+$/);

    TestBed.resetTestingModule();
    const second = await render('/admin/users', undefined, undefined, true);
    await second.fixture.whenStable();
    const toggle = second.root.querySelector<HTMLButtonElement>('nav button.group-toggle')!;
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
  });

  it('R-UI-002 the pinned sidebar can be compacted to an icon rail that keeps every accessible name, and the choice is stored', async () => {
    const { fixture, root } = await render('/admin/users', undefined, true);
    const sidebar = root.querySelector('mat-sidenav')!;
    const rail = root.querySelector<HTMLButtonElement>('button.rail-toggle')!;
    expect(rail.getAttribute('aria-pressed')).toBe('false');
    expect(sidebar.classList.contains('rail')).toBe(false);

    rail.click();
    await fixture.whenStable();
    expect(rail.getAttribute('aria-pressed')).toBe('true');
    expect(sidebar.classList.contains('rail')).toBe(true);
    expect(TestBed.inject(NavState).rail()).toBe(true);
    // The labels are still in the document for assistive technology.
    expect([...root.querySelectorAll('nav a .nav-label')].map((l) => l.textContent)).toContain('Users');
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-UI-002 below the pinned width there is no rail toggle and the menu button opens the drawer', async () => {
    const { root } = await render('/', undefined, false);
    expect(root.querySelector('button.rail-toggle')).toBeNull();
    expect(root.querySelector('mat-toolbar button[aria-label="Open navigation"]')).toBeTruthy();
  });

  it('R-UI-002 the breadcrumb trail names the area and the screen and marks the current page', async () => {
    const { root } = await render('/admin/users');
    const crumbs = root.querySelector('main nav[aria-label="Breadcrumb"]')!;
    expect([...crumbs.querySelectorAll('li')].map((li) => li.textContent?.trim())).toEqual(['Administration', 'Users']);
    expect(crumbs.querySelector('[aria-current="page"]')?.textContent).toBe('Users');
    expect(crumbs.querySelectorAll('a')).toHaveLength(0);
  });

  it('R-UI-002 a detail screen links back to its list in the breadcrumb trail', async () => {
    const { root } = await render('/admin/users/alice');
    const crumbs = root.querySelector('main nav[aria-label="Breadcrumb"]')!;
    expect([...crumbs.querySelectorAll('li')].map((li) => li.textContent?.trim())).toEqual([
      'Administration',
      'Users',
      'alice',
    ]);
    expect(crumbs.querySelector('a')?.getAttribute('href')).toBe('/admin/users');
  });

  it('R-UI-002 the repository search is offered to a caller who may list repositories and opens the list filtered', async () => {
    const { fixture, root } = await render();
    const search = root.querySelector<HTMLFormElement>('mat-toolbar form[role="search"]')!;
    expect(search.querySelector('label')?.textContent).toContain('Find a repository');
    const input = search.querySelector<HTMLInputElement>('input')!;
    input.value = 'generic';
    input.dispatchEvent(new Event('input', { bubbles: true }));
    search.dispatchEvent(new Event('submit', { cancelable: true }));
    await fixture.whenStable();
    expect(TestBed.inject(Router).url).toBe('/admin/repositories?q=generic');
  });

  it('R-UI-005 the repository search is hidden from a caller without the capability behind it', async () => {
    const { root } = await render('/', (api) => (api.meResult = { capabilities: ['audit:read'] }));
    expect(root.querySelector('mat-toolbar form[role="search"]')).toBeNull();
  });
});

