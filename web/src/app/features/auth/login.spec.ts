import { provideHttpClient } from '@angular/common/http';
import { TestBed } from '@angular/core/testing';
import { Router, provideRouter, withComponentInputBinding } from '@angular/router';
import { LoginApi } from '../../api/login-api';
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

  it('R-UI-004 a click navigates to the provider start URL with return_to under /ui/{locale}/', async () => {
    const { root, assign } = await open('/login?returnTo=%2Fadmin%2Fusers');
    (root.querySelector('main button') as HTMLButtonElement).click();
    expect(assign).toHaveBeenCalledTimes(1);
    const target = new URL(assign.mock.calls[0]?.[0] ?? '', 'https://trove.test');
    expect(target.pathname).toBe('/auth/oidc/corp/start');
    expect(target.searchParams.get('return_to')).toBe('/ui/en/admin/users');
  });

  it('R-UI-004 a foreign returnTo is replaced by the start page', async () => {
    const { root, assign } = await open('/login?returnTo=https%3A%2F%2Fevil.test%2F');
    (root.querySelector('main button') as HTMLButtonElement).click();
    const target = new URL(assign.mock.calls[0]?.[0] ?? '', 'https://trove.test');
    expect(target.searchParams.get('return_to')).toBe('/ui/en/');
  });

  it('R-UI-004 autoRedirect navigates at once, unless ?local=1 asks for the break-glass form', async () => {
    const auto = await open('/login', (api) => (api.optionsResult = { ...api.optionsResult, autoRedirect: 'corp' }));
    expect(auto.assign).toHaveBeenCalledTimes(1);
    TestBed.resetTestingModule();

    const local = await open('/login?local=1', (api) => (api.optionsResult = { ...api.optionsResult, autoRedirect: 'corp' }));
    expect(local.assign).not.toHaveBeenCalled();
    expect(local.root.querySelector('input[type="password"]')).toBeTruthy();
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
});
