import { provideHttpClient, withInterceptors } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import { AuthzEpoch } from '../core/authz-epoch';
import { conventionsInterceptor, problemInterceptor, resilienceInterceptor } from '../core/http/interceptors';
import { HttpAuthSettingsApi } from './auth-settings-api';
import { HttpPolicyApi, HttpRolesApi } from './authz-api';
import { HttpIdentityApi } from './identity-api';
import { HttpPreferencesApi } from './preferences-api';
import { HttpSessionApi } from './session-api';
import { HttpSessionsApi } from './sessions-api';
import { HttpTokensApi } from './tokens-api';
import { provideFakeApis } from './testing/fakes';

/*
 * The HTTP implementations of the access services against the real interceptors: the paths, the
 * methods, the conditional headers and the epoch handoff of R-AUTHZ-032 / R-UI-042.
 */

function setup() {
  TestBed.configureTestingModule({
    providers: [
      provideRouter([]),
      provideHttpClient(withInterceptors([problemInterceptor, resilienceInterceptor, conventionsInterceptor])),
      provideHttpClientTesting(),
      // The stores the interceptors reach through the injector need the fakes of the session services.
      ...provideFakeApis(),
      HttpSessionApi,
      HttpPreferencesApi,
      HttpPolicyApi,
      HttpRolesApi,
      HttpIdentityApi,
      HttpTokensApi,
      HttpSessionsApi,
      HttpAuthSettingsApi,
    ],
  });
  return { http: TestBed.inject(HttpTestingController), epoch: TestBed.inject(AuthzEpoch) };
}

describe('access services over HTTP', () => {
  afterEach(() => TestBed.inject(HttpTestingController).verify());

  it('R-AUTHZ-032 the epoch of a policy write goes out as Trove-Authz-Min-Epoch on the next read', async () => {
    const { http, epoch } = setup();
    const api = TestBed.inject(HttpPolicyApi);
    const body = { resourceType: 'repository', description: 'd' } as const;

    const write = api.replace('team-web-publish', body, '"3"');
    const put = http.expectOne('/api/v1/permission-policies/team-web-publish');
    expect(put.request.method).toBe('PUT');
    expect(put.request.headers.get('If-Match')).toBe('"3"');
    expect(put.request.headers.has('Trove-Authz-Min-Epoch')).toBe(false);
    put.flush(
      { name: 'team-web-publish', version: 4, resources: {}, entries: [] },
      { headers: { ETag: '"4"', 'Trove-Authz-Epoch': '1842' } },
    );
    expect((await write).etag).toBe('"4"');
    expect(epoch.value()).toBe(1842);

    // The follow-up reads of the screen: the policy itself and the caller's capabilities.
    const read = api.load('team-web-publish');
    const get = http.expectOne('/api/v1/permission-policies/team-web-publish');
    expect(get.request.headers.get('Trove-Authz-Min-Epoch')).toBe('1842');
    get.flush({ name: 'team-web-publish', version: 4, resources: {}, entries: [] }, { headers: { ETag: '"4"' } });
    await read;

    const me = TestBed.inject(HttpSessionApi).authzMe();
    const meReq = http.expectOne('/api/v1/authz/me');
    expect(meReq.request.headers.get('Trove-Authz-Min-Epoch')).toBe('1842');
    meReq.flush({ roles: [], capabilities: [], principals: [], scope: null, epoch: 1842 });
    await me;
  });

  it('R-AUTHZ-032 a role binding write hands its epoch on to the list that follows', async () => {
    const { http } = setup();
    const api = TestBed.inject(HttpRolesApi);
    const bind = api.bind('auditor', 'group:platform-admins');
    const put = http.expectOne('/api/v1/role-bindings/auditor/group%3Aplatform-admins');
    expect(put.request.method).toBe('PUT');
    expect(put.request.headers.has('If-Match')).toBe(false);
    put.flush(
      { role: 'auditor', principal: 'group:platform-admins', grantedAt: '2026-10-29T10:00:00Z', grantedBy: '' },
      { status: 201, statusText: 'Created', headers: { 'Trove-Authz-Epoch': '77' } },
    );
    await bind;
    const list = api.bindings({ principal: 'group:platform-admins' })(null, 50);
    const get = http.expectOne((r) => r.url === '/api/v1/role-bindings');
    expect(get.request.params.get('principal')).toBe('group:platform-admins');
    expect(get.request.params.get('limit')).toBe('50');
    expect(get.request.headers.get('Trove-Authz-Min-Epoch')).toBe('77');
    get.flush({ items: [], nextCursor: null });
    await list;
  });

  it('R-API-014 a policy create sends the idempotency key of the form', async () => {
    const { http } = setup();
    const created = TestBed.inject(HttpPolicyApi).create({ name: 'x', resourceType: 'repository' }, 'key-1');
    const req = http.expectOne('/api/v1/permission-policies');
    expect(req.request.headers.get('Idempotency-Key')).toBe('key-1');
    req.flush({ name: 'x', version: 1, resources: {}, entries: [] }, { status: 201, statusText: 'Created' });
    expect((await created).etag).toBe('"1"');
  });

  it('R-API-013 users, groups and service accounts are patched without a precondition, memberships as plain JSON', async () => {
    const { http } = setup();
    const api = TestBed.inject(HttpIdentityApi);

    const user = api.updateUser('a@b+c', { displayName: 'A' });
    const patch = http.expectOne('/api/v1/users/a%40b%2Bc');
    expect(patch.request.method).toBe('PATCH');
    expect(patch.request.headers.get('Content-Type')).toBe('application/merge-patch+json');
    expect(patch.request.headers.has('If-Match')).toBe(false);
    patch.flush({});
    await user;

    const groups = api.changeUserGroups('alice', { add: ['team-web'] });
    const memberships = http.expectOne('/api/v1/users/alice/groups');
    expect(memberships.request.method).toBe('PATCH');
    expect(memberships.request.headers.get('Content-Type') ?? 'application/json').not.toContain('merge-patch');
    memberships.flush({ groups: [{ name: 'team-web', source: 'manual' }] });
    expect(await groups).toEqual([{ name: 'team-web', source: 'manual' }]);
  });

  it('R-API-012 settings and preferences are patched with the loaded ETag', async () => {
    const { http } = setup();
    const settings = TestBed.inject(HttpAuthSettingsApi).patch({ anonymousAccess: true }, '"100"');
    const s = http.expectOne('/api/v1/settings/auth');
    expect(s.request.headers.get('If-Match')).toBe('"100"');
    expect(s.request.headers.get('Content-Type')).toBe('application/merge-patch+json');
    s.flush({}, { headers: { ETag: '"101"' } });
    expect((await settings).etag).toBe('"101"');

    const prefs = TestBed.inject(HttpPreferencesApi).patch({ theme: 'dark' }, '"1"');
    const p = http.expectOne('/api/v1/users/me/preferences');
    expect(p.request.headers.get('If-Match')).toBe('"1"');
    p.flush({ locale: null, theme: 'dark', keyboardShortcuts: false, projectFilter: null, revision: 2 });
    expect((await prefs).etag).toBe('"2"');
  });

  it('R-AUTH-017 ends the other sessions with keepCurrent and one session by id', async () => {
    const { http } = setup();
    const api = TestBed.inject(HttpSessionsApi);
    const others = api.endOwnAll(true);
    http.expectOne((r) => r.url === '/api/v1/users/me/sessions' && r.params.get('keepCurrent') === 'true').flush(null, {
      status: 204,
      statusText: 'No Content',
    });
    await others;
    const one = api.endOwn('abc');
    http.expectOne('/api/v1/users/me/sessions/abc').flush(null, { status: 204, statusText: 'No Content' });
    await one;
  });

  it('R-AUTH-028 lists tokens of an owner with the state filter and never asks for a value', async () => {
    const { http } = setup();
    const page = TestBed.inject(HttpTokensApi).list({ owner: 'sa:ci-bot', state: 'all', type: undefined })(null, 50);
    const req = http.expectOne((r) => r.url === '/api/v1/tokens');
    expect(req.request.params.get('owner')).toBe('sa:ci-bot');
    expect(req.request.params.get('state')).toBe('all');
    expect(req.request.params.has('type')).toBe(false);
    req.flush({ items: [], nextCursor: null });
    await page;
  });
});
