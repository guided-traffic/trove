import { sampleInfo, sampleReplica, sampleRuntime } from '../../api/testing/fakes';
import { checkSummary, roleLoad, share } from './dashboard-model';
import { roleGroups } from './system-status-model';

describe('Admin dashboard model (Q-OPS-175)', () => {
  it('R-UI-038 sums a role\'s load over its live replicas and takes the worst p95', () => {
    const replicas = [
      sampleReplica({ name: 'a', instanceUid: 'a', runtime: sampleRuntime({ requests: 100, errors: 5, windowSeconds: 10, latencyMs: { p50: 2, p95: 20, p99: 30 }, cpuCores: 0.5, inFlight: 2 }) }),
      sampleReplica({ name: 'b', instanceUid: 'b', runtime: sampleRuntime({ requests: 50, errors: 0, windowSeconds: 10, latencyMs: { p50: 3, p95: 45, p99: 90 }, cpuCores: 0.25, inFlight: 1 }) }),
      // A lost replica's last snapshot describes a process that no longer answers.
      sampleReplica({ name: 'c', instanceUid: 'c', lost: true, runtime: sampleRuntime({ requests: 9999, cpuCores: 9 }) }),
    ];
    const [api] = roleGroups(sampleInfo(), replicas).map(roleLoad);
    expect(api?.reporting).toBe(2);
    expect(api?.requestsPerSecond).toBe(15);
    expect(api?.errorsPerSecond).toBe(0.5);
    expect(api?.p95Ms).toBe(45);
    expect(api?.inFlight).toBe(3);
    expect(api?.cpuCores).toBe(0.75);
    expect(api?.cpuLimitCores).toBe(4);
  });

  it('R-UI-038 leaves a limit unknown when one replica does not know its own, and has no p95 without requests', () => {
    const replicas = [
      sampleReplica({ name: 'a', instanceUid: 'a', runtime: sampleRuntime({ cpuLimitCores: null, memoryLimitBytes: 0, latencyMs: { p50: null, p95: null, p99: null } }) }),
      sampleReplica({ name: 'b', instanceUid: 'b', runtime: sampleRuntime({ latencyMs: { p50: null, p95: null, p99: null } }) }),
    ];
    const [api] = roleGroups(sampleInfo(), replicas).map(roleLoad);
    expect(api?.cpuLimitCores).toBeNull();
    expect(api?.memoryLimitBytes).toBeNull();
    expect(api?.p95Ms).toBeNull();
  });

  it('R-UI-038 a replica without a snapshot counts as live but not as reporting', () => {
    const [api] = roleGroups(sampleInfo(), [sampleReplica({ runtime: null })]).map(roleLoad);
    expect(api?.live).toBeGreaterThan(0);
    expect(api?.reporting).toBe(0);
    expect(api?.cpuLimitCores).toBeNull();
  });

  it('R-OPS-003 summarises a dependency check over the live replicas with its slowest run', () => {
    const replicas = [
      sampleReplica({ name: 'a', instanceUid: 'a', checks: { database: { status: 'pass', durationMs: 1.5 } } }),
      sampleReplica({ name: 'b', instanceUid: 'b', checks: { database: { status: 'fail', reason: 'timeout', durationMs: 2000 } } }),
      sampleReplica({ name: 'c', instanceUid: 'c', checks: { database: { status: 'pass' } } }),
    ];
    expect(checkSummary(replicas, 'database')).toEqual({ check: 'database', reporting: 3, passing: 2, failing: 1, maxMs: 2000 });
    expect(checkSummary(replicas, 'valkey')).toEqual({ check: 'valkey', reporting: 0, passing: 0, failing: 0, maxMs: null });
  });

  it('R-UI-038 a share is bounded and needs a known whole', () => {
    expect(share(3, 4)).toBe(0.75);
    expect(share(5, 4)).toBe(1);
    expect(share(1, 0)).toBeNull();
    expect(share(1, null)).toBeNull();
  });
});
