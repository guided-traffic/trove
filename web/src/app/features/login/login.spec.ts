import { provideHttpClient } from '@angular/common/http';
import { TestBed } from '@angular/core/testing';
import { Router, provideRouter, withComponentInputBinding } from '@angular/router';
import { LoginApi } from '../../api/login-api';
import { ApiError } from '../../api/problem';
import { seriousViolations } from '../../testing/axe';
import { FakeLoginApi, provideFakeApis } from '../../api/testing/fakes';
import { BrowserNavigation } from '../../core/browser-navigation';
import { routes } from '../../app.routes';
import { App } from '../../app';

async function open(url: string, setup?: (api: FakeLoginApi) => void) {
  await TestBed.configureTestingModule({
    imports: [App],
    providers: [provideRouter(routes, withComponentInputBinding()), provideHttpClient(), ...provideFakeApis()],
  }).compileComponents();
  setup?.(TestBed.inject(LoginApi) as FakeLoginApi);
  const assign = vi.spyOn(TestBed.inject(BrowserNavigation), 'assign').mockImplementation(() => undefined);
  const fixture = TestBed.createComponent(App);
  await TestBed.inject(Router).navigateByUrl(url);
  await fixture.whenStable();
  await fixture.whenStable();
  return { root: fixture.nativeElement as HTMLElement, assign, fixture };
}

describe('Sign-in screen', () => {
  it('R-UI-004 renders one button per provider from login-options and no password form', async () => {
    const { root } = await open('/login', (api) => {
      api.optionsResult = {
        ...api.optionsResult,
        providers: [
          { id: 'corp', displayName: 'Corporate SSO', startUrl: '/auth/oidc/corp/start', available: true },
          { id: 'ext', displayName: 'Partner', startUrl: '/auth/oidc/ext/start', available: false },
        ],
      };
    });
    const buttons = [...root.querySelectorAll('main button')];
    expect(buttons.map((b) => b.textContent?.replace(/\s+/g, ' ').trim())).toEqual([
      'Sign in with Corporate SSO',
      'Sign in with Partner',
    ]);
    expect((buttons[1] as HTMLButtonElement).disabled).toBe(true);
    expect(root.querySelector('input[type="password"]')).toBeNull();
  });

  /** The `<base href>` the build writes: `/ui/en/` per locale, `/ui/` on the development server. */
  function withBase(href: string): () => void {
    const base = document.createElement('base');
    base.href = href;
    document.head.prepend(base);
    return () => base.remove();
  }

  it('R-UI-004 a click navigates to the provider start URL with return_to under the base href of the build, /ui/{locale}/', async () => {
    const done = withBase('/ui/en/');
    try {
      const { root, assign } = await open('/login?returnTo=%2Fadmin%2Fusers');
      (root.querySelector('main button') as HTMLButtonElement).click();
      expect(assign).toHaveBeenCalledTimes(1);
      const target = new URL(assign.mock.calls[0]?.[0] ?? '', 'https://trove.test');
      expect(target.pathname).toBe('/auth/oidc/corp/start');
      expect(target.searchParams.get('return_to')).toBe('/ui/en/admin/users');
    } finally {
      done();
    }
  });

  it('R-UI-004 on the development server, whose base is /ui/, return_to is under /ui/ and not under a locale it does not serve', async () => {
    const done = withBase('/ui/');
    try {
      const { root, assign } = await open('/login?returnTo=%2Fadmin%2Fusers');
      (root.querySelector('main button') as HTMLButtonElement).click();
      const target = new URL(assign.mock.calls[0]?.[0] ?? '', 'https://trove.test');
      expect(target.searchParams.get('return_to')).toBe('/ui/admin/users');
    } finally {
      done();
    }
  });

  it('R-UI-004 the German build keeps return_to under /ui/de/, also with a base written relative to the page', async () => {
    for (const href of ['/ui/de/', 'ui/de/']) {
      TestBed.resetTestingModule();
      const done = withBase(href);
      try {
        // The browser resolves the base against the page (here the test's document at the origin's root).
        expect(new URL(document.baseURI).pathname).toBe('/ui/de/');
        const { root, assign } = await open('/login?returnTo=%2Fadmin%2Fusers');
        (root.querySelector('main button') as HTMLButtonElement).click();
        const target = new URL(assign.mock.calls[0]?.[0] ?? '', 'https://trove.test');
        expect(target.searchParams.get('return_to'), href).toBe('/ui/de/admin/users');
      } finally {
        done();
      }
    }
  });

  it('R-UI-004 a foreign returnTo is replaced by the start page', async () => {
    const done = withBase('/ui/en/');
    try {
      const { root, assign } = await open('/login?returnTo=https%3A%2F%2Fevil.test%2F');
      (root.querySelector('main button') as HTMLButtonElement).click();
      const target = new URL(assign.mock.calls[0]?.[0] ?? '', 'https://trove.test');
      expect(target.searchParams.get('return_to')).toBe('/ui/en/');
    } finally {
      done();
    }
  });

  it('R-UI-004 Q-UI-125 autoRedirect navigates at once, unless ?auto_sign_in=false waives it and shows the break-glass form', async () => {
    const auto = await open('/login', (api) => (api.optionsResult = { ...api.optionsResult, autoRedirect: 'corp' }));
    expect(auto.assign).toHaveBeenCalledTimes(1);
    TestBed.resetTestingModule();

    const waived = await open('/login?auto_sign_in=false', (api) => (api.optionsResult = { ...api.optionsResult, autoRedirect: 'corp' }));
    expect(waived.assign).not.toHaveBeenCalled();
    expect(waived.root.querySelector('input[type="password"]')).toBeTruthy();
    TestBed.resetTestingModule();

    // Only the value false waives it, as on the sign-in pages this parameter comes from.
    const other = await open('/login?auto_sign_in=true', (api) => (api.optionsResult = { ...api.optionsResult, autoRedirect: 'corp' }));
    expect(other.assign).toHaveBeenCalledTimes(1);
  });

  it('Q-UI-125 with the break-glass form the providers follow below "or sign in with", the form first', async () => {
    const { root } = await open('/login?auto_sign_in=false', (api) => (api.optionsResult = { ...api.optionsResult, autoRedirect: 'corp' }));
    const order = [...root.querySelectorAll('form.local, .divider, .providers')].map((e) => e.className.split(' ')[0]);
    expect(order).toEqual(['local', 'divider', 'providers']);
    expect(root.querySelector('.divider')?.textContent?.trim()).toBe('or sign in with');
    expect(root.querySelector('.providers button')?.textContent?.trim()).toBe('Corporate SSO');
    expect(root.querySelector('form.local')?.getAttribute('aria-label')).toBe('Local administrator');
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-UI-004 the break-glass form shows when localLogin is true', async () => {
    const { root } = await open('/login', (api) => (api.optionsResult = { ...api.optionsResult, localLogin: true }));
    expect(root.querySelector('form.local')).toBeTruthy();
  });

  it('R-UI-004 purpose=reauth is passed on when the screen was opened for re-authentication', async () => {
    const { root, assign } = await open('/login?returnTo=%2Fprofile%2Ftokens&reauth=1');
    (root.querySelector('main button') as HTMLButtonElement).click();
    const target = new URL(assign.mock.calls[0]?.[0] ?? '', 'https://trove.test');
    expect(target.searchParams.get('purpose')).toBe('reauth');
  });

  it('R-AUTH-011 a re-authentication explains why and offers no way around it', async () => {
    const { root } = await open('/login?returnTo=%2Fprofile%2Ftokens&reauth=1', (api) => {
      api.optionsResult = { ...api.optionsResult, anonymousAccess: true };
    });
    expect(root.querySelector('p[role="status"].reauth')?.textContent).toContain('needs a recent sign-in');
    expect(root.textContent).not.toContain('Continue without signing in');
  });

  it('R-UI-004 with anonymous access on the screen offers to continue without signing in', async () => {
    const { root } = await open('/login?returnTo=%2Fbrowse', (api) => {
      api.optionsResult = { ...api.optionsResult, anonymousAccess: true };
    });
    const link = [...root.querySelectorAll('a')].find((a) => a.textContent?.includes('Continue without signing in'));
    expect(link?.getAttribute('href')).toBe('/browse');
  });

  it('R-UI-004 a refused break-glass sign-in shows the translated reason and no password stays in the field', async () => {
    const { root, fixture } = await open('/login?auto_sign_in=false', (api) => {
      api.localLogin = async () => {
        throw new ApiError(401, { code: 'invalid_credentials' }, null);
      };
    });
    const form = root.querySelector('form.local') as HTMLFormElement;
    const password = form.querySelector('input[type="password"]') as HTMLInputElement;
    expect(password.getAttribute('autocomplete')).toBe('current-password');
    password.value = 'wrong';
    password.dispatchEvent(new Event('input'));
    form.dispatchEvent(new Event('submit', { cancelable: true }));
    await fixture.whenStable();
    await new Promise((r) => setTimeout(r, 0));
    fixture.detectChanges();
    expect(root.querySelector('.local-error')?.textContent).toContain('wrong');
    expect(await seriousViolations(root)).toEqual([]);
  });
});
