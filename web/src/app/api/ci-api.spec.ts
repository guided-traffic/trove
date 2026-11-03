import { provideHttpClient, withInterceptors } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { describe, expect, it } from 'vitest';
import { problemInterceptor } from '../core/http/interceptors';
import { SessionStore } from '../core/session-store';
import { CiApi, HttpCiApi } from './ci-api';
import { ApiError } from './problem';

function setup(): { api: CiApi; http: HttpTestingController } {
  TestBed.resetTestingModule();
  TestBed.configureTestingModule({
    providers: [
      provideHttpClient(withInterceptors([problemInterceptor])),
      provideHttpClientTesting(),
      { provide: CiApi, useClass: HttpCiApi },
      { provide: SessionStore, useValue: { markEnded: () => undefined, markReauthRequired: () => undefined, dropCheckCache: () => undefined } },
    ],
  });
  return { api: TestBed.inject(CiApi), http: TestBed.inject(HttpTestingController) };
}

describe('The CI service (R-API-035, R-UI-036)', () => {
  it('R-UI-036 the dry run sends the claims and nothing else: no token, no signature', async () => {
    const { api, http } = setup();
    const claims = { project_id: '4242', ref: 'main', ref_protected: 'true' };
    const pending = api.evaluate('gitlab-com', { subjectKind: 'id_token', claims }, null);
    const req = http.expectOne('/api/v1/ci/issuers/gitlab-com/evaluate');
    expect(req.request.method).toBe('POST');
    expect(Object.keys(req.request.body as object).sort()).toEqual(['claims', 'subjectKind']);
    expect((req.request.body as { claims: unknown }).claims).toEqual(claims);
    expect(req.request.headers.has('Authorization')).toBe(false);
    req.flush({ crossProject: false, trace: [], selected: [] });
    await pending;
    http.verify();
  });

  it('R-UI-036 a project dry run goes to the project route', async () => {
    const { api, http } = setup();
    const pending = api.evaluate('gitlab-com', { subjectKind: 'id_token', claims: {} }, 'web');
    http.expectOne('/api/v1/projects/web/ci/issuers/gitlab-com/evaluate').flush({ crossProject: false, trace: [], selected: [] });
    await pending;
  });

  it('R-API-035 the project routes carry the issuer in the body of a create and in the path of a replace', async () => {
    const { api, http } = setup();
    const body = { name: 'web-x', priority: 10, match: {}, grants: {} } as never;
    const created = api.createMapping('gitlab-com', body, 'web');
    const post = http.expectOne('/api/v1/projects/web/ci/mappings');
    expect((post.request.body as { issuer: string }).issuer).toBe('gitlab-com');
    post.flush({ name: 'web-x', version: 1 }, { headers: { ETag: '"1"' } });
    expect((await created).etag).toBe('"1"');
    const replaced = api.replaceMapping('gitlab-com', 'web-x', body, '"1"', 'web');
    const put = http.expectOne('/api/v1/projects/web/ci/mappings/gitlab-com/web-x');
    expect(put.request.headers.get('If-Match')).toBe('"1"');
    put.flush({ name: 'web-x', version: 2 }, { headers: { ETag: '"2"' } });
    await replaced;
  });

  it('R-API-035 the system list of mappings reads the issuer route, a project list filters its issuer client-side', async () => {
    const { api, http } = setup();
    const system = api.listMappings('gitlab-com', null);
    http.expectOne('/api/v1/ci/issuers/gitlab-com/mappings').flush({ items: [] });
    expect(await system).toEqual([]);
    const project = api.listMappings('b', 'web');
    http.expectOne('/api/v1/projects/web/ci/mappings').flush({ items: [{ issuer: 'a', name: 'x' }, { issuer: 'b', name: 'y' }] });
    expect((await project).map((m) => m.name)).toEqual(['y']);
  });

  it('R-UI-035 the snippet is read as text with the mapping and the format', async () => {
    const { api, http } = setup();
    const pending = api.snippet('gitlab-com', 'team-a-release', 'nuget', null);
    const req = http.expectOne((r) => r.url === '/api/v1/ci/issuers/gitlab-com/snippets/gitlab-ci');
    expect(req.request.params.get('mapping')).toBe('team-a-release');
    expect(req.request.params.get('format')).toBe('nuget');
    expect(req.request.responseType).toBe('text');
    req.flush('id_tokens:\n  X:\n    aud: a\n');
    expect(await pending).toContain('id_tokens:');
  });

  it('R-UI-035 Q-CI-26 a project reads the snippet through its own route', async () => {
    const { api, http } = setup();
    const pending = api.snippet('gitlab-com', 'web-publish', 'npm', 'web');
    const req = http.expectOne((r) => r.url === '/api/v1/projects/web/ci/issuers/gitlab-com/snippets/gitlab-ci');
    expect(req.request.params.get('mapping')).toBe('web-publish');
    req.flush('id_tokens:\n');
    expect(await pending).toContain('id_tokens');
  });

  it('R-UI-035 a problem answer to the text read is still an ApiError with its code', async () => {
    const { api, http } = setup();
    const pending = api.snippet('gitlab-com', 'm', 'npm', null);
    http
      .expectOne((r) => r.url.endsWith('/snippets/gitlab-ci'))
      .flush(JSON.stringify({ code: 'not_found', title: 'Not found', status: 404 }), { status: 404, statusText: 'Not Found' });
    const error = await pending.catch((e: unknown) => e);
    expect(error).toBeInstanceOf(ApiError);
    expect((error as ApiError).code).toBe('not_found');
    expect((error as ApiError).status).toBe(404);
  });

  it('R-CI-020 the bulk revoke sends the filters and returns the count', async () => {
    const { api, http } = setup();
    const pending = api.revokeIssuances({ issuer: 'gitlab-com', jobId: '98765' });
    const req = http.expectOne('/api/v1/ci/issuances/revoke');
    expect(req.request.body).toEqual({ issuer: 'gitlab-com', jobId: '98765' });
    req.flush({ revoked: 3 });
    expect(await pending).toBe(3);
  });

  it('R-CI-020 the issuance list sends only the filters that are set', async () => {
    const { api, http } = setup();
    const fetchPage = api.listIssuances({ issuer: 'gitlab-com', jobId: '', active: true }, null);
    const pending = fetchPage(null, 50);
    const req = http.expectOne((r) => r.url === '/api/v1/ci/issuances');
    expect(req.request.params.get('issuer')).toBe('gitlab-com');
    expect(req.request.params.get('active')).toBe('true');
    expect(req.request.params.has('jobId')).toBe(false);
    req.flush({ items: [], nextCursor: null });
    await pending;
  });
});
