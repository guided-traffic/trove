import { EncryptionStatus, Replica, SystemInfo } from '../../api/system-api';

/**
 * Pure derivations of the system status screen (R-UI-038, Q-UI-7). They read the registry as the
 * API reports it and add no server knowledge of their own, so they are testable without a
 * component.
 */

export type ServiceState = 'ready' | 'degraded' | 'unready';

export type RoleWarning = 'fewer-than-two-ready' | 'one-node' | 'no-live-worker';

export interface RoleGroup {
  readonly role: 'web' | 'worker' | 'all';
  readonly replicas: readonly Replica[];
  /** Live replicas of the role as `/system/info` counts them. */
  readonly live: number;
  readonly ready: number;
  readonly degraded: number;
  /** Ready replicas that are not lost. */
  readonly readyNodes: readonly string[];
  readonly warnings: readonly RoleWarning[];
  /** Draining any one node leaves a ready replica of this role (INV-12, R-HA-059). */
  readonly drainSafe: boolean;
}

const ROLES = ['web', 'worker'] as const;

/** One group per role; `all` appears only when a replica reports it (the development role). */
export function roleGroups(info: SystemInfo, replicas: readonly Replica[]): RoleGroup[] {
  const roles: ('web' | 'worker' | 'all')[] = [...ROLES];
  if (replicas.some((r) => r.roles.includes('all'))) roles.push('all');
  return roles.map((role) => {
    const members = replicas.filter((r) => r.roles.includes(role));
    const summary = info.replicas.roles[role];
    const ready = members.filter((r) => r.state === 'ready' && !r.lost);
    const nodes = ready.map((r) => r.node);
    const allReportNode = nodes.every((n) => n !== null);
    const distinct = new Set(nodes.filter((n): n is string => n !== null));
    const warnings: RoleWarning[] = [];
    if (ready.length < 2) warnings.push('fewer-than-two-ready');
    else if (allReportNode && distinct.size < 2) warnings.push('one-node');
    const live = summary?.live ?? members.filter((r) => !r.lost).length;
    if (role === 'worker' && live === 0) warnings.push('no-live-worker');
    return {
      role,
      replicas: members,
      live,
      ready: summary?.ready ?? ready.length,
      degraded: summary?.degraded ?? members.filter((r) => r.degraded.length > 0).length,
      readyNodes: [...distinct],
      warnings,
      drainSafe: !warnings.includes('fewer-than-two-ready') && !warnings.includes('one-node'),
    };
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
      role: r.role,
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
