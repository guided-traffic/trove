import { Injectable, inject } from '@angular/core';
import { ApiClient } from './client';
import { PageFetcher } from './pagination';
import { ApiError } from './problem';
import { cleanQuery, seg } from './query';
import { Schema } from './types';

/* Packages, versions, README and client setup: R-API-029, R-API-030. */

export type PackageSummary = Schema<'PackageSummary'>;
export type PackageVersionSummary = Schema<'PackageVersionSummary'>;
export type PackageVersion = Schema<'PackageVersion'>;
export type PackageFile = Schema<'PackageFile'>;
export type PackageReadme = Schema<'PackageReadme'>;
export type ClientSetup = Schema<'ClientSetup'>;
export type ClientSnippet = Schema<'ClientSnippet'>;

/** The sort fields `GET /api/v1/packages` offers (R-API-029); a leading `-` sorts descending. */
export type PackageSort = 'name' | '-name' | 'createdAt' | '-createdAt' | 'updatedAt' | '-updatedAt';

/** The filters of `GET /api/v1/packages`; all optional. */
export interface PackageFilters {
  readonly format?: readonly string[];
  readonly repository?: readonly string[];
  readonly q?: string;
  readonly namePrefix?: string;
  readonly keyword?: string;
  readonly createdFrom?: string;
  readonly createdTo?: string;
  readonly sort?: PackageSort;
}

export interface VersionFilters {
  readonly prerelease?: boolean;
  readonly repository?: readonly string[];
  readonly sort?: '-publishedAt' | 'publishedAt' | '-version' | 'version';
}

/** The query of `GET …/client-setup` (R-API-030). */
export interface ClientSetupQuery {
  readonly client: string;
  readonly credentials?: 'placeholder' | 'env' | 'none';
  readonly package?: string;
  readonly version?: string;
  readonly scope?: string;
}

export function packageUrl(id: string): string {
  return `/api/v1/packages/${seg(id)}`;
}

/** Packages across repositories (R-API-029). */
export abstract class PackagesApi {
  /** One page fetcher; a new filter or sort is a new fetcher (cursor binding, R-API-010). */
  abstract list(filters: PackageFilters): PageFetcher<PackageSummary>;
  /** Resolves a format and a name; rejects with 404 when no visible version exists. */
  abstract lookup(format: string, name: string): Promise<PackageSummary>;
  abstract versions(packageId: string, filters?: VersionFilters): PageFetcher<PackageVersionSummary>;
  abstract version(packageId: string, version: string): Promise<PackageVersion>;
  /** The sanitized README, or `null` when the version has none (404). */
  abstract readme(packageId: string, version: string): Promise<PackageReadme | null>;
}

/** The client setup snippets of a repository (R-API-030). */
export abstract class ClientSetupApi {
  abstract get(repository: string, query: ClientSetupQuery): Promise<ClientSetup>;
}

@Injectable()
export class HttpPackagesApi extends PackagesApi {
  private readonly client = inject(ApiClient);

  override list(filters: PackageFilters): PageFetcher<PackageSummary> {
    return this.client.page<PackageSummary>('/api/v1/packages', cleanQuery(filters));
  }

  override async lookup(format: string, name: string): Promise<PackageSummary> {
    return (await this.client.get('/api/v1/packages/lookup', { params: { format, name } })).body;
  }

  override versions(packageId: string, filters: VersionFilters = {}): PageFetcher<PackageVersionSummary> {
    return this.client.page<PackageVersionSummary>(`${packageUrl(packageId)}/versions`, cleanQuery(filters));
  }

  override async version(packageId: string, version: string): Promise<PackageVersion> {
    return (await this.client.getJson<PackageVersion>(`${packageUrl(packageId)}/versions/${seg(version)}`)).body;
  }

  override async readme(packageId: string, version: string): Promise<PackageReadme | null> {
    try {
      return (
        await this.client.getJson<PackageReadme>(`${packageUrl(packageId)}/versions/${seg(version)}/readme`)
      ).body;
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) return null;
      throw e;
    }
  }
}

@Injectable()
export class HttpClientSetupApi extends ClientSetupApi {
  private readonly client = inject(ApiClient);

  override async get(repository: string, query: ClientSetupQuery): Promise<ClientSetup> {
    return (
      await this.client.getJson<ClientSetup>(`/api/v1/repositories/${seg(repository)}/client-setup`, {
        params: cleanQuery(query),
      })
    ).body;
  }
}
