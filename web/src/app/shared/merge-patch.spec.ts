import { applyMergePatch, mergePatchOf } from './merge-patch';

describe('merge patch (RFC 7396, R-API-013)', () => {
  it('R-UI-021 the patch holds only the members that changed; arrays are replaced as a whole', () => {
    const before = { description: 'a', includePatterns: ['x'], nested: { keep: 1, change: 2 } };
    const after = { description: 'a', includePatterns: ['x', 'y'], nested: { keep: 1, change: 3 } };
    expect(mergePatchOf(before, after)).toEqual({
      includePatterns: ['x', 'y'],
      nested: { change: 3 },
    });
  });

  it('R-UI-021 nothing changed is an empty patch, a removed member becomes null', () => {
    expect(mergePatchOf({ a: 1 }, { a: 1 })).toEqual({});
    expect(mergePatchOf({ a: 1, b: 2 }, { a: 1 })).toEqual({ b: null });
  });

  it('R-UI-021 applying the patch to the before document gives the after document', () => {
    const before = { a: 1, b: { c: 2, d: 3 }, e: [1] };
    const after = { a: 2, b: { c: 2 }, e: [1, 2], f: 'new' };
    expect(applyMergePatch(before, mergePatchOf(before, after))).toEqual(after);
  });
});
