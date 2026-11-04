import { EncryptionStatus, Replica, SystemInfo } from '../../api/system-api';

/**
 * Pure derivations of the system status screen (R-UI-038, Q-UI-7). They read the registry as the
 * API reports it and add no server knowledge of their own, so they are testable without a
 * component.
 */

export type ServiceState = 'ready' | 'degraded' | 'unready';

export type RoleWarning =
  | 'fewer-than-two-ready'
  | 'one-node'
  | 'no-live-worker'
  | 'no-live-packages'
  | 'no-live-pool';

/** What a group states about its replicas and about a draining node (INV-12, R-HA-059). */
export interface GroupHealth {
  readonly replicas: readonly Replica[];
  readonly live: number;
  readonly ready: number;
  readonly degraded: number;
  /** Distinct nodes of the ready replicas that are not lost. */
  readonly readyNodes: readonly string[];
  readonly warnings: readonly RoleWarning[];
  /** Draining any one node leaves a ready replica of this group (INV-12, R-HA-059). */
  readonly drainSafe: boolean;
}

/** The replicas of one pool of a role (R-HA-062, R-EP-008); `pool` is null for the role's main Deployment. */
export interface PoolGroup extends GroupHealth {
  readonly pool: string | null;
}

/** One role of the registry: its counts as `/system/info` reports them, and its replicas by pool (Q-UI-107). */
export interface RoleGroup extends GroupHealth {
  /** The exact role of the registry (`roleName`): api, packages, worker, web (the upgrade bridge), all. */
  readonly role: string;
  /** One sub-group per pool, the role's main Deployment first; the pools of a role count in the role. */
  readonly pools: readonly PoolGroup[];
}

/** The roles every installation has after the cut, in the order the screen shows them (R-UI-038, Q-UI-107). */
const MAIN_ROLES = ['api', 'packages', 'worker'] as const;
/** Roles that have a group only while a replica reports them: the upgrade bridge and the development role. */
const OPTIONAL_ROLES = ['web', 'all'] as const;

/** The exact role of a replica; `roles` holds the closed class of the released contract (Q-API-194). */
export function roleOf(replica: Replica): string {
  return replica.roleName || replica.roles[0] || '';
}

interface Counts {
  readonly live: number;
  readonly ready: number;
  readonly degraded: number;
}

/** Counts, nodes and the two drain warnings of a set of replicas; `counts` replaces the derived ones. */
function health(members: readonly Replica[], counts?: Counts): GroupHealth {
  const ready = members.filter((r) => r.state === 'ready' && !r.lost);
  const nodes = ready.map((r) => r.node);
  const allReportNode = nodes.every((n) => n !== null);
  const distinct = new Set(nodes.filter((n): n is string => n !== null));
  const warnings: RoleWarning[] = [];
  if (ready.length < 2) warnings.push('fewer-than-two-ready');
  else if (allReportNode && distinct.size < 2) warnings.push('one-node');
  return {
    replicas: members,
    live: counts?.live ?? members.filter((r) => !r.lost).length,
    ready: counts?.ready ?? ready.length,
    degraded: counts?.degraded ?? members.filter((r) => r.degraded.length > 0).length,
    readyNodes: [...distinct],
    warnings,
    drainSafe: warnings.length === 0,
  };
}

/**
 * One group per role in the order api, packages, worker (Q-UI-107), then `web` and `all` while a replica of that
 * role is listed; within a group one sub-group per pool, the role's main Deployment first, the pools by name. The
 * header counts of a role are `replicas.roles` of `/system/info`. The drain warnings and the drain safety are
 * decided per sub-group, and a role is drain-safe when each of its sub-groups is. The group of `worker` warns while no
 * replica is live, the group of `packages` likewise, and a pool whose replicas are all lost warns by its name: the
 * registry does not say which queues a pool claims.
 */
export function roleGroups(info: SystemInfo, replicas: readonly Replica[]): RoleGroup[] {
  const present = new Set(replicas.map(roleOf));
  const roles: string[] = [...MAIN_ROLES, ...OPTIONAL_ROLES.filter((r) => present.has(r))];
  // A role this screen does not know (the role value is open) is shown after the known ones, not hidden.
  const known = new Set<string>([...MAIN_ROLES, ...OPTIONAL_ROLES]);
  for (const r of [...present].sort()) if (r && !known.has(r)) roles.push(r);
  return roles.map((role) => {
    const members = replicas.filter((r) => roleOf(r) === role);
    const names = [...new Set(members.map((r) => r.pool ?? null))]
      .filter((p): p is string => p !== null)
      .sort();
    const pools: PoolGroup[] = [null, ...names].map((pool) => {
      const own = health(members.filter((r) => (r.pool ?? null) === pool));
      const warnings = [...own.warnings];
      if (pool !== null && own.live === 0) warnings.push('no-live-pool');
      return { ...own, pool, warnings };
    });
    const own = health(members, info.replicas.roles[role]);
    const warnings: RoleWarning[] = [];
    if (own.live === 0 && role === 'worker') warnings.push('no-live-worker');
    if (own.live === 0 && role === 'packages') warnings.push('no-live-packages');
    return { ...own, role, pools, warnings, drainSafe: pools.every((p) => p.drainSafe) };
  });
}

/** Gating checks that fail on a replica: it is `unready` (shown as an error). */
export function failingChecks(replica: Replica): string[] {
  return Object.entries(replica.checks)
    .filter(([, c]) => c.status === 'fail')
    .map(([name]) => name);
}

/** Reported checks that fail on a ready replica: `objectStore`, `valkey` (shown as a warning). */
export function degradedChecks(replica: Replica): string[] {
  const named = Object.entries(replica.checks)
    .filter(([, c]) => c.status === 'degraded')
    .map(([name]) => name);
  return [...new Set([...replica.degraded, ...named])];
}

/** The summary in the page header: "all ready", "degraded" or "replicas unready". */
export function serviceState(replicas: readonly Replica[]): ServiceState {
  if (replicas.some((r) => r.lost || r.state !== 'ready')) return 'unready';
  if (replicas.some((r) => degradedChecks(r).length > 0)) return 'degraded';
  return 'ready';
}

export interface ValkeySummary {
  /** `k` of `n` replicas degraded; 0 means available on every replica. */
  readonly degradedReplicas: number;
  readonly liveReplicas: number;
  readonly reasons: readonly string[];
  /** Replicas that are `available` report different primaries. */
  readonly primariesDisagree: boolean;
  /** A replica sees fewer than a majority of its configured Sentinels. */
  readonly sentinelsUnreachable: boolean;
  readonly lastPrimaryChangeAt: string | null;
  readonly serverVersions: readonly string[];
}

/** Majority of `n` Sentinels. */
export function sentinelMajority(n: number): number {
  return Math.floor(n / 2) + 1;
}

export function valkeySummary(info: SystemInfo, replicas: readonly Replica[]): ValkeySummary {
  const views = replicas.flatMap((r) => (r.valkey ? [r.valkey] : []));
  const available = views.filter((v) => v.state === 'available');
  const primaries = new Set(available.map((v) => v.primary).filter((p) => p !== null));
  const changes = views
    .map((v) => v.lastPrimaryChangeAt)
    .filter((t): t is string => t !== null)
    .sort();
  const versions = new Set(
    views.map((v) => v.serverVersion).filter((v): v is string => v !== null),
  );
  const reasons = new Set(
    views.map((v) => v.reason).filter((r): r is string => r !== null && r !== ''),
  );
  return {
    degradedReplicas:
      info.valkey.degradedReplicas ?? views.filter((v) => v.state === 'degraded').length,
    liveReplicas: info.replicas.live,
    reasons: [...reasons],
    primariesDisagree: primaries.size > 1,
    sentinelsUnreachable: views.some(
      (v) =>
        v.sentinelsConfigured > 0 && v.sentinelsReachable < sentinelMajority(v.sentinelsConfigured),
    ),
    lastPrimaryChangeAt: changes.at(-1) ?? null,
    serverVersions: [...versions],
  };
}

/** Versions of live replicas when more than one runs side by side (INV-3): an information notice. */
export function mixedVersions(info: SystemInfo): string[] {
  const versions = Object.keys(info.replicas.versions);
  return versions.length > 1 ? versions.sort() : [];
}

export type KeyStatus = 'active' | 're-encryption-pending' | 'unused';

/** Status of a key ID of the stored-secret key ring (R-API-049, R-HA-027). */
export function keyStatus(key: { active: boolean; sealedValues: number }): KeyStatus {
  if (key.active) return 'active';
  return key.sealedValues > 0 ? 're-encryption-pending' : 'unused';
}

// >>> M1 ui-a
/** A replica whose key ring lacks key IDs that others use or that still seal values (E1.9). */
export interface RingGap {
  readonly replica: string;
  readonly role: string;
  readonly missing: readonly string[];
}

/**
 * The key IDs every live replica must hold: the active key ID of every live replica and every
 * key ID that still seals a value (R-API-049). A replica that lacks one cannot open what another
 * replica sealed. The list is derived from the same answer that carries `ringsCompatible`, so the
 * card shows which replica is the incompatible one.
 */
export function ringGaps(status: EncryptionStatus): RingGap[] {
  const needed = new Set<string>([status.activeKeyId]);
  for (const r of status.replicas) needed.add(r.activeKeyId);
  for (const k of status.keys) if (k.sealedValues > 0) needed.add(k.id);
  return status.replicas
    .map((r) => ({
      replica: r.name,
      role: r.roleName,
      missing: [...needed].filter((id) => !r.keyIds.includes(id)).sort(),
    }))
    .filter((g) => g.missing.length > 0);
}

export type RotationStep =
  | { readonly kind: 'incompatible' }
  | { readonly kind: 'reencrypt'; readonly keyIds: readonly string[] }
  | { readonly kind: 'remove'; readonly keyIds: readonly string[] }
  | { readonly kind: 'none' };

/**
 * The next step of the rotation (R-HA-027) that the state allows: make the rings equal first;
 * then re-encrypt values still sealed under an old key; then remove a key file that seals nothing.
 */
export function rotationStep(status: EncryptionStatus): RotationStep {
  if (!status.ringsCompatible) return { kind: 'incompatible' };
  const pending = status.keys
    .filter((k) => keyStatus(k) === 're-encryption-pending')
    .map((k) => k.id);
  if (pending.length > 0) return { kind: 'reencrypt', keyIds: pending };
  const unused = status.keys.filter((k) => keyStatus(k) === 'unused').map((k) => k.id);
  return unused.length > 0 ? { kind: 'remove', keyIds: unused } : { kind: 'none' };
}
// <<< M1 ui-a
