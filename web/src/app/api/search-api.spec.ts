import { provideHttpClient, withInterceptors } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import {
  conventionsInterceptor,
  problemInterceptor,
  resilienceInterceptor,
} from '../core/http/interceptors';
import { ApiError } from './problem';
import { HttpRepositoriesApi } from './repositories-api';
import { HttpSearchApi } from './search-api';
import { FakeSearchApi } from './testing/fakes-search';
import { provideFakeApis } from './testing/fakes';

/*
 * The suggestions over HTTP against the real interceptors (R-API-060): the one request of a settled text, its cancellation, and
 * the repository list's `name` (R-API-063); and the fake the search field's specs run on.
 */

function setup() {
  TestBed.configureTestingModule({
    providers: [
      provideRouter([]),
      provideHttpClient(
        withInterceptors([problemInterceptor, resilienceInterceptor, conventionsInterceptor]),
      ),
      provideHttpClientTesting(),
      ...provideFakeApis(),
      HttpSearchApi,
      HttpRepositoriesApi,
    ],
  });
  return TestBed.inject(HttpTestingController);
}

const EMPTY = {
  repositories: { items: [], more: false },
  packages: { items: [], more: false },
  projects: { items: [], more: false },
};

describe('the suggestions over HTTP', () => {
  afterEach(() => TestBed.inject(HttpTestingController).verify());

  it('R-API-060 one request carries the text, the kinds comma-separated and the limit', async () => {
    const http = setup();
    const api = TestBed.inject(HttpSearchApi);
    const answer = api.suggest({
      q: 'vend',
      kinds: ['repository', 'package', 'project'],
      limit: 5,
    });
    const req = http.expectOne(
      (r) => r.url === '/api/v1/search/suggestions' && r.params.get('q') === 'vend',
    );
    expect(req.request.method).toBe('GET');
    expect(req.request.params.get('kind')).toBe('repository,package,project');
    expect(req.request.params.get('limit')).toBe('5');
    req.flush(EMPTY);
    expect(await answer).toEqual(EMPTY);
  });

  it('R-API-060 without kinds and limit the server decides them', async () => {
    const http = setup();
    const answer = TestBed.inject(HttpSearchApi).suggest({ q: 'npm' });
    const req = http.expectOne((r) => r.url === '/api/v1/search/suggestions');
    expect(req.request.params.has('kind')).toBe(false);
    expect(req.request.params.has('limit')).toBe(false);
    req.flush(EMPTY);
    await answer;
  });

  it('R-API-060 an aborted request is cancelled and rejects with an AbortError', async () => {
    const http = setup();
    const abort = new AbortController();
    const answer = TestBed.inject(HttpSearchApi).suggest({ q: 'ven', signal: abort.signal });
    const req = http.expectOne((r) => r.url === '/api/v1/search/suggestions');
    abort.abort();
    expect(req.cancelled).toBe(true);
    await expect(answer).rejects.toMatchObject({ name: 'AbortError' });
  });

  it('R-API-060 a signal aborted before the request sends nothing', async () => {
    setup();
    const abort = new AbortController();
    abort.abort();
    await expect(
      TestBed.inject(HttpSearchApi).suggest({ q: 'ven', signal: abort.signal }),
    ).rejects.toMatchObject({
      name: 'AbortError',
    });
  });

  it('R-API-063 the repository list sends the names as one comma-separated parameter', async () => {
    const http = setup();
    const page = TestBed.inject(HttpRepositoriesApi).list({
      name: ['web/npm-public', 'web/npm-private'],
    })(null, 50);
    const req = http.expectOne((r) => r.url === '/api/v1/repositories');
    expect(req.request.params.get('name')).toBe('web/npm-public,web/npm-private');
    req.flush({ items: [], nextCursor: null });
    await page;
  });
});

describe('the fake of the suggestions', () => {
  it('R-API-060 answers the kinds asked, cut at the limit with more', async () => {
    const fake = new FakeSearchApi();
    const all = await fake.suggest({ q: 'web', limit: 1 });
    expect(Object.keys(all).sort()).toEqual(['packages', 'projects', 'repositories']);
    expect(all.repositories?.items.length).toBe(1);
    expect(all.repositories?.more).toBe(true);
    const repos = await fake.suggest({ q: 'web npm', kinds: ['repository'] });
    expect(Object.keys(repos)).toEqual(['repositories']);
    expect(repos.repositories?.items.map((r) => `${r.project}/${r.key}`)).toEqual([
      'web/npm-local',
      'web/npm-all',
      'web-frontend/npm-vendor',
    ]);
  });

  it('R-API-061 refuses the admin kinds without identities:read and fails as told', async () => {
    const fake = new FakeSearchApi();
    fake.identitiesRead = false;
    await expect(fake.suggest({ q: 'sa', kinds: ['user'] })).rejects.toMatchObject({ status: 403 });
    fake.failure = new ApiError(429, { status: 429, code: 'rate_limited' }, null, 3);
    await expect(fake.suggest({ q: 'sa' })).rejects.toMatchObject({
      status: 429,
      retryAfterSeconds: 3,
    });
  });

  it('R-API-060 holds a request until released, and an abort rejects it', async () => {
    const fake = new FakeSearchApi();
    fake.hold = true;
    const abort = new AbortController();
    const first = fake.suggest({ q: 'ven', signal: abort.signal });
    const second = fake.suggest({ q: 'vend' });
    abort.abort();
    await expect(first).rejects.toMatchObject({ name: 'AbortError' });
    fake.release();
    expect((await second).repositories?.items.map((r) => r.key)).toEqual(['npm-vendor']);
    expect(fake.requests.map((r) => r.q)).toEqual(['ven', 'vend']);
  });
});
