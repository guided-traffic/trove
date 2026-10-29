import { Injectable, inject } from '@angular/core';
import { ApiClient } from './client';
import { PageFetcher } from './pagination';
import { cleanQuery, seg } from './query';
import { Versioned } from './repositories-api';
import { Schema } from './types';

/*
 * Users, groups and service accounts (R-AUTH-016, R-AUTH-018, R-AUTH-020; docs/developer/identity.md).
 * The shapes are the generated ones. A user, a group and a service account carry an `ETag`
 * (Q-API-42, Q-API-43): the reads return it with the body, a PATCH is a merge patch of the members
 * it names (R-API-013) sent with the loaded tag, and a stale tag is a 412 that opens the conflict
 * dialog. Membership changes, disable, enable and unlock carry no tag; disable, enable and unlock
 * answer 204 and the screens read the record again.
 */

export type User = Schema<'User'>;
export type NewUser = Schema<'NewUser'>;
export type UserPatch = Schema<'UserPatch'>;
export type Group = Schema<'Group'>;
export type NewGroup = Schema<'NewGroup'>;
export type GroupPatch = Schema<'GroupPatch'>;
export type GroupMember = Schema<'GroupMember'>;
export type ServiceAccount = Schema<'ServiceAccount'>;
export type NewServiceAccount = Schema<'NewServiceAccount'>;
export type MembershipChange = Schema<'MembershipChange'>;
export type UserGroupMembership = Schema<'UserGroupMembership'>;

export interface UserFilters {
  readonly q?: string;
  readonly status?: 'active' | 'disabled' | 'locked';
  readonly origin?: User['origin'];
  readonly group?: string;
  readonly admin?: boolean;
}

export interface GroupFilters {
  readonly q?: string;
  readonly origin?: Group['origin'];
}

export interface ServiceAccountFilters {
  readonly q?: string;
}

export interface DisableOptions {
  readonly reason?: string;
  readonly revokeTokens?: boolean;
}

export interface GroupMembersChange {
  readonly add?: readonly string[];
  readonly remove?: readonly string[];
  readonly source?: 'manual' | 'auto';
}

export abstract class IdentityApi {
  abstract me(): Promise<User>;

  abstract users(filters: UserFilters): PageFetcher<User>;
  abstract user(username: string): Promise<Versioned<User>>;
  abstract createUser(body: NewUser, idempotencyKey: string): Promise<User>;
  abstract updateUser(username: string, patch: UserPatch, etag: string): Promise<Versioned<User>>;
  abstract deleteUser(username: string): Promise<void>;
  abstract disableUser(username: string, options: DisableOptions): Promise<void>;
  abstract enableUser(username: string): Promise<void>;
  abstract changeUserGroups(
    username: string,
    change: MembershipChange,
  ): Promise<readonly UserGroupMembership[]>;
  abstract unlockUser(username: string): Promise<void>;

  abstract groups(filters: GroupFilters): PageFetcher<Group>;
  abstract group(name: string): Promise<Versioned<Group>>;
  abstract createGroup(body: NewGroup, idempotencyKey: string): Promise<Group>;
  abstract updateGroup(name: string, patch: GroupPatch, etag: string): Promise<Versioned<Group>>;
  abstract deleteGroup(name: string): Promise<void>;
  abstract groupMembers(name: string, source?: string): PageFetcher<GroupMember>;
  abstract changeGroupMembers(name: string, change: GroupMembersChange): Promise<readonly GroupMember[]>;

  abstract serviceAccounts(filters: ServiceAccountFilters): PageFetcher<ServiceAccount>;
  abstract serviceAccount(name: string): Promise<Versioned<ServiceAccount>>;
  abstract createServiceAccount(body: NewServiceAccount, idempotencyKey: string): Promise<ServiceAccount>;
  abstract updateServiceAccount(
    name: string,
    patch: { description?: string },
    etag: string,
  ): Promise<Versioned<ServiceAccount>>;
  abstract deleteServiceAccount(name: string): Promise<void>;
  abstract disableServiceAccount(name: string, options: DisableOptions): Promise<void>;
  abstract enableServiceAccount(name: string): Promise<void>;
}

function versioned<T>(r: { readonly body: T; readonly etag: string | null }): Versioned<T> {
  return { value: r.body, etag: r.etag ?? '' };
}

@Injectable()
export class HttpIdentityApi extends IdentityApi {
  private readonly client = inject(ApiClient);

  override async me(): Promise<User> {
    return (await this.client.get('/api/v1/users/me')).body;
  }

  override users(filters: UserFilters): PageFetcher<User> {
    return this.client.page<User>('/api/v1/users', cleanQuery(filters));
  }

  override async user(username: string): Promise<Versioned<User>> {
    return versioned(await this.client.getJson<User>(`/api/v1/users/${seg(username)}`));
  }

  override async createUser(body: NewUser, idempotencyKey: string): Promise<User> {
    return (await this.client.post<User>('/api/v1/users', body, { idempotencyKey })).body;
  }

  override async updateUser(username: string, patch: UserPatch, etag: string): Promise<Versioned<User>> {
    return versioned(await this.client.patch<User>(`/api/v1/users/${seg(username)}`, patch, etag));
  }

  override async deleteUser(username: string): Promise<void> {
    await this.client.delete(`/api/v1/users/${seg(username)}`);
  }

  override async disableUser(username: string, options: DisableOptions): Promise<void> {
    await this.client.post(`/api/v1/users/${seg(username)}/disable`, options);
  }

  override async enableUser(username: string): Promise<void> {
    await this.client.post(`/api/v1/users/${seg(username)}/enable`);
  }

  override async changeUserGroups(
    username: string,
    change: MembershipChange,
  ): Promise<readonly UserGroupMembership[]> {
    const r = await this.client.patchJson<{ groups: UserGroupMembership[] }>(
      `/api/v1/users/${seg(username)}/groups`,
      change,
    );
    return r.body.groups;
  }

  override async unlockUser(username: string): Promise<void> {
    await this.client.post(`/api/v1/users/${seg(username)}/unlock`);
  }

  override groups(filters: GroupFilters): PageFetcher<Group> {
    return this.client.page<Group>('/api/v1/groups', cleanQuery(filters));
  }

  override async group(name: string): Promise<Versioned<Group>> {
    return versioned(await this.client.getJson<Group>(`/api/v1/groups/${seg(name)}`));
  }

  override async createGroup(body: NewGroup, idempotencyKey: string): Promise<Group> {
    return (await this.client.post<Group>('/api/v1/groups', body, { idempotencyKey })).body;
  }

  override async updateGroup(name: string, patch: GroupPatch, etag: string): Promise<Versioned<Group>> {
    return versioned(await this.client.patch<Group>(`/api/v1/groups/${seg(name)}`, patch, etag));
  }

  override async deleteGroup(name: string): Promise<void> {
    await this.client.delete(`/api/v1/groups/${seg(name)}`);
  }

  override groupMembers(name: string, source?: string): PageFetcher<GroupMember> {
    return this.client.page<GroupMember>(`/api/v1/groups/${seg(name)}/members`, cleanQuery({ source }));
  }

  override async changeGroupMembers(
    name: string,
    change: GroupMembersChange,
  ): Promise<readonly GroupMember[]> {
    const r = await this.client.patchJson<{ items: GroupMember[] }>(
      `/api/v1/groups/${seg(name)}/members`,
      change,
    );
    return r.body.items;
  }

  override serviceAccounts(filters: ServiceAccountFilters): PageFetcher<ServiceAccount> {
    return this.client.page<ServiceAccount>('/api/v1/service-accounts', cleanQuery(filters));
  }

  override async serviceAccount(name: string): Promise<Versioned<ServiceAccount>> {
    return versioned(await this.client.getJson<ServiceAccount>(`/api/v1/service-accounts/${seg(name)}`));
  }

  override async createServiceAccount(
    body: NewServiceAccount,
    idempotencyKey: string,
  ): Promise<ServiceAccount> {
    return (await this.client.post<ServiceAccount>('/api/v1/service-accounts', body, { idempotencyKey }))
      .body;
  }

  override async updateServiceAccount(
    name: string,
    patch: { description?: string },
    etag: string,
  ): Promise<Versioned<ServiceAccount>> {
    return versioned(
      await this.client.patch<ServiceAccount>(`/api/v1/service-accounts/${seg(name)}`, patch, etag),
    );
  }

  override async deleteServiceAccount(name: string): Promise<void> {
    await this.client.delete(`/api/v1/service-accounts/${seg(name)}`);
  }

  override async disableServiceAccount(name: string, options: DisableOptions): Promise<void> {
    await this.client.post(`/api/v1/service-accounts/${seg(name)}/disable`, options);
  }

  override async enableServiceAccount(name: string): Promise<void> {
    await this.client.post(`/api/v1/service-accounts/${seg(name)}/enable`);
  }
}
