import { matchName, matchPath, verdict } from './glob';
import { diffPolicy, expandActions, formFromPolicy, inputFromForm, newRow } from './policy-model';
import { samplePolicy } from '../../api/testing/access-fakes';

describe('pattern syntax (R-AUTHZ-005)', () => {
  it('R-AUTHZ-005 names: * stops at /, ** crosses it, ? is one character', () => {
    expect(matchName('@acme/*', '@acme/ui')).toBe(true);
    expect(matchName('@acme/*', '@acme-tools/ui')).toBe(false);
    expect(matchName('*', '@acme/ui')).toBe(false);
    expect(matchName('**', '@acme/ui')).toBe(true);
    expect(matchName('ui-?', 'ui-1')).toBe(true);
    expect(matchName('ui-?', 'ui-12')).toBe(false);
    expect(matchName('a\\*b', 'a*b')).toBe(true);
    expect(matchName('a\\*b', 'axb')).toBe(false);
    expect(matchName('bad\\', 'bad')).toBeNull();
  });

  it('R-AUTHZ-005 paths: ** is a whole segment of any number of segments, zero included', () => {
    expect(matchPath('dist/**', 'dist')).toBe(true);
    expect(matchPath('dist/**', 'dist/a/b.tgz')).toBe(true);
    expect(matchPath('dist/*', 'dist/a/b.tgz')).toBe(false);
    expect(matchPath('**/*.tgz', 'a/b/c.tgz')).toBe(true);
    expect(matchPath('/abs', 'abs')).toBeNull();
    expect(matchPath('a//b', 'a/b')).toBeNull();
    expect(matchPath('a**b', 'ab')).toBeNull();
  });

  it('R-AUTHZ-005 exclude beats include and an empty include list is unrestricted', () => {
    expect(verdict('packages', [], [], '@a/b')).toEqual({ kind: 'unrestricted' });
    expect(verdict('packages', ['@a/*'], ['@a/secret-*'], '@a/secret-keys')).toEqual({
      kind: 'excluded',
      pattern: '@a/secret-*',
    });
    expect(verdict('packages', ['@a/*'], [], '@a/b')).toEqual({ kind: 'included', pattern: '@a/*' });
    expect(verdict('packages', ['@a/*'], [], '@b/b')).toEqual({ kind: 'not-included' });
    expect(verdict('packages', ['bad\\'], [], 'x')).toEqual({ kind: 'invalid', pattern: 'bad\\' });
  });
});

describe('policy form and diff (R-UI-027)', () => {
  const stored = samplePolicy();
  const before = inputFromForm(formFromPolicy(stored), false);

  it('R-UI-027 round-trips a policy through the form unchanged', () => {
    expect(diffPolicy(before, inputFromForm(formFromPolicy(stored), false))).toEqual([]);
  });

  it('R-UI-027 the diff lists exactly the added principal when only one was added', () => {
    const form = formFromPolicy(stored);
    const after = inputFromForm({ ...form, entries: [...form.entries, newRow('user:bob', ['read'])] }, false);
    const diff = diffPolicy(before, after);
    expect(diff).toHaveLength(1);
    expect(diff[0]?.change).toBe('added');
    expect(diff[0]?.text).toContain('user:bob');
  });

  it('R-UI-027 lists removed principals, changed actions and changed resources', () => {
    const form = formFromPolicy(stored);
    const after = inputFromForm(
      {
        ...form,
        repositories: 'npm-internal\nnpm-extra',
        packagesInclude: '@team-web/*\n@other/*',
        entries: [newRow('group:team-web', ['read', 'deploy', 'delete'])],
      },
      false,
    );
    const texts = diffPolicy(before, after).map((l) => `${l.change}:${l.text}`);
    expect(texts.some((t) => t.startsWith('added:') && t.includes('delete'))).toBe(true);
    expect(texts.some((t) => t.startsWith('removed:') && t.includes('annotate'))).toBe(true);
    expect(texts.some((t) => t.includes('npm-extra'))).toBe(true);
    expect(texts.some((t) => t.includes('@other/*'))).toBe(true);
  });

  it('R-AUTHZ-001 a preset that expands to the stored actions is no change', () => {
    const form = formFromPolicy(stored);
    const after = inputFromForm({ ...form, entries: [newRow('group:team-web', ['write'])] }, false);
    expect(expandActions(['write'])).toEqual(['read', 'annotate', 'deploy']);
    expect(diffPolicy(before, after)).toEqual([]);
  });

  it('R-API-031 sends the name on create only', () => {
    const form = { ...formFromPolicy(stored), name: ' team-x ' };
    expect(inputFromForm(form, true).name).toBe('team-x');
    expect('name' in inputFromForm(form, false)).toBe(false);
  });
});
