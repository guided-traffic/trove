import { Replica } from '../../api/system-api';
import { RoleGroup } from './system-status-model';

/**
 * Pure derivations of the admin dashboard (Q-OPS-175): the load of each role from the heartbeat measurements of its
 * replicas, and the duration of the checks against the dependencies. Lost replicas are left out: their last snapshot
 * describes a process that no longer answers.
 */

/** The load of one role, summed or taken at the worst over the replicas that reported a snapshot. */
export interface RoleLoad {
  readonly role: string;
  readonly live: number;
  readonly ready: number;
  readonly degraded: number;
  readonly nodes: number;
  /** Replicas of the role whose snapshot is in; the figures below are theirs. */
  readonly reporting: number;
  readonly requestsPerSecond: number;
  readonly errorsPerSecond: number;
  /** The worst p95 of the replicas: the one a client may hit. */
  readonly p95Ms: number | null;
  readonly inFlight: number;
  readonly cpuCores: number;
  /** Null when a replica does not know its limit, because a sum would understate it. */
  readonly cpuLimitCores: number | null;
  readonly memoryBytes: number;
  /** Null when a replica runs without a known limit. */
  readonly memoryLimitBytes: number | null;
}

export function roleLoad(group: RoleGroup): RoleLoad {
  const snapshots = group.replicas.filter((r) => !r.lost).flatMap((r) => (r.runtime ? [r.runtime] : []));
  const sum = (f: (s: (typeof snapshots)[number]) => number) => snapshots.reduce((a, s) => a + f(s), 0);
  const p95s = snapshots.flatMap((s) => (s.latencyMs.p95 === null ? [] : [s.latencyMs.p95]));
  const knownCpu = snapshots.every((s) => s.cpuLimitCores !== null);
  const knownMemory = snapshots.every((s) => s.memoryLimitBytes > 0);
  return {
    role: group.role,
    live: group.live,
    ready: group.ready,
    degraded: group.degraded,
    nodes: group.readyNodes.length,
    reporting: snapshots.length,
    requestsPerSecond: sum((s) => (s.windowSeconds > 0 ? s.requests / s.windowSeconds : 0)),
    errorsPerSecond: sum((s) => (s.windowSeconds > 0 ? s.errors / s.windowSeconds : 0)),
    p95Ms: p95s.length > 0 ? Math.max(...p95s) : null,
    inFlight: sum((s) => s.inFlight),
    cpuCores: sum((s) => s.cpuCores),
    cpuLimitCores: snapshots.length > 0 && knownCpu ? sum((s) => s.cpuLimitCores ?? 0) : null,
    memoryBytes: sum((s) => s.memoryBytes),
    memoryLimitBytes: snapshots.length > 0 && knownMemory ? sum((s) => s.memoryLimitBytes) : null,
  };
}

/** One dependency's readiness check over the live replicas: how many pass and how long the slowest run took. */
export interface CheckSummary {
  readonly check: string;
  /** Live replicas that report the check at all. */
  readonly reporting: number;
  readonly passing: number;
  readonly failing: number;
  readonly maxMs: number | null;
}

export function checkSummary(replicas: readonly Replica[], check: string): CheckSummary {
  const results = replicas.filter((r) => !r.lost).flatMap((r) => {
    const c = r.checks[check];
    return c ? [c] : [];
  });
  const durations = results.flatMap((c) => (c.durationMs === undefined ? [] : [c.durationMs]));
  return {
    check,
    reporting: results.length,
    passing: results.filter((c) => c.status === 'pass').length,
    failing: results.filter((c) => c.status !== 'pass').length,
    maxMs: durations.length > 0 ? Math.max(...durations) : null,
  };
}

/** A share between 0 and 1, or null when the whole is unknown or zero. */
export function share(part: number, whole: number | null): number | null {
  return whole && whole > 0 ? Math.min(1, Math.max(0, part / whole)) : null;
}
