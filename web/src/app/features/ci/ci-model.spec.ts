import { describe, expect, it } from 'vitest';
import { CiCondition, CiMapping } from '../../api/ci-api';
import { sampleMapping } from '../../api/testing/fakes-ci';
import {
  claimsSatisfying,
  conditionFromNode,
  depthOf,
  editAt,
  emptyMappingForm,
  formFromMapping,
  inputFromForm,
  leafCount,
  newGroup,
  newLeaf,
  nextPriority,
  nodeAt,
  nodeFromCondition,
  parseClaimsInput,
  pinsProject,
  requiresProtectedRef,
  sampleClaims,
  summarizeCondition,
  summarizeGrants,
  unprotectedWrite,
  withProtectedRef,
} from './ci-model';

const b64url = (v: unknown): string =>
  btoa(String.fromCharCode(...new TextEncoder().encode(JSON.stringify(v))))
    .replace(/\+/g, '-')
    .replace(/\//g, '_')
    .replace(/=+$/, '');
const jwt = (claims: unknown): string => `${b64url({ alg: 'RS256', typ: 'JWT' })}.${b64url(claims)}.c2lnbmF0dXJl`;

describe('The condition model (R-UI-035, R-CI-010)', () => {
  const tree: CiCondition = {
    all: [
      { claim: 'namespace_id', in: ['4711', '4712'] },
      { claim: 'ref_protected', equals: true },
      { any: [{ claim: 'pipeline_source', equals: 'push' }, { claim: 'pipeline_source', equals: 'web' }] },
      { not: { claim: 'environment_action', in: ['prepare', 'verify'] } },
      { claim: 'project_path', glob: 'acme/**', ignoreCase: true },
      { claim: 'ref', regex: 'v[0-9]+' },
      { claim: 'environment', present: false },
      { path: ['job_config', 'url'], equals: 'x' },
    ],
  };

  it('R-UI-035 a condition survives the trip through the builder model unchanged', () => {
    expect(conditionFromNode(nodeFromCondition(tree))).toEqual(tree);
  });

  it('R-UI-035 the summary reads like the spec examples', () => {
    expect(summarizeCondition({ claim: 'namespace_id', in: ['4711'] })).toBe('namespace_id in [4711]');
    expect(summarizeCondition({ claim: 'ref_protected', equals: true })).toBe('ref_protected = true');
    const s = summarizeCondition(tree);
    expect(s).toContain('namespace_id in [4711, 4712]');
    expect(s).toContain('(pipeline_source = push or pipeline_source = web)');
    expect(s).toContain('not environment_action in [prepare, verify]');
    expect(s).toContain('job_config.url = x');
  });

  it('R-UI-035 a boolean claim is written as a boolean, other claims keep their text', () => {
    const c = conditionFromNode(newGroup('all', [newLeaf('ref_protected', { value: 'true' }), newLeaf('ref', { value: 'true' })]));
    expect(c).toEqual({ all: [{ claim: 'ref_protected', equals: true }, { claim: 'ref', equals: 'true' }] });
  });

  it('R-UI-035 an in list takes lines and commas and drops empty entries', () => {
    expect(conditionFromNode(newLeaf('namespace_id', { operator: 'in', value: '4711, 4712\n\n4713' }))).toEqual({
      claim: 'namespace_id',
      in: ['4711', '4712', '4713'],
    });
  });

  it('R-UI-035 edits by path change one node, remove another and keep the rest', () => {
    const root = nodeFromCondition({ all: [{ claim: 'a', equals: 'x' }, { any: [{ claim: 'b', equals: 'y' }] }] });
    const changed = editAt(root, [1, 0], (n) => ({ ...n, claim: 'c' }) as typeof n)!;
    expect(conditionFromNode(changed)).toEqual({ all: [{ claim: 'a', equals: 'x' }, { any: [{ claim: 'c', equals: 'y' }] }] });
    const removed = editAt(root, [0], () => null)!;
    expect(conditionFromNode(removed)).toEqual({ all: [{ any: [{ claim: 'b', equals: 'y' }] }] });
    expect(nodeAt(root, [1, 0])?.kind).toBe('leaf');
    expect(depthOf(root)).toBe(3);
    expect(leafCount(root)).toBe(2);
  });
});

describe('The rules the editor mirrors (R-CI-011, R-CI-010)', () => {
  it('R-CI-011 a writing grant without ref_protected = true is flagged, the option or the leaf clears it', () => {
    const m = sampleMapping({ match: { all: [{ claim: 'namespace_id', in: ['4711'] }] } });
    const form = formFromMapping(m);
    expect(unprotectedWrite(form)).toBe(true);
    expect(unprotectedWrite({ ...form, allowUnprotectedRefs: true })).toBe(false);
    const fixed = { ...form, match: withProtectedRef(form.match) };
    expect(requiresProtectedRef(fixed.match)).toBe(true);
    expect(unprotectedWrite(fixed)).toBe(false);
  });

  it('R-CI-011 a read-only mapping needs no protected ref', () => {
    const m: CiMapping = sampleMapping({ match: { all: [{ claim: 'namespace_id', in: ['1'] }] }, grants: { policies: ['p'] } });
    expect(unprotectedWrite(formFromMapping(m))).toBe(false);
  });

  it('R-CI-010 a project is pinned by an equals or in leaf of an ID claim in the top-level all', () => {
    expect(pinsProject(nodeFromCondition({ all: [{ claim: 'project_id', equals: '4242' }] }))).toBe(true);
    expect(pinsProject(nodeFromCondition({ all: [{ claim: 'project_path', equals: 'a/b' }] }))).toBe(false);
    expect(pinsProject(nodeFromCondition({ any: [{ claim: 'project_id', equals: '4242' }] }))).toBe(false);
  });

  it('R-CI-009 the next priority is the next multiple of ten', () => {
    expect(nextPriority([])).toBe(10);
    expect(nextPriority([{ priority: 10 }, { priority: 100 }])).toBe(110);
    expect(nextPriority([{ priority: 105 }])).toBe(110);
  });

  it('R-CI-013 a project mapping sends no groups and the write body carries the resolved options', () => {
    const form = { ...emptyMappingForm(10), groups: ['g'], policies: ['web-read'], name: 'web-x' };
    expect(inputFromForm(form, 'web').grants.groups).toBeUndefined();
    expect(inputFromForm(form, null).grants.groups).toEqual(['g']);
    expect(inputFromForm(form, null).options).toEqual({ crossProjectSource: 'deny', allowUnprotectedRefs: false });
    expect(inputFromForm(form, null).subjectKinds).toEqual(['id_token']);
  });

  it('R-UI-035 the grants summary names policies, groups and inline grants', () => {
    const lines = summarizeGrants(sampleMapping().grants);
    expect(lines[0]).toContain('ci-read-internal');
    expect(lines[1]).toBe('read, deploy: npm-internal · @team-a/*');
  });
});

describe('The claim input of the tester (R-UI-036)', () => {
  const now = 1_800_000_000;

  it('R-UI-036 a JSON object is taken as claims', () => {
    expect(parseClaimsInput('{"ref":"main"}', now)).toEqual({ kind: 'claims', claims: { ref: 'main' } });
  });

  it('R-UI-036 a compact token is decoded to its claims, header and payload only, and says whether exp lies ahead', () => {
    const live = parseClaimsInput(jwt({ ref: 'main', exp: now + 60 }), now);
    expect(live).toEqual({ kind: 'jwt', claims: { ref: 'main', exp: now + 60 }, stillValid: true });
    const dead = parseClaimsInput(jwt({ ref: 'main', exp: now - 60 }), now);
    expect(dead).toMatchObject({ kind: 'jwt', stillValid: false });
    const none = parseClaimsInput(jwt({ ref: 'main' }), now);
    expect(none).toMatchObject({ kind: 'jwt', stillValid: false });
  });

  it('R-UI-036 the claims of a token keep their unicode text', () => {
    expect(parseClaimsInput(jwt({ user_login: 'ünïcode' }), now)).toMatchObject({ claims: { user_login: 'ünïcode' } });
  });

  it('R-UI-036 other input is refused with a reason', () => {
    expect(parseClaimsInput('  ', now)).toEqual({ kind: 'error', reason: 'empty' });
    expect(parseClaimsInput('not json', now)).toEqual({ kind: 'error', reason: 'not-json' });
    expect(parseClaimsInput('[1]', now)).toEqual({ kind: 'error', reason: 'not-object' });
    expect(parseClaimsInput('aaa.bbb.ccc', now)).toEqual({ kind: 'error', reason: 'bad-token' });
  });

  it('R-UI-036 the sample has the claims of a GitLab ID token and starts out matching the mapping it is opened from', () => {
    const base = sampleClaims('https://gitlab.example.com', 'https://trove.example.com', now);
    for (const claim of ['iss', 'aud', 'sub', 'namespace_id', 'namespace_path', 'project_id', 'project_path', 'ref', 'ref_type', 'ref_protected', 'pipeline_source', 'environment', 'user_login', 'job_id', 'iat', 'nbf', 'exp']) {
      expect(base, claim).toHaveProperty(claim);
    }
    const tuned = claimsSatisfying(base, { all: [{ claim: 'namespace_id', in: ['9000'] }, { claim: 'ref_protected', equals: true }, { claim: 'ref', glob: 'v*' }] });
    expect(tuned['namespace_id']).toBe('9000');
    expect(tuned['ref_protected']).toBe('true');
    expect(tuned['ref']).toBe('main');
  });
});
