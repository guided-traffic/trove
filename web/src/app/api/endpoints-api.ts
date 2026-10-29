import { Injectable, inject } from '@angular/core';
import { ApiClient, QueryParams } from './client';
import { collectAll } from './pagination';
import { Versioned } from './repositories-api';
import { Schema } from './types';

export type Endpoint = Schema<'Endpoint'>;
export type EndpointConfiguration = Schema<'EndpointConfiguration'>;
export type ExposureRule = Schema<'ExposureRule'>;
export type ExposureCheck = Schema<'ExposureCheck'>;
export type ExposureCheckRequest = Schema<'ExposureCheckRequest'>;

export interface RuleFilters {
  readonly repository?: string;
  readonly enabled?: boolean;
}

/** The ETag of a rule is its quoted revision (R-API-012). */
export function ruleEtag(rule: ExposureRule): string {
  return `"${rule.revision ?? 0}"`;
}

function endpointUrl(name: string): string {
  return `/api/v1/endpoints/${encodeURIComponent(name)}`;
}

function ruleUrl(endpoint: string, name: string): string {
  return `${endpointUrl(endpoint)}/exposure-rules/${encodeURIComponent(name)}`;
}

/** Endpoints, exposure rules and the dry run: R-EP-016, R-EP-017. */
export abstract class EndpointsApi {
  abstract list(): Promise<readonly Endpoint[]>;
  abstract get(name: string): Promise<Endpoint>;
  abstract rules(endpoint: string, filters?: RuleFilters): Promise<readonly ExposureRule[]>;
  abstract rule(endpoint: string, name: string): Promise<Versioned<ExposureRule>>;
  abstract createRule(
    endpoint: string,
    rule: ExposureRule,
    idempotencyKey: string,
  ): Promise<Versioned<ExposureRule>>;
  abstract replaceRule(
    endpoint: string,
    name: string,
    rule: ExposureRule,
    etag: string,
  ): Promise<Versioned<ExposureRule>>;
  abstract patchRule(
    endpoint: string,
    name: string,
    patch: Readonly<Record<string, unknown>>,
    etag: string,
  ): Promise<Versioned<ExposureRule>>;
  abstract deleteRule(endpoint: string, name: string, etag: string): Promise<void>;
  /** `POST …/check-exposure`; `rule` is the unsaved draft of the editor. */
  abstract check(endpoint: string, request: ExposureCheckRequest): Promise<ExposureCheck>;
}

@Injectable()
export class HttpEndpointsApi extends EndpointsApi {
  private readonly client = inject(ApiClient);

  list(): Promise<readonly Endpoint[]> {
    return collectAll(this.client.page<Endpoint>('/api/v1/endpoints'));
  }

  async get(name: string): Promise<Endpoint> {
    return (await this.client.getJson<Endpoint>(endpointUrl(name))).body;
  }

  rules(endpoint: string, filters: RuleFilters = {}): Promise<readonly ExposureRule[]> {
    const params: Record<string, string | undefined> = {
      repository: filters.repository,
      enabled: filters.enabled === undefined ? undefined : String(filters.enabled),
    };
    return collectAll(
      this.client.page<ExposureRule>(
        `${endpointUrl(endpoint)}/exposure-rules`,
        params as QueryParams,
      ),
    );
  }

  async rule(endpoint: string, name: string): Promise<Versioned<ExposureRule>> {
    const r = await this.client.getJson<ExposureRule>(ruleUrl(endpoint, name));
    return { value: r.body, etag: r.etag ?? '' };
  }

  async createRule(
    endpoint: string,
    rule: ExposureRule,
    idempotencyKey: string,
  ): Promise<Versioned<ExposureRule>> {
    const r = await this.client.post<ExposureRule>(
      `${endpointUrl(endpoint)}/exposure-rules`,
      rule,
      {
        idempotencyKey,
      },
    );
    return { value: r.body, etag: r.etag ?? '' };
  }

  async replaceRule(
    endpoint: string,
    name: string,
    rule: ExposureRule,
    etag: string,
  ): Promise<Versioned<ExposureRule>> {
    const r = await this.client.put<ExposureRule>(ruleUrl(endpoint, name), rule, etag);
    return { value: r.body, etag: r.etag ?? '' };
  }

  async patchRule(
    endpoint: string,
    name: string,
    patch: Readonly<Record<string, unknown>>,
    etag: string,
  ): Promise<Versioned<ExposureRule>> {
    const r = await this.client.patch<ExposureRule>(ruleUrl(endpoint, name), patch, etag);
    return { value: r.body, etag: r.etag ?? '' };
  }

  async deleteRule(endpoint: string, name: string, etag: string): Promise<void> {
    await this.client.delete(ruleUrl(endpoint, name), { etag });
  }

  async check(endpoint: string, request: ExposureCheckRequest): Promise<ExposureCheck> {
    return (
      await this.client.post<ExposureCheck>(`${endpointUrl(endpoint)}/check-exposure`, request)
    ).body;
  }
}
