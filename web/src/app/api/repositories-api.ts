import { Injectable, inject } from '@angular/core';
import { ApiClient, QueryParams } from './client';
import { PageFetcher, collectAll } from './pagination';
import { Schema } from './types';

export type Repository = Schema<'Repository'>;
export type RepositoryConsumerView = Schema<'RepositoryConsumerView'>;
export type RepositorySummary = Schema<'RepositorySummary'>;
export type FormatDescriptor = Schema<'FormatDescriptor'>;
export type JobReference = Schema<'JobReference'>;
export type RepositoryClass = Repository['class'];

/** A resource together with the `ETag` it was read with (R-API-012). */
export interface Versioned<T> {
  readonly value: T;
  readonly etag: string;
}

/** A delete answers 204 (done) or 202 with a job to poll (R-API-015). */
export type DeleteOutcome =
  | { readonly done: true }
  | { readonly done: false; readonly job: JobReference };

/** The filters of `GET /api/v1/repositories` (R-REPO-012); all optional. */
export interface RepositoryFilters {
  readonly q?: string;
  readonly class?: string;
  readonly format?: string;
  /** `inactive` keeps the disabled repositories. */
  readonly state?: string;
}

/** True when the answer is the narrower consumer view (the caller cannot read the configuration). */
export function isConsumerView(
  value: Repository | RepositoryConsumerView,
): value is RepositoryConsumerView {
  return 'addresses' in value;
}

export function repositoryUrl(key: string): string {
  return `/api/v1/repositories/${encodeURIComponent(key)}`;
}

/** Repositories and the format catalog: R-UI-020, R-UI-021, R-UI-010. */
export abstract class RepositoriesApi {
  /** `GET /api/v1/formats`; the only source of the classes a format offers (R-UI-021). */
  abstract formats(): Promise<readonly FormatDescriptor[]>;
  /** One page fetcher over the summary view; a new filter set is a new fetcher (cursor binding). */
  abstract list(filters: RepositoryFilters): PageFetcher<RepositorySummary>;
  /** Every readable repository, for pickers and the tree root. */
  abstract listAll(): Promise<readonly RepositorySummary[]>;
  abstract get(key: string): Promise<Versioned<Repository | RepositoryConsumerView>>;
  abstract create(doc: Repository, idempotencyKey: string): Promise<Versioned<Repository>>;
  /** JSON merge patch with the loaded ETag. */
  abstract patch(
    key: string,
    patch: Readonly<Record<string, unknown>>,
    etag: string,
  ): Promise<Versioned<Repository>>;
  /** `detach` also removes the repository from the virtual repositories that include it. */
  abstract delete(key: string, idempotencyKey: string, detach?: boolean): Promise<DeleteOutcome>;
}

@Injectable()
export class HttpRepositoriesApi extends RepositoriesApi {
  private readonly client = inject(ApiClient);

  async formats(): Promise<readonly FormatDescriptor[]> {
    return (await this.client.get('/api/v1/formats')).body.formats;
  }

  list(filters: RepositoryFilters): PageFetcher<RepositorySummary> {
    const params: Record<string, string | undefined> = {
      q: filters.q || undefined,
      class: filters.class || undefined,
      format: filters.format || undefined,
      state: filters.state || undefined,
    };
    return this.client.page<RepositorySummary>('/api/v1/repositories', params as QueryParams);
  }

  listAll(): Promise<readonly RepositorySummary[]> {
    return collectAll(this.list({}));
  }

  async get(key: string): Promise<Versioned<Repository | RepositoryConsumerView>> {
    const r = await this.client.getJson<Repository | RepositoryConsumerView>(repositoryUrl(key));
    return { value: r.body, etag: r.etag ?? '' };
  }

  async create(doc: Repository, idempotencyKey: string): Promise<Versioned<Repository>> {
    const r = await this.client.post<Repository>('/api/v1/repositories', doc, { idempotencyKey });
    return { value: r.body, etag: r.etag ?? '' };
  }

  async patch(
    key: string,
    patch: Readonly<Record<string, unknown>>,
    etag: string,
  ): Promise<Versioned<Repository>> {
    const r = await this.client.patch<Repository>(repositoryUrl(key), patch, etag);
    return { value: r.body, etag: r.etag ?? '' };
  }

  async delete(key: string, idempotencyKey: string, detach = false): Promise<DeleteOutcome> {
    const r = await this.client.delete<{ job: JobReference } | null>(repositoryUrl(key), {
      idempotencyKey,
      params: detach ? { detach: true } : undefined,
    });
    return r.status === 202 && r.body ? { done: false, job: r.body.job } : { done: true };
  }
}
