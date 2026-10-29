import {
  HASH_LIMIT_BYTES,
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

  it('R-EP-017 a client address template of the raw files family is recognised', () => {
    expect(isFilesAddress('/files/{key}/')).toBe(true);
    expect(isFilesAddress('/api/npm/{key}/')).toBe(false);
  });
});
