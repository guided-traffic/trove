import { Provider } from '@angular/core';
import { Page, PageFetcher } from '../pagination';
import { ApiError } from '../problem';
import { Versioned } from '../repositories-api';
import {
  FindingFilters,
  ScopeFilters,
  SEVERITIES,
  TrendQuery,
  VulnerabilitiesApi,
  VulnerabilityAdvisory,
  VulnerabilityAdvisoryDetail,
  VulnerabilityFinding,
  VulnerabilityFindingDetail,
  VulnerabilityFindingPatch,
  VulnerabilityPackageReport,
  VulnerabilitySetStatusRequest,
  VulnerabilitySetStatusResult,
  VulnerabilitySeverity,
  VulnerabilitySeverityCounts,
  VulnerabilitySourceState,
  VulnerabilitySummary,
  VulnerabilityTrend,
  VulnerabilityVersionReport,
} from '../vulnerabilities-api';

/*
 * In-memory fake of the vulnerability API (R-VULN-020, R-VULN-021, R-UI-047 component tier). It models what the
 * screens depend on: the filters and sorts of the list, the `ETag` of a triage (412 for a stale tag), the
 * transitions of R-VULN-013 (`blocked` only from `confirmed`, a dismissal needs a reason, `resolved` is not
 * settable) and the block rule of R-VULN-015 for the `blocked` and `blockReason` members.
 */

const problem = (status: number, code: string): ApiError => new ApiError(status, { code }, 'req-1');

const DAY = 86_400_000;
const ago = (days: number): string => new Date(Date.now() - days * DAY).toISOString();

export const FINDING_VULN = '0193a000-0000-7000-8000-000000000001';
export const FINDING_LEFTPAD = '0193a000-0000-7000-8000-000000000002';
export const FINDING_MAL = '0193a000-0000-7000-8000-000000000003';
export const FINDING_NUGET = '0193a000-0000-7000-8000-000000000004';
export const FINDING_DISMISSED = '0193a000-0000-7000-8000-000000000005';
export const FINDING_RESOLVED = '0193a000-0000-7000-8000-000000000006';

export function sampleFinding(over: Partial<VulnerabilityFinding> = {}): VulnerabilityFinding {
  return {
    id: FINDING_VULN,
    project: 'web',
    repository: 'web/npm-remote',
    format: 'npm',
    package: 'trove-test-vuln',
    version: '1.0.0',
    advisory: 'GHSA-aaaa-aaaa-0001',
    aliases: ['CVE-2026-1111'],
    kind: 'vulnerability',
    summary: 'Prototype pollution in trove-test-vuln',
    severity: 'high',
    cvss: { version: '3.1', score: 7.5, vector: 'CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:N/A:H' },
    knownExploited: false,
    knownExploitedAddedAt: null,
    fixAvailable: true,
    fixedVersions: ['1.0.1'],
    status: 'needs_triage',
    dismissalReason: null,
    resolvedReason: null,
    blocked: false,
    blockReason: null,
    detectedAt: ago(3),
    resolvedAt: null,
    ...over,
  };
}

export function sampleAdvisory(over: Partial<VulnerabilityAdvisory> = {}): VulnerabilityAdvisory {
  return {
    ecosystem: 'npm',
    id: 'GHSA-aaaa-aaaa-0001',
    aliases: ['CVE-2026-1111'],
    kind: 'vulnerability',
    summary: 'Prototype pollution in trove-test-vuln',
    details: 'A crafted object can pollute the prototype.\n\n<script>window.pwned = 1</script>\n\nSee [the report](javascript:alert(1)).',
    // What the server's sanitizer of the README makes of `details` (R-UI-008): no script, no javascript: link.
    detailsHtml: '<p>A crafted object can pollute the prototype.</p>\n<p>See <a href="https://example.com/report">the report</a>.</p>',
    severity: 'high',
    cvss: { version: '3.1', score: 7.5, vector: 'CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:N/A:H' },
    knownExploited: false,
    knownExploitedAddedAt: null,
    cwe: ['CWE-1321'],
    affected: [{ name: 'trove-test-vuln', ranges: [{ type: 'SEMVER', events: [{ introduced: '0' }, { fixed: '1.0.1' }] }], versions: [], fixed: ['1.0.1'] }],
    fixedVersions: ['1.0.1'],
    references: [
      { type: 'ADVISORY', url: 'https://github.com/advisories/GHSA-aaaa-aaaa-0001' },
      { type: 'WEB', url: 'javascript:alert(1)' },
    ],
    source: { name: 'GitHub Advisory Database', license: 'CC-BY 4.0', url: 'https://github.com/advisories' },
    modified: ago(2),
    published: ago(30),
    withdrawn: null,
    ...over,
  };
}

const SEED: readonly VulnerabilityFinding[] = [
  sampleFinding(),
  sampleFinding({
    id: FINDING_LEFTPAD,
    package: 'left-pad',
    version: '1.3.0',
    advisory: 'GHSA-aaaa-aaaa-0003',
    aliases: [],
    summary: 'left-pad pads too much',
    severity: 'medium',
    cvss: { version: '3.1', score: 5.3, vector: 'CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:L/I:N/A:N' },
    fixAvailable: false,
    fixedVersions: [],
    status: 'confirmed',
    detectedAt: ago(5),
  }),
  sampleFinding({
    id: FINDING_MAL,
    package: 'trove-test-mal',
    version: '1.0.0',
    advisory: 'MAL-2026-1234',
    aliases: [],
    kind: 'malicious',
    summary: 'Malicious code in trove-test-mal',
    severity: 'critical',
    cvss: null,
    knownExploited: true,
    knownExploitedAddedAt: '2026-10-01',
    fixAvailable: false,
    fixedVersions: [],
    status: 'needs_triage',
    blocked: true,
    blockReason: 'malicious',
    detectedAt: ago(1),
  }),
  sampleFinding({
    id: FINDING_NUGET,
    project: 'web',
    repository: 'web/nuget-local',
    format: 'nuget',
    package: 'newtonsoft.json',
    version: '13.0.1',
    advisory: 'GHSA-aaaa-aaaa-0101',
    aliases: [],
    summary: 'Denial of service in Newtonsoft.Json',
    severity: 'high',
    cvss: { version: '3.1', score: 7.5, vector: 'CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:N/A:H' },
    fixedVersions: ['13.0.3'],
    status: 'needs_triage',
    detectedAt: ago(4),
  }),
  sampleFinding({
    id: FINDING_DISMISSED,
    package: 'lodash',
    version: '4.17.20',
    advisory: 'GHSA-35jh-r3h4-6jhm',
    aliases: ['CVE-2021-23337'],
    summary: 'Command Injection in lodash',
    severity: 'low',
    cvss: { version: '3.1', score: 3.1, vector: 'CVSS:3.1/AV:L/AC:H/PR:L/UI:R/S:U/C:L/I:N/A:N' },
    fixedVersions: ['4.17.21'],
    status: 'dismissed',
    dismissalReason: 'not_exploitable',
    detectedAt: ago(20),
  }),
  sampleFinding({
    id: FINDING_RESOLVED,
    package: 'minimist',
    version: '1.2.0',
    advisory: 'GHSA-aaaa-aaaa-0004',
    aliases: [],
    summary: 'Resolved finding',
    severity: 'unknown',
    cvss: null,
    status: 'resolved',
    resolvedReason: 'no_longer_affected',
    resolvedAt: ago(1),
    detectedAt: ago(40),
  }),
];

const RANK: Record<VulnerabilitySeverity, number> = { critical: 5, high: 4, medium: 3, low: 2, none: 1, unknown: 0 };
const STATUS_RANK: Record<string, number> = { needs_triage: 0, confirmed: 1, blocked: 2, dismissed: 3, resolved: 4 };

const OPEN = new Set(['needs_triage', 'confirmed', 'blocked']);

function zero(): { -readonly [K in keyof VulnerabilitySeverityCounts]: number } {
  return { critical: 0, high: 0, medium: 0, low: 0, none: 0, unknown: 0 };
}

export class FakeVulnerabilitiesApi extends VulnerabilitiesApi {
  /** The calls of the screens, in order, for the specs. */
  readonly calls: string[] = [];
  readonly keys: string[] = [];
  /** The `If-Match` of every triage. */
  readonly ifMatch: string[] = [];
  /** The filters of every list fetch, in order. */
  readonly listed: FindingFilters[] = [];
  /** The last bulk request. */
  lastBulk: VulnerabilitySetStatusRequest | null = null;
  failure: ApiError | null = null;
  listFailure: ApiError | null = null;
  exportFailure: ApiError | null = null;
  exported: FindingFilters | null = null;
  rows: VulnerabilityFinding[] = SEED.map((f) => ({ ...f }));
  advisories = new Map<string, VulnerabilityAdvisory>([
    ['npm/GHSA-aaaa-aaaa-0001', sampleAdvisory()],
    ['npm/MAL-2026-1234', sampleAdvisory({ id: 'MAL-2026-1234', kind: 'malicious', summary: 'Malicious code in trove-test-mal', severity: 'critical', cvss: null, aliases: [], references: [] })],
  ]);
  /** Rows the list pages through; set to a small number to test "Load more". */
  pageSize = 50;
  data: VulnerabilitySummary['data'] = [
    { ecosystem: 'npm', state: 'complete', stale: false, syncedAt: ago(0.1) },
    { ecosystem: 'NuGet', state: 'complete', stale: false, syncedAt: ago(0.1) },
  ];
  coverage: VulnerabilitySummary['coverage'] = { evaluated: 3, pendingData: 0, internal: 1, notCovered: 2 };
  sourceStates: VulnerabilitySourceState[] = [
    sampleSource({ ecosystem: 'npm' }),
    sampleSource({ ecosystem: 'NuGet' }),
    sampleSource({ source: 'kev', ecosystem: '' }),
  ];
  /** The history of a finding the fake keeps, by id. */
  private readonly revisions = new Map<string, number>();
  private readonly comments = new Map<string, string>();
  private readonly blockedBy = new Map<string, { at: string; comment: string | null }>();
  private readonly history = new Map<string, VulnerabilityFindingDetail['history']>();
  /** Versions of the version report, by `<packageId>/<version>`. */
  versionReports = new Map<string, VulnerabilityVersionReport>();
  packageReports = new Map<string, VulnerabilityPackageReport>();

  etagOf(id: string): string {
    return `"${this.revisions.get(id) ?? 1}"`;
  }

  bumpRevision(id: string): void {
    this.revisions.set(id, (this.revisions.get(id) ?? 1) + 1);
  }

  private fail(): void {
    if (this.failure) {
      const e = this.failure;
      this.failure = null;
      throw e;
    }
  }

  private visible(f: FindingFilters): VulnerabilityFinding[] {
    const hasStatus = (f.status?.length ?? 0) > 0;
    const out = this.rows.filter(
      (x) =>
        (hasStatus ? f.status!.includes(x.status) : x.status !== 'resolved') &&
        (!f.repository?.length || f.repository.includes(x.repository)) &&
        (!f.format?.length || f.format.includes(x.format)) &&
        (!f.package?.length || f.package.includes(x.package)) &&
        (!f.packagePrefix || x.package.startsWith(f.packagePrefix)) &&
        (!f.version?.length || f.version.includes(x.version)) &&
        (!f.advisory || x.advisory === f.advisory || x.aliases.includes(f.advisory)) &&
        (!f.severity?.length || f.severity.includes(x.severity)) &&
        (!f.kind || x.kind === f.kind) &&
        (f.knownExploited === undefined || x.knownExploited === f.knownExploited) &&
        (f.fixAvailable === undefined || x.fixAvailable === f.fixAvailable) &&
        (f.blocked === undefined || x.blocked === f.blocked) &&
        (!f.blockReason || x.blockReason === f.blockReason) &&
        (!f.q || `${x.package} ${x.advisory} ${x.summary}`.toLowerCase().includes(f.q.toLowerCase())),
    );
    const sort = f.sort ?? '-severity';
    const dir = sort.startsWith('-') ? -1 : 1;
    const key = sort.replace('-', '');
    const cmp = (a: VulnerabilityFinding, b: VulnerabilityFinding): number => {
      switch (key) {
        case 'severity':
          return RANK[a.severity] - RANK[b.severity] || (a.cvss?.score ?? 0) - (b.cvss?.score ?? 0);
        case 'detectedAt':
          return a.detectedAt.localeCompare(b.detectedAt);
        case 'package':
          return a.package.localeCompare(b.package);
        case 'advisory':
          return a.advisory.localeCompare(b.advisory);
        default:
          return (STATUS_RANK[a.status] ?? 0) - (STATUS_RANK[b.status] ?? 0);
      }
    };
    return out.sort((a, b) => dir * cmp(a, b) || a.id.localeCompare(b.id));
  }

  override findings(filters: FindingFilters): PageFetcher<VulnerabilityFinding> {
    return async (cursor, limit): Promise<Page<VulnerabilityFinding>> => {
      if (this.listFailure) throw this.listFailure;
      this.listed.push(filters);
      const all = this.visible(filters);
      const start = cursor === null ? 0 : Number(cursor);
      const size = Math.min(limit, this.pageSize);
      const items = all.slice(start, start + size);
      return { items, nextCursor: start + size < all.length ? String(start + size) : null };
    };
  }

  private find(id: string): VulnerabilityFinding {
    const found = this.rows.find((f) => f.id === id);
    if (!found) throw problem(404, 'not_found');
    return found;
  }

  private detailOf(f: VulnerabilityFinding): VulnerabilityFindingDetail {
    const advisory = this.advisories.get(`${f.format === 'npm' ? 'npm' : 'NuGet'}/${f.advisory}`) ?? sampleAdvisory({ id: f.advisory, summary: f.summary, ecosystem: f.format === 'npm' ? 'npm' : 'NuGet' });
    const by = this.blockedBy.get(f.id);
    return {
      finding: f,
      advisory,
      virtualRepositories: f.repository === 'web/npm-remote' ? ['web/npm-all'] : [],
      blockedBy:
        f.blockReason === 'triage' && by
          ? { principal: { id: 'u-alice', name: 'alice' }, at: by.at, comment: by.comment }
          : null,
      comment: this.comments.get(f.id) ?? null,
      history: this.history.get(f.id) ?? [
        { at: f.detectedAt, actor: null, change: 'detected', fromStatus: null, toStatus: 'needs_triage', reason: null, comment: null },
      ],
    };
  }

  override async finding(id: string): Promise<Versioned<VulnerabilityFindingDetail>> {
    this.fail();
    this.calls.push(`finding ${id}`);
    return { value: this.detailOf(this.find(id)), etag: this.etagOf(id) };
  }

  /** The block rule of R-VULN-015 for the sample policy: a malicious finding blocks until dismissed, a triage block is the status. */
  private blockOf(f: VulnerabilityFinding): VulnerabilityFinding {
    if (f.status === 'blocked') return { ...f, blocked: true, blockReason: 'triage' };
    if (f.kind === 'malicious' && (f.status === 'needs_triage' || f.status === 'confirmed')) return { ...f, blocked: true, blockReason: 'malicious' };
    return { ...f, blocked: false, blockReason: null };
  }

  private apply(f: VulnerabilityFinding, patch: VulnerabilityFindingPatch): VulnerabilityFinding {
    if (f.status === 'resolved') throw problem(409, 'finding_resolved');
    const status = patch.status;
    if (status === undefined) return f;
    if (status === 'resolved') throw problem(422, 'status_not_settable');
    if (status === 'blocked' && f.status !== 'confirmed') throw problem(409, 'finding_not_confirmed');
    if (status === 'dismissed' && !patch.dismissalReason) {
      throw new ApiError(422, { code: 'validation_failed', errors: [{ pointer: '/dismissalReason', code: 'required' }] }, 'req-1');
    }
    return this.blockOf({ ...f, status, dismissalReason: status === 'dismissed' ? (patch.dismissalReason ?? null) : null });
  }

  private record(id: string, from: string, to: string, reason: string | null, comment: string | null): void {
    const row = { at: new Date().toISOString(), actor: { id: 'u-alice', name: 'alice' }, change: 'status_changed' as const, fromStatus: from, toStatus: to, reason, comment };
    this.history.set(id, [...(this.history.get(id) ?? this.detailOf(this.find(id)).history), row]);
  }

  override async triage(
    id: string,
    patch: VulnerabilityFindingPatch,
    etag: string,
  ): Promise<Versioned<VulnerabilityFindingDetail>> {
    this.fail();
    this.ifMatch.push(etag);
    this.calls.push(`triage ${id} ${patch.status ?? ''}`);
    const current = this.find(id);
    if (etag !== this.etagOf(id)) {
      throw new ApiError(412, { code: 'precondition_failed', currentEtag: this.etagOf(id) }, 'req-1');
    }
    const next = this.apply(current, patch);
    if (patch.status !== undefined && next.status !== current.status) {
      this.record(id, current.status, next.status, patch.dismissalReason ?? null, patch.comment ?? null);
      if (next.status === 'blocked') this.blockedBy.set(id, { at: new Date().toISOString(), comment: patch.comment ?? null });
      else this.blockedBy.delete(id);
    }
    if (patch.comment !== undefined) this.comments.set(id, patch.comment ?? '');
    this.bumpRevision(id);
    this.rows = this.rows.map((f) => (f.id === id ? next : f));
    return { value: this.detailOf(next), etag: this.etagOf(id) };
  }

  override async setStatus(body: VulnerabilitySetStatusRequest, idempotencyKey: string): Promise<VulnerabilitySetStatusResult> {
    this.fail();
    this.keys.push(idempotencyKey);
    this.lastBulk = body;
    this.calls.push(`setStatus ${body.status} ${body.ids.length}`);
    const results: VulnerabilitySetStatusResult['results'][number][] = [];
    for (const id of body.ids) {
      const current = this.rows.find((f) => f.id === id);
      if (!current) {
        results.push({ id, outcome: 'failed', status: 404, code: 'not_found' });
        continue;
      }
      try {
        const next = this.apply(current, { status: body.status, dismissalReason: body.dismissalReason, comment: body.comment });
        results.push({ id, outcome: next.status === current.status ? 'unchanged' : 'changed', status: 200, code: null });
        if (next.status !== current.status) {
          this.record(id, current.status, next.status, body.dismissalReason ?? null, body.comment ?? null);
          this.bumpRevision(id);
        }
        this.rows = this.rows.map((f) => (f.id === id ? next : f));
      } catch (e) {
        const err = e as ApiError;
        results.push({ id, outcome: 'failed', status: err.status, code: err.code });
      }
    }
    return { bulkId: 'bulk-1', results };
  }

  override async advisory(ecosystem: string, id: string): Promise<VulnerabilityAdvisoryDetail> {
    this.fail();
    this.calls.push(`advisory ${ecosystem} ${id}`);
    const advisory = this.advisories.get(`${ecosystem}/${id}`);
    if (!advisory) throw problem(404, 'not_found');
    return { advisory, findings: this.rows.filter((f) => f.advisory === id || f.aliases.includes(id)) };
  }

  private scoped(scope: ScopeFilters): VulnerabilityFinding[] {
    return this.rows.filter(
      (f) => OPEN.has(f.status) && (!scope.repository?.length || scope.repository.includes(f.repository)) && (!scope.format?.length || scope.format.includes(f.format)),
    );
  }

  override async summary(scope: ScopeFilters): Promise<VulnerabilitySummary> {
    this.fail();
    this.calls.push('summary');
    const open = this.scoped(scope);
    const bySeverity = zero();
    for (const f of open) bySeverity[f.severity]++;
    const blockedVersions = new Set(open.filter((f) => f.blocked).map((f) => `${f.repository}/${f.package}/${f.version}`));
    return {
      bySeverity,
      byKind: {
        vulnerability: open.filter((f) => f.kind === 'vulnerability').length,
        malicious: open.filter((f) => f.kind === 'malicious').length,
      },
      needsTriage: open.filter((f) => f.status === 'needs_triage').length,
      blockedVersions: blockedVersions.size,
      fixAvailable: open.filter((f) => f.fixAvailable).length,
      coverage: this.coverage,
      data: this.data,
    };
  }

  override async trend(query: TrendQuery): Promise<VulnerabilityTrend> {
    this.fail();
    this.calls.push(`trend ${query.from ?? ''} ${query.to ?? ''}`);
    const to = query.to ? Date.parse(query.to) : Date.now();
    const from = query.from ? Date.parse(query.from) : to - 89 * DAY;
    const points: VulnerabilityTrend['points'] = [];
    const open = this.scoped(query);
    for (let t = from; t <= to; t += DAY) {
      const counts = zero();
      for (const f of open) if (Date.parse(f.detectedAt) <= t + DAY) counts[f.severity]++;
      points.push({ date: new Date(t).toISOString().slice(0, 10), counts });
    }
    return {
      from: new Date(from).toISOString().slice(0, 10),
      to: new Date(to).toISOString().slice(0, 10),
      basis: 'A finding counts under its current severity, and a dismissed finding counts until it resolves.',
      points,
    };
  }

  override async sources(): Promise<readonly VulnerabilitySourceState[]> {
    this.fail();
    this.calls.push('sources');
    return this.sourceStates;
  }

  override async exportCsv(filters: FindingFilters): Promise<string> {
    if (this.exportFailure) throw this.exportFailure;
    this.exported = filters;
    this.calls.push('export');
    const rows = this.visible(filters);
    const head = 'findingId,status,severity,advisory,repository,package,version';
    return [head, ...rows.map((f) => [f.id, f.status, f.severity, f.advisory, f.repository, f.package, f.version].join(','))].join('\n') + '\n';
  }

  override async versionReport(packageId: string, version: string): Promise<VulnerabilityVersionReport> {
    this.fail();
    this.calls.push(`versionReport ${packageId} ${version}`);
    const given = this.versionReports.get(`${packageId}/${version}`);
    if (given) return given;
    // Not configured: the scanning is not available, as the operation answers until it is built (Q-VULN-25).
    throw problem(503, 'unavailable');
  }

  override async packageReport(packageId: string): Promise<VulnerabilityPackageReport> {
    this.fail();
    this.calls.push(`packageReport ${packageId}`);
    const given = this.packageReports.get(packageId);
    if (given) return given;
    throw problem(503, 'unavailable');
  }
}

export function sampleSource(over: Partial<VulnerabilitySourceState> = {}): VulnerabilitySourceState {
  return {
    source: 'osv',
    ecosystem: 'npm',
    mode: 'online',
    state: 'complete',
    stale: false,
    syncedAt: ago(0.1),
    fullSyncedAt: ago(1),
    attemptedAt: ago(0.1),
    failures: 0,
    repository: null,
    lastError: null,
    records: 1234,
    rejected: 0,
    unevaluable: 2,
    nextRunAt: new Date(Date.now() + 3_600_000).toISOString(),
    ...over,
  };
}

export function severityCounts(over: Partial<VulnerabilitySeverityCounts> = {}): VulnerabilitySeverityCounts {
  return { ...Object.fromEntries(SEVERITIES.map((s) => [s, 0])), ...over } as VulnerabilitySeverityCounts;
}

export function provideVulnerabilityFakes(): Provider[] {
  return [{ provide: VulnerabilitiesApi, useClass: FakeVulnerabilitiesApi }];
}
