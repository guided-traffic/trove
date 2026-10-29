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
  sampleReplica({ name, instanceUid: name, node, roles: ['worker'], ...over });

describe('system status model', () => {
  it('R-UI-038 a role with two ready replicas on two nodes is drain-safe', () => {
    const groups = roleGroups(sampleInfo(), [web('a', 'n1'), web('b', 'n2'), worker('w1', 'n1'), worker('w2', 'n2')]);
    expect(groups.find((g) => g.role === 'web')?.drainSafe).toBe(true);
    expect(groups.find((g) => g.role === 'web')?.warnings).toEqual([]);
  });

  it('R-UI-038 fewer than two ready replicas is flagged and is not drain-safe', () => {
    const [webGroup] = roleGroups(sampleInfo(), [web('a', 'n1'), web('b', 'n2', { state: 'unready' })]);
    expect(webGroup?.warnings).toContain('fewer-than-two-ready');
    expect(webGroup?.drainSafe).toBe(false);
  });

  it('R-UI-038 all ready replicas on one node is flagged only when every replica reports its node', () => {
    const one = roleGroups(sampleInfo(), [web('a', 'n1'), web('b', 'n1')])[0];
    expect(one?.warnings).toEqual(['one-node']);
    const unknown = roleGroups(sampleInfo(), [web('a', null), web('b', 'n1')])[0];
    expect(unknown?.warnings).toEqual([]);
  });

  it('R-UI-038 a lost replica does not count as ready', () => {
    const g = roleGroups(sampleInfo(), [web('a', 'n1'), web('b', 'n2', { lost: true })])[0];
    expect(g?.warnings).toContain('fewer-than-two-ready');
  });

  it('R-UI-038 no live worker replica is reported for the worker role', () => {
    const groups = roleGroups(sampleInfo(), [web('a', 'n1'), web('b', 'n2')]);
    expect(groups.find((g) => g.role === 'worker')?.warnings).toContain('no-live-worker');
  });

  it('R-UI-038 the all role appears only for a replica that reports it', () => {
    expect(roleGroups(sampleInfo(), [web('a', 'n1')]).map((g) => g.role)).toEqual(['web', 'worker']);
    expect(roleGroups(sampleInfo(), [web('a', 'n1', { roles: ['all'] })]).map((g) => g.role)).toContain('all');
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
