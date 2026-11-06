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
      // jsdom has no viewport: the sidebar is pinned unless a spec asks for the drawer.
      { provide: Layout, useValue: { pinned: signal(pinned ?? true) } },
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
    expect(root.querySelector('header.topbar')?.textContent).toContain('trove');
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

  it('R-UI-002 Q-UI-127 the admin area has a sidebar of its own behind the Admin button, which only its users see', async () => {
    const admin = await render();
    const sidebar = () => admin.root.querySelector('aside.sidebar')?.textContent ?? '';
    expect(sidebar()).toContain('Artifacts');
    expect(sidebar()).not.toContain('System status');
    const button = admin.root.querySelector<HTMLAnchorElement>('header a.admin-button')!;
    expect(button.getAttribute('href')).toBe('/admin');
    expect(button.getAttribute('aria-current')).toBeNull();
    await TestBed.inject(Router).navigateByUrl('/admin/system');
    await admin.fixture.whenStable();
    expect(sidebar()).toContain('Admin area');
    expect(sidebar()).toContain('System status');
    expect(sidebar()).not.toContain('Browse');
    expect(button.getAttribute('aria-current')).toBe('true');
    // Inside the admin area the button keeps the default look of the reference; only aria-current tells it apart.
    expect([...button.classList]).toEqual(['btn', 'btn-default', 'admin-button']);
    TestBed.resetTestingModule();

    const plain = await render('/', (api) => (api.meResult = { capabilities: [] }));
    expect(plain.root.querySelector('aside.sidebar')?.textContent).toContain('Artifacts');
    expect(plain.root.querySelector('header a.admin-button')).toBeNull();
  });

  it('R-UI-002 shows only the entries whose capability the caller holds, in each area', async () => {
    const { fixture, root } = await render('/', (api) => (api.meResult = { capabilities: ['audit:read'] }));
    const links = () => [...root.querySelectorAll('nav a .nav-label')].map((a) => a.textContent?.trim());
    expect(links()).toEqual(['Packages', 'Browse', 'Guides', 'Vulnerabilities']);
    await TestBed.inject(Router).navigateByUrl('/admin/audit');
    await fixture.whenStable();
    expect(links()).toEqual(['Audit log']);
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

  it('R-UI-004 Q-UI-125 the sign-in screen stands alone, whether opened directly or reached from a guarded route', async () => {
    const shellOf = (root: HTMLElement) => ({
      toolbar: root.querySelector('header.topbar') !== null,
      sidebar: root.querySelector('aside.sidebar') !== null,
      skipLink: root.querySelector('.skip-link') !== null,
      heading: root.querySelector('main h1')?.textContent?.trim(),
    });
    const guarded = await render('/admin/users', (api) => {
      api.whoamiResult = new ApiError(401, { code: 'unauthenticated' }, null);
    });
    const viaGuard = shellOf(guarded.root);
    TestBed.resetTestingModule();
    const direct = await render('/login');
    expect(viaGuard).toEqual({ toolbar: false, sidebar: false, skipLink: false, heading: 'Sign in' });
    expect(shellOf(direct.root)).toEqual(viaGuard);
    expect(await seriousViolations(direct.root)).toEqual([]);
  });

  it('Q-UI-125 the signed-out screen stands alone, and the shell returns on the next screen', async () => {
    const { root, fixture } = await render('/logged-out');
    expect(root.querySelector('header.topbar')).toBeNull();
    expect(root.querySelector('main h1')?.textContent).toContain('Signed out');
    await TestBed.inject(Router).navigateByUrl('/admin/users');
    await fixture.whenStable();
    expect(root.querySelector('header.topbar')).toBeTruthy();
    expect(root.querySelector('nav[aria-label]')).toBeTruthy();
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
    expect(JSON.parse(storage.get('local', key)!)).toEqual({ collapsed: false, closed: ['identity'] });
    // Nothing of the layout is a secret (R-UI-043): the key carries the principal's name only.
    expect(key).toMatch(/^trove\.nav\.[^\s]+$/);

    TestBed.resetTestingModule();
    const second = await render('/admin/users', undefined, undefined, true);
    await second.fixture.whenStable();
    const toggle = second.root.querySelector<HTMLButtonElement>('nav button.group-toggle')!;
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
  });

  it('R-UI-002 Q-UI-127 the pinned sidebar can be collapsed and opened again from the top bar, and the choice is stored', async () => {
    const { fixture, root } = await render('/admin/users', undefined, true);
    const open = () => root.querySelector<HTMLButtonElement>('header button[aria-label="Open navigation"]');
    expect(root.querySelector('aside.sidebar')).toBeTruthy();
    expect(open()).toBeNull();

    [...root.querySelectorAll<HTMLButtonElement>('aside button')].find((b) => b.textContent?.includes('Collapse sidebar'))!.click();
    await fixture.whenStable();
    expect(root.querySelector('aside.sidebar')).toBeNull();
    expect(TestBed.inject(NavState).collapsed()).toBe(true);
    expect(await seriousViolations(root)).toEqual([]);

    open()!.click();
    await fixture.whenStable();
    expect(root.querySelector('aside.sidebar')).toBeTruthy();
    expect(TestBed.inject(NavState).collapsed()).toBe(false);
  });

  it('R-UI-002 below the pinned width the sidebar is a drawer: closed at first, opened from the top bar, closed by Escape', async () => {
    const { fixture, root } = await render('/', undefined, false);
    expect(root.querySelector('aside.sidebar')).toBeNull();
    root.querySelector<HTMLButtonElement>('header button[aria-label="Open navigation"]')!.click();
    await fixture.whenStable();
    const drawer = root.querySelector<HTMLElement>('aside.sidebar.drawer')!;
    expect(drawer).toBeTruthy();
    expect(root.querySelector('.backdrop')).toBeTruthy();
    drawer.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    await fixture.whenStable();
    expect(root.querySelector('aside.sidebar')).toBeNull();
  });

  it('R-UI-002 the breadcrumb trail names the area and the screen and marks the current page', async () => {
    const { root } = await render('/admin/users');
    const crumbs = root.querySelector('nav[aria-label="Breadcrumb"]')!;
    expect([...crumbs.querySelectorAll('li')].map((li) => li.textContent?.trim())).toEqual(['Admin area', 'Users']);
    expect(crumbs.querySelector('[aria-current="page"]')?.textContent).toBe('Users');
    expect(crumbs.querySelectorAll('a')).toHaveLength(0);
  });

  it('R-UI-002 a detail screen links back to its list in the breadcrumb trail', async () => {
    const { root } = await render('/admin/users/alice');
    const crumbs = root.querySelector('nav[aria-label="Breadcrumb"]')!;
    expect([...crumbs.querySelectorAll('li')].map((li) => li.textContent?.trim())).toEqual([
      'Admin area',
      'Users',
      'alice',
    ]);
    expect(crumbs.querySelector('a')?.getAttribute('href')).toBe('/admin/users');
  });

  it('R-UI-002 the repository search is offered to a caller who may list repositories and opens the list filtered', async () => {
    const { fixture, root } = await render();
    const search = root.querySelector<HTMLFormElement>('header form[role="search"]')!;
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
    expect(root.querySelector('header form[role="search"]')).toBeNull();
  });

  it('R-UI-050 the Documentation link opens the manual page of the current screen, or the overview', async () => {
    const { fixture, root } = await render('/admin/endpoints');
    const help = () => [...root.querySelectorAll<HTMLAnchorElement>('header a')].find((a) => a.textContent?.trim().endsWith('Documentation'));
    expect(help()?.getAttribute('href')).toBe('/help/admin/endpoints');
    await TestBed.inject(Router).navigateByUrl('/browse');
    await fixture.whenStable();
    expect(help()?.getAttribute('href')).toBe('/help/user/files');
    await TestBed.inject(Router).navigateByUrl('/admin/system/jobs');
    await fixture.whenStable();
    expect(help()?.getAttribute('href')).toBe('/help/admin/operations');
    // A screen without a manual page of its own (here the not-found screen) opens the overview.
    await TestBed.inject(Router).navigateByUrl('/no/such/page');
    await fixture.whenStable();
    expect(help()?.getAttribute('href')).toBe('/help');
  });

  it('R-API-021 the theme button offers the three choices, marks the current one and applies a choice', async () => {
    const { fixture, root } = await render();
    const button = root.querySelector<HTMLButtonElement>('header button[aria-label="Theme"]')!;
    button.click();
    await fixture.whenStable();
    const items = () => [...document.querySelectorAll<HTMLElement>('[role="menuitemradio"]')];
    expect(items().map((i) => i.querySelector('span:last-child')?.textContent?.trim())).toEqual([
      'Follow the system',
      'Light',
      'Dark',
    ]);
    expect(items().map((i) => i.getAttribute('aria-checked'))).toEqual(['true', 'false', 'false']);
    items()[2]!.click();
    await fixture.whenStable();
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark');
    button.click();
    await fixture.whenStable();
    expect(items().map((i) => i.getAttribute('aria-checked'))).toEqual(['false', 'false', 'true']);
    items()[0]!.click();
    await fixture.whenStable();
    expect(document.documentElement.hasAttribute('data-theme')).toBe(false);
  });

  it('R-UI-002 the user menu shows the initial and the name; the theme is not in it any more', async () => {
    const { fixture, root } = await render();
    const user = root.querySelector<HTMLButtonElement>('header .user-button')!;
    const name = user.querySelector('.user-name')?.textContent?.trim() ?? '';
    expect(user.querySelector('.avatar')?.textContent?.trim()).toBe(name.charAt(0));
    user.click();
    await fixture.whenStable();
    const menu = [...document.querySelectorAll('.menu [role="menuitem"]')].map((e) => e.textContent?.trim());
    expect(menu.some((t) => t?.startsWith('Language'))).toBe(true);
    expect(menu).not.toContain('Theme');
  });

  it('R-UI-045 the Language item opens a submenu whose languages are menu items', async () => {
    const { fixture, root } = await render();
    root.querySelector<HTMLButtonElement>('header .user-button')!.click();
    await fixture.whenStable();
    const language = [...document.querySelectorAll<HTMLElement>('.menu [role="menuitem"]')].find((e) =>
      e.textContent?.trim().startsWith('Language'),
    )!;
    // A pointer opens the submenu on entering the item; the click that follows must not close it again.
    language.dispatchEvent(new MouseEvent('mouseenter'));
    await fixture.whenStable();
    expect(language.getAttribute('aria-expanded')).toBe('true');
    language.click();
    await fixture.whenStable();
    expect(language.getAttribute('aria-expanded')).toBe('true');
    const languages = [...document.querySelectorAll<HTMLElement>('[role="menuitem"][lang]')];
    expect(languages.map((e) => e.textContent?.trim())).toEqual(['English', 'Deutsch']);
  });
});
