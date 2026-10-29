import { provideHttpClient } from '@angular/common/http';
import { TestBed } from '@angular/core/testing';
import { Router, provideRouter } from '@angular/router';
import { App } from './app';
import { routes } from './app.routes';
import { FakeSessionApi, provideFakeApis } from './api/testing/fakes';
import { ApiError } from './api/problem';
import { SessionApi } from './api/session-api';
import { BannerStore } from './core/banner-store';
import { SessionStore } from './core/session-store';
import { seriousViolations } from './testing/axe';

async function render(url = '/', setup?: (api: FakeSessionApi) => void) {
  await TestBed.configureTestingModule({
    imports: [App],
    providers: [provideRouter(routes), provideHttpClient(), ...provideFakeApis()],
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
    const links = [...root.querySelectorAll('nav a')].map((a) => a.textContent?.trim());
    expect(links).toEqual(['Browse', 'Audit log']);
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
});
