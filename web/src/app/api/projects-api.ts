import { Injectable, inject } from '@angular/core';
import { ApiClient } from './client';
import { PageFetcher } from './pagination';
import { cleanQuery, seg } from './query';
import { Versioned } from './repositories-api';
import { Schema } from './types';

/*
 * Projects and their administrators (R-AUTHZ-026, R-UI-041; docs/developer/projects.md). A project
 * carries an `ETag` (its version): the replace needs it (`If-Match` is required, 412 opens the
 * conflict dialog of R-UI-042). The administrator and repository writes are plain PUT and DELETE
 * without a tag. Every write answers `Trove-Authz-Epoch`, which the HTTP layer keeps (R-AUTHZ-032).
 */

export type Project = Schema<'Project'>;
export type ProjectInput = Schema<'ProjectInput'>;
export type ProjectPrincipal = Schema<'ProjectPrincipal'>;
export type AdministeredProject = Schema<'AdministeredProject'>;

/** The key pattern of a project (R-AUTHZ-026), checked live in the dialog; the server decides. */
export const PROJECT_KEY_PATTERN = /^[a-z][a-z0-9-]{1,31}$/;

export interface ProjectFilters {
  readonly q?: string;
  /** `key` (default) or `-key`. */
  readonly sort?: 'key' | '-key';
}

export abstract class ProjectsApi {
  abstract list(filters: ProjectFilters): PageFetcher<Project>;
  abstract get(key: string): Promise<Versioned<Project>>;
  abstract create(input: ProjectInput, idempotencyKey: string): Promise<Versioned<Project>>;
  /** `PUT` with the loaded `ETag`; the body needs the display name and both switches. */
  abstract replace(key: string, input: ProjectInput, etag: string): Promise<Versioned<Project>>;
  abstract delete(key: string): Promise<void>;
  /** `principal` is `user:<name>` or `group:<name>`; idempotent. */
  abstract addAdmin(key: string, principal: string): Promise<void>;
  abstract removeAdmin(key: string, principal: string): Promise<void>;
  /**
   * `move` also takes a repository away from another project (409 `repository_assigned` otherwise).
   * `from` is the owner the caller read (null: none): the server answers 409 `project_owner_changed`
   * when the repository belongs to another project by then (Q-AUTHZ-205).
   */
  abstract assignRepository(key: string, repository: string, move: boolean, from: string | null): Promise<void>;
  abstract unassignRepository(key: string, repository: string): Promise<void>;
  /** Users and groups whose name starts with `q` (two characters at least), for the entries of a project policy. */
  abstract lookupPrincipals(
    key: string,
    q: string,
    kind?: ProjectPrincipal['kind'],
  ): Promise<readonly ProjectPrincipal[]>;
}

function versioned(r: { readonly body: Project; readonly etag: string | null }): Versioned<Project> {
  return { value: r.body, etag: r.etag ?? `"${r.body.version}"` };
}

@Injectable()
export class HttpProjectsApi extends ProjectsApi {
  private readonly client = inject(ApiClient);

  override list(filters: ProjectFilters): PageFetcher<Project> {
    return this.client.page<Project>('/api/v1/projects', cleanQuery(filters));
  }

  override async get(key: string): Promise<Versioned<Project>> {
    return versioned(await this.client.getJson<Project>(`/api/v1/projects/${seg(key)}`));
  }

  override async create(input: ProjectInput, idempotencyKey: string): Promise<Versioned<Project>> {
    return versioned(await this.client.post<Project>('/api/v1/projects', input, { idempotencyKey }));
  }

  override async replace(key: string, input: ProjectInput, etag: string): Promise<Versioned<Project>> {
    return versioned(await this.client.put<Project>(`/api/v1/projects/${seg(key)}`, input, etag));
  }

  override async delete(key: string): Promise<void> {
    await this.client.delete(`/api/v1/projects/${seg(key)}`);
  }

  override async addAdmin(key: string, principal: string): Promise<void> {
    await this.client.putBinding(`/api/v1/projects/${seg(key)}/admins/${seg(principal)}`);
  }

  override async removeAdmin(key: string, principal: string): Promise<void> {
    await this.client.delete(`/api/v1/projects/${seg(key)}/admins/${seg(principal)}`);
  }

  override async assignRepository(key: string, repository: string, move: boolean, from: string | null): Promise<void> {
    const query = [...(move ? ['move=true'] : []), `from=${encodeURIComponent(from ?? '')}`].join('&');
    await this.client.putBinding(`/api/v1/projects/${seg(key)}/repositories/${seg(repository)}?${query}`);
  }

  override async unassignRepository(key: string, repository: string): Promise<void> {
    await this.client.delete(`/api/v1/projects/${seg(key)}/repositories/${seg(repository)}`);
  }

  override async lookupPrincipals(
    key: string,
    q: string,
    kind?: ProjectPrincipal['kind'],
  ): Promise<readonly ProjectPrincipal[]> {
    const r = await this.client.getJson<{ items: ProjectPrincipal[] }>(
      `/api/v1/projects/${seg(key)}/principals`,
      { params: cleanQuery({ q, kind }) },
    );
    return r.body.items;
  }
}
