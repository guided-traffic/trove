import { Injectable, inject } from '@angular/core';
import { ApiClient } from './client';
import { PageFetcher } from './pagination';
import { ApiError } from './problem';
import { repositoryUrl } from './repositories-api';
import { cleanQuery, seg } from './query';
import { Schema } from './types';

/* Packages, versions, README and client setup: R-API-029, R-API-030. */

export type PackageSummary = Schema<'PackageSummary'>;
export type PackageVersionSummary = Schema<'PackageVersionSummary'>;
export type PackageVersion = Schema<'PackageVersion'>;
export type PackageFile = Schema<'PackageFile'>;
export type PackageReadme = Schema<'PackageReadme'>;
export type PackageDocTree = Schema<'PackageDocTree'>;
export type PackageDocGroup = Schema<'PackageDocGroup'>;
export type PackageDocType = Schema<'PackageDocType'>;
export type PackageDocEntryHead = Schema<'PackageDocEntryHead'>;
export type PackageDocEntry = Schema<'PackageDocEntry'>;
export type PackageDocOption = Schema<'PackageDocOption'>;
export type PackageDocReturnValue = Schema<'PackageDocReturnValue'>;
export type PackageDocFile = Schema<'PackageDocFile'>;
export type PackageVersionDocs = Schema<'PackageVersionDocs'>;
export type ClientSetup = Schema<'ClientSetup'>;
export type ClientSnippet = Schema<'ClientSnippet'>;

/** The sort fields `GET /api/v1/packages` offers (R-API-029); a leading `-` sorts descending. */
export type PackageSort = 'name' | '-name' | 'createdAt' | '-createdAt' | 'updatedAt' | '-updatedAt';

/** The filters of `GET /api/v1/packages`; all optional. */
export interface PackageFilters {
  /** A project key: only the packages in that project's repositories (Q-UI-129). */
  readonly project?: string;
  readonly format?: readonly string[];
  readonly repository?: readonly string[];
  readonly q?: string;
  readonly namePrefix?: string;
  readonly keyword?: string;
  readonly createdFrom?: string;
  readonly createdTo?: string;
  readonly sort?: PackageSort;
  /** `vulnerabilities` adds the counts of open findings per entry (R-UI-052). */
  readonly include?: readonly string[];
}

export interface VersionFilters {
  readonly prerelease?: boolean;
  readonly repository?: readonly string[];
  readonly sort?: '-publishedAt' | 'publishedAt' | '-version' | 'version';
  /** `digest` adds each version's content digest from the format (R-UI-052). */
  readonly include?: readonly string[];
}

/** The query of `GET …/docs` (R-ANS-036): the repository to read from (`<projectKey>/<repoKey>`) and the search text. */
export interface DocsQuery {
  readonly repository?: string;
  readonly q?: string;
}

/** The query of `GET …/client-setup` (R-API-030). */
export interface ClientSetupQuery {
  readonly client: string;
  readonly credentials?: 'placeholder' | 'env' | 'none';
  readonly package?: string;
  readonly version?: string;
  readonly scope?: string;
  /** The name of a Bastion registration: the snippets are for that Bastion (R-BAS-007). */
  readonly bastion?: string;
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
  /**
   * The documentation tree of a version (R-ANS-036), or `null` for a 404 or a refusal: the documentation is wiki content and a
   * caller who may not read it is told what it is told for a version that does not exist, so the tab goes without a message (Q-UI-244).
   */
  abstract docs(packageId: string, version: string, query?: DocsQuery): Promise<PackageDocTree | null>;
  /** One page of the documentation: a module, a plugin or a role. */
  abstract docEntry(
    packageId: string,
    version: string,
    type: string,
    name: string,
    repository?: string,
  ): Promise<PackageDocEntry>;
  /** The README or a Markdown file of the documentation as sanitized HTML. */
  abstract docFile(packageId: string, version: string, file: string, repository?: string): Promise<PackageDocFile>;
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

  override async docs(packageId: string, version: string, query: DocsQuery = {}): Promise<PackageDocTree | null> {
    try {
      return (
        await this.client.getJson<PackageDocTree>(`${packageUrl(packageId)}/versions/${seg(version)}/docs`, {
          params: cleanQuery(query),
        })
      ).body;
    } catch (e) {
      if (e instanceof ApiError && (e.status === 404 || e.status === 403)) return null;
      throw e;
    }
  }

  override async docEntry(
    packageId: string,
    version: string,
    type: string,
    name: string,
    repository?: string,
  ): Promise<PackageDocEntry> {
    return (
      await this.client.getJson<PackageDocEntry>(
        `${packageUrl(packageId)}/versions/${seg(version)}/docs/entries/${seg(type)}/${seg(name)}`,
        { params: cleanQuery({ repository }) },
      )
    ).body;
  }

  override async docFile(
    packageId: string,
    version: string,
    file: string,
    repository?: string,
  ): Promise<PackageDocFile> {
    return (
      await this.client.getJson<PackageDocFile>(
        `${packageUrl(packageId)}/versions/${seg(version)}/docs/files/${seg(file)}`,
        { params: cleanQuery({ repository }) },
      )
    ).body;
  }
}

@Injectable()
export class HttpClientSetupApi extends ClientSetupApi {
  private readonly client = inject(ApiClient);

  override async get(repository: string, query: ClientSetupQuery): Promise<ClientSetup> {
    return (
      await this.client.getJson<ClientSetup>(`${repositoryUrl(repository)}/client-setup`, {
        params: cleanQuery(query),
      })
    ).body;
  }
}
