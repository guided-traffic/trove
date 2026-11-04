import { sampleInfo, sampleReplica } from '../../api/testing/fakes';
import {
  degradedChecks,
  failingChecks,
  keyStatus,
  mixedVersions,
  roleGroups,
  sentinelMajority,
  serviceState,
  valkeySummary,
} from './system-status-model';

const web = (name: string, node: string | null, over = {}) =>
  sampleReplica({ name, instanceUid: name, node, ...over });
const worker = (name: string, node: string | null, over = {}) =>
  sampleReplica({ name, instanceUid: name, node, roles: ['worker'], roleName: 'worker', ...over });
const packages = (name: string, node: string | null, over = {}) =>
  sampleReplica({ name, instanceUid: name, node, roleName: 'packages', ...over });

describe('system status model', () => {
  it('R-UI-038 a role with two ready replicas on two nodes is drain-safe', () => {
    const groups = roleGroups(sampleInfo(), [web('a', 'n1'), web('b', 'n2'), worker('w1', 'n1'), worker('w2', 'n2')]);
    expect(groups.find((g) => g.role === 'api')?.drainSafe).toBe(true);
    expect(groups.find((g) => g.role === 'api')?.warnings).toEqual([]);
    expect(groups.find((g) => g.role === 'api')?.pools[0]?.warnings).toEqual([]);
  });

  it('R-UI-038 fewer than two ready replicas is flagged and is not drain-safe', () => {
    const [apiGroup] = roleGroups(sampleInfo(), [web('a', 'n1'), web('b', 'n2', { state: 'unready' })]);
    expect(apiGroup?.pools[0]?.warnings).toContain('fewer-than-two-ready');
    expect(apiGroup?.drainSafe).toBe(false);
  });

  it('R-UI-038 all ready replicas on one node is flagged only when every replica reports its node', () => {
    const one = roleGroups(sampleInfo(), [web('a', 'n1'), web('b', 'n1')])[0];
    expect(one?.pools[0]?.warnings).toEqual(['one-node']);
    const unknown = roleGroups(sampleInfo(), [web('a', null), web('b', 'n1')])[0];
    expect(unknown?.pools[0]?.warnings).toEqual([]);
  });

  it('R-UI-038 a lost replica does not count as ready', () => {
    const g = roleGroups(sampleInfo(), [web('a', 'n1'), web('b', 'n2', { lost: true })])[0];
    expect(g?.pools[0]?.warnings).toContain('fewer-than-two-ready');
  });

  it('R-UI-038 Q-UI-107 the groups are api, packages and worker in that order, whatever the replicas are', () => {
    expect(roleGroups(sampleInfo(), []).map((g) => g.role)).toEqual(['api', 'packages', 'worker']);
    expect(roleGroups(sampleInfo(), [web('a', 'n1')]).map((g) => g.role)).toEqual(['api', 'packages', 'worker']);
  });

  it('R-UI-038 Q-UI-107 a replica lands in the group of its roleName, not of the closed role class', () => {
    const groups = roleGroups(sampleInfo(), [web('a', 'n1'), packages('p', 'n2'), worker('w', 'n3')]);
    expect(groups.map((g) => [g.role, g.replicas.map((r) => r.name)])).toEqual([
      ['api', ['a']],
      ['packages', ['p']],
      ['worker', ['w']],
    ]);
  });

  it('R-UI-038 no live worker replica is reported for the worker role, no live packages replica for packages', () => {
    const groups = roleGroups(sampleInfo(), [web('a', 'n1'), web('b', 'n2')]);
    expect(groups.find((g) => g.role === 'worker')?.warnings).toContain('no-live-worker');
    expect(groups.find((g) => g.role === 'packages')?.warnings).toContain('no-live-packages');
    expect(groups.find((g) => g.role === 'api')?.warnings).toEqual([]);
  });

  it('R-UI-038 a worker pool is a sub-group with its own counts and drain safety, and the role counts come from the info', () => {
    const info = sampleInfo({
      replicas: {
        live: 3,
        ready: 3,
        versions: { '0.1.1': 3 },
        roles: { worker: { live: 3, ready: 3, degraded: 0, nodes: 2 } },
      },
    });
    const [, , w] = roleGroups(info, [
      worker('w1', 'n1'),
      worker('w2', 'n2'),
      worker('v1', 'n1', { pool: 'vuln' }),
    ]);
    expect(w?.live).toBe(3);
    expect(w?.pools.map((p) => p.pool)).toEqual([null, 'vuln']);
    expect(w?.pools[0]).toMatchObject({ live: 2, ready: 2, drainSafe: true });
    expect(w?.pools[1]).toMatchObject({ live: 1, ready: 1, drainSafe: false });
    expect(w?.pools[1]?.warnings).toEqual(['fewer-than-two-ready']);
    expect(w?.drainSafe).toBe(false);
  });

  it('R-UI-038 a pool whose replicas are all lost is named in its warning', () => {
    const [, , w] = roleGroups(sampleInfo(), [
      worker('w1', 'n1'),
      worker('w2', 'n2'),
      worker('v1', 'n1', { pool: 'vuln', lost: true }),
    ]);
    expect(w?.pools[1]?.warnings).toContain('no-live-pool');
  });

  it('R-UI-038 Q-UI-107 the bridge role web and the development role all have a group only while a replica reports them', () => {
    expect(roleGroups(sampleInfo(), [web('a', 'n1')]).map((g) => g.role)).not.toContain('web');
    expect(roleGroups(sampleInfo(), [web('a', 'n1', { roleName: 'web' })]).map((g) => g.role)).toEqual([
      'api',
      'packages',
      'worker',
      'web',
    ]);
    expect(roleGroups(sampleInfo(), [web('a', 'n1', { roles: ['all'], roleName: 'all' })]).map((g) => g.role)).toContain('all');
  });

  it('R-UI-038 a role this screen does not know is shown after the known ones', () => {
    expect(roleGroups(sampleInfo(), [web('a', 'n1', { roleName: 'fetch' })]).map((g) => g.role).at(-1)).toBe('fetch');
  });

  it('R-UI-038 failing and degraded checks stay separate kinds', () => {
    const r = sampleReplica({
      state: 'unready',
      checks: { database: { status: 'fail' }, valkey: { status: 'degraded' } },
      degraded: ['objectStore'],
    });
    expect(failingChecks(r)).toEqual(['database']);
    expect(degradedChecks(r).sort()).toEqual(['objectStore', 'valkey']);
  });

  it('R-UI-038 the header summary reads ready, degraded or unready', () => {
    expect(serviceState([web('a', 'n1')])).toBe('ready');
    expect(serviceState([web('a', 'n1', { degraded: ['valkey'] })])).toBe('degraded');
    expect(serviceState([web('a', 'n1'), web('b', 'n2', { state: 'unready' })])).toBe('unready');
    expect(serviceState([web('a', 'n1', { lost: true })])).toBe('unready');
  });

  it('R-UI-038 the Valkey summary counts degraded replicas, flags primary disagreement and missing Sentinel majority', () => {
    const view = (primary: string, reachable = 3) => ({
      state: 'available' as const,
      reason: null,
      circuit: 'closed',
      primary,
      sentinelsReachable: reachable,
      sentinelsConfigured: 3,
      serverVersion: '9.0.0',
      lastPrimaryChangeAt: null,
    });
    const agree = valkeySummary(sampleInfo(), [web('a', 'n1', { valkey: view('p1') }), web('b', 'n2', { valkey: view('p1') })]);
    expect(agree.primariesDisagree).toBe(false);
    expect(agree.sentinelsUnreachable).toBe(false);

    const split = valkeySummary(sampleInfo(), [web('a', 'n1', { valkey: view('p1') }), web('b', 'n2', { valkey: view('p2', 1) })]);
    expect(split.primariesDisagree).toBe(true);
    expect(split.sentinelsUnreachable).toBe(true);

    const info = sampleInfo({ valkey: { ...sampleInfo().valkey, degradedReplicas: 2 } });
    expect(valkeySummary(info, [web('a', 'n1')]).degradedReplicas).toBe(2);
  });

  it('R-UI-038 a Sentinel majority is more than half', () => {
    expect([1, 2, 3, 5].map(sentinelMajority)).toEqual([1, 2, 2, 3]);
  });

  it('R-UI-038 two live versions produce the information notice, one does not', () => {
    const one = sampleInfo();
    expect(mixedVersions(one)).toEqual([]);
    const two = sampleInfo({ replicas: { ...one.replicas, versions: { '0.1.1': 1, '0.1.0': 1 } } });
    expect(mixedVersions(two)).toEqual(['0.1.0', '0.1.1']);
  });

  it('R-UI-038 a key that is not active but still seals values is re-encryption pending', () => {
    expect(keyStatus({ active: true, sealedValues: 4 })).toBe('active');
    expect(keyStatus({ active: false, sealedValues: 3 })).toBe('re-encryption-pending');
    expect(keyStatus({ active: false, sealedValues: 0 })).toBe('unused');
  });
});
