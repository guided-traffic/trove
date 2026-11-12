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
  MirrorStatus,
  Repository,
  RepositoriesApi,
  RepositoryClass,
  RepositoryConsumerView,
  RepositoryDefaults,
  RepositoryFilters,
  RepositoryResolution,
  RepositorySummary,
  UpstreamPreset,
  UpstreamTestBody,
  UpstreamTestResult,
  Versioned,
  refOf,
  repoRef,
  repositoryUrl,
} from '../repositories-api';

/*
 * In-memory fakes of the catalog services (repositories, files, endpoints, audit, jobs) for the
 * component tier (R-UI-047). They keep the behaviour the screens depend on: cursor paging, ETag
 * checks that answer 412, and the field errors of the real validation.
 */

/** A document as a read shows it: a password given as a value is sealed and reads as `{"set": true}` (R-REPO-033). */
function sealed(doc: JsonObject): JsonObject {
  const remote = doc['remote'] as JsonObject | undefined;
  const password = remote?.['password'] as JsonObject | undefined;
  if (password && typeof password['value'] === 'string') remote!['password'] = { set: true };
  return doc;
}

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

/*
 * The upstream presets of each format as the catalog serves them (R-REPO-062; internal/formats/<format>/format.go). A fake format
 * of the component tier names the presets of its real format, so a screen meets the buttons, the default URL and the hosts it
 * meets on a real installation.
 */
export const NPM_PRESETS: readonly UpstreamPreset[] = [
  { id: 'npmjs', displayName: 'npm public registry', url: 'https://registry.npmjs.org/', extraHosts: [], default: true },
];
export const NUGET_PRESETS: readonly UpstreamPreset[] = [
  {
    id: 'nuget-org',
    displayName: 'nuget.org',
    url: 'https://api.nuget.org/v3/index.json',
    extraHosts: [{ pattern: '*.nuget.org', sendCredentials: false }],
    default: true,
  },
];
export const OCI_PRESETS: readonly UpstreamPreset[] = [
  {
    id: 'docker-hub',
    displayName: 'Docker Hub',
    url: 'https://registry-1.docker.io/',
    extraHosts: [{ pattern: 'auth.docker.io', sendCredentials: true }],
    default: true,
  },
  { id: 'quay', displayName: 'Quay', url: 'https://quay.io/', extraHosts: [], default: false },
  { id: 'ghcr', displayName: 'GitHub (ghcr.io)', url: 'https://ghcr.io/', extraHosts: [], default: false },
  {
    id: 'gitlab',
    displayName: 'GitLab',
    url: 'https://registry.gitlab.com/',
    extraHosts: [{ pattern: 'gitlab.com', sendCredentials: true }],
    default: false,
  },
];
export const DEB_PRESETS: readonly UpstreamPreset[] = [
  { id: 'debian', displayName: 'Debian', url: 'https://deb.debian.org/debian/', extraHosts: [], default: true },
  {
    id: 'debian-security',
    displayName: 'Debian security updates',
    url: 'https://deb.debian.org/debian-security/',
    extraHosts: [],
    default: false,
  },
  { id: 'ubuntu', displayName: 'Ubuntu', url: 'https://archive.ubuntu.com/ubuntu/', extraHosts: [], default: false },
  {
    id: 'ubuntu-security',
    displayName: 'Ubuntu security updates',
    url: 'https://security.ubuntu.com/ubuntu/',
    extraHosts: [],
    default: false,
  },
  { id: 'ubuntu-ports', displayName: 'Ubuntu ports', url: 'https://ports.ubuntu.com/ubuntu-ports/', extraHosts: [], default: false },
];
export const ANSIBLE_PRESETS: readonly UpstreamPreset[] = [
  {
    id: 'galaxy',
    displayName: 'Ansible Galaxy',
    url: 'https://galaxy.ansible.com/api/',
    extraHosts: [
      { pattern: 'github.com', sendCredentials: false },
      { pattern: 'codeload.github.com', sendCredentials: false },
    ],
    default: true,
  },
];

export const GENERIC_FORMAT: FormatDescriptor = {
  name: 'generic',
  displayName: 'Generic',
  classes: ['local'],
  clients: [{ id: 'curl', displayName: 'curl' }],
  layout: 'generic',
  protocolBasePath: '/files/{project}/{key}/',
  upstreamPresets: [],
  defaultMetadataTtlSeconds: null,
  defaultNotFoundTtlSeconds: 1800,
  patternSubject: 'path',
  priorityMembers: false,
  pinsAndClaims: false,
  caps: false,
  vulnerabilityScanning: false,
  mirror: false,
  settingsSchema: null,
  wiki: false,
};

/** The settings schema npm registers (internal/formats/npm/settings.schema.json), as the catalog serves it. */
export const NPM_SETTINGS_SCHEMA = {
  $schema: 'https://json-schema.org/draft/2020-12/schema',
  type: 'object',
  additionalProperties: false,
  properties: {
    latestGuard: {
      title: 'Guard the latest tag',
      description:
        'A publish of a version below latest, or of a pre-release while latest is a release, is stored without moving latest. Turn it off if every publish under latest should move it.',
      type: 'boolean',
      default: true,
      'x-trove-classes': ['local'],
    },
    serveRemovedVersions: {
      title: 'Serve removed versions',
      description:
        'Keep serving cached versions and packages that the upstream has removed, to exact downloads such as from a lockfile; they are no longer listed. Leave it off so that what the upstream took down, often malware, stops being served.',
      type: 'boolean',
      default: false,
      'x-trove-classes': ['remote'],
    },
    latestStrategy: {
      title: 'Latest tag strategy',
      description: 'Which latest tag this virtual repository offers when the latest tags of its members differ.',
      type: 'string',
      enum: ['highest-member-latest', 'newest-published'],
      'x-trove-enum-titles': { 'highest-member-latest': 'Highest version', 'newest-published': 'Most recently published' },
      default: 'highest-member-latest',
      'x-trove-classes': ['virtual'],
    },
    distTagStrategy: {
      title: 'Dist-tag strategy',
      description: 'Which value the other dist-tags, such as next or beta, take when the values of the members differ.',
      type: 'string',
      enum: ['member-order', 'highest-semver', 'newest-published'],
      'x-trove-enum-titles': {
        'member-order': 'First member in order',
        'highest-semver': 'Highest version',
        'newest-published': 'Most recently published',
      },
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
        // One value with a label and one without, which the select shows as it is written.
        mode: { type: 'string', enum: ['forward', 'disabled'], 'x-trove-enum-titles': { forward: 'Forward to an advisory service' }, default: 'forward' },
        providerURL: { type: ['string', 'null'], format: 'uri', default: null },
        timeout: { type: 'string', default: '30s', description: 'A duration such as 30s.' },
        maxBodySize: { type: 'string', default: '10MiB' },
        retries: { title: 'Retries', type: 'integer', default: 2 },
        hosts: { type: 'array', items: { type: 'object' } },
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
  clients: [
    { id: 'npm', displayName: 'npm' },
    { id: 'pnpm', displayName: 'pnpm' },
    { id: 'yarn', displayName: 'Yarn Berry (2 and later)' },
    { id: 'yarn-classic', displayName: 'Yarn Classic (1.x)' },
  ],
  protocolBasePath: '/api/npm/{project}/{key}/',
  upstreamPresets: [...NPM_PRESETS],
  patternSubject: 'name',
  vulnerabilityScanning: true,
  mirror: true,
  settingsSchema: NPM_SETTINGS_SCHEMA,
  wiki: true,
};

/** The settings schema NuGet registers (internal/formats/nuget/settings.schema.json), as the catalog serves it. */
export const NUGET_SETTINGS_SCHEMA = {
  $schema: 'https://json-schema.org/draft/2020-12/schema',
  type: 'object',
  additionalProperties: false,
  properties: {
    deleteMode: {
      title: 'Delete mode',
      description:
        "What dotnet nuget delete and nuget delete do in this repository: Unlist hides the version from search and keeps it downloadable, Delete for good removes it. A delete in trove's own pages or API always removes the version.",
      type: 'string',
      enum: ['unlist', 'delete'],
      'x-trove-enum-titles': { unlist: 'Unlist', delete: 'Delete for good' },
      default: 'unlist',
      'x-trove-classes': ['local'],
    },
    forwardSearch: {
      title: 'Forward search',
      description:
        'Pass searches on to the upstream, so that clients also find packages nobody has fetched through trove yet. Turn it off to search only what this repository has cached and to keep search terms inside your network.',
      type: 'boolean',
      default: true,
      'x-trove-classes': ['remote'],
    },
  },
};

/** NuGet as the catalog lists it: the .NET CLI, nuget.exe and Visual Studio, and its settings. */
export const NUGET_FORMAT: FormatDescriptor = {
  ...PACKAGE_FORMAT,
  name: 'nuget',
  displayName: 'NuGet',
  clients: [
    { id: 'dotnet', displayName: '.NET CLI (dotnet)' },
    { id: 'nuget', displayName: 'NuGet CLI (nuget.exe)' },
    { id: 'visual-studio', displayName: 'Visual Studio' },
  ],
  protocolBasePath: '/api/nuget/{project}/{key}/v3/',
  upstreamPresets: [...NUGET_PRESETS],
  settingsSchema: NUGET_SETTINGS_SCHEMA,
  wiki: true,
};

/**
 * OCI as the catalog lists it (internal/formats/oci/format.go): the three classes, the four registries of its presets, the
 * periods of 300 s, the Docker CLI and Podman, the registry root as base path. Its settings schema is not copied.
 */
export const OCI_FORMAT: FormatDescriptor = {
  ...GENERIC_FORMAT,
  name: 'oci',
  vulnerabilityScanning: true,
  displayName: 'OCI (Docker)',
  classes: ['local', 'remote', 'virtual'],
  clients: [
    { id: 'docker', displayName: 'Docker CLI (with Buildx)' },
    { id: 'podman', displayName: 'Podman' },
  ],
  protocolBasePath: '/v2/{project}/{key}/',
  upstreamPresets: [...OCI_PRESETS],
  defaultMetadataTtlSeconds: 300,
  defaultNotFoundTtlSeconds: 300,
  patternSubject: 'name',
  mirror: true,
};

/**
 * The settings schema Debian registers (internal/formats/deb/settings.schema.json) with the constructs the form meets in it: arrays of
 * strings, an array whose items are an enum, a long nullable string and a nested object of the remote class. The read-only
 * `upstreamKeyFingerprints` is not part of the registered schema.
 */
export const DEB_SETTINGS_SCHEMA = {
  $schema: 'https://json-schema.org/draft/2020-12/schema',
  type: 'object',
  additionalProperties: false,
  properties: {
    architectures: {
      description: 'The architectures apt clients can install from, such as amd64 and arm64; in a local repository an upload of another architecture adds it.',
      type: 'array',
      minItems: 1,
      uniqueItems: true,
      default: ['amd64', 'arm64'],
      'x-trove-classes': ['local', 'virtual'],
      items: { type: 'string' },
    },
    compression: {
      description: 'Package indexes are always offered gzip-compressed; choose xz to offer smaller xz-compressed ones as well.',
      type: 'array',
      maxItems: 1,
      uniqueItems: true,
      default: [],
      'x-trove-classes': ['local', 'virtual'],
      items: { type: 'string', enum: ['xz'] },
    },
    origin: { description: 'The origin apt reports for this repository, which pinning rules can match.', type: 'string', default: 'trove', 'x-trove-classes': ['local', 'virtual'] },
    label: { description: "The label apt reports for this repository, which pinning rules can match. Left empty, it is the repository's key.", type: 'string', 'x-trove-classes': ['local', 'virtual'] },
    suites: { description: 'Further suites to serve from the remote members, such as bookworm-backports.', type: 'array', default: [], 'x-trove-classes': ['virtual'], items: { type: 'string' } },
    components: { description: 'The components to serve, such as main and contrib, merged from all members.', type: 'array', default: ['main'], 'x-trove-classes': ['virtual'], items: { type: 'string' } },
    upstreamKeys: {
      description: "The upstream's public signing keys, pasted as one armored OpenPGP block of up to 32 keys.",
      type: ['string', 'null'],
      default: null,
      'x-trove-classes': ['remote'],
      maxLength: 262144,
    },
    mirror: {
      description: 'What a mirror of the APT archive keeps.',
      type: 'object',
      additionalProperties: false,
      'x-trove-classes': ['remote'],
      properties: {
        suites: { description: 'The suites to mirror, such as bookworm and bookworm-updates. A mirror needs at least one.', type: 'array', default: [], items: { type: 'string' } },
        components: { description: 'The components to keep, such as main, contrib or non-free-firmware.', type: 'array', default: ['main'], items: { type: 'string' } },
        architectures: { description: 'The architectures whose packages are kept, such as amd64.', type: 'array', default: ['amd64'], items: { type: 'string' } },
        translations: { description: 'The languages of the package descriptions to keep, such as en or de.', type: 'array', default: ['en'], items: { type: 'string' } },
        sources: { description: 'Also keep the source packages.', type: 'boolean', default: false },
      },
    },
  },
};

/** Debian (APT) as the catalog lists it (internal/formats/deb/format.go): the three classes, one client for apt and apt-get. */
export const DEB_FORMAT: FormatDescriptor = {
  ...PACKAGE_FORMAT,
  name: 'deb',
  displayName: 'Debian (APT)',
  clients: [{ id: 'apt', displayName: 'APT (apt, apt-get)' }],
  protocolBasePath: '/api/deb/{project}/{key}/',
  upstreamPresets: [...DEB_PRESETS],
  settingsSchema: DEB_SETTINGS_SCHEMA,
  wiki: false,
};

/** The settings of the Ansible Galaxy format (internal/formats/ansible/settings.schema.json): two members, both for remote repositories. */
export const ANSIBLE_SETTINGS_SCHEMA = {
  $schema: 'https://json-schema.org/draft/2020-12/schema',
  type: 'object',
  additionalProperties: false,
  properties: {
    authUrl: {
      description: 'For a hub behind single sign-on: where trove exchanges the stored offline token for a short-lived access token.',
      type: 'string',
      pattern: '^https://',
      maxLength: 2048,
      'x-trove-classes': ['remote'],
    },
    clientId: {
      description: 'The client ID trove presents when it exchanges the token.',
      type: 'string',
      default: 'cloud-services',
      pattern: '^[A-Za-z0-9._-]{1,128}$',
      'x-trove-classes': ['remote'],
    },
  },
};

/** Ansible Galaxy as the catalog lists it (internal/formats/ansible/format.go): the three classes, one client. */
export const ANSIBLE_FORMAT: FormatDescriptor = {
  ...PACKAGE_FORMAT,
  name: 'ansible',
  displayName: 'Ansible Galaxy',
  clients: [{ id: 'ansible-galaxy', displayName: 'Ansible Galaxy (ansible-galaxy)' }],
  protocolBasePath: '/api/ansible/{project}/{key}/',
  upstreamPresets: [...ANSIBLE_PRESETS],
  settingsSchema: ANSIBLE_SETTINGS_SCHEMA,
  wiki: true,
};

/** A repository of the fake: the project is writable so that a test can move one. */
export type FakeRepository = { -readonly [K in keyof Repository]: Repository[K] };

export class FakeRepositoriesApi extends RepositoriesApi {
  formatsResult: readonly FormatDescriptor[] = [GENERIC_FORMAT];
  formatsError: ApiError | null = null;
  /** The `baseUrl` of the formats answer. */
  baseUrlResult: string | null = 'https://trove.example.net';
  baseUrlCalls = 0;
  repositories: FakeRepository[] = [
    {
      key: 'generic-local',
      project: 'web',
      class: 'local',
      format: 'generic',
      description: 'Installers and build outputs',
      includePatterns: [],
      excludePatterns: [],
      disabled: false,
      visibility: { artifacts: 'private', wiki: 'private' },
      hideExistence: 'inherit',
      createdAt: '2026-10-01T10:00:00Z',
      updatedAt: '2026-10-01T10:00:00Z',
      lastActivityAt: null,
    },
    {
      key: 'releases',
      project: 'web',
      class: 'local',
      format: 'generic',
      description: 'Customer releases',
      disabled: true,
    },
  ];
  /** The version of each repository by qualified name; the ETag is `"<n>"`. */
  versions = new Map<string, number>();
  createKeys: string[] = [];
  /** The project of each create, in order. */
  createProjects: string[] = [];
  moves: { ref: string; project: string }[] = [];
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
      .filter((r) => !filters.project || r.project === filters.project)
      .filter((r) => !filters.name?.length || filters.name.includes(refOf(r)))
      .filter(
        (r) =>
          !filters.q ||
          refOf(r).includes(filters.q.toLowerCase()) ||
          (r.description ?? '').toLowerCase().includes(filters.q.toLowerCase()),
      )
      .sort((a, b) => refOf(a).localeCompare(refOf(b)))
      .map((r) => ({
        key: r.key,
        class: r.class,
        format: r.format,
        description: r.description ?? '',
        visibility: r.visibility ?? { artifacts: 'private', wiki: 'private' },
        addresses: [`https://trove.example.net/files/${refOf(r)}/`],
        disabled: r.disabled ?? false,
        lifecycle: (r.lifecycle ?? 'active') as 'active' | 'deleting',
        lastActivityAt: r.lastActivityAt ?? null,
        project: r.project ?? '',
      }));
  }

  override list(filters: RepositoryFilters): PageFetcher<RepositorySummary> {
    this.listFilters.push(filters);
    return paged(this.summaries(filters));
  }

  override async listAll(): Promise<readonly RepositorySummary[]> {
    return this.summaries({});
  }

  override async get(ref: string): Promise<Versioned<Repository | RepositoryConsumerView>> {
    const key = ref;
    const repo = this.repositories.find((r) => refOf(r) === ref);
    if (!repo) throw notFound();
    if (this.consumerOnly) {
      return {
        value: {
          key: repo.key,
          class: repo.class,
          format: repo.format,
          description: repo.description ?? '',
          layout: 'generic',
          visibility: repo.visibility ?? { artifacts: 'private', wiki: 'private' },
          addresses: [`https://trove.example.net/files/${ref}/`],
        },
        etag: `"${this.version(key)}-consumer"`,
      };
    }
    return { value: structuredClone(repo), etag: `"${this.version(key)}"` };
  }

  override async create(project: string, doc: Repository, idempotencyKey: string): Promise<Versioned<Repository>> {
    this.createKeys.push(idempotencyKey);
    this.createProjects.push(project);
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
    if (this.repositories.some((r) => r.project === project && r.key === doc.key)) {
      throw new ApiError(409, { code: 'already_exists', detail: 'exists' }, null);
    }
    const stored = sealed({ ...(structuredClone(doc) as unknown as JsonObject), project }) as unknown as Repository;
    this.repositories.push(stored);
    this.versions.set(repoRef(project, doc.key), 1);
    return { value: structuredClone(stored), etag: '"1"' };
  }

  override async patch(
    key: string,
    patch: Readonly<Record<string, unknown>>,
    etag: string,
  ): Promise<Versioned<Repository>> {
    this.beforePatch?.();
    this.patches.push({ key, patch, etag });
    const index = this.repositories.findIndex((r) => refOf(r) === key);
    const current = this.repositories[index];
    if (!current) throw notFound();
    if (etag !== `"${this.version(key)}"`) {
      throw new ApiError(
        412,
        { code: 'precondition_failed', currentEtag: `"${this.version(key)}"` },
        null,
      );
    }
    const next = sealed(
      applyMergePatch(current as unknown as JsonObject, patch as JsonObject),
    ) as unknown as Repository;
    this.repositories[index] = next;
    this.versions.set(key, this.version(key) + 1);
    const warned =
      next.class === 'remote' && next.visibility?.artifacts === 'public'
        ? [
            {
              code: 'public_populates_remote',
              detail: 'Anyone can fill the cache of this remote repository from its upstream.',
            },
          ]
        : [];
    return { value: { ...structuredClone(next), warnings: warned }, etag: `"${this.version(key)}"` };
  }

  deleteCalls: { key: string; idempotencyKey: string; detach: boolean }[] = [];

  override async delete(
    key: string,
    idempotencyKey: string,
    detach = false,
  ): Promise<DeleteOutcome> {
    this.deleteCalls.push({ key, idempotencyKey, detach });
    if (this.deleteResult.done) this.repositories = this.repositories.filter((r) => refOf(r) !== key);
    return this.deleteResult;
  }

  override async move(ref: string, targetProject: string): Promise<Versioned<Repository>> {
    this.moves.push({ ref, project: targetProject });
    const index = this.repositories.findIndex((r) => refOf(r) === ref);
    const current = this.repositories[index];
    if (!current) throw notFound();
    const moved = { ...current, project: targetProject } as Repository;
    this.repositories[index] = moved;
    return { value: structuredClone(moved), etag: '"1"' };
  }

  /** The answers of `GET …/mirror`: the next ones are taken in order, the last one stays. */
  mirrorStatuses: MirrorStatus[] = [sampleMirrorStatus()];
  mirrorStatusError: ApiError | null = null;
  mirrorStatusCalls = 0;
  /** Set to make the next `runMirror` fail (a 409 with its code, for example). */
  runMirrorError: ApiError | null = null;
  runMirrorResult: JobReference = {
    id: '0193f2c4-7a7e-7b51-9b1e-2f0d6c9e1a10',
    kind: 'mirror-pass',
    state: 'queued',
    href: '/api/v1/system/jobs/0193f2c4-7a7e-7b51-9b1e-2f0d6c9e1a10',
  };
  mirrorRuns: { key: string; full: boolean; idempotencyKey: string }[] = [];
  upstreamTests: UpstreamTestBody[] = [];
  upstreamTestProjects: string[] = [];
  upstreamTestResult: UpstreamTestResult = sampleUpstreamTestResult();
  upstreamTestError: ApiError | null = null;

  override async mirrorStatus(key: string): Promise<MirrorStatus> {
    this.mirrorStatusCalls++;
    if (this.mirrorStatusError) throw this.mirrorStatusError;
    if (!this.repositories.some((r) => refOf(r) === key)) throw notFound();
    const index = Math.min(this.mirrorStatusCalls - 1, this.mirrorStatuses.length - 1);
    return structuredClone(this.mirrorStatuses[index]!);
  }

  override async runMirror(key: string, full: boolean, idempotencyKey: string): Promise<JobReference> {
    this.mirrorRuns.push({ key, full, idempotencyKey });
    if (this.runMirrorError) throw this.runMirrorError;
    return this.runMirrorResult;
  }

  /** The answers of `GET …/resolution`; `resolutionFor` decides per repository and path, the default lists every member as a hit. */
  resolutionCalls: { ref: string; path: string }[] = [];
  resolutionError: ApiError | null = null;
  resolutionFor: ((ref: string, path: string, members: readonly string[]) => RepositoryResolution) | null = null;

  override async resolution(ref: string, path: string): Promise<RepositoryResolution> {
    this.resolutionCalls.push({ ref, path });
    if (this.resolutionError) throw this.resolutionError;
    const repo = this.repositories.find((r) => refOf(r) === ref);
    if (!repo) throw notFound();
    const members = (repo.virtual?.members ?? []) as readonly string[];
    if (this.resolutionFor) return this.resolutionFor(ref, path, members);
    return {
      repository: ref,
      descriptor: { path, kind: 'artifact' },
      members: members.map((key) => ({
        key,
        class: this.repositories.find((r) => r.project === repo.project && r.key === key)?.class === 'remote' ? 'remote' : 'local',
        decision: 'hit' as const,
      })),
      unreadableMembers: 0,
    };
  }

  override async testUpstream(project: string, body: UpstreamTestBody): Promise<UpstreamTestResult> {
    this.upstreamTests.push(body);
    this.upstreamTestProjects.push(project);
    if (this.upstreamTestError) throw this.upstreamTestError;
    return structuredClone(this.upstreamTestResult);
  }

  /** The calls of `defaults` and what they answer: `defaultsFor` decides when set, else `sampleRepositoryDefaults` over the fake. */
  defaultsCalls: { project: string; format: string; cls: RepositoryClass }[] = [];
  defaultsError: ApiError | null = null;
  defaultsFor: ((project: string, format: string, cls: RepositoryClass) => RepositoryDefaults) | null = null;

  override async defaults(project: string, format: string, cls: RepositoryClass): Promise<RepositoryDefaults> {
    this.defaultsCalls.push({ project, format, cls });
    if (this.defaultsError) throw this.defaultsError;
    if (this.defaultsFor) return structuredClone(this.defaultsFor(project, format, cls));
    const descriptor = this.formatsResult.find((f) => f.name === format);
    if (!descriptor || !descriptor.classes.includes(cls)) {
      throw new ApiError(
        422,
        { code: 'validation_failed', detail: `${format} has no class ${cls}`, requestId: 'rid-422' },
        'rid-422',
      );
    }
    const taken = this.repositories.filter((r) => r.project === project).map((r) => r.key);
    return sampleRepositoryDefaults(descriptor, cls, taken);
  }
}

/**
 * What `getRepositoryDefaults` answers for a format of the fake (R-REPO-063), from the server's rules: the first free key of
 * `<format>-<class>` and `-2` to `-9`, the common members at their defaults, the visibility `private`, and the class's section
 * as a bare create stores it (a remote's default preset with its hosts and the format's periods; a virtual without members).
 */
export function sampleRepositoryDefaults(
  format: FormatDescriptor,
  cls: RepositoryClass,
  takenKeys: readonly string[] = [],
): RepositoryDefaults {
  const base = `${format.name}-${cls}`;
  const candidates = [base, ...[2, 3, 4, 5, 6, 7, 8, 9].map((n) => `${base}-${n}`)];
  const suggestedKey = candidates.find((k) => !takenKeys.includes(k)) ?? null;
  const repository: RepositoryDefaults['repository'] = {
    class: cls,
    format: format.name,
    description: '',
    adminNotes: '',
    includePatterns: [],
    excludePatterns: [],
    disabled: false,
    hideExistence: 'inherit',
    visibility: { artifacts: 'private', wiki: 'private' },
  };
  if (cls === 'remote') {
    const preset = format.upstreamPresets.find((p) => p.default);
    (repository as Record<string, unknown>)['remote'] = {
      url: preset?.url ?? '',
      extraHosts: (preset?.extraHosts ?? []).map((h) => ({ ...h })),
      authMode: 'none',
      username: '',
      password: null,
      cache: {
        metadataTtlSeconds: format.defaultMetadataTtlSeconds ?? 0,
        notFoundTtlSeconds: format.defaultNotFoundTtlSeconds ?? 0,
      },
      availability: { mode: 'online' },
      mode: 'pullThrough',
      mirror: {
        paused: false,
        intervalSeconds: 3600,
        fullPassIntervalSeconds: 604800,
        discovery: 'auto',
        concurrency: 4,
        maxVersionsPerPackage: 0,
        includePrereleases: true,
        maxBytes: null,
      },
    };
  }
  if (cls === 'virtual') {
    (repository as Record<string, unknown>)['virtual'] = { members: [], deployTarget: null };
  }
  return { suggestedKey, repository };
}

/** A mirror status of the API's example: a scheduled mirror after a complete pass. */
export function sampleMirrorStatus(over: Partial<MirrorStatus> = {}): MirrorStatus {
  return {
    mode: 'mirror',
    state: 'scheduled',
    pass: null,
    lastCompletePass: {
      kind: 'full',
      method: 'feed',
      startedAt: '2026-11-03T10:00:00Z',
      finishedAt: '2026-11-03T13:12:09Z',
      outcome: 'complete',
      packages: 1200,
      packagesFailed: 0,
      itemsStored: 5021,
      bytesStored: 9126805504,
      discoveryComplete: true,
      discoveryNote: null,
    },
    nextPassAt: '2026-11-04T11:00:00Z',
    lastFullPassAt: '2026-11-03T13:12:09Z',
    errors: { consecutiveFailures: 0, lastAt: null, lastCode: null, lastMessage: null },
    rateLimit: { waits: 0, waitSeconds: 0, lastAt: null, lastStatus: null, until: null },
    bound: { reached: null, at: null, maxBytes: null, usedBytes: 9200000000 },
    ...over,
  };
}

export function sampleUpstreamTestResult(over: Partial<UpstreamTestResult> = {}): UpstreamTestResult {
  return {
    reachable: true,
    httpStatus: 200,
    authenticated: true,
    durationMs: 184,
    tls: { peerSubject: 'CN=registry.npmjs.org', notAfter: '2027-03-01T00:00:00Z' },
    outboundProxy: null,
    sourceAddress: null,
    tokenService: null,
    error: null,
    ...over,
  };
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
      'web/generic-local',
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
  /** While set, every children read waits for it: a load that stays in flight. */
  childGate: Promise<void> | null = null;

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
            contentUrl: `${repositoryUrl(repository)}/content/${node.path}`,
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
      itemUrl: `https://trove.example.net${repositoryUrl(repository)}/items?path=${node.path}`,
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
    return async (cursor) => {
      this.childCalls.push({ path, cursor, limit });
      await this.childGate;
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
    repository: 'web/generic-local',
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
    repository: 'web/generic-local',
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
      repository: 'web/generic-local',
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
