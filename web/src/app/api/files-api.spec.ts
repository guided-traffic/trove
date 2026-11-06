import { provideHttpClient } from '@angular/common/http';
import { TestBed } from '@angular/core/testing';
import { SessionStore } from '../core/session-store';
import { ApiError } from './problem';
import {
  HttpFilesApi,
  HASH_LIMIT_BYTES,
  contentPath,
  encodePath,
  hexOfReprDigest,
  isFilesAddress,
  reprDigestOf,
} from './files-api';

describe('files API helpers', () => {
  it('R-UI-012 the Repr-Digest of a file is its SHA-256 as RFC 9530 writes it, and the hex form round-trips', async () => {
    const digest = await reprDigestOf(new Blob(['hello']));
    expect(digest).toBe('sha-256=:LPJNul+wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ=:');
    expect(hexOfReprDigest(digest!)).toBe(
      '2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824',
    );
  });

  it('R-UI-012 a file above the hashing limit is not hashed in the browser', async () => {
    const big = new Blob(['x']);
    Object.defineProperty(big, 'size', { value: HASH_LIMIT_BYTES + 1 });
    expect(await reprDigestOf(big)).toBeUndefined();
  });

  it('R-GENERIC-002 every path segment is percent-encoded and the slashes stay', () => {
    expect(encodePath('@team-a/ui/-/ui 1.0.0.tgz')).toBe('%40team-a/ui/-/ui%201.0.0.tgz');
  });

  it('R-API-025 Q-UI-106 the upload goes to the content route of the repository, on the API origin, segment by segment', () => {
    expect(contentPath('web/generic-local', 'releases/1.0/app.zip')).toBe(
      '/api/v1/projects/web/repositories/generic-local/content/releases/1.0/app.zip',
    );
    expect(contentPath('web/my repo', '@team-a/ui 1.0.tgz')).toBe(
      '/api/v1/projects/web/repositories/my%20repo/content/%40team-a/ui%201.0.tgz',
    );
  });

  it('R-EP-017 a client address template of the raw files family is recognised', () => {
    expect(isFilesAddress('/files/{key}/')).toBe(true);
    expect(isFilesAddress('/api/npm/{key}/')).toBe(false);
  });
});

describe('The upload (R-API-025, Q-UI-111)', () => {
  type Put = (options: object, url: string, body: Blob, headers: Record<string, string>) => Promise<unknown>;

  function setup(answer: (url: string) => Promise<unknown>) {
    TestBed.resetTestingModule();
    TestBed.configureTestingModule({
      providers: [
        provideHttpClient(),
        { provide: SessionStore, useValue: { markEnded: () => undefined } },
      ],
    });
    const api = TestBed.runInInjectionContext(() => new HttpFilesApi());
    const put = vi.fn<Put>((_options, url) => answer(url));
    (api as unknown as { put: Put }).put = put;
    return { api, put };
  }

  const file = new File(['x'], 'app.zip', { type: 'application/zip' });
  const request = { repository: 'web/generic-local', path: 'rel/app.zip', file, overwrite: false };
  const content = '/api/v1/projects/web/repositories/generic-local/content/rel/app.zip';

  it('R-API-025 the upload goes to the content route of the project and carries the headers', async () => {
    const { api, put } = setup(async () => ({ path: 'rel/app.zip' }));
    await expect(api.upload(request)).resolves.toEqual({ path: 'rel/app.zip' });
    expect(put.mock.calls.map((c) => c[1])).toEqual([content]);
    expect(put.mock.calls[0]![3]['If-None-Match']).toBe('*');
  });

  it.each([
    [404, 'repository_not_found'],
    [403, 'not_admitted'],
    [401, 'unauthenticated'],
    [409, 'conflict'],
    [413, 'payload_too_large'],
    [404, 'not_found_other'],
  ])('a %s %s on the content route is the answer: no second request', async (status, code) => {
    const { api, put } = setup(async () => {
      throw new ApiError(status, { code }, null);
    });
    await expect(api.upload(request)).rejects.toMatchObject({ status, problem: { code } });
    expect(put).toHaveBeenCalledTimes(1);
  });
});
