import { Provider } from '@angular/core';
import { applyMergePatch, JsonObject } from '../../shared/merge-patch';
import { AuditApi, AuditEvent, AuditFilters, ExportFormat } from '../audit-api';
import {
  Endpoint,
  EndpointsApi,
  ExposureCheck,
  ExposureCheckRequest,
  ExposureRule,
  RuleFilters,
  ruleEtag,
} from '../endpoints-api';
import { Child, FilesApi, Item, ItemStats, UploadRequest } from '../files-api';
import { Job, JobFilters, JobReference, JobsApi, Task } from '../jobs-api';
import { Page, PageFetcher } from '../pagination';
import { ApiError } from '../problem';
import {
  DeleteOutcome,
  FormatDescriptor,
  Repository,
  RepositoriesApi,
  RepositoryConsumerView,
  RepositoryFilters,
  RepositorySummary,
  Versioned,
} from '../repositories-api';

/*
 * In-memory fakes of the catalog services (repositories, files, endpoints, audit, jobs) for the
 * component tier (R-UI-047). They keep the behaviour the screens depend on: cursor paging, ETag
 * checks that answer 412, and the field errors of the real validation.
 */

function paged<T>(all: readonly T[]): PageFetcher<T> {
  return async (cursor, limit): Promise<Page<T>> => {
    const start = cursor === null ? 0 : Number(cursor);
    const end = start + limit;
    return { items: all.slice(start, end), nextCursor: end < all.length ? String(end) : null };
  };
}

function notFound(): ApiError {
  return new ApiError(
    404,
    { code: 'not_found', detail: 'not found', requestId: 'rid-404' },
    'rid-404',
  );
}

export const GENERIC_FORMAT: FormatDescriptor = {
  name: 'generic',
  displayName: 'Generic',
  classes: ['local'],
  layout: 'generic',
  protocolBasePath: '/files/{key}/',
  defaultUpstreamUrl: null,
  defaultExtraHosts: [],
  defaultMetadataTtlSeconds: null,
  defaultNotFoundTtlSeconds: 1800,
  patternSubject: 'path',
  priorityMembers: false,
  pinsAndClaims: false,
  caps: false,
  settingsSchema: null,
};

/** The settings schema npm registers (internal/formats/npm/settings.schema.json), as the catalog serves it. */
export const NPM_SETTINGS_SCHEMA = {
  $schema: 'https://json-schema.org/draft/2020-12/schema',
  type: 'object',
  additionalProperties: false,
  properties: {
    latestGuard: {
      description:
        'A publish under latest does not move latest backwards or onto a pre-release in front of a release.',
      type: 'boolean',
      default: true,
      'x-trove-classes': ['local'],
    },
    serveRemovedVersions: {
      description: 'Serve the cached tarball of a version the upstream no longer lists.',
      type: 'boolean',
      default: false,
      'x-trove-classes': ['remote'],
    },
    latestStrategy: {
      description: 'How a virtual repository merges latest.',
      type: 'string',
      enum: ['highest-member-latest', 'newest-published'],
      default: 'highest-member-latest',
      'x-trove-classes': ['virtual'],
    },
    distTagStrategy: {
      description: 'How a virtual repository merges the other dist-tags.',
      type: 'string',
      enum: ['member-order', 'highest-semver', 'newest-published'],
      default: 'member-order',
      'x-trove-classes': ['virtual'],
    },
  },
};

/**
 * npm's members plus the constructs a format may add (a nested object of one class, an enum, a
 * nullable URI, durations and sizes as strings, an integer, an array the form does not edit).
 */
export const RICH_SETTINGS_SCHEMA = {
  type: 'object',
  additionalProperties: false,
  properties: {
    latestGuard: { type: 'boolean', default: true, 'x-trove-classes': ['local'] },
    serveRemovedVersions: { type: 'boolean', default: false, 'x-trove-classes': ['remote'] },
    audit: {
      type: 'object',
      description: 'The security audit.',
      additionalProperties: false,
      'x-trove-classes': ['remote'],
      properties: {
        mode: { type: 'string', enum: ['forward', 'disabled'], default: 'forward' },
        providerURL: { type: ['string', 'null'], format: 'uri', default: null },
        timeout: { type: 'string', default: '30s', description: 'A duration such as 30s.' },
        maxBodySize: { type: 'string', default: '10MiB' },
        retries: { title: 'Retries', type: 'integer', default: 2 },
        hosts: { type: 'array', items: { type: 'string' } },
      },
    },
    latestStrategy: {
      type: 'string',
      enum: ['highest-member-latest', 'newest-published'],
      default: 'highest-member-latest',
      'x-trove-classes': ['virtual'],
    },
  },
};

/** A package format as the catalog could list it: name patterns, versions, tags and settings. */
export const PACKAGE_FORMAT: FormatDescriptor = {
  ...GENERIC_FORMAT,
  name: 'npm',
  displayName: 'npm',
  classes: ['local', 'remote', 'virtual'],
  protocolBasePath: '/api/npm/{key}/',
  patternSubject: 'name',
  settingsSchema: NPM_SETTINGS_SCHEMA,
};

export class FakeRepositoriesApi extends RepositoriesApi {
  formatsResult: readonly FormatDescriptor[] = [GENERIC_FORMAT];
  formatsError: ApiError | null = null;
  /** The `baseUrl` of the formats answer. */
  baseUrlResult: string | null = 'https://trove.example.net';
  baseUrlCalls = 0;
  repositories: Repository[] = [
    {
      key: 'generic-local',
      class: 'local',
      format: 'generic',
      description: 'Installers and build outputs',
      includePatterns: [],
      excludePatterns: [],
      disabled: false,
      requireAuthentication: true,
      hideExistence: 'inherit',
      createdAt: '2026-10-01T10:00:00Z',
      updatedAt: '2026-10-01T10:00:00Z',
      lastActivityAt: null,
    },
    {
      key: 'releases',
      class: 'local',
      format: 'generic',
      description: 'Customer releases',
      disabled: true,
    },
  ];
  /** The version of each repository; the ETag is `"<n>"`. */
  versions = new Map<string, number>();
  createKeys: string[] = [];
  patches: { key: string; patch: Readonly<Record<string, unknown>>; etag: string }[] = [];
  deleteResult: DeleteOutcome = { done: true };
  /** Set to make the next `get` answer the consumer view. */
  consumerOnly = false;
  listFilters: RepositoryFilters[] = [];
  /** Called before a patch is applied, to simulate a concurrent change. */
  beforePatch: (() => void) | null = null;

  private version(key: string): number {
    return this.versions.get(key) ?? 1;
  }

  override async formats(): Promise<readonly FormatDescriptor[]> {
    if (this.formatsError) throw this.formatsError;
    return this.formatsResult;
  }

  override async baseUrl(): Promise<string | null> {
    this.baseUrlCalls++;
    if (this.formatsError) throw this.formatsError;
    return this.baseUrlResult;
  }

  private summaries(filters: RepositoryFilters): RepositorySummary[] {
    return this.repositories
      .filter((r) => !filters.class || r.class === filters.class)
      .filter((r) => !filters.format || r.format === filters.format)
      .filter((r) => filters.state !== 'inactive' || r.disabled === true)
      .filter(
        (r) =>
          !filters.q ||
          r.key.includes(filters.q.toLowerCase()) ||
          (r.description ?? '').toLowerCase().includes(filters.q.toLowerCase()),
      )
      .sort((a, b) => a.key.localeCompare(b.key))
      .map((r) => ({
        key: r.key,
        class: r.class,
        format: r.format,
        description: r.description ?? '',
        addresses: [`https://trove.example.net/files/${r.key}/`],
        disabled: r.disabled ?? false,
        lifecycle: 'active' as const,
        lastActivityAt: r.lastActivityAt ?? null,
        project: null,
      }));
  }

  override list(filters: RepositoryFilters): PageFetcher<RepositorySummary> {
    this.listFilters.push(filters);
    return paged(this.summaries(filters));
  }

  override async listAll(): Promise<readonly RepositorySummary[]> {
    return this.summaries({});
  }

  override async get(key: string): Promise<Versioned<Repository | RepositoryConsumerView>> {
    const repo = this.repositories.find((r) => r.key === key);
    if (!repo) throw notFound();
    if (this.consumerOnly) {
      return {
        value: {
          key,
          class: repo.class,
          format: repo.format,
          description: repo.description ?? '',
          layout: 'generic',
          addresses: [`https://trove.example.net/files/${key}/`],
        },
        etag: `"${this.version(key)}-consumer"`,
      };
    }
    return { value: structuredClone(repo), etag: `"${this.version(key)}"` };
  }

  override async create(doc: Repository, idempotencyKey: string): Promise<Versioned<Repository>> {
    this.createKeys.push(idempotencyKey);
    if (!/^[a-z0-9][a-z0-9._-]{1,63}$/.test(doc.key)) {
      throw new ApiError(
        422,
        {
          code: 'invalid_key',
          detail: 'invalid key',
          errors: [{ pointer: '/key', code: 'invalid_key', detail: 'must match the key pattern' }],
        },
        null,
      );
    }
    if (this.repositories.some((r) => r.key === doc.key)) {
      throw new ApiError(409, { code: 'repository_exists', detail: 'exists' }, null);
    }
    this.repositories.push(structuredClone(doc));
    this.versions.set(doc.key, 1);
    return { value: structuredClone(doc), etag: '"1"' };
  }

  override async patch(
    key: string,
    patch: Readonly<Record<string, unknown>>,
    etag: string,
  ): Promise<Versioned<Repository>> {
    this.beforePatch?.();
    this.patches.push({ key, patch, etag });
    const index = this.repositories.findIndex((r) => r.key === key);
    const current = this.repositories[index];
    if (!current) throw notFound();
    if (etag !== `"${this.version(key)}"`) {
      throw new ApiError(
        412,
        { code: 'precondition_failed', currentEtag: `"${this.version(key)}"` },
        null,
      );
    }
    const next = applyMergePatch(
      current as unknown as JsonObject,
      patch as JsonObject,
    ) as unknown as Repository;
    this.repositories[index] = next;
    this.versions.set(key, this.version(key) + 1);
    return { value: structuredClone(next), etag: `"${this.version(key)}"` };
  }

  deleteCalls: { key: string; idempotencyKey: string; detach: boolean }[] = [];

  override async delete(
    key: string,
    idempotencyKey: string,
    detach = false,
  ): Promise<DeleteOutcome> {
    this.deleteCalls.push({ key, idempotencyKey, detach });
    if (this.deleteResult.done) this.repositories = this.repositories.filter((r) => r.key !== key);
    return this.deleteResult;
  }
}

export interface FakeNode {
  readonly path: string;
  readonly kind: 'file' | 'folder';
  readonly size?: number;
}

export class FakeFilesApi extends FilesApi {
  /** Items by repository; folders are listed explicitly. */
  tree = new Map<string, FakeNode[]>([
    [
      'generic-local',
      [
        { path: 'releases', kind: 'folder' },
        { path: 'releases/1.0', kind: 'folder' },
        { path: 'releases/1.0/app.zip', kind: 'file', size: 2048 },
        { path: 'releases/1.0/app.zip.sha256', kind: 'file', size: 64 },
        { path: 'readme.txt', kind: 'file', size: 120 },
      ],
    ],
  ]);
  uploads: UploadRequest[] = [];
  folders: string[] = [];
  deleted: string[] = [];
  deleteResult: DeleteOutcome = { done: true };
  uploadError: ApiError | null = null;
  countResult: { count: number; capped: boolean } | null = null;
  childCalls: { path: string; cursor: string | null; limit: number }[] = [];

  private nodes(repository: string): FakeNode[] {
    const nodes = this.tree.get(repository);
    if (!nodes) throw notFound();
    return nodes;
  }

  private toItem(repository: string, node: FakeNode): Item {
    const stamp = { at: '2026-10-28T09:14:03Z', by: 'user:alice' };
    return {
      repository,
      path: node.path,
      kind: node.kind,
      ...(node.kind === 'file'
        ? {
            size: node.size,
            mediaType: 'application/octet-stream',
            digests: { sha256: 'a'.repeat(64), sha1: 'b'.repeat(40) },
            contentStatus: 'ok' as const,
            downloadUrl: `https://trove.example.net/files/${repository}/${node.path}`,
          }
        : {
            explicit: true,
            listingUrl: `https://trove.example.net/files/${repository}/${node.path}/`,
          }),
      created: stamp,
      modified: stamp,
      updatedAt: stamp.at,
      package: null,
      upstreamUrl: null,
      itemUrl: `https://trove.example.net/api/v1/repositories/${repository}/items?path=${node.path}`,
    };
  }

  override async item(repository: string, path: string): Promise<Item> {
    const nodes = this.nodes(repository);
    if (path === '') return this.toItem(repository, { path: '', kind: 'folder' });
    const node = nodes.find((n) => n.path === path);
    if (!node) throw notFound();
    return this.toItem(repository, node);
  }

  override children(repository: string, path: string, limit: number): PageFetcher<Child> {
    const prefix = path === '' ? '' : `${path}/`;
    const all: Child[] = this.nodes(repository)
      .filter(
        (n) =>
          n.path.startsWith(prefix) &&
          !n.path.slice(prefix.length).includes('/') &&
          n.path !== path,
      )
      .map((n) => ({
        path: n.path.slice(prefix.length),
        kind: n.kind,
        size: n.size,
        modifiedAt: '2026-10-28T09:14:03Z',
        ...(n.kind === 'file' ? { sha256: 'a'.repeat(64) } : {}),
      }));
    const inner = paged(all);
    return (cursor) => {
      this.childCalls.push({ path, cursor, limit });
      return inner(cursor, limit);
    };
  }

  override async stats(repository: string, path: string): Promise<ItemStats> {
    this.nodes(repository);
    if (!this.tree.get(repository)?.some((n) => n.path === path && n.kind === 'file'))
      throw notFound();
    return { downloads: 7, lastDownloadAt: '2026-10-27T22:01:55Z', lastDownloader: 'ci:acme/web' };
  }

  override async countBelow(
    repository: string,
    path: string,
    cap: number,
  ): Promise<{ count: number; capped: boolean }> {
    if (this.countResult) return this.countResult;
    const count = this.nodes(repository).filter((n) => n.path.startsWith(`${path}/`)).length;
    return { count: Math.min(count, cap + 1), capped: count > cap };
  }

  override async deleteItem(repository: string, path: string): Promise<DeleteOutcome> {
    this.deleted.push(`${repository}/${path}`);
    if (this.deleteResult.done) {
      this.tree.set(
        repository,
        this.nodes(repository).filter((n) => n.path !== path && !n.path.startsWith(`${path}/`)),
      );
    }
    return this.deleteResult;
  }

  override async upload(request: UploadRequest): Promise<Item> {
    this.uploads.push(request);
    if (this.uploadError) throw this.uploadError;
    request.onProgress?.(request.file.size / 2, request.file.size);
    request.onProgress?.(request.file.size, request.file.size);
    const node: FakeNode = { path: request.path, kind: 'file', size: request.file.size };
    this.nodes(request.repository).push(node);
    return this.toItem(request.repository, node);
  }

  override async createFolder(repository: string, path: string): Promise<Item> {
    this.folders.push(`${repository}/${path}`);
    const node: FakeNode = { path, kind: 'folder' };
    this.nodes(repository).push(node);
    return this.toItem(repository, node);
  }
}

export function sampleRule(over: Partial<ExposureRule> = {}): ExposureRule {
  return {
    name: 'releases',
    description: 'Release files',
    repository: 'generic-local',
    enabled: true,
    paths: { include: ['releases/**'], exclude: [] },
    prereleases: false,
    revision: 1,
    endpoint: 'customers',
    ...over,
  };
}

export class FakeEndpointsApi extends EndpointsApi {
  endpoints: Endpoint[] = [
    {
      name: 'default',
      inert: false,
      ruleCount: 0,
      enabledRuleCount: 0,
      configuration: {
        default: true,
        port: 8080,
        baseUrl: 'https://trove.example.net',
        hosts: ['trove.example.net'],
        wildcardHost: null,
        hostRoots: {},
        wildcardRoot: 'none',
        pool: null,
        surface: ['ui', 'api', 'ci_exchange', 'packages', 'files'],
        auth: { tokenTypes: ['pat', 'sat', 'cit'], boundTokensOnly: false, anonymous: false },
        exposure: 'all',
        cacheFill: true,
        rateLimit: null,
        replicaHeader: 'authenticated',
      },
    },
    {
      name: 'customers',
      inert: false,
      ruleCount: 1,
      enabledRuleCount: 1,
      configuration: {
        default: false,
        port: 8443,
        baseUrl: 'https://packages.example.net',
        hosts: ['packages.example.net'],
        wildcardHost: null,
        hostRoots: {},
        wildcardRoot: 'none',
        pool: 'ep-customers',
        surface: ['packages'],
        auth: { tokenTypes: ['sat'], boundTokensOnly: true, anonymous: false },
        exposure: 'rules',
        cacheFill: false,
        rateLimit: { class: 'external', requestsPerSecond: 50, burst: 200, key: 'principal' },
        replicaHeader: 'never',
      },
    },
  ];
  ruleStore: ExposureRule[] = [sampleRule()];
  created: { rule: ExposureRule; key: string }[] = [];
  replaced: { name: string; rule: ExposureRule; etag: string }[] = [];
  patched: { name: string; patch: Readonly<Record<string, unknown>>; etag: string }[] = [];
  deleted: string[] = [];
  checks: ExposureCheckRequest[] = [];
  checkResult: ExposureCheck | ApiError = {
    endpoint: 'customers',
    repository: 'generic-local',
    mode: 'rules',
    package: null,
    packageExposed: true,
    rule: 'releases',
    reason: 'exposed',
    versions: [],
    paths: [],
    distTags: [],
    exposureFingerprint: 'sha256:00',
    warnings: [],
  };
  beforeReplace: (() => void) | null = null;

  override async list(): Promise<readonly Endpoint[]> {
    return this.endpoints;
  }

  override async get(name: string): Promise<Endpoint> {
    const e = this.endpoints.find((x) => x.name === name);
    if (!e) throw notFound();
    return e;
  }

  override async rules(
    endpoint: string,
    filters: RuleFilters = {},
  ): Promise<readonly ExposureRule[]> {
    await this.get(endpoint);
    return this.ruleStore
      .filter((r) => !filters.repository || r.repository === filters.repository)
      .filter((r) => filters.enabled === undefined || r.enabled === filters.enabled);
  }

  override async rule(endpoint: string, name: string): Promise<Versioned<ExposureRule>> {
    await this.get(endpoint);
    const r = this.ruleStore.find((x) => x.name === name);
    if (!r) throw notFound();
    return { value: structuredClone(r), etag: ruleEtag(r) };
  }

  override async createRule(
    _endpoint: string,
    rule: ExposureRule,
    idempotencyKey: string,
  ): Promise<Versioned<ExposureRule>> {
    this.created.push({ rule, key: idempotencyKey });
    if (this.ruleStore.some((r) => r.name === rule.name)) {
      throw new ApiError(409, { code: 'already_exists', detail: 'exists' }, null);
    }
    const stored = { ...rule, revision: 1 };
    this.ruleStore.push(stored);
    return { value: stored, etag: ruleEtag(stored) };
  }

  override async replaceRule(
    _endpoint: string,
    name: string,
    rule: ExposureRule,
    etag: string,
  ): Promise<Versioned<ExposureRule>> {
    this.beforeReplace?.();
    this.replaced.push({ name, rule, etag });
    const index = this.ruleStore.findIndex((r) => r.name === name);
    const current = this.ruleStore[index];
    if (!current) throw notFound();
    if (etag !== ruleEtag(current)) {
      throw new ApiError(
        412,
        { code: 'precondition_failed', currentEtag: ruleEtag(current) },
        null,
      );
    }
    const stored = { ...rule, revision: (current.revision ?? 0) + 1 };
    this.ruleStore[index] = stored;
    return { value: stored, etag: ruleEtag(stored) };
  }

  override async patchRule(
    _endpoint: string,
    name: string,
    patch: Readonly<Record<string, unknown>>,
    etag: string,
  ): Promise<Versioned<ExposureRule>> {
    this.patched.push({ name, patch, etag });
    const index = this.ruleStore.findIndex((r) => r.name === name);
    const current = this.ruleStore[index];
    if (!current) throw notFound();
    if (etag !== ruleEtag(current)) {
      throw new ApiError(
        412,
        { code: 'precondition_failed', currentEtag: ruleEtag(current) },
        null,
      );
    }
    const stored = {
      ...(applyMergePatch(
        current as unknown as JsonObject,
        patch as JsonObject,
      ) as unknown as ExposureRule),
      revision: (current.revision ?? 0) + 1,
    };
    this.ruleStore[index] = stored;
    return { value: stored, etag: ruleEtag(stored) };
  }

  override async deleteRule(_endpoint: string, name: string): Promise<void> {
    this.deleted.push(name);
    this.ruleStore = this.ruleStore.filter((r) => r.name !== name);
  }

  override async check(endpoint: string, request: ExposureCheckRequest): Promise<ExposureCheck> {
    this.checks.push(request);
    if (this.checkResult instanceof ApiError) throw this.checkResult;
    return { ...this.checkResult, endpoint, repository: request.repository };
  }
}

export function sampleEvent(over: Partial<AuditEvent> = {}): AuditEvent {
  return {
    id: '01933b1e-7c2a-7f4e-9d1b-6a2f0c9e8b11',
    occurredAt: '2026-10-28T11:58:02.417Z',
    action: 'authz.policy.updated',
    category: 'authz',
    outcome: 'success',
    actor: { type: 'user', id: 'u-1', name: 'alice', tokenId: null, authMethod: 'session' },
    source: { ip: '10.12.4.7', userAgent: 'Mozilla/5.0' },
    traceId: '4bf92f3577b34da6a3ce929d0e0e4736',
    replica: 'trove-web-0',
    endpoint: 'default',
    resource: { type: 'permission_policy', id: 'p-1' },
    repository: null,
    path: null,
    reason: null,
    details: { before: { grants: 1 }, after: { grants: 2 } },
    ...over,
  };
}

export class FakeAuditApi extends AuditApi {
  eventList: AuditEvent[] = [
    sampleEvent(),
    sampleEvent({
      id: '01933b1e-7c2a-7f4e-9d1b-6a2f0c9e8b10',
      action: 'artifact.downloaded',
      category: 'content',
      outcome: 'denied',
      repository: 'generic-local',
      path: 'releases/1.0/app.zip',
      resource: { type: 'item', id: null },
    }),
  ];
  filterCalls: AuditFilters[] = [];

  override events(filters: AuditFilters): PageFetcher<AuditEvent> {
    this.filterCalls.push(filters);
    return paged(
      this.eventList
        .filter((e) => !filters.category || e.category === filters.category)
        .filter((e) => !filters.outcome || e.outcome === filters.outcome)
        .filter((e) => !filters.action || e.action === filters.action)
        .filter((e) => !filters.actor || e.actor.name === filters.actor)
        .filter((e) => !filters.repository || e.repository === filters.repository),
    );
  }

  override async event(id: string): Promise<AuditEvent> {
    const e = this.eventList.find((x) => x.id === id);
    if (!e) throw notFound();
    return e;
  }

  override exportUrl(
    filters: AuditFilters & { from: string; to: string },
    format: ExportFormat,
  ): string {
    return `/api/v1/audit/events/export?from=${encodeURIComponent(filters.from)}&to=${encodeURIComponent(filters.to)}&format=${format}`;
  }
}

export function sampleJob(over: Partial<Job> = {}): Job {
  return {
    id: '01933b2a-7c41-7d1e-9a52-3f0e4b8c2d11',
    kind: 'gc',
    state: 'succeeded',
    replica: 'trove-worker-0',
    enqueuedAt: '2026-10-28T12:40:00Z',
    startedAt: '2026-10-28T12:40:01Z',
    finishedAt: '2026-10-28T12:40:02Z',
    attempt: 1,
    maxAttempts: 10,
    progress: null,
    result: { deleted: 2 },
    error: null,
    triggeredBy: null,
    target: null,
    href: '/api/v1/system/jobs/01933b2a-7c41-7d1e-9a52-3f0e4b8c2d11',
    ...over,
  };
}

export class FakeJobsApi extends JobsApi {
  jobList: Job[] = [
    sampleJob(),
    sampleJob({
      id: '01933b2a-7c41-7d1e-9a52-3f0e4b8c2d12',
      kind: 'gc-full',
      state: 'running',
      finishedAt: null,
      progress: { done: 3, total: 10, unit: 'items' },
      result: null,
    }),
  ];
  taskList: Task[] = [
    {
      name: 'gc',
      schedule: '0 3 * * *',
      enabled: true,
      nextRunAt: '2026-10-30T03:00:00Z',
      running: false,
      lastRun: {
        jobId: '01933b2a-7c41-7d1e-9a52-3f0e4b8c2d11',
        startedAt: '2026-10-29T03:00:01Z',
        finishedAt: '2026-10-29T03:00:09Z',
        outcome: 'succeeded',
        replica: 'trove-worker-0',
      },
    },
    {
      name: 'secrets-reencrypt',
      schedule: null,
      enabled: true,
      nextRunAt: null,
      running: false,
      lastRun: null,
    },
  ];
  runs: { name: string; parameters: Readonly<Record<string, unknown>> | undefined; key: string }[] =
    [];
  cancelled: string[] = [];
  filterCalls: JobFilters[] = [];
  runError: ApiError | null = null;

  override jobs(filters: JobFilters): PageFetcher<Job> {
    this.filterCalls.push(filters);
    return paged(
      this.jobList
        .filter((j) => !filters.kind?.length || filters.kind.includes(j.kind))
        .filter((j) => !filters.state?.length || filters.state.includes(j.state)),
    );
  }

  override async job(id: string): Promise<Job> {
    const j = this.jobList.find((x) => x.id === id);
    if (!j) throw notFound();
    return j;
  }

  override async cancel(id: string): Promise<Job> {
    this.cancelled.push(id);
    const index = this.jobList.findIndex((j) => j.id === id);
    const current = this.jobList[index];
    if (!current) throw notFound();
    const next = { ...current, state: 'cancelling' as const };
    this.jobList[index] = next;
    return next;
  }

  override async tasks(): Promise<readonly Task[]> {
    return this.taskList;
  }

  override async runTask(
    name: string,
    parameters: Readonly<Record<string, unknown>> | undefined,
    idempotencyKey: string,
  ): Promise<JobReference> {
    this.runs.push({ name, parameters, key: idempotencyKey });
    if (this.runError) throw this.runError;
    return { id: 'job-run-1', kind: name, state: 'queued', href: '/api/v1/system/jobs/job-run-1' };
  }
}

/** The fakes of the catalog services; `provideFakeApis` spreads them. */
export function provideCatalogFakes(): Provider[] {
  return [
    { provide: RepositoriesApi, useClass: FakeRepositoriesApi },
    { provide: FilesApi, useClass: FakeFilesApi },
    { provide: EndpointsApi, useClass: FakeEndpointsApi },
    { provide: AuditApi, useClass: FakeAuditApi },
    { provide: JobsApi, useClass: FakeJobsApi },
  ];
}
