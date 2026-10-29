import { TestBed } from '@angular/core/testing';
import { Router, provideRouter } from '@angular/router';
import { ApiError } from '../api/problem';
import { SessionApi } from '../api/session-api';
import { FakeSessionApi, provideFakeApis } from '../api/testing/fakes';
import { AuthzEpoch } from './authz-epoch';
import { BrowserNavigation, safeReturnTo } from './browser-navigation';
import { CHECK_TTL_MS, SessionStore } from './session-store';

function setup() {
  TestBed.configureTestingModule({ providers: [provideRouter([]), ...provideFakeApis()] });
  return {
    store: TestBed.inject(SessionStore),
    api: TestBed.inject(SessionApi) as FakeSessionApi,
    epoch: TestBed.inject(AuthzEpoch),
  };
}

describe('SessionStore', () => {
  it('R-UI-004 loads whoami and authz/me once and shares concurrent loads', async () => {
    const { store, api } = setup();
    const whoami = vi.spyOn(api, 'whoami');
    await Promise.all([store.load(), store.load()]);
    await store.load();
    expect(whoami).toHaveBeenCalledTimes(1);
    expect(store.state()).toBe('authenticated');
    expect(store.whoami()?.principal.name).toBe('alice');
    expect(store.isAdministrator()).toBe(true);
  });

  it('R-UI-004 a 401 of whoami means anonymous, any other failure means failed', async () => {
    const a = setup();
    a.api.whoamiResult = new ApiError(401, { code: 'unauthenticated' }, null);
    await a.store.load();
    expect(a.store.state()).toBe('anonymous');
    TestBed.resetTestingModule();

    const b = setup();
    b.api.whoamiResult = new ApiError(503, { code: 'unavailable' }, 'rid');
    await b.store.load();
    expect(b.store.state()).toBe('failed');
    expect(b.store.loadError()?.requestId).toBe('rid');
  });

  it('R-UI-005 batches checks in chunks of 100 and caches the answers for 60 seconds', async () => {
    const { store, api } = setup();
    const checks = Array.from({ length: 250 }, (_, i) => ({ repository: `r${i}`, action: 'read' }));
    api.allow = (c) => c.repository !== 'r3';
    const first = await store.check(checks);
    expect(api.checkCalls.map((c) => c.length)).toEqual([100, 100, 50]);
    expect(first[3]).toBe(false);
    expect(first[4]).toBe(true);

    await store.check(checks);
    expect(api.checkCalls.length).toBe(3);

    vi.useFakeTimers({ now: Date.now() + CHECK_TTL_MS + 1, toFake: ['Date'] });
    await store.check(checks.slice(0, 1));
    vi.useRealTimers();
    expect(api.checkCalls.length).toBe(4);
  });

  it('R-UI-042 reset clears the epoch; the cached decisions are dropped without touching it', async () => {
    const { store, epoch } = setup();
    await store.load();
    epoch.note(12);
    store.dropCheckCache();
    expect(epoch.value()).toBe(12);
    store.reset();
    expect(epoch.value()).toBeNull();
    expect(store.state()).toBe('unknown');
  });

  it('R-UI-004 sign-out posts the logout, resets and lands on /logged-out', async () => {
    const { store, api, epoch } = setup();
    await store.load();
    epoch.note(3);
    const router = TestBed.inject(Router);
    const navigate = vi.spyOn(router, 'navigateByUrl').mockResolvedValue(true);
    await store.signOut();
    expect(api.loggedOut).toBe(true);
    expect(navigate).toHaveBeenCalledWith('/logged-out');
    expect(epoch.value()).toBeNull();
    expect(store.state()).toBe('unknown');
  });

  it('R-UI-004 sign-out follows the provider redirect when the server names one', async () => {
    const { store, api } = setup();
    api.logoutResult = { redirectTo: 'https://idp.example.net/logout' };
    const assign = vi.spyOn(TestBed.inject(BrowserNavigation), 'assign').mockImplementation(() => undefined);
    await store.signOut();
    expect(assign).toHaveBeenCalledWith('https://idp.example.net/logout');
  });

  it('R-UI-004 a sign-out in another tab resets this one and shows /logged-out', async () => {
    const { store } = setup();
    await store.load();
    const router = TestBed.inject(Router);
    const navigate = vi.spyOn(router, 'navigateByUrl').mockResolvedValue(true);
    const other = new BroadcastChannel('trove-session');
    other.postMessage('signed-out');
    await vi.waitFor(() => expect(navigate).toHaveBeenCalledWith('/logged-out'));
    other.close();
    expect(store.state()).toBe('unknown');
  });
});

describe('safeReturnTo', () => {
  it('R-AUTH-009 keeps a server-relative path and rejects everything that could leave the origin', () => {
    expect(safeReturnTo('/admin/users?x=1')).toBe('/admin/users?x=1');
    for (const bad of ['https://evil.test/', '//evil.test', '/\\evil.test', 'admin', '', null, undefined, '/a\nb']) {
      expect(safeReturnTo(bad)).toBe('/');
    }
  });
});
