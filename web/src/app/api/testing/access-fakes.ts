import { Provider } from '@angular/core';
import { AuthSettingsApi, AuthSettingsPatch, LoadedAuthSettings } from '../auth-settings-api';
import {
  LoadedPolicy,
  PermissionPolicy,
  PermissionPolicyInput,
  PolicyApi,
  PolicyFilters,
  PolicyValidation,
  Role,
  RoleBinding,
  RoleBindingFilters,
  RoleName,
  RolesApi,
} from '../authz-api';
import {
  DisableOptions,
  Group,
  GroupFilters,
  GroupMember,
  GroupMembersChange,
  GroupPatch,
  IdentityApi,
  MembershipChange,
  NewGroup,
  NewServiceAccount,
  NewUser,
  ServiceAccount,
  ServiceAccountFilters,
  User,
  UserFilters,
  UserGroupMembership,
  UserPatch,
} from '../identity-api';
import { Page, PageFetcher } from '../pagination';
import { DraftPolicyExplainer, PolicyExplainer } from '../policy-explainer';
import { ApiError } from '../problem';
import { Versioned } from '../repositories-api';
import { SessionsApi, UserSession } from '../sessions-api';
import {
  CreateTokenRequest,
  RevokedToken,
  Token,
  TokenCreated,
  TokenFilters,
  TokensApi,
} from '../tokens-api';

/*
 * In-memory fakes of the identity, token, session, settings, policy and role services (R-UI-047
 * component tier). They keep state across calls so that a screen can be driven through a whole
 * journey; every mutating call is also appended to `calls` for assertions.
 */

/** A page fetcher over a fixed array; the cursor is the offset. */
export function arrayPages<T>(items: () => readonly T[]): PageFetcher<T> {
  return async (cursor, limit): Promise<Page<T>> => {
    const all = items();
    const start = cursor === null ? 0 : Number.parseInt(cursor, 10);
    const end = start + limit;
    return { items: all.slice(start, end), nextCursor: end < all.length ? String(end) : null };
  };
}

export function problem(status: number, code: string, extra: Record<string, unknown> = {}): ApiError {
  return new ApiError(status, { code, ...extra }, 'req-1');
}

export function sampleUser(over: Partial<User> = {}): User {
  return {
    id: '00000000-0000-7000-8000-000000000001',
    username: 'alice',
    email: 'alice@example.com',
    emailVerified: true,
    displayName: 'Alice Example',
    origin: 'jit',
    status: 'active',
    disabledReason: null,
    isAdmin: true,
    adminVia: ['direct'],
    identities: [{ provider: 'corp', linked: true }],
    groups: [
      { name: 'team-web', source: 'oidc:corp' },
      { name: 'platform', source: 'manual' },
    ],
    groupClaimMissing: null,
    lockedUntil: null,
    lastLoginAt: '2026-10-28T09:12:00Z',
    createdAt: '2026-09-01T08:00:00Z',
    ...over,
  };
}

export function sampleToken(over: Partial<Token> = {}): Token {
  return {
    id: '00000000-0000-7000-8000-0000000000a1',
    type: 'pat',
    hint: 'trv_pat_3kQ9',
    owner: { kind: 'user', name: 'alice' },
    description: 'laptop npm',
    scope: { inherit: true, admin: false, repositories: [], system: [] },
    unknownEndpoints: [],
    createdAt: '2026-10-01T10:00:00Z',
    createdBy: null,
    createdVia: 'api',
    expiresAt: '2027-01-01T10:00:00Z',
    lastUsedAt: null,
    lastUsedAddr: null,
    state: 'active',
    revokedAt: null,
    revokedBy: null,
    revocationReason: null,
    rotatedFrom: null,
    selfRotatable: false,
    ciContext: null,
    ...over,
  };
}

export const SECRET_VALUE = 'trv_pat_ABCDEFGHIJKLMNOPQRSTUVWXYZ012345';

export class FakeIdentityApi extends IdentityApi {
  readonly calls: string[] = [];
  /** Page size of the list fetchers, to exercise "Load more". */
  pageSize = 50;
  meResult: User | ApiError = sampleUser();
  userList: User[] = [
    sampleUser(),
    sampleUser({
      id: '00000000-0000-7000-8000-000000000002',
      username: 'bob',
      displayName: 'Bob Example',
      email: 'bob@example.com',
      isAdmin: false,
      adminVia: [],
      groups: [],
      status: 'disabled',
      disabledReason: 'admin',
    }),
  ];
  groupList: Group[] = [
    {
      id: '00000000-0000-7000-8000-000000000011',
      name: 'platform',
      description: 'Platform team',
      autoJoin: false,
      origin: 'admin',
      memberCount: 1,
      roles: ['repository-admin'],
      createdAt: '2026-09-01T08:00:00Z',
    },
    {
      id: '00000000-0000-7000-8000-000000000012',
      name: 'team-web',
      description: 'Web team',
      autoJoin: false,
      origin: 'oidc',
      memberCount: 2,
      roles: [],
      createdAt: '2026-09-01T08:00:00Z',
    },
    {
      id: '00000000-0000-7000-8000-000000000013',
      name: 'scim-sales',
      description: 'Managed by SCIM',
      autoJoin: false,
      origin: 'scim',
      memberCount: 0,
      roles: [],
      createdAt: '2026-09-01T08:00:00Z',
    },
  ];
  members: Record<string, GroupMember[]> = {
    platform: [{ principal: 'alice', kind: 'user', sources: ['manual'] }],
    'team-web': [
      { principal: 'alice', kind: 'user', sources: ['oidc:corp'] },
      { principal: 'sa:ci-bot', kind: 'service_account', sources: ['manual', 'oidc:corp'] },
    ],
  };
  accounts: ServiceAccount[] = [
    {
      id: '00000000-0000-7000-8000-000000000021',
      name: 'ci-bot',
      description: 'Deploy jobs',
      status: 'active',
      createdAt: '2026-09-01T08:00:00Z',
    },
  ];
  failure: ApiError | null = null;
  keys: string[] = [];
  /** The `If-Match` of every guarded write, in order, for assertions. */
  readonly ifMatch: string[] = [];
  private readonly revisions = new Map<string, number>();

  /** The `ETag` the fake serves for `user:alice`, `group:platform` or `service-account:ci-bot`. */
  etagOf(key: string): string {
    return `"${this.revisions.get(key) ?? 1}"`;
  }

  /** Simulates a write by someone else: the stored revision moves on. */
  bumpRevision(key: string): void {
    this.revisions.set(key, (this.revisions.get(key) ?? 1) + 1);
  }

  /** The precondition of a PATCH: a stale tag is a 412 with the current one (Q-API-42). */
  private guard(key: string, etag: string): void {
    this.ifMatch.push(etag);
    if (etag !== this.etagOf(key)) throw problem(412, 'precondition_failed', { currentEtag: this.etagOf(key) });
    this.bumpRevision(key);
  }

  private fail(): void {
    if (this.failure) {
      const e = this.failure;
      this.failure = null;
      throw e;
    }
  }

  override async me(): Promise<User> {
    if (this.meResult instanceof ApiError) throw this.meResult;
    return this.meResult;
  }

  override users(filters: UserFilters): PageFetcher<User> {
    return (cursor, limit) =>
      arrayPages(() =>
        this.userList.filter(
          (u) =>
            (!filters.q || u.username.includes(filters.q)) &&
            (!filters.status || u.status === filters.status) &&
            (!filters.origin || u.origin === filters.origin),
        ),
      )(cursor, Math.min(limit, this.pageSize));
  }

  override async user(username: string): Promise<Versioned<User>> {
    const found = this.userList.find((u) => u.username === username);
    if (!found) throw problem(404, 'not_found');
    return { value: found, etag: this.etagOf(`user:${username}`) };
  }

  override async createUser(body: NewUser, idempotencyKey: string): Promise<User> {
    this.fail();
    this.keys.push(idempotencyKey);
    this.calls.push(`createUser ${body.username}`);
    const created = sampleUser({
      id: `u-${body.username}`,
      username: body.username,
      email: body.email ?? null,
      displayName: body.displayName ?? null,
      origin: 'admin',
      isAdmin: false,
      adminVia: [],
      groups: [],
      identities: (body.identities ?? []).map((i) => ({ provider: i.provider, linked: false })),
    });
    this.userList = [...this.userList, created];
    return created;
  }

  override async updateUser(username: string, patch: UserPatch, etag: string): Promise<Versioned<User>> {
    this.fail();
    this.calls.push(`updateUser ${username} ${JSON.stringify(patch)}`);
    const current = (await this.user(username)).value;
    this.guard(`user:${username}`, etag);
    const next: User = {
      ...current,
      email: patch.email === undefined ? current.email : patch.email,
      displayName: patch.displayName === undefined ? current.displayName : patch.displayName,
    };
    this.userList = this.userList.map((u) => (u.username === username ? next : u));
    return { value: next, etag: this.etagOf(`user:${username}`) };
  }

  override async deleteUser(username: string): Promise<void> {
    this.fail();
    this.calls.push(`deleteUser ${username}`);
    this.userList = this.userList.filter((u) => u.username !== username);
  }

  override async disableUser(username: string, options: DisableOptions): Promise<void> {
    this.fail();
    this.calls.push(`disableUser ${username} revokeTokens=${options.revokeTokens}`);
    this.userList = this.userList.map((u) =>
      u.username === username ? { ...u, status: 'disabled', disabledReason: 'admin' } : u,
    );
  }

  override async enableUser(username: string): Promise<void> {
    this.fail();
    this.calls.push(`enableUser ${username}`);
    this.userList = this.userList.map((u) =>
      u.username === username ? { ...u, status: 'active', disabledReason: null } : u,
    );
  }

  override async changeUserGroups(
    username: string,
    change: MembershipChange,
  ): Promise<readonly UserGroupMembership[]> {
    this.fail();
    this.calls.push(`changeUserGroups ${username} +${(change.add ?? []).join(',')} -${(change.remove ?? []).join(',')}`);
    const current = (await this.user(username)).value;
    const kept = current.groups.filter((g) => !(change.remove ?? []).includes(g.name));
    const added = (change.add ?? []).map((name) => ({ name, source: 'manual' }));
    const groups = [...kept, ...added];
    this.userList = this.userList.map((u) => (u.username === username ? { ...u, groups } : u));
    return groups;
  }

  override async unlockUser(username: string): Promise<void> {
    this.calls.push(`unlockUser ${username}`);
    this.userList = this.userList.map((u) => (u.username === username ? { ...u, lockedUntil: null } : u));
  }

  override groups(filters: GroupFilters): PageFetcher<Group> {
    return (cursor, limit) =>
      arrayPages(() =>
        this.groupList.filter(
          (g) => (!filters.q || g.name.includes(filters.q)) && (!filters.origin || g.origin === filters.origin),
        ),
      )(cursor, Math.min(limit, this.pageSize));
  }

  override async group(name: string): Promise<Versioned<Group>> {
    const found = this.groupList.find((g) => g.name === name);
    if (!found) throw problem(404, 'not_found');
    return {
      value: { ...found, memberCount: (this.members[name] ?? []).length },
      etag: this.etagOf(`group:${name}`),
    };
  }

  override async createGroup(body: NewGroup, idempotencyKey: string): Promise<Group> {
    this.fail();
    this.keys.push(idempotencyKey);
    this.calls.push(`createGroup ${body.name}`);
    const created: Group = {
      id: `g-${body.name}`,
      name: body.name,
      description: body.description ?? '',
      autoJoin: body.autoJoin ?? false,
      origin: 'admin',
      memberCount: 0,
      roles: [],
      createdAt: '2026-10-29T10:00:00Z',
    };
    this.groupList = [...this.groupList, created];
    return created;
  }

  override async updateGroup(name: string, patch: GroupPatch, etag: string): Promise<Versioned<Group>> {
    this.fail();
    this.calls.push(`updateGroup ${name} ${JSON.stringify(patch)}`);
    const current = (await this.group(name)).value;
    this.guard(`group:${name}`, etag);
    const next: Group = { ...current, ...patch };
    this.groupList = this.groupList.map((g) => (g.name === name ? next : g));
    if (patch.name && patch.name !== name) {
      this.members[patch.name] = this.members[name] ?? [];
    }
    return { value: next, etag: this.etagOf(`group:${name}`) };
  }

  override async deleteGroup(name: string): Promise<void> {
    this.fail();
    this.calls.push(`deleteGroup ${name}`);
    this.groupList = this.groupList.filter((g) => g.name !== name);
  }

  override groupMembers(name: string, source?: string): PageFetcher<GroupMember> {
    return (cursor, limit) =>
      arrayPages(() =>
        (this.members[name] ?? []).filter((m) => !source || m.sources.includes(source)),
      )(cursor, Math.min(limit, this.pageSize));
  }

  override async changeGroupMembers(
    name: string,
    change: GroupMembersChange,
  ): Promise<readonly GroupMember[]> {
    this.fail();
    this.calls.push(`changeGroupMembers ${name} +${(change.add ?? []).join(',')} -${(change.remove ?? []).join(',')}`);
    const list = this.members[name] ?? [];
    const removed = new Set(change.remove ?? []);
    const next = list.filter((m) => !removed.has(m.principal));
    for (const p of change.add ?? []) {
      next.push({
        principal: p,
        kind: p.startsWith('sa:') ? 'service_account' : 'user',
        sources: [change.source ?? 'manual'],
      });
    }
    this.members[name] = next;
    return next;
  }

  override serviceAccounts(filters: ServiceAccountFilters): PageFetcher<ServiceAccount> {
    return (cursor, limit) =>
      arrayPages(() => this.accounts.filter((a) => !filters.q || a.name.includes(filters.q)))(
        cursor,
        Math.min(limit, this.pageSize),
      );
  }

  override async serviceAccount(name: string): Promise<Versioned<ServiceAccount>> {
    const found = this.accounts.find((a) => a.name === name);
    if (!found) throw problem(404, 'not_found');
    return { value: found, etag: this.etagOf(`service-account:${name}`) };
  }

  override async createServiceAccount(
    body: NewServiceAccount,
    idempotencyKey: string,
  ): Promise<ServiceAccount> {
    this.fail();
    this.keys.push(idempotencyKey);
    this.calls.push(`createServiceAccount ${body.name}`);
    const created: ServiceAccount = {
      id: `sa-${body.name}`,
      name: body.name,
      description: body.description ?? '',
      status: 'active',
      createdAt: '2026-10-29T10:00:00Z',
    };
    this.accounts = [...this.accounts, created];
    return created;
  }

  override async updateServiceAccount(
    name: string,
    patch: { description?: string },
    etag: string,
  ): Promise<Versioned<ServiceAccount>> {
    this.fail();
    this.calls.push(`updateServiceAccount ${name}`);
    const current = (await this.serviceAccount(name)).value;
    this.guard(`service-account:${name}`, etag);
    const next = { ...current, ...patch };
    this.accounts = this.accounts.map((a) => (a.name === name ? next : a));
    return { value: next, etag: this.etagOf(`service-account:${name}`) };
  }

  override async deleteServiceAccount(name: string): Promise<void> {
    this.fail();
    this.calls.push(`deleteServiceAccount ${name}`);
    this.accounts = this.accounts.filter((a) => a.name !== name);
  }

  override async disableServiceAccount(name: string, options: DisableOptions): Promise<void> {
    this.fail();
    this.calls.push(`disableServiceAccount ${name} revokeTokens=${options.revokeTokens}`);
    this.accounts = this.accounts.map((a) => (a.name === name ? { ...a, status: 'disabled' } : a));
  }

  override async enableServiceAccount(name: string): Promise<void> {
    this.calls.push(`enableServiceAccount ${name}`);
    this.accounts = this.accounts.map((a) => (a.name === name ? { ...a, status: 'active' } : a));
  }
}

export class FakeTokensApi extends TokensApi {
  readonly calls: string[] = [];
  readonly keys: string[] = [];
  pageSize = 50;
  tokenList: Token[] = [
    sampleToken(),
    sampleToken({
      id: '00000000-0000-7000-8000-0000000000a2',
      description: 'ci deploy',
      type: 'sat',
      owner: { kind: 'service_account', name: 'ci-bot' },
      expiresAt: null,
      state: 'revoked',
      revokedAt: '2026-10-10T10:00:00Z',
    }),
  ];
  /** Rejects the next write once. */
  failure: ApiError | null = null;
  lastFilters: TokenFilters | null = null;
  lastRequest: CreateTokenRequest | null = null;

  private fail(): void {
    if (this.failure) {
      const e = this.failure;
      this.failure = null;
      throw e;
    }
  }

  override list(filters: TokenFilters): PageFetcher<Token> {
    this.lastFilters = filters;
    return (cursor, limit) =>
      arrayPages(() =>
        this.tokenList.filter(
          (t) =>
            (!filters.type || t.type === filters.type) &&
            (!filters.owner || filters.owner === 'me' || t.owner.name === filters.owner.replace('sa:', '')) &&
            (filters.state === 'all' || (filters.state ?? 'active') === t.state),
        ),
      )(cursor, Math.min(limit, this.pageSize));
  }

  private created(request: CreateTokenRequest, owner: Token['owner'], type: 'pat' | 'sat'): TokenCreated {
    this.lastRequest = request;
    const id = `00000000-0000-7000-8000-00000000b${this.keys.length}`;
    this.tokenList = [
      ...this.tokenList,
      sampleToken({ id, description: request.description, owner, type, scope: request.scope ?? {} }),
    ];
    return {
      id,
      type,
      token: SECRET_VALUE,
      hint: 'trv_pat_ABCD',
      owner,
      description: request.description,
      scope: request.scope ?? { inherit: true },
      createdAt: '2026-10-29T10:00:00Z',
      expiresAt: null,
      selfRotatable: false,
    };
  }

  override async create(request: CreateTokenRequest, idempotencyKey: string): Promise<TokenCreated> {
    this.fail();
    this.keys.push(idempotencyKey);
    this.calls.push(`create ${request.description}`);
    return this.created(request, { kind: 'user', name: 'alice' }, 'pat');
  }

  override async createForServiceAccount(
    name: string,
    request: CreateTokenRequest,
    idempotencyKey: string,
  ): Promise<TokenCreated> {
    this.fail();
    this.keys.push(idempotencyKey);
    this.calls.push(`createForServiceAccount ${name} ${request.description}`);
    return this.created(request, { kind: 'service_account', name }, 'sat');
  }

  override async rotate(id: string, overlapSeconds: number, idempotencyKey: string): Promise<TokenCreated> {
    this.fail();
    this.keys.push(idempotencyKey);
    this.calls.push(`rotate ${id} overlap=${overlapSeconds}`);
    const old = this.tokenList.find((t) => t.id === id);
    return this.created(
      { description: old?.description ?? 'rotated', selfRotatable: false },
      old?.owner ?? { kind: 'user', name: 'alice' },
      'pat',
    );
  }

  override async revoke(id: string): Promise<void> {
    this.fail();
    this.calls.push(`revoke ${id}`);
    this.tokenList = this.tokenList.map((t) => (t.id === id ? { ...t, state: 'revoked' } : t));
  }

  override async revokeByValue(value: string): Promise<RevokedToken> {
    this.fail();
    this.calls.push(`revokeByValue ${value.length}`);
    return { id: '00000000-0000-7000-8000-0000000000a1', owner: 'alice' };
  }

  override async revokeAllOfUser(username: string): Promise<void> {
    this.calls.push(`revokeAllOfUser ${username}`);
  }

  override async revokeAllOfServiceAccount(name: string): Promise<void> {
    this.calls.push(`revokeAllOfServiceAccount ${name}`);
  }

  override async revokeAllOwn(): Promise<void> {
    this.calls.push('revokeAllOwn');
  }
}

export class FakeSessionsApi extends SessionsApi {
  readonly calls: string[] = [];
  list: UserSession[] = [
    {
      id: '00000000-0000-7000-8000-0000000000c1',
      createdAt: '2026-10-29T08:00:00Z',
      lastSeenAt: '2026-10-29T09:00:00Z',
      createdAddr: '192.0.2.10',
      userAgent: 'Firefox 140',
      authMethod: 'oidc',
      providerId: 'corp',
      current: true,
    },
    {
      id: '00000000-0000-7000-8000-0000000000c2',
      createdAt: '2026-10-28T08:00:00Z',
      lastSeenAt: '2026-10-28T09:00:00Z',
      createdAddr: '192.0.2.11',
      userAgent: 'Safari 19',
      authMethod: 'oidc',
      providerId: 'corp',
      current: false,
    },
  ];

  override async own(): Promise<readonly UserSession[]> {
    return this.list;
  }

  override async endOwn(id: string): Promise<void> {
    this.calls.push(`endOwn ${id}`);
    this.list = this.list.filter((s) => s.id !== id);
  }

  override async endOwnAll(keepCurrent: boolean): Promise<void> {
    this.calls.push(`endOwnAll keepCurrent=${keepCurrent}`);
    this.list = keepCurrent ? this.list.filter((s) => s.current) : [];
  }

  override async ofUser(username: string): Promise<readonly UserSession[]> {
    this.calls.push(`ofUser ${username}`);
    return this.list.map((s) => ({ ...s, current: false }));
  }

  override async endAllOfUser(username: string): Promise<void> {
    this.calls.push(`endAllOfUser ${username}`);
    this.list = [];
  }
}

export function sampleAuthSettings(): LoadedAuthSettings {
  const lifetimes = { defaultTTLSeconds: 7_776_000, maxTTLSeconds: 31_536_000 };
  return {
    etag: '"100"',
    settings: {
      anonymousAccess: false,
      tokens: { pat: lifetimes, sat: lifetimes, expiryNoticeSeconds: 604_800 },
      ceilings: {
        anonymousAllowed: true,
        tokens: { pat: lifetimes, sat: lifetimes, expiryNoticeSeconds: 604_800 },
      },
      status: {
        providers: [{ id: 'corp', displayName: 'Corporate SSO', available: true }],
        breakGlass: { enabled: true, lockedUntil: null },
        sessions: {
          idleTimeoutSeconds: 3600,
          absoluteTimeoutSeconds: 43_200,
          maxPerUser: 20,
          recentAuthWindowSeconds: 3600,
        },
        clients: { basicEnabled: true },
      },
    },
  };
}

export class FakeAuthSettingsApi extends AuthSettingsApi {
  stored = sampleAuthSettings();
  readonly patches: { patch: AuthSettingsPatch; etag: string }[] = [];
  failure: ApiError | null = null;

  override async load(): Promise<LoadedAuthSettings> {
    return this.stored;
  }

  override async patch(patch: AuthSettingsPatch, etag: string): Promise<LoadedAuthSettings> {
    this.patches.push({ patch, etag });
    if (this.failure) {
      const e = this.failure;
      this.failure = null;
      throw e;
    }
    if (etag !== this.stored.etag) {
      throw problem(412, 'precondition_failed', { currentEtag: this.stored.etag });
    }
    const s = this.stored.settings;
    this.stored = {
      etag: `"${Number.parseInt(this.stored.etag.replaceAll('"', ''), 10) + 1}"`,
      settings: {
        ...s,
        anonymousAccess: patch.anonymousAccess ?? s.anonymousAccess,
        tokens: {
          pat: { ...s.tokens.pat, ...patch.tokens?.pat },
          sat: { ...s.tokens.sat, ...patch.tokens?.sat },
          expiryNoticeSeconds: patch.tokens?.expiryNoticeSeconds ?? s.tokens.expiryNoticeSeconds,
        },
      },
    };
    return this.stored;
  }
}

export function samplePolicy(over: Partial<PermissionPolicy> = {}): PermissionPolicy {
  return {
    name: 'team-web-publish',
    description: 'Team web publishes its npm scope',
    resourceType: 'repository',
    resources: {
      repositories: ['npm-internal'],
      packages: { include: ['@team-web/*'], exclude: [] },
      paths: { include: [], exclude: [] },
    },
    entries: [{ principal: 'group:team-web', actions: ['annotate', 'deploy', 'read'] }],
    inert: false,
    warnings: [],
    version: 3,
    createdAt: '2026-09-01T08:00:00Z',
    createdBy: 'user:alice',
    updatedAt: '2026-10-01T08:00:00Z',
    updatedBy: 'user:alice',
    ...over,
  };
}

export class FakePolicyApi extends PolicyApi {
  readonly calls: string[] = [];
  readonly keys: string[] = [];
  readonly validated: PermissionPolicyInput[] = [];
  policies: PermissionPolicy[] = [samplePolicy()];
  /** What `validate` answers; a function so that a test can look at the input. */
  validation: (input: PermissionPolicyInput) => PolicyValidation = () => ({
    valid: true,
    errors: [],
    warnings: [],
    covers: ['npm-internal'],
  });
  failure: ApiError | null = null;

  private fail(): void {
    if (this.failure) {
      const e = this.failure;
      this.failure = null;
      throw e;
    }
  }

  override list(filters: PolicyFilters): PageFetcher<PermissionPolicy> {
    return arrayPages(() =>
      this.policies.filter(
        (p) =>
          (!filters.principal || p.entries.some((e) => e.principal === filters.principal)) &&
          (!filters.repository || (p.resources.repositories ?? []).includes(filters.repository)),
      ),
    );
  }

  override async load(name: string): Promise<LoadedPolicy> {
    const p = this.policies.find((x) => x.name === name);
    if (!p) throw problem(404, 'not_found');
    return { policy: p, etag: `"${p.version}"` };
  }

  override async create(input: PermissionPolicyInput, idempotencyKey: string): Promise<LoadedPolicy> {
    this.fail();
    this.keys.push(idempotencyKey);
    this.calls.push(`create ${input.name}`);
    const policy = samplePolicy({
      name: input.name ?? '',
      description: input.description ?? '',
      resources: input.resources ?? {},
      entries: input.entries ?? [],
      version: 1,
    });
    this.policies = [...this.policies, policy];
    return { policy, etag: '"1"' };
  }

  override async replace(name: string, input: PermissionPolicyInput, etag: string): Promise<LoadedPolicy> {
    this.fail();
    this.calls.push(`replace ${name} ${etag}`);
    const current = await this.load(name);
    if (current.etag !== etag) {
      throw problem(412, 'precondition_failed', { currentEtag: current.etag });
    }
    const policy = samplePolicy({
      ...current.policy,
      description: input.description ?? '',
      resources: input.resources ?? {},
      entries: input.entries ?? [],
      version: current.policy.version + 1,
    });
    this.policies = this.policies.map((p) => (p.name === name ? policy : p));
    return { policy, etag: `"${policy.version}"` };
  }

  override async delete(name: string, etag?: string): Promise<void> {
    this.fail();
    this.calls.push(`delete ${name} ${etag ?? ''}`);
    this.policies = this.policies.filter((p) => p.name !== name);
  }

  override async validate(input: PermissionPolicyInput): Promise<PolicyValidation> {
    this.validated.push(input);
    return this.validation(input);
  }
}

export const ROLES: readonly Role[] = [
  { name: 'admin', capabilities: ['content:bypass', 'identities:write', 'roles:write'] },
  { name: 'security-admin', capabilities: ['identities:read', 'tokens:revoke'] },
  { name: 'auditor', capabilities: ['audit:read', 'policies:read', 'roles:read'] },
  { name: 'repository-admin', capabilities: ['repositories:write-config'] },
  { name: 'webhook-admin', capabilities: ['webhooks:write'] },
];

export class FakeRolesApi extends RolesApi {
  readonly calls: string[] = [];
  bindingList: RoleBinding[] = [
    { role: 'admin', principal: 'group:platform', grantedAt: '2026-09-01T08:00:00Z', grantedBy: '' },
    { role: 'auditor', principal: 'user:bob', grantedAt: '2026-09-02T08:00:00Z', grantedBy: 'user:alice' },
  ];
  failure: ApiError | null = null;

  override async roles(): Promise<readonly Role[]> {
    return ROLES;
  }

  override bindings(filters: RoleBindingFilters): PageFetcher<RoleBinding> {
    return arrayPages(() =>
      this.bindingList.filter(
        (b) => (!filters.role || b.role === filters.role) && (!filters.principal || b.principal === filters.principal),
      ),
    );
  }

  override async bind(role: RoleName, principal: string): Promise<RoleBinding> {
    if (this.failure) {
      const e = this.failure;
      this.failure = null;
      throw e;
    }
    this.calls.push(`bind ${role} ${principal}`);
    const binding: RoleBinding = { role, principal, grantedAt: '2026-10-29T10:00:00Z', grantedBy: 'user:alice' };
    this.bindingList = [...this.bindingList, binding];
    return binding;
  }

  override async unbind(role: RoleName, principal: string): Promise<void> {
    if (this.failure) {
      const e = this.failure;
      this.failure = null;
      throw e;
    }
    this.calls.push(`unbind ${role} ${principal}`);
    this.bindingList = this.bindingList.filter((b) => !(b.role === role && b.principal === principal));
  }
}

export function provideAccessFakes(): Provider[] {
  return [
    { provide: IdentityApi, useClass: FakeIdentityApi },
    { provide: TokensApi, useClass: FakeTokensApi },
    { provide: SessionsApi, useClass: FakeSessionsApi },
    { provide: AuthSettingsApi, useClass: FakeAuthSettingsApi },
    { provide: PolicyApi, useClass: FakePolicyApi },
    { provide: RolesApi, useClass: FakeRolesApi },
    // The draft explainer depends on the identity service only, so it runs on the fakes as is.
    { provide: PolicyExplainer, useClass: DraftPolicyExplainer },
  ];
}
