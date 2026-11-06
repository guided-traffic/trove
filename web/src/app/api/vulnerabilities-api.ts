import { Injectable, inject } from '@angular/core';
import { ApiClient } from './client';
import { PageFetcher } from './pagination';
import { cleanQuery, seg } from './query';
import { Versioned } from './repositories-api';
import { Schema } from './types';

/*
 * Vulnerability findings, advisories, the summary, the trend, the advisory sources and the vulnerabilities
 * of a package version (R-VULN-020, R-VULN-021, R-VULN-022, R-VULN-023; docs/developer/vulnerabilities.md).
 * The shapes are the generated ones; the screens read nothing the API does not carry (INV-17).
 */

export type VulnerabilitySeverity = Schema<'VulnerabilitySeverity'>;
export type VulnerabilityKind = Schema<'VulnerabilityKind'>;
export type VulnerabilityStatus = Schema<'VulnerabilityStatus'>;
export type VulnerabilityBlockReason = Schema<'VulnerabilityBlockReason'>;
export type VulnerabilityVersionBlockReason = Schema<'VulnerabilityVersionBlockReason'>;
export type VulnerabilityDismissalReason = Schema<'VulnerabilityDismissalReason'>;
export type VulnerabilityResolvedReason = Schema<'VulnerabilityResolvedReason'>;
export type VulnerabilityEvaluation = Schema<'VulnerabilityEvaluation'>;
export type VulnerabilityFinding = Schema<'VulnerabilityFinding'>;
export type VulnerabilityFindingDetail = Schema<'VulnerabilityFindingDetail'>;
export type VulnerabilityFindingEvent = Schema<'VulnerabilityFindingEvent'>;
export type VulnerabilityFindingPatch = Schema<'VulnerabilityFindingPatch'>;
export type VulnerabilityAdvisory = Schema<'VulnerabilityAdvisory'>;
export type VulnerabilityAdvisoryDetail = Schema<'VulnerabilityAdvisoryDetail'>;
export type VulnerabilitySummary = Schema<'VulnerabilitySummary'>;
export type VulnerabilitySeverityCounts = Schema<'VulnerabilitySeverityCounts'>;
export type VulnerabilityTrend = Schema<'VulnerabilityTrend'>;
export type VulnerabilitySourceState = Schema<'VulnerabilitySourceState'>;
export type VulnerabilitySetStatusRequest = Schema<'VulnerabilitySetStatusRequest'>;
export type VulnerabilitySetStatusResult = Schema<'VulnerabilitySetStatusResult'>;
export type VulnerabilityVersionReport = Schema<'VulnerabilityVersionReport'>;
export type VulnerabilityPackageReport = Schema<'VulnerabilityPackageReport'>;

export const SEVERITIES: readonly VulnerabilitySeverity[] = ['critical', 'high', 'medium', 'low', 'none', 'unknown'];
export const STATUSES: readonly VulnerabilityStatus[] = ['needs_triage', 'confirmed', 'blocked', 'dismissed', 'resolved'];
/** The statuses of an open finding (R-VULN-012): what the counts of the report count. */
export const OPEN_STATUSES: readonly VulnerabilityStatus[] = ['needs_triage', 'confirmed', 'blocked'];
export const KINDS: readonly VulnerabilityKind[] = ['vulnerability', 'malicious'];
export const BLOCK_REASONS: readonly VulnerabilityBlockReason[] = ['malicious', 'triage'];
export const DISMISSAL_REASONS: readonly VulnerabilityDismissalReason[] = [
  'not_exploitable',
  'false_positive',
  'risk_accepted',
  'mitigated',
  'test_only',
];

/** The sort values of `GET /api/v1/vulnerabilities/findings`; a leading `-` sorts descending. */
export const FINDING_SORTS = [
  '-severity',
  'severity',
  '-detectedAt',
  'detectedAt',
  'package',
  '-package',
  'advisory',
  '-advisory',
  'status',
  '-status',
] as const;
export type FindingSort = (typeof FINDING_SORTS)[number];

/** The filters of the findings list and of its export (R-VULN-020); all optional. */
export interface FindingFilters {
  readonly project?: readonly string[];
  readonly repository?: readonly string[];
  readonly format?: readonly string[];
  readonly package?: readonly string[];
  readonly packagePrefix?: string;
  readonly version?: readonly string[];
  readonly advisory?: string;
  readonly severity?: readonly VulnerabilitySeverity[];
  readonly status?: readonly VulnerabilityStatus[];
  readonly kind?: VulnerabilityKind;
  readonly knownExploited?: boolean;
  readonly fixAvailable?: boolean;
  readonly blocked?: boolean;
  readonly blockReason?: VulnerabilityBlockReason;
  readonly detectedFrom?: string;
  readonly detectedTo?: string;
  readonly q?: string;
  readonly sort?: FindingSort;
}

/** What the summary and the trend can be narrowed by (R-VULN-021). */
export interface ScopeFilters {
  readonly project?: readonly string[];
  readonly repository?: readonly string[];
  readonly format?: readonly string[];
}

export interface TrendQuery extends ScopeFilters {
  readonly from?: string;
  readonly to?: string;
  readonly kind?: VulnerabilityKind;
}

export abstract class VulnerabilitiesApi {
  /** One page fetcher; a new filter or sort is a new fetcher (cursor binding, R-API-010). */
  abstract findings(filters: FindingFilters): PageFetcher<VulnerabilityFinding>;
  /** The finding with the `ETag` a triage needs. */
  abstract finding(id: string): Promise<Versioned<VulnerabilityFindingDetail>>;
  /** A merge patch of one finding (R-VULN-013). */
  abstract triage(id: string, patch: VulnerabilityFindingPatch, etag: string): Promise<Versioned<VulnerabilityFindingDetail>>;
  abstract setStatus(body: VulnerabilitySetStatusRequest, idempotencyKey: string): Promise<VulnerabilitySetStatusResult>;
  abstract advisory(ecosystem: string, id: string): Promise<VulnerabilityAdvisoryDetail>;
  abstract summary(scope: ScopeFilters): Promise<VulnerabilitySummary>;
  abstract trend(query: TrendQuery): Promise<VulnerabilityTrend>;
  /** Needs `settings:read`. */
  abstract sources(): Promise<readonly VulnerabilitySourceState[]>;
  /** The CSV document of a filter; `422 export_too_large` rejects before anything is streamed. */
  abstract exportCsv(filters: FindingFilters): Promise<string>;
  abstract versionReport(packageId: string, version: string): Promise<VulnerabilityVersionReport>;
  abstract packageReport(packageId: string): Promise<VulnerabilityPackageReport>;
}

@Injectable()
export class HttpVulnerabilitiesApi extends VulnerabilitiesApi {
  private readonly client = inject(ApiClient);

  override findings(filters: FindingFilters): PageFetcher<VulnerabilityFinding> {
    return this.client.page<VulnerabilityFinding>('/api/v1/vulnerabilities/findings', cleanQuery(filters));
  }

  override async finding(id: string): Promise<Versioned<VulnerabilityFindingDetail>> {
    const r = await this.client.getJson<VulnerabilityFindingDetail>(`/api/v1/vulnerabilities/findings/${seg(id)}`);
    return { value: r.body, etag: r.etag ?? '' };
  }

  override async triage(
    id: string,
    patch: VulnerabilityFindingPatch,
    etag: string,
  ): Promise<Versioned<VulnerabilityFindingDetail>> {
    const r = await this.client.patch<VulnerabilityFindingDetail>(`/api/v1/vulnerabilities/findings/${seg(id)}`, patch, etag);
    return { value: r.body, etag: r.etag ?? '' };
  }

  override async setStatus(body: VulnerabilitySetStatusRequest, idempotencyKey: string): Promise<VulnerabilitySetStatusResult> {
    return (await this.client.post<VulnerabilitySetStatusResult>('/api/v1/vulnerabilities/findings/set-status', body, { idempotencyKey }))
      .body;
  }

  override async advisory(ecosystem: string, id: string): Promise<VulnerabilityAdvisoryDetail> {
    return (
      await this.client.getJson<VulnerabilityAdvisoryDetail>(
        `/api/v1/vulnerabilities/advisories/${seg(ecosystem)}/${seg(id)}`,
      )
    ).body;
  }

  override async summary(scope: ScopeFilters): Promise<VulnerabilitySummary> {
    return (await this.client.getJson<VulnerabilitySummary>('/api/v1/vulnerabilities/summary', { params: cleanQuery(scope) })).body;
  }

  override async trend(query: TrendQuery): Promise<VulnerabilityTrend> {
    return (await this.client.getJson<VulnerabilityTrend>('/api/v1/vulnerabilities/trend', { params: cleanQuery(query) })).body;
  }

  override async sources(): Promise<readonly VulnerabilitySourceState[]> {
    return (await this.client.getJson<{ items: VulnerabilitySourceState[] }>('/api/v1/vulnerabilities/sources')).body.items;
  }

  override async exportCsv(filters: FindingFilters): Promise<string> {
    return (await this.client.getText('/api/v1/vulnerabilities/findings/export', { params: cleanQuery(filters), accept: 'text/csv' })).body;
  }

  override async versionReport(packageId: string, version: string): Promise<VulnerabilityVersionReport> {
    return (
      await this.client.getJson<VulnerabilityVersionReport>(
        `/api/v1/packages/${seg(packageId)}/versions/${seg(version)}/vulnerabilities`,
      )
    ).body;
  }

  override async packageReport(packageId: string): Promise<VulnerabilityPackageReport> {
    return (await this.client.getJson<VulnerabilityPackageReport>(`/api/v1/packages/${seg(packageId)}/vulnerabilities`)).body;
  }
}
