import { Injectable, inject } from '@angular/core';
import { ApiClient } from './client';
import { PageFetcher } from './pagination';
import { cleanQuery, seg } from './query';
import { Schema } from './types';

/*
 * Permission policies, roles and role bindings (R-AUTHZ-021, R-AUTHZ-022; docs/developer/authorization.md).
 * Every write answers `Trove-Authz-Epoch`; the HTTP layer keeps the highest one of the tab and sends it
 * as `Trove-Authz-Min-Epoch` on whatever request follows (R-AUTHZ-032, R-UI-042), so the reads that a
 * screen issues after a write need no extra code.
 */

export type PermissionPolicy = Schema<'PermissionPolicy'>;
export type PermissionPolicyInput = Schema<'PermissionPolicyInput'>;
export type PolicyEntry = Schema<'PolicyEntry'>;
export type PolicyResources = Schema<'PolicyResources'>;
export type PolicyValidation = Schema<'PolicyValidation'>;
export type PolicyProblem = Schema<'PolicyProblem'>;
export type Role = Schema<'Role'>;
export type RoleName = Schema<'RoleName'>;
export type RoleBinding = Schema<'RoleBinding'>;

export interface PolicyFilters {
  /** A principal reference such as `group:team-web`. */
  readonly principal?: string;
  readonly expandGroups?: boolean;
  /** A repository key. */
  readonly repository?: string;
  readonly sort?: 'name' | '-name';
}

export interface LoadedPolicy {
  readonly policy: PermissionPolicy;
  readonly etag: string;
}

export interface RoleBindingFilters {
  readonly role?: RoleName;
  readonly principal?: string;
}

export abstract class PolicyApi {
  abstract list(filters: PolicyFilters): PageFetcher<PermissionPolicy>;
  abstract load(name: string): Promise<LoadedPolicy>;
  abstract create(input: PermissionPolicyInput, idempotencyKey: string): Promise<LoadedPolicy>;
  /** `PUT` with the loaded `ETag` (R-UI-027). */
  abstract replace(name: string, input: PermissionPolicyInput, etag: string): Promise<LoadedPolicy>;
  abstract delete(name: string, etag?: string): Promise<void>;
  /** `POST /authz/validate-policy`: problems, warnings and the covered repositories, nothing stored. */
  abstract validate(input: PermissionPolicyInput): Promise<PolicyValidation>;
}

export abstract class RolesApi {
  abstract roles(): Promise<readonly Role[]>;
  abstract bindings(filters: RoleBindingFilters): PageFetcher<RoleBinding>;
  abstract bind(role: RoleName, principal: string): Promise<RoleBinding>;
  abstract unbind(role: RoleName, principal: string): Promise<void>;
}

@Injectable()
export class HttpPolicyApi extends PolicyApi {
  private readonly client = inject(ApiClient);

  override list(filters: PolicyFilters): PageFetcher<PermissionPolicy> {
    return this.client.page<PermissionPolicy>('/api/v1/permission-policies', cleanQuery(filters));
  }

  override async load(name: string): Promise<LoadedPolicy> {
    const r = await this.client.getJson<PermissionPolicy>(`/api/v1/permission-policies/${seg(name)}`);
    return { policy: r.body, etag: r.etag ?? `"${r.body.version}"` };
  }

  override async create(input: PermissionPolicyInput, idempotencyKey: string): Promise<LoadedPolicy> {
    const r = await this.client.post<PermissionPolicy>('/api/v1/permission-policies', input, {
      idempotencyKey,
    });
    return { policy: r.body, etag: r.etag ?? `"${r.body.version}"` };
  }

  override async replace(name: string, input: PermissionPolicyInput, etag: string): Promise<LoadedPolicy> {
    const r = await this.client.put<PermissionPolicy>(
      `/api/v1/permission-policies/${seg(name)}`,
      input,
      etag,
    );
    return { policy: r.body, etag: r.etag ?? `"${r.body.version}"` };
  }

  override async delete(name: string, etag?: string): Promise<void> {
    await this.client.delete(`/api/v1/permission-policies/${seg(name)}`, { etag });
  }

  override async validate(input: PermissionPolicyInput): Promise<PolicyValidation> {
    return (await this.client.post<PolicyValidation>('/api/v1/authz/validate-policy', input)).body;
  }
}

@Injectable()
export class HttpRolesApi extends RolesApi {
  private readonly client = inject(ApiClient);

  override async roles(): Promise<readonly Role[]> {
    return (await this.client.get('/api/v1/roles')).body.items;
  }

  override bindings(filters: RoleBindingFilters): PageFetcher<RoleBinding> {
    return this.client.page<RoleBinding>('/api/v1/role-bindings', cleanQuery(filters));
  }

  override async bind(role: RoleName, principal: string): Promise<RoleBinding> {
    return (await this.client.putBinding<RoleBinding>(`/api/v1/role-bindings/${seg(role)}/${seg(principal)}`))
      .body;
  }

  override async unbind(role: RoleName, principal: string): Promise<void> {
    await this.client.delete(`/api/v1/role-bindings/${seg(role)}/${seg(principal)}`);
  }
}
