import { provideHttpClient, withInterceptors } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { describe, expect, it } from 'vitest';
import { IDEMPOTENCY_KEY } from './client';
import { problemInterceptor } from '../core/http/interceptors';
import { SessionStore } from '../core/session-store';
import { HttpVulnerabilitiesApi, VulnerabilitiesApi } from './vulnerabilities-api';

function setup(): { api: VulnerabilitiesApi; http: HttpTestingController } {
  TestBed.resetTestingModule();
  TestBed.configureTestingModule({
    providers: [
      provideHttpClient(withInterceptors([problemInterceptor])),
      provideHttpClientTesting(),
      { provide: VulnerabilitiesApi, useClass: HttpVulnerabilitiesApi },
      { provide: SessionStore, useValue: { markEnded: () => undefined, markReauthRequired: () => undefined, dropCheckCache: () => undefined } },
    ],
  });
  return { api: TestBed.inject(VulnerabilitiesApi), http: TestBed.inject(HttpTestingController) };
}

describe('The vulnerability service (R-VULN-020, R-VULN-021)', () => {
  it('R-VULN-020 the list sends the filters as the API names them, repeats the arrays and drops the empty ones', async () => {
    const { api, http } = setup();
    const pending = api.findings({ severity: ['critical', 'high'], status: ['needs_triage'], repository: [], q: '', blocked: false, sort: '-detectedAt' })(null, 50);
    const req = http.expectOne((r) => r.url === '/api/v1/vulnerabilities/findings');
    expect(req.request.params.getAll('severity')).toEqual(['critical', 'high']);
    expect(req.request.params.getAll('status')).toEqual(['needs_triage']);
    expect(req.request.params.has('repository')).toBe(false);
    expect(req.request.params.has('q')).toBe(false);
    expect(req.request.params.get('blocked')).toBe('false');
    expect(req.request.params.get('sort')).toBe('-detectedAt');
    expect(req.request.params.get('limit')).toBe('50');
    req.flush({ items: [], nextCursor: null });
    expect((await pending).items).toEqual([]);
    http.verify();
  });

  it('R-VULN-013 a triage is a merge patch with the loaded ETag, and the answer carries the new one', async () => {
    const { api, http } = setup();
    const pending = api.triage('f1', { status: 'dismissed', dismissalReason: 'not_exploitable', comment: 'x' }, '"3"');
    const req = http.expectOne('/api/v1/vulnerabilities/findings/f1');
    expect(req.request.method).toBe('PATCH');
    expect(req.request.headers.get('If-Match')).toBe('"3"');
    expect(req.request.headers.get('Content-Type')).toBe('application/merge-patch+json');
    expect(req.request.body).toEqual({ status: 'dismissed', dismissalReason: 'not_exploitable', comment: 'x' });
    req.flush({ finding: {} }, { headers: { ETag: '"4"' } });
    expect((await pending).etag).toBe('"4"');
  });

  it('R-VULN-013 the bulk change posts the ids with an idempotency key', async () => {
    const { api, http } = setup();
    const pending = api.setStatus({ ids: ['a', 'b'], status: 'confirmed' }, 'key-1');
    const req = http.expectOne('/api/v1/vulnerabilities/findings/set-status');
    expect(req.request.method).toBe('POST');
    expect(req.request.context.get(IDEMPOTENCY_KEY)).toBe('key-1');
    expect(req.request.body).toEqual({ ids: ['a', 'b'], status: 'confirmed' });
    req.flush({ bulkId: 'b1', results: [] });
    await pending;
  });

  it('R-VULN-020 the advisory path encodes the identifier and the ecosystem as segments', async () => {
    const { api, http } = setup();
    const pending = api.advisory('NuGet', 'GHSA/odd id');
    http.expectOne('/api/v1/vulnerabilities/advisories/NuGet/GHSA%2Fodd%20id').flush({ advisory: {}, findings: [] });
    await pending;
  });

  it('R-VULN-020 the export asks for text/csv and returns the document as text, with the list filters and no limit', async () => {
    const { api, http } = setup();
    const pending = api.exportCsv({ severity: ['high'], sort: '-severity' });
    const req = http.expectOne((r) => r.url === '/api/v1/vulnerabilities/findings/export');
    expect(req.request.headers.get('Accept')).toContain('text/csv');
    expect(req.request.responseType).toBe('text');
    expect(req.request.params.has('limit')).toBe(false);
    expect(req.request.params.getAll('severity')).toEqual(['high']);
    req.flush('findingId,status\n');
    expect(await pending).toBe('findingId,status\n');
  });

  it('R-VULN-021 the summary and the trend carry the scope, the sources need no parameters', async () => {
    const { api, http } = setup();
    const summary = api.summary({ repository: ['npm-remote'], format: [] });
    const s = http.expectOne((r) => r.url === '/api/v1/vulnerabilities/summary');
    expect(s.request.params.getAll('repository')).toEqual(['npm-remote']);
    expect(s.request.params.has('format')).toBe(false);
    s.flush({});
    await summary;
    const trend = api.trend({ from: '2026-08-01', to: '2026-11-01', kind: 'malicious' });
    const t = http.expectOne((r) => r.url === '/api/v1/vulnerabilities/trend');
    expect(t.request.params.get('from')).toBe('2026-08-01');
    expect(t.request.params.get('kind')).toBe('malicious');
    t.flush({});
    await trend;
    const sources = api.sources();
    http.expectOne('/api/v1/vulnerabilities/sources').flush({ items: [{ source: 'osv' }] });
    expect(await sources).toHaveLength(1);
  });

  it('R-VULN-023 the package views are read per package and per version', async () => {
    const { api, http } = setup();
    const version = api.versionReport('pkg_1', '1.0.0-beta+1');
    http.expectOne('/api/v1/packages/pkg_1/versions/1.0.0-beta%2B1/vulnerabilities').flush({ repositories: [] });
    await version;
    const pkg = api.packageReport('pkg_1');
    http.expectOne('/api/v1/packages/pkg_1/vulnerabilities').flush({ versions: [] });
    await pkg;
    http.verify();
  });
});
