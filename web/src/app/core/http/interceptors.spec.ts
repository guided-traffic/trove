import {
  HttpXsrfTokenExtractor,
  provideHttpClient,
  withInterceptors,
  withXsrfConfiguration,
} from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import { CSRF_COOKIE, CSRF_HEADER } from '../../app.config';
import { ApiClient } from '../../api/client';
import { ApiError } from '../../api/problem';
import { FakeSessionApi, provideFakeApis } from '../../api/testing/fakes';
import { SessionApi } from '../../api/session-api';
import { AuthzEpoch } from '../authz-epoch';
import { SessionStore } from '../session-store';
import { conventionsInterceptor, problemInterceptor, resilienceInterceptor } from './interceptors';

function setup() {
  TestBed.configureTestingModule({
    providers: [
      provideRouter([]),
      provideHttpClient(
        withXsrfConfiguration({ cookieName: CSRF_COOKIE, headerName: CSRF_HEADER }),
        withInterceptors([problemInterceptor, resilienceInterceptor, conventionsInterceptor]),
      ),
      provideHttpClientTesting(),
      { provide: HttpXsrfTokenExtractor, useValue: { getToken: () => 'csrf-token' } },
      ...provideFakeApis(),
    ],
  });
  return {
    client: TestBed.inject(ApiClient),
    http: TestBed.inject(HttpTestingController),
    epoch: TestBed.inject(AuthzEpoch),
    session: TestBed.inject(SessionStore),
  };
}

const tick = (ms = 5): Promise<void> => new Promise((r) => setTimeout(r, ms));

describe('HTTP layer', () => {
  afterEach(() => TestBed.inject(HttpTestingController).verify());

  it('R-AUTH-012 sends X-CSRF-Token on unsafe requests and not on GET', async () => {
    const { client, http } = setup();
    const get = client.getJson('/api/v1/system/info');
    const getReq = http.expectOne('/api/v1/system/info');
    expect(getReq.request.headers.has(CSRF_HEADER)).toBe(false);
    getReq.flush({});
    await get;

    const post = client.post('/auth/logout');
    const req = http.expectOne('/auth/logout');
    expect(req.request.headers.get(CSRF_HEADER)).toBe('csrf-token');
    req.flush(null);
    await post;
  });

  it('R-API-014 sends the idempotency key of the form on a create', async () => {
    const { client, http } = setup();
    const p = client.post('/api/v1/repositories', { key: 'npm-local' }, { idempotencyKey: 'form-1' });
    const req = http.expectOne('/api/v1/repositories');
    expect(req.request.headers.get('Idempotency-Key')).toBe('form-1');
    req.flush({ key: 'npm-local' }, { status: 201, statusText: 'Created', headers: { Location: '/api/v1/repositories/npm-local', ETag: '"1"' } });
    const r = await p;
    expect(r.status).toBe(201);
    expect(r.etag).toBe('"1"');
    expect(r.location).toBe('/api/v1/repositories/npm-local');
  });

  it('R-API-012 / R-API-013 a patch carries If-Match and the merge-patch media type', async () => {
    const { client, http } = setup();
    const p = client.patch('/api/v1/repositories/x', { description: null }, '"3"');
    const req = http.expectOne('/api/v1/repositories/x');
    expect(req.request.method).toBe('PATCH');
    expect(req.request.headers.get('If-Match')).toBe('"3"');
    expect(req.request.headers.get('Content-Type')).toBe('application/merge-patch+json');
    req.flush({});
    await p;
  });

  it('R-API-010 a page request carries limit and the opaque cursor', async () => {
    const { client, http } = setup();
    const fetchPage = client.page<string>('/api/v1/groups', { sort: '-name', format: ['a', 'b'] });
    const p = fetchPage('abc', 50);
    const req = http.expectOne((r) => r.url === '/api/v1/groups');
    expect(req.request.params.get('cursor')).toBe('abc');
    expect(req.request.params.get('limit')).toBe('50');
    expect(req.request.params.getAll('format')).toEqual(['a', 'b']);
    req.flush({ items: ['x'], nextCursor: null });
    expect((await p).items).toEqual(['x']);
  });

  it('R-API-009 maps a problem+json answer to an ApiError with code, fields and request id', async () => {
    const { client, http } = setup();
    const p = client.post('/api/v1/repositories', {});
    http.expectOne('/api/v1/repositories').flush(
      {
        code: 'validation_failed',
        status: 422,
        requestId: 'rid-1',
        errors: [{ pointer: '/key', code: 'pattern', detail: 'bad' }],
      },
      { status: 422, statusText: 'Unprocessable', headers: { 'Content-Type': 'application/problem+json' } },
    );
    const err = (await p.catch((e: unknown) => e)) as ApiError;
    expect(err).toBeInstanceOf(ApiError);
    expect(err.code).toBe('validation_failed');
    expect(err.requestId).toBe('rid-1');
    expect(err.fieldErrors.get('/key')?.detail).toBe('bad');
  });

  it('R-UI-042 keeps the highest own authz epoch and sends it as Trove-Authz-Min-Epoch', async () => {
    const { client, http, epoch } = setup();
    const w = client.put('/api/v1/permission-policies/p', {}, '"1"');
    http.expectOne('/api/v1/permission-policies/p').flush({}, { status: 200, statusText: 'OK', headers: { 'Trove-Authz-Epoch': '1842' } });
    await w;
    expect(epoch.value()).toBe(1842);

    const r = client.getJson('/api/v1/authz/me');
    const meReq = http.expectOne('/api/v1/authz/me');
    expect(meReq.request.headers.get('Trove-Authz-Min-Epoch')).toBe('1842');
    meReq.flush({});
    await r;
    epoch.note(5);
    expect(epoch.value()).toBe(1842);
  });

  it('R-UI-042 412 authz_epoch_unknown drops the epoch and repeats the GET without it', async () => {
    const { client, http, epoch } = setup();
    epoch.note(99);
    const p = client.getJson('/api/v1/authz/me');
    http
      .expectOne('/api/v1/authz/me')
      .flush({ code: 'authz_epoch_unknown' }, { status: 412, statusText: 'Precondition Failed' });
    await tick();
    const retry = http.expectOne('/api/v1/authz/me');
    expect(retry.request.headers.has('Trove-Authz-Min-Epoch')).toBe(false);
    retry.flush({ capabilities: [] });
    await p;
    expect(epoch.value()).toBeNull();
  });

  it('R-UI-042 a network error retries a GET once and then gives up', async () => {
    const { client, http } = setup();
    const p = client.getJson('/api/v1/system/info').catch((e: unknown) => e);
    http.expectOne('/api/v1/system/info').error(new ProgressEvent('error'));
    await tick();
    http.expectOne('/api/v1/system/info').error(new ProgressEvent('error'));
    const err = (await p) as ApiError;
    expect(err.status).toBe(0);
    expect(err.afterRetry).toBe(true);
  });

  it('R-UI-042 a POST without an idempotency key is not repeated after a network error', async () => {
    const { client, http } = setup();
    const p = client.post('/api/v1/users/bob/disable').catch((e: unknown) => e);
    http.expectOne('/api/v1/users/bob/disable').error(new ProgressEvent('error'));
    const err = (await p) as ApiError;
    expect(err.afterRetry).toBe(false);
    http.expectNone('/api/v1/users/bob/disable');
  });

  it('R-UI-042 a create with an idempotency key is repeated once with the same key', async () => {
    const { client, http } = setup();
    const p = client.post('/api/v1/repositories', {}, { idempotencyKey: 'k' });
    http.expectOne('/api/v1/repositories').error(new ProgressEvent('error'));
    await tick();
    const retry = http.expectOne('/api/v1/repositories');
    expect(retry.request.headers.get('Idempotency-Key')).toBe('k');
    retry.flush({}, { status: 201, statusText: 'Created' });
    expect((await p).status).toBe(201);
  });

  it('R-UI-042 a DELETE that answers 404 after a retry counts as done', async () => {
    const { client, http } = setup();
    const p = client.delete('/api/v1/groups/g');
    http.expectOne('/api/v1/groups/g').error(new ProgressEvent('error'));
    await tick();
    http.expectOne('/api/v1/groups/g').flush({ code: 'not_found' }, { status: 404, statusText: 'Not Found' });
    expect((await p).status).toBe(204);
  });

  it('R-UI-042 409 idempotency_in_progress waits Retry-After and repeats with the same key', async () => {
    const { client, http } = setup();
    const p = client.post('/api/v1/repositories', {}, { idempotencyKey: 'k' });
    http
      .expectOne('/api/v1/repositories')
      .flush({ code: 'idempotency_in_progress' }, { status: 409, statusText: 'Conflict', headers: { 'Retry-After': '0' } });
    await tick();
    const retry = http.expectOne('/api/v1/repositories');
    expect(retry.request.headers.get('Idempotency-Key')).toBe('k');
    retry.flush({}, { status: 201, statusText: 'Created' });
    await p;
  });

  it('R-UI-042 a 429 on a GET retries once after Retry-After, a 429 on a POST is surfaced', async () => {
    const { client, http } = setup();
    const g = client.getJson('/api/v1/system/info');
    http.expectOne('/api/v1/system/info').flush({}, { status: 429, statusText: 'Too Many', headers: { 'Retry-After': '0' } });
    await tick();
    http.expectOne('/api/v1/system/info').flush({});
    await g;

    const p = client.post('/api/v1/users/bob/disable').catch((e: unknown) => e);
    http.expectOne('/api/v1/users/bob/disable').flush({ code: 'rate_limited' }, { status: 429, statusText: 'Too Many', headers: { 'Retry-After': '7' } });
    const err = (await p) as ApiError;
    expect(err.retryAfterSeconds).toBe(7);
  });

  it('R-UI-004 a 401 during use ends the session; a 401 of whoami does not', async () => {
    const { client, http, session } = setup();
    await session.load();
    expect(session.state()).toBe('authenticated');

    const probe = client.getJson('/api/v1/auth/whoami').catch(() => null);
    http.expectOne('/api/v1/auth/whoami').flush({ code: 'unauthenticated' }, { status: 401, statusText: 'Unauthorized' });
    await probe;
    expect(session.ended()).toBe(false);

    const p = client.getJson('/api/v1/repositories').catch(() => null);
    http.expectOne('/api/v1/repositories').flush({ code: 'unauthenticated' }, { status: 401, statusText: 'Unauthorized' });
    await p;
    expect(session.ended()).toBe(true);
    expect(session.state()).toBe('unknown');
  });

  it('R-UI-042 401 reauthentication_required asks for a recent sign-in and keeps the session', async () => {
    const { client, http, session } = setup();
    await session.load();
    const p = client.post('/api/v1/tokens', {}).catch(() => null);
    http.expectOne('/api/v1/tokens').flush({ code: 'reauthentication_required' }, { status: 401, statusText: 'Unauthorized' });
    await p;
    expect(session.reauthRequired()).toBe(true);
    expect(session.ended()).toBe(false);
    expect(session.state()).toBe('authenticated');
  });

  it('R-UI-005 a 403 drops the cached decisions but keeps the epoch', async () => {
    const { client, http, session, epoch } = setup();
    await session.load();
    const api = TestBed.inject(SessionApi) as FakeSessionApi;
    await session.check([{ repository: 'r', action: 'read' }]);
    epoch.note(7);
    const p = client.getJson('/api/v1/repositories').catch(() => null);
    http.expectOne('/api/v1/repositories').flush({ code: 'forbidden' }, { status: 403, statusText: 'Forbidden' });
    await p;
    await session.check([{ repository: 'r', action: 'read' }]);
    expect(api.checkCalls.length).toBe(2);
    expect(epoch.value()).toBe(7);
  });

  it('does not touch requests outside /api/v1/ and /auth/', async () => {
    const { client, http } = setup();
    const p = client.post('/somewhere/else', {}, { idempotencyKey: 'k' });
    const req = http.expectOne('/somewhere/else');
    expect(req.request.headers.has('Idempotency-Key')).toBe(false);
    req.flush({});
    await p;
  });
});
