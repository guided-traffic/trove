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
export type MirrorStatus = Schema<'MirrorStatus'>;
export type MirrorState = MirrorStatus['state'];
export type UpstreamTestResult = Schema<'UpstreamTestResult'>;

/**
 * The body of `POST /api/v1/projects/{projectKey}/upstream-tests`: the format and an unsaved `remote` section. Secret values in
 * it are used once and stored nowhere (R-REPO-024, R-REPO-033). The section is typed loosely because the
 * form writes only the members that differ from their defaults.
 */
export interface UpstreamTestBody {
  readonly format: string;
  readonly remote: Readonly<Record<string, unknown>>;
}

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
  /** A project key: the repositories assigned to that project (R-AUTHZ-026). */
  readonly project?: string;
}

/** True when the answer is the narrower consumer view (the caller cannot read the configuration). */
export function isConsumerView(
  value: Repository | RepositoryConsumerView,
): value is RepositoryConsumerView {
  return 'addresses' in value;
}

/**
 * A repository is named `<project>/<key>` outside its project (R-REPO-002, Q-REPO-212); every API client of the UI takes
 * that qualified name as the identity of a repository. A project key and a repository key hold no slash.
 */
export function repoRef(project: string, key: string): string {
  return `${project}/${key}`;
}

/** The qualified name of a repository or summary as the API returns it. */
export function refOf(repository: { readonly project?: string; readonly key: string }): string {
  return repoRef(repository.project ?? '', repository.key);
}

/** The two parts of a qualified repository name; a name without a project has an empty `project`. */
export function splitRef(ref: string): { readonly project: string; readonly key: string } {
  const at = ref.indexOf('/');
  return at < 0 ? { project: '', key: ref } : { project: ref.slice(0, at), key: ref.slice(at + 1) };
}

/** `/api/v1/projects/{projectKey}/repositories/{repoKey}` of a qualified repository name. */
export function repositoryUrl(ref: string): string {
  const { project, key } = splitRef(ref);
  return `/api/v1/projects/${encodeURIComponent(project)}/repositories/${encodeURIComponent(key)}`;
}

/** Repositories and the format catalog: R-UI-020, R-UI-021, R-UI-010. */
export abstract class RepositoriesApi {
  /** `GET /api/v1/formats`; the only source of the classes a format offers (R-UI-021). */
  abstract formats(): Promise<readonly FormatDescriptor[]>;
  /**
   * The `baseUrl` member of the same answer: the configured base URL of the endpoint the page was
   * served from, or `null` when none is configured (Q-UI-75). Every caller that may list the formats
   * may read it.
   */
  abstract baseUrl(): Promise<string | null>;
  /** One page fetcher over the summary view; a new filter set is a new fetcher (cursor binding). */
  abstract list(filters: RepositoryFilters): PageFetcher<RepositorySummary>;
  /** Every readable repository, for pickers and the tree root. */
  abstract listAll(): Promise<readonly RepositorySummary[]>;
  /** `ref` is the qualified name `<project>/<key>`; so it is in every method below. */
  abstract get(ref: string): Promise<Versioned<Repository | RepositoryConsumerView>>;
  /** `POST /api/v1/projects/{project}/repositories`: the repository is created in the project (R-REPO-001). */
  abstract create(project: string, doc: Repository, idempotencyKey: string): Promise<Versioned<Repository>>;
  /** JSON merge patch with the loaded ETag. */
  abstract patch(
    ref: string,
    patch: Readonly<Record<string, unknown>>,
    etag: string,
  ): Promise<Versioned<Repository>>;
  /** `detach` also removes the repository from the virtual repositories that include it. */
  abstract delete(ref: string, idempotencyKey: string, detach?: boolean): Promise<DeleteOutcome>;
  /**
   * `POST /api/v1/projects/{project}/repositories/{key}/move`: the repository in the target project (R-REPO-060); it
   * keeps its key, so the answer has the new qualified name.
   */
  abstract move(ref: string, targetProject: string, idempotencyKey: string): Promise<Versioned<Repository>>;
  /** `GET …/repositories/{key}/mirror`: the status of a remote repository's mirror (R-REPO-056). */
  abstract mirrorStatus(ref: string): Promise<MirrorStatus>;
  /** `POST …/repositories/{key}/mirror/run`: requests a pass now; the job that was queued (R-REPO-056). */
  abstract runMirror(ref: string, full: boolean, idempotencyKey: string): Promise<JobReference>;
  /** `POST /api/v1/projects/{project}/upstream-tests`: probes an unsaved `remote` section (R-REPO-024). */
  abstract testUpstream(project: string, body: UpstreamTestBody): Promise<UpstreamTestResult>;
}

@Injectable()
export class HttpRepositoriesApi extends RepositoriesApi {
  private readonly client = inject(ApiClient);

  async formats(): Promise<readonly FormatDescriptor[]> {
    return (await this.client.get('/api/v1/formats')).body.formats;
  }

  async baseUrl(): Promise<string | null> {
    return (await this.client.get('/api/v1/formats')).body.baseUrl;
  }

  list(filters: RepositoryFilters): PageFetcher<RepositorySummary> {
    const params: Record<string, string | undefined> = {
      q: filters.q || undefined,
      class: filters.class || undefined,
      format: filters.format || undefined,
      state: filters.state || undefined,
      project: filters.project || undefined,
    };
    return this.client.page<RepositorySummary>('/api/v1/repositories', params as QueryParams);
  }

  listAll(): Promise<readonly RepositorySummary[]> {
    return collectAll(this.list({}));
  }

  async get(ref: string): Promise<Versioned<Repository | RepositoryConsumerView>> {
    const r = await this.client.getJson<Repository | RepositoryConsumerView>(repositoryUrl(ref));
    return { value: r.body, etag: r.etag ?? '' };
  }

  async create(project: string, doc: Repository, idempotencyKey: string): Promise<Versioned<Repository>> {
    const r = await this.client.post<Repository>(
      `/api/v1/projects/${encodeURIComponent(project)}/repositories`,
      doc,
      { idempotencyKey },
    );
    return { value: r.body, etag: r.etag ?? '' };
  }

  async move(ref: string, targetProject: string, idempotencyKey: string): Promise<Versioned<Repository>> {
    const r = await this.client.post<Repository>(
      `${repositoryUrl(ref)}/move`,
      { project: targetProject },
      { idempotencyKey },
    );
    return { value: r.body, etag: r.etag ?? '' };
  }

  async patch(
    ref: string,
    patch: Readonly<Record<string, unknown>>,
    etag: string,
  ): Promise<Versioned<Repository>> {
    const r = await this.client.patch<Repository>(repositoryUrl(ref), patch, etag);
    return { value: r.body, etag: r.etag ?? '' };
  }

  async delete(ref: string, idempotencyKey: string, detach = false): Promise<DeleteOutcome> {
    const r = await this.client.delete<{ job: JobReference } | null>(repositoryUrl(ref), {
      idempotencyKey,
      params: detach ? { detach: true } : undefined,
    });
    return r.status === 202 && r.body ? { done: false, job: r.body.job } : { done: true };
  }

  async mirrorStatus(ref: string): Promise<MirrorStatus> {
    return (await this.client.getJson<MirrorStatus>(`${repositoryUrl(ref)}/mirror`)).body;
  }

  async runMirror(ref: string, full: boolean, idempotencyKey: string): Promise<JobReference> {
    const r = await this.client.post<{ job: JobReference }>(
      `${repositoryUrl(ref)}/mirror/run`,
      { full },
      { idempotencyKey },
    );
    return r.body.job;
  }

  async testUpstream(project: string, body: UpstreamTestBody): Promise<UpstreamTestResult> {
    return (
      await this.client.post<UpstreamTestResult>(
        `/api/v1/projects/${encodeURIComponent(project)}/upstream-tests`,
        body,
      )
    ).body;
  }
}
