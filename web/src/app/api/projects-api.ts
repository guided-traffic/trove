import { Injectable, inject } from '@angular/core';
import { ApiClient } from './client';
import { PageFetcher } from './pagination';
import { cleanQuery, seg } from './query';
import { Versioned } from './repositories-api';
import { Schema } from './types';

/*
 * Projects and their members (R-AUTHZ-026, R-UI-041; docs/developer/projects.md). A project
 * carries an `ETag` (its version): the replace needs it (`If-Match` is required, 412 opens the
 * conflict dialog of R-UI-042). The member writes are plain PUT and DELETE without a tag. Every write answers `Trove-Authz-Epoch`, which the HTTP layer keeps (R-AUTHZ-032).
 */

export type Project = Schema<'Project'>;
export type ProjectInput = Schema<'ProjectInput'>;
export type ProjectPrincipal = Schema<'ProjectPrincipal'>;
export type ProjectRole = Schema<'ProjectRole'>;
export type ProjectMember = Schema<'Member'>;
/** A project the caller is a member of, with the role in force for the credential (`authz/me`). */
export type Membership = Schema<'Membership'>;

/** The roles of a member from the weakest to the strongest (Q-AUTHZ-208). */
export const PROJECT_ROLES: readonly ProjectRole[] = ['reader', 'developer', 'maintainer', 'owner'];

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
  /** `PUT` with the loaded `ETag`; the body needs the display name. */
  abstract replace(key: string, input: ProjectInput, etag: string): Promise<Versioned<Project>>;
  abstract delete(key: string): Promise<void>;
  /** The members, users and groups, each with its role (Q-AUTHZ-207). */
  abstract members(key: string): Promise<readonly ProjectMember[]>;
  /** Adds the principal (`user:<name>` or `group:<name>`) or changes its role; idempotent. 409 `last_owner` on the last Owner. */
  abstract setMember(key: string, principal: string, role: ProjectRole): Promise<ProjectMember>;
  /** 409 `last_owner` when it is the last Owner. */
  abstract removeMember(key: string, principal: string): Promise<void>;
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

  override async members(key: string): Promise<readonly ProjectMember[]> {
    return (await this.client.getJson<{ items: ProjectMember[] }>(`/api/v1/projects/${seg(key)}/members`)).body.items;
  }

  override async setMember(key: string, principal: string, role: ProjectRole): Promise<ProjectMember> {
    return (
      await this.client.putJson<ProjectMember>(`/api/v1/projects/${seg(key)}/members/${seg(principal)}`, { role })
    ).body;
  }

  override async removeMember(key: string, principal: string): Promise<void> {
    await this.client.delete(`/api/v1/projects/${seg(key)}/members/${seg(principal)}`);
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
