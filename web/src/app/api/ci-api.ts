import { Injectable, inject } from '@angular/core';
import { ApiClient } from './client';
import { PageFetcher } from './pagination';
import { cleanQuery, seg } from './query';
import { Versioned } from './repositories-api';
import { Schema } from './types';

/*
 * GitLab CI identity (R-UI-035, R-UI-036, R-API-035; docs/developer/ci-oidc.md). The system routes
 * belong to the administrators, the project routes (`/projects/{key}/ci/…`, Q-CI-26) to the
 * administrators of a project. Every method takes the project key of the scope or null for the
 * system tier, so one screen serves both. An issuer, a mapping and a replace carry an `ETag`; the
 * evaluate request carries claims and never a token (R-CI-019).
 */

export type CiIssuer = Schema<'CiIssuer'>;
export type CiIssuerInput = Schema<'CiIssuerInput'>;
export type CiIssuerCreate = Schema<'CiIssuerCreate'>;
export type CiIssuerChoice = Schema<'CiIssuerChoice'>;
export type CiKeyStatus = Schema<'CiKeyStatus'>;
export type CiKeySummary = Schema<'CiKeySummary'>;
export type CiMapping = Schema<'CiMapping'>;
export type CiMappingInput = Schema<'CiMappingInput'>;
export type CiCondition = Schema<'CiCondition'>;
export type CiGrants = Schema<'CiGrants'>;
export type CiInlineGrant = Schema<'CiInlineGrant'>;
export type CiWarning = Schema<'CiWarning'>;
export type CiEvaluateRequest = Schema<'CiEvaluateRequest'>;
export type CiEvaluateResult = Schema<'CiEvaluateResult'>;
export type CiTraceEntry = Schema<'CiTraceEntry'>;
export type CiLeafResult = Schema<'CiLeafResult'>;
export type CiIssuance = Schema<'CiIssuance'>;
export type CiIssuanceDetail = Schema<'CiIssuanceDetail'>;
export type CiRevokeRequest = Schema<'CiRevokeRequest'>;

/** The issuer name pattern (R-CI-002), checked live in the form; the server decides. */
export const CI_NAME_PATTERN = /^[a-z0-9][a-z0-9._-]{1,63}$/;

export type SnippetFormat = 'npm' | 'nuget';

/** The filters of the issuance list; a project's list understands `issuer`, `jobId` and `active` only. */
export interface IssuanceFilters {
  readonly issuer?: string;
  readonly projectId?: string;
  readonly projectPath?: string;
  readonly pipelineId?: string;
  readonly jobId?: string;
  readonly mapping?: string;
  readonly active?: boolean;
  readonly from?: string;
  readonly to?: string;
}

export abstract class CiApi {
  abstract listIssuers(): Promise<readonly CiIssuer[]>;
  abstract getIssuer(name: string): Promise<Versioned<CiIssuer>>;
  /** `validate` fetches the discovery document and the key set before the issuer is stored. */
  abstract createIssuer(input: CiIssuerCreate): Promise<Versioned<CiIssuer>>;
  abstract replaceIssuer(name: string, input: CiIssuerInput, etag: string): Promise<Versioned<CiIssuer>>;
  abstract deleteIssuer(name: string, cascade: boolean): Promise<void>;
  abstract keys(name: string): Promise<CiKeyStatus>;
  abstract refreshKeys(name: string): Promise<CiKeyStatus>;
  /** What a project administrator sees of the issuers (Q-CI-26). */
  abstract projectIssuers(project: string): Promise<readonly CiIssuerChoice[]>;

  /** In evaluation order. With a project: the project's mappings of every issuer; without: the system tier's of `issuer`. */
  abstract listMappings(issuer: string, project: string | null): Promise<readonly CiMapping[]>;
  abstract getMapping(issuer: string, name: string, project: string | null): Promise<Versioned<CiMapping>>;
  /** The project route takes the issuer from the body. */
  abstract createMapping(issuer: string, input: CiMappingInput, project: string | null): Promise<Versioned<CiMapping>>;
  abstract replaceMapping(
    issuer: string,
    name: string,
    input: CiMappingInput,
    etag: string,
    project: string | null,
  ): Promise<Versioned<CiMapping>>;
  abstract deleteMapping(issuer: string, name: string, project: string | null): Promise<void>;
  abstract evaluate(issuer: string, request: CiEvaluateRequest, project: string | null): Promise<CiEvaluateResult>;
  /** The text of the snippet of `mapping` (R-UI-035); the route is the system administrators'. */
  abstract snippet(issuer: string, mapping: string, format: SnippetFormat): Promise<string>;

  abstract listIssuances(filters: IssuanceFilters, project: string | null): PageFetcher<CiIssuance>;
  abstract getIssuance(tokenId: string): Promise<CiIssuanceDetail>;
  /** The number of tokens revoked. */
  abstract revokeIssuances(request: CiRevokeRequest): Promise<number>;
}

function versioned<T extends { readonly version: number }>(r: {
  readonly body: T;
  readonly etag: string | null;
}): Versioned<T> {
  return { value: r.body, etag: r.etag ?? `"${r.body.version}"` };
}

@Injectable()
export class HttpCiApi extends CiApi {
  private readonly client = inject(ApiClient);

  override async listIssuers(): Promise<readonly CiIssuer[]> {
    return (await this.client.getJson<{ items: CiIssuer[] }>('/api/v1/ci/issuers')).body.items;
  }

  override async getIssuer(name: string): Promise<Versioned<CiIssuer>> {
    return versioned(await this.client.getJson<CiIssuer>(`/api/v1/ci/issuers/${seg(name)}`));
  }

  override async createIssuer(input: CiIssuerCreate): Promise<Versioned<CiIssuer>> {
    return versioned(await this.client.post<CiIssuer>('/api/v1/ci/issuers', input));
  }

  override async replaceIssuer(name: string, input: CiIssuerInput, etag: string): Promise<Versioned<CiIssuer>> {
    return versioned(await this.client.put<CiIssuer>(`/api/v1/ci/issuers/${seg(name)}`, input, etag));
  }

  override async deleteIssuer(name: string, cascade: boolean): Promise<void> {
    await this.client.delete(`/api/v1/ci/issuers/${seg(name)}`, { params: cascade ? { cascade: true } : {} });
  }

  override async keys(name: string): Promise<CiKeyStatus> {
    return (await this.client.getJson<CiKeyStatus>(`/api/v1/ci/issuers/${seg(name)}/keys`)).body;
  }

  override async refreshKeys(name: string): Promise<CiKeyStatus> {
    return (await this.client.post<CiKeyStatus>(`/api/v1/ci/issuers/${seg(name)}/keys/refresh`)).body;
  }

  override async projectIssuers(project: string): Promise<readonly CiIssuerChoice[]> {
    return (await this.client.getJson<{ items: CiIssuerChoice[] }>(`/api/v1/projects/${seg(project)}/ci/issuers`)).body
      .items;
  }

  override async listMappings(issuer: string, project: string | null): Promise<readonly CiMapping[]> {
    const url = project
      ? `/api/v1/projects/${seg(project)}/ci/mappings`
      : `/api/v1/ci/issuers/${seg(issuer)}/mappings`;
    const items = (await this.client.getJson<{ items: CiMapping[] }>(url)).body.items;
    return project && issuer ? items.filter((m) => m.issuer === issuer) : items;
  }

  override async getMapping(issuer: string, name: string, project: string | null): Promise<Versioned<CiMapping>> {
    return versioned(await this.client.getJson<CiMapping>(mappingUrl(issuer, name, project)));
  }

  override async createMapping(issuer: string, input: CiMappingInput, project: string | null): Promise<Versioned<CiMapping>> {
    const url = project ? `/api/v1/projects/${seg(project)}/ci/mappings` : `/api/v1/ci/issuers/${seg(issuer)}/mappings`;
    return versioned(await this.client.post<CiMapping>(url, project ? { ...input, issuer } : input));
  }

  override async replaceMapping(
    issuer: string,
    name: string,
    input: CiMappingInput,
    etag: string,
    project: string | null,
  ): Promise<Versioned<CiMapping>> {
    return versioned(await this.client.put<CiMapping>(mappingUrl(issuer, name, project), input, etag));
  }

  override async deleteMapping(issuer: string, name: string, project: string | null): Promise<void> {
    await this.client.delete(mappingUrl(issuer, name, project));
  }

  override async evaluate(issuer: string, request: CiEvaluateRequest, project: string | null): Promise<CiEvaluateResult> {
    const url = project
      ? `/api/v1/projects/${seg(project)}/ci/issuers/${seg(issuer)}/evaluate`
      : `/api/v1/ci/issuers/${seg(issuer)}/evaluate`;
    return (await this.client.post<CiEvaluateResult>(url, request)).body;
  }

  override async snippet(issuer: string, mapping: string, format: SnippetFormat): Promise<string> {
    return (
      await this.client.getText(`/api/v1/ci/issuers/${seg(issuer)}/snippets/gitlab-ci`, {
        params: { mapping, format },
      })
    ).body;
  }

  override listIssuances(filters: IssuanceFilters, project: string | null): PageFetcher<CiIssuance> {
    const url = project ? `/api/v1/projects/${seg(project)}/ci/issuances` : '/api/v1/ci/issuances';
    return this.client.page<CiIssuance>(url, cleanQuery(filters));
  }

  override async getIssuance(tokenId: string): Promise<CiIssuanceDetail> {
    return (await this.client.getJson<CiIssuanceDetail>(`/api/v1/ci/issuances/${seg(tokenId)}`)).body;
  }

  override async revokeIssuances(request: CiRevokeRequest): Promise<number> {
    return (await this.client.post<{ revoked: number }>('/api/v1/ci/issuances/revoke', request)).body.revoked;
  }
}

function mappingUrl(issuer: string, name: string, project: string | null): string {
  return project
    ? `/api/v1/projects/${seg(project)}/ci/mappings/${seg(issuer)}/${seg(name)}`
    : `/api/v1/ci/issuers/${seg(issuer)}/mappings/${seg(name)}`;
}
