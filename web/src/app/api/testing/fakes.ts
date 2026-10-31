import { Provider } from '@angular/core';
import { LoginApi, LoginOptions } from '../login-api';
import { ApiError } from '../problem';
import { LoadedPreferences, PreferencesApi, Preferences, PreferencesPatch } from '../preferences-api';
import {
  AuthzMe,
  LogoutAnswer,
  PermissionCheck,
  PermissionCheckAnswer,
  SessionApi,
  Whoami,
} from '../session-api';
import { EncryptionApi, EncryptionStatus, Replica, SystemApi, SystemInfo } from '../system-api';
// >>> M1 ui-b
import { provideAccessFakes } from './access-fakes';
// <<< M1 ui-b
// >>> M1 ui-a
import { provideCatalogFakes } from './fakes-catalog';
// <<< M1 ui-a
// >>> M2 ui-packages
import { providePackageFakes } from './fakes-packages';
import { provideGuideFakes } from './fakes-guides';
// <<< M2 ui-packages

/*
 * In-memory fakes of the typed service interfaces (R-UI-047 component tier). Screens of the later
 * work packages are written against the interfaces and tested with these until the endpoints
 * exist; they hold no behaviour beyond what the spec fixes.
 */

export const ADMIN_CAPABILITIES: readonly string[] = [
  'content:bypass',
  'identities:read',
  'identities:write',
  'policies:read',
  'policies:write',
  'roles:read',
  'roles:write',
  'tokens:read',
  'tokens:revoke',
  'repositories:read-config',
  'repositories:write-config',
  'settings:read',
  'settings:write',
  'audit:read',
  'endpoints:read',
  'endpoints:write',
];

export class FakeSessionApi extends SessionApi {
  whoamiResult: Whoami | ApiError = {
    principal: { kind: 'user', name: 'alice', id: '00000000-0000-0000-0000-000000000001' },
    credential: { type: 'session', tokenId: null, scope: null, expiresAt: null },
  };
  /** `capabilities` is what specs set; the other members default to an administrator's. */
  meResult: (Pick<AuthzMe, 'capabilities'> & Partial<AuthzMe>) | ApiError = {
    roles: ['admin'],
    capabilities: [...ADMIN_CAPABILITIES],
    principals: ['user:alice'],
    scope: null,
    epoch: 1,
  };
  logoutResult: LogoutAnswer = {};
  checkCalls: (readonly PermissionCheck[])[] = [];
  allow: (check: PermissionCheck) => boolean = () => true;
  loggedOut = false;

  override async whoami(): Promise<Whoami> {
    if (this.whoamiResult instanceof ApiError) throw this.whoamiResult;
    return this.whoamiResult;
  }

  override async authzMe(): Promise<AuthzMe> {
    if (this.meResult instanceof ApiError) throw this.meResult;
    return { roles: [], principals: [], scope: null, epoch: 1, ...this.meResult };
  }

  override async check(checks: readonly PermissionCheck[]): Promise<PermissionCheckAnswer> {
    this.checkCalls.push(checks);
    return { results: checks.map((c) => ({ allowed: this.allow(c) })), epoch: 1 };
  }

  override async logout(): Promise<LogoutAnswer> {
    this.loggedOut = true;
    return this.logoutResult;
  }
}

export class FakeLoginApi extends LoginApi {
  optionsResult: LoginOptions = {
    providers: [
      { id: 'corp', displayName: 'Corporate SSO', startUrl: '/auth/oidc/corp/start', available: true },
    ],
    localLogin: false,
    anonymousAccess: false,
    autoRedirect: null,
  };
  localLogins: { username: string; password: string }[] = [];

  override async options(): Promise<LoginOptions> {
    return this.optionsResult;
  }

  override async localLogin(username: string, password: string): Promise<void> {
    this.localLogins.push({ username, password });
  }
}

export class FakePreferencesApi extends PreferencesApi {
  stored: Preferences = {
    locale: null,
    theme: null,
    keyboardShortcuts: false,
    projectFilter: null,
    revision: 1,
  };

  override async load(): Promise<LoadedPreferences> {
    return { preferences: this.stored, etag: `"${this.stored.revision}"` };
  }

  override async patch(
    patch: PreferencesPatch,
    etag: string,
  ): Promise<LoadedPreferences> {
    if (etag !== `"${this.stored.revision}"`) {
      throw new ApiError(412, { code: 'precondition_failed', currentEtag: `"${this.stored.revision}"` }, null);
    }
    this.stored = {
      ...this.stored,
      ...(patch as Partial<Preferences>),
      revision: this.stored.revision + 1,
    };
    return this.load();
  }
}

export class FakeEncryptionApi extends EncryptionApi {
  statusResult: EncryptionStatus | ApiError = {
    answeredBy: 'trove-web-0',
    activeKeyId: '2026-10',
    keys: [
      { id: '2026-10', active: true, sealedValues: 14, canary: 'ok' },
      { id: '2026-04', active: false, sealedValues: 3, canary: 'ok' },
    ],
    replicas: [
      { name: 'trove-web-0', role: 'web', activeKeyId: '2026-10', keyIds: ['2026-04', '2026-10'] },
    ],
    ringsCompatible: true,
    lastReencryption: null,
  };
  reencryptKeys: string[] = [];

  override async status(): Promise<EncryptionStatus> {
    if (this.statusResult instanceof ApiError) throw this.statusResult;
    return this.statusResult;
  }

  override async reencrypt(idempotencyKey: string): Promise<string> {
    this.reencryptKeys.push(idempotencyKey);
    return 'job-1';
  }
}

export function sampleReplica(over: Partial<Replica> = {}): Replica {
  return {
    name: 'trove-web-0',
    instanceUid: 'uid-0',
    version: '0.1.1',
    revision: 'abc',
    roles: ['web'],
    state: 'ready',
    lost: false,
    startedAt: '2026-10-29T10:00:00Z',
    heartbeatAt: '2026-10-29T10:05:00Z',
    leases: [],
    node: 'node-a',
    checks: { database: { status: 'pass' } },
    degraded: [],
    valkey: {
      state: 'available',
      reason: null,
      circuit: 'closed',
      primary: 'valkey-0:6379',
      sentinelsReachable: 3,
      sentinelsConfigured: 3,
      serverVersion: '9.0.0',
      lastPrimaryChangeAt: null,
    },
    ...over,
  };
}

export function sampleInfo(over: Partial<SystemInfo> = {}): SystemInfo {
  return {
    installationId: null,
    incarnation: null,
    answeredBy: { replica: 'trove-web-0', role: 'web' },
    version: { version: '0.1.1', revision: 'abc', goVersion: 'go1.27.3' },
    baseUrl: 'https://trove.example.net',
    baseUrlSource: 'config',
    maintenanceMode: 'normal',
    replicas: {
      live: 2,
      ready: 2,
      versions: { '0.1.1': 2 },
      roles: {
        web: { live: 2, ready: 2, degraded: 0, nodes: 2 },
        worker: { live: 0, ready: 0, degraded: 0, nodes: 0 },
      },
    },
    database: {
      serverVersion: '17.2',
      schemaVersion: 12,
      supportedSchemaVersions: [12],
      poolerMode: 'none',
      pools: {},
    },
    valkey: {
      mode: 'sentinel',
      primaryName: 'trove',
      sentinels: 3,
      tls: 'verify',
      keyPrefix: 'trove:',
      keyspace: null,
      degradedReplicas: 0,
    },
    objectStore: { endpoint: 'https://s3.example.net', bucket: 'trove', prefix: '', credentialSource: 'static' },
    backup: null,
    features: { tracing: false, webhooks: false, configAsCode: false },
    formats: [],
    runtime: { goos: 'linux', goarch: 'amd64', gomaxprocs: 2, memoryLimitBytes: 0 },
    ...over,
  };
}

export class FakeSystemApi extends SystemApi {
  infoResult: SystemInfo = sampleInfo();
  replicasResult: readonly Replica[] = [
    sampleReplica(),
    sampleReplica({ name: 'trove-web-1', instanceUid: 'uid-1', node: 'node-b' }),
  ];

  override async info(): Promise<SystemInfo> {
    return this.infoResult;
  }

  override async replicas(): Promise<readonly Replica[]> {
    return this.replicasResult;
  }
}

/** Providers that replace every interface-backed service with its in-memory fake. */
export function provideFakeApis(): Provider[] {
  return [
    { provide: SessionApi, useClass: FakeSessionApi },
    { provide: LoginApi, useClass: FakeLoginApi },
    { provide: PreferencesApi, useClass: FakePreferencesApi },
    { provide: EncryptionApi, useClass: FakeEncryptionApi },
    { provide: SystemApi, useClass: FakeSystemApi },
    // >>> M1 ui-b
    ...provideAccessFakes(),
    // <<< M1 ui-b
    // >>> M1 ui-a
    ...provideCatalogFakes(),
    // <<< M1 ui-a
    // >>> M2 ui-packages
    ...providePackageFakes(),
    ...provideGuideFakes(),
    // <<< M2 ui-packages
  ];
}
