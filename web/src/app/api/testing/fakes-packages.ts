import { Provider } from '@angular/core';
import {
  DocsQuery,
  PackageDocEntry,
  PackageDocFile,
  PackageDocTree,
  ClientSetup,
  ClientSetupApi,
  ClientSetupQuery,
  ClientSnippet,
  PackageFilters,
  PackageReadme,
  PackageSummary,
  PackageVersion,
  PackageVersionSummary,
  PackagesApi,
  VersionFilters,
} from '../packages-api';
import { DocsFixture, commonFixture, docFile, docTree } from './fakes-ansible-docs';
import { Page, PageFetcher } from '../pagination';
import { ApiError } from '../problem';
import { Repository, refOf } from '../repositories-api';

/*
 * In-memory fakes of the packages and client setup services (R-UI-047 component tier). The setup
 * fake builds the npm snippets the way R-API-030 and R-UI-017 describe them, and the curl snippets
 * of the generic repository the way R-GENERIC-016 does, so a screen is tested against realistic
 * text: placeholders, the environment reference, quoting and the scheme-less key.
 */

const BASE = 'https://trove.example.net';

function notFound(): ApiError {
  return new ApiError(404, { code: 'not_found', detail: 'not found' }, 'rid-404');
}

function pages<T>(all: readonly T[]): PageFetcher<T> {
  return async (cursor, limit): Promise<Page<T>> => {
    const start = cursor === null ? 0 : Number(cursor);
    const end = start + limit;
    return { items: all.slice(start, end), nextCursor: end < all.length ? String(end) : null };
  };
}

/** The generic repository of the fakes, the first repository of the catalog fake. */
export const GENERIC_REPOSITORY: Repository = {
  key: 'generic-local',
  project: 'web',
  class: 'local',
  format: 'generic',
  description: 'Installers and build outputs',
};

/** The npm repositories of the fakes: a local, a remote, a virtual with and one without a target. */
export const NPM_REPOSITORIES: readonly Repository[] = [
  { key: 'npm-local', project: 'web', class: 'local', format: 'npm', description: 'Internal packages' },
  { key: 'npm-remote', project: 'web', class: 'remote', format: 'npm', description: 'Public registry cache' },
  { key: 'npm-all', project: 'web', class: 'virtual', format: 'npm', description: 'Everything for npm clients' },
  { key: 'npm-readonly', project: 'web', class: 'virtual', format: 'npm', description: 'Read-only view' },
];

/** The OCI repositories of the fakes: the format registers the class local only (R-OCI-001). */
export const OCI_REPOSITORIES: readonly Repository[] = [
  { key: 'oci-local', project: 'web', class: 'local', format: 'oci', description: 'Internal images' },
];

/** The NuGet repositories of the fakes: a local, a remote and a virtual one that deploys to the local one. */
export const NUGET_REPOSITORIES: readonly Repository[] = [
  { key: 'nuget-local', project: 'web', class: 'local', format: 'nuget', description: 'Internal packages' },
  { key: 'nuget-remote', project: 'web', class: 'remote', format: 'nuget', description: 'nuget.org cache' },
  { key: 'nuget-all', project: 'web', class: 'virtual', format: 'nuget', description: 'Everything for NuGet clients' },
];

/** The Ansible repository of the fakes: the one the package of {@link ansibleFixture} is in. */
export const ANSIBLE_REPOSITORIES: readonly Repository[] = [
  { key: 'ansible-local', project: 'web', class: 'local', format: 'ansible', description: 'Internal collections' },
];

interface FakeVersion extends PackageVersion {
  readme: PackageReadme | null;
}

function sha(n: number): string {
  return n.toString(16).padStart(64, '0');
}

function version(
  id: string,
  name: string,
  v: string,
  over: Partial<FakeVersion> = {},
): FakeVersion {
  const file = `${name}/-/${name.split('/').pop()}-${v}.tgz`;
  return {
    packageId: id,
    format: 'npm',
    name,
    version: v,
    prerelease: v.includes('-'),
    publishedAt: '2026-10-20T09:12:44.512Z',
    licenses: ['MIT'],
    sizeBytes: 48213,
    downloadCount: 410,
    hasReadme: true,
    metadata: {
      distTags: v === '1.4.0' ? { latest: '1.4.0', next: '1.5.0-beta.1' } : {},
      dependencies: { lit: '^3.1.0' },
      devDependencies: { vitest: '^3.0.0' },
      authors: ['Team A <team-a@example.com>'],
      homepage: 'https://example.com/team-a/ui',
      repositoryUrl: 'git+https://example.com/team-a/ui.git',
      bugsUrl: 'https://example.com/team-a/ui/issues',
      engines: { node: '>=22' },
    },
    repositories: [
      { key: 'web/npm-local', class: 'local', publishedAt: '2026-10-20T09:12:44.512Z' },
      { key: 'web/npm-all', class: 'virtual', publishedAt: '2026-10-20T09:12:44.512Z' },
    ],
    files: [
      {
        path: file,
        sizeBytes: 48213,
        sha256: sha(0x5be0c1d2),
        downloadUrl: `${BASE}/api/npm/web/npm-local/${file}`,
        contentUrl: `/api/v1/projects/web/repositories/npm-local/content/${file}`,
      },
    ],
    readme: {
      contentType: 'text/markdown',
      html: `<h1>${name}</h1>\n<p>Shared UI components.</p>`,
      truncated: false,
      externalImagesBlocked: 0,
    },
    ...over,
  };
}

/**
 * A NuGet version as the server's view adapter describes it (formats/nuget.md R-NUGET-007): the ID
 * keeps its original casing, `listed` is explicit, `dependencyGroups` carry the target framework
 * verbatim, `authors` is the list the server splits at commas and `homepage` is the project URL
 * (Q-NUGET-48, internal/formats/nuget/view.go).
 */
function nugetVersion(v: string, over: Partial<FakeVersion> = {}): FakeVersion {
  const file = `contoso.logging/${v}/contoso.logging.${v}.nupkg`;
  return {
    packageId: 'pkg_01JBQ7ZZZZZZ',
    format: 'nuget',
    name: 'Contoso.Logging',
    version: v,
    prerelease: v.includes('-'),
    publishedAt: '2026-10-25T00:00:00.000Z',
    licenses: ['MIT'],
    sizeBytes: 20480,
    downloadCount: 12,
    hasReadme: true,
    metadata: {
      id: 'Contoso.Logging',
      listed: true,
      title: 'Contoso Logging',
      authors: ['Contoso Team', 'Ada Lovelace'],
      homepage: 'https://example.com/contoso/logging',
      minClientVersion: '4.3.0',
      packageTypes: ['Dependency'],
      licenseUrl: 'https://licenses.nuget.org/MIT',
      repositoryUrl: 'https://example.com/contoso/logging.git',
      tags: ['logging', 'diagnostics'],
      dependencyGroups: [
        {
          targetFramework: 'net8.0',
          dependencies: [
            { id: 'Microsoft.Extensions.Logging.Abstractions', range: '[8.0.0, )' },
            { id: 'System.Text.Json', range: '[8.0.4, )' },
          ],
        },
        { targetFramework: '.NETStandard2.0', dependencies: [{ id: 'System.Text.Json', range: '[6.0.0, )' }] },
        { targetFramework: 'net462', dependencies: [] },
      ],
    },
    repositories: [{ key: 'web/nuget-local', class: 'local', publishedAt: '2026-10-25T00:00:00.000Z' }],
    files: [
      {
        path: file,
        sizeBytes: 20480,
        sha256: sha(0x7ac3),
        downloadUrl: `${BASE}/api/nuget/web/nuget-local/v3/flatcontainer/${file}`,
        contentUrl: `/api/v1/projects/web/repositories/nuget-local/content/${file}`,
      },
    ],
    readme: {
      contentType: 'text/markdown',
      html: '<h1>Contoso.Logging</h1>\n<p>Structured logging for Contoso services.</p>',
      truncated: false,
      externalImagesBlocked: 0,
    },
    ...over,
  };
}

const digestOf = (n: number): string => 'sha256:' + n.toString(16).padEnd(64, 'f');
const OCI_SINGLE_DIGEST = digestOf(0xa4d64);
const OCI_INDEX_DIGEST = digestOf(0x1d3a);

/** The package of the OCI fakes. It is not in the default list of {@link FakePackagesApi}; a test adds it with {@link ociFixture}. */
export const OCI_PACKAGE: PackageSummary = {
  id: 'pkg_01JBQ8OCIOCI',
  format: 'oci',
  name: 'team/app',
  description: null,
  latestVersion: '1.2.0',
  keywords: [],
  repositories: ['web/oci-local'],
  createdAt: '2026-11-01T00:00:00.000Z',
  updatedAt: '2026-11-05T00:00:00.000Z',
  versionCount: 2,
  downloadCount: 0,
};

/**
 * An OCI tag as the view adapter describes it (formats/oci.md R-OCI-031, internal/formats/oci/view.go). A tag has no README and no
 * license. Its one file is the lead item `<name>/_tags/<tag>`, the bytes of the manifest the tag points at, so its SHA-256 is the
 * manifest's digest; the manifest item is not linked to the version (InsertVersion links the lead item only, OCI never calls LinkItem).
 * The version's `sizeBytes` is the sum of its linked items, the manifest document's bytes, not the content size of the metadata.
 */
function ociVersion(tag: string, metadata: Record<string, unknown>, manifestSize: number, over: Partial<FakeVersion> = {}): FakeVersion {
  const hex = String(metadata['digest']).replace('sha256:', '');
  const file = (path: string): PackageVersion['files'][number] => ({
    path,
    sizeBytes: manifestSize,
    sha256: hex,
    downloadUrl: `${BASE}/api/v1/projects/web/repositories/oci-local/content/${path}`,
    contentUrl: `/api/v1/projects/web/repositories/oci-local/content/${path}`,
  });
  return {
    packageId: OCI_PACKAGE.id,
    format: 'oci',
    name: OCI_PACKAGE.name,
    version: tag,
    prerelease: false,
    publishedAt: '2026-11-05T00:00:00.000Z',
    licenses: [],
    sizeBytes: manifestSize,
    downloadCount: 0,
    hasReadme: false,
    metadata,
    repositories: [{ key: 'web/oci-local', class: 'local', publishedAt: '2026-11-05T00:00:00.000Z' }],
    files: [file(`team/app/_tags/${tag}`)],
    readme: null,
    ...over,
  };
}

const MANIFEST = 'application/vnd.oci.image.manifest.v1+json';

/**
 * The OCI package with a single-platform tag (`1.1.0`, which has an artifact type: the media type of its config) and a multi-platform
 * index (`1.2.0`, which has none): two images, two provenance attestations that are both `unknown/unknown`, and a child the index
 * names without a platform (`""` on the wire, the COALESCE of the rows).
 */
export function ociFixture(fake: FakePackagesApi): void {
  fake.packages.push(OCI_PACKAGE);
  const children = [
    { platform: 'linux/amd64', digest: digestOf(0xa4d64), mediaType: MANIFEST, size: 31_457_280 },
    { platform: 'linux/arm64', digest: digestOf(0xa4c64), mediaType: MANIFEST, size: 31_457_000 },
    { platform: 'unknown/unknown', digest: digestOf(0xa77e1), mediaType: MANIFEST, size: 280 },
    { platform: 'unknown/unknown', digest: digestOf(0xa77e2), mediaType: MANIFEST, size: 281 },
    { platform: '', digest: digestOf(0xb1a4c), mediaType: MANIFEST, size: 99 },
  ];
  fake.versionList.push(
    ociVersion(
      '1.1.0',
      { digest: OCI_SINGLE_DIGEST, mediaType: MANIFEST, size: 31_457_280, artifactType: 'application/vnd.oci.image.config.v1+json' },
      482,
      { publishedAt: '2026-11-01T00:00:00.000Z' },
    ),
    ociVersion(
      '1.2.0',
      {
        digest: OCI_INDEX_DIGEST,
        mediaType: 'application/vnd.oci.image.index.v1+json',
        size: children.reduce((n, c) => n + c.size, 0),
        platforms: children,
      },
      1024,
    ),
  );
}

/** The Debian repositories of the fakes: a local, a remote and a virtual one. */
export const DEB_REPOSITORIES: readonly Repository[] = [
  { key: 'deb-local', project: 'web', class: 'local', format: 'deb', description: 'Internal packages' },
  { key: 'deb-remote', project: 'web', class: 'remote', format: 'deb', description: 'Debian archive cache' },
  { key: 'deb-all', project: 'web', class: 'virtual', format: 'deb', description: 'Everything for APT clients' },
];

/** The package of the Debian fakes. It is not in the default list of {@link FakePackagesApi}; a test adds it with {@link debFixture}. */
export const DEB_PACKAGE: PackageSummary = {
  id: 'pkg_01JBQ9DEBDEB',
  format: 'deb',
  name: 'hello',
  description: 'example package based on GNU hello',
  latestVersion: '2.10-3',
  keywords: [],
  repositories: ['web/deb-local'],
  createdAt: '2026-11-01T00:00:00.000Z',
  updatedAt: '2026-11-08T00:00:00.000Z',
  versionCount: 2,
  downloadCount: 0,
};

/**
 * A Debian version as the view adapter describes it (formats/debian.md R-DEB-030, internal/formats/deb/view.go): the architectures of the
 * files, the coordinates as `suite/component` and the control fields it sends, those the version has. There is no README.
 */
function debVersion(v: string, metadata: Record<string, unknown>, over: Partial<FakeVersion> = {}): FakeVersion {
  const file = (arch: string): PackageVersion['files'][number] => {
    const path = `pool/main/h/hello/hello_${v}_${arch}.deb`;
    return {
      path,
      sizeBytes: 56_132,
      sha256: sha(arch === 'amd64' ? 0x3d1 : 0x3d2),
      downloadUrl: `${BASE}/api/deb/web/deb-local/${path}`,
      contentUrl: `/api/v1/projects/web/repositories/deb-local/content/${path}`,
    };
  };
  return {
    packageId: DEB_PACKAGE.id,
    format: 'deb',
    name: 'hello',
    version: v,
    prerelease: false,
    publishedAt: '2026-11-08T00:00:00.000Z',
    licenses: [],
    sizeBytes: 112_264,
    downloadCount: 0,
    hasReadme: false,
    metadata,
    repositories: [{ key: 'web/deb-local', class: 'local', publishedAt: '2026-11-08T00:00:00.000Z' }],
    files: [file('amd64'), file('arm64')],
    readme: null,
    ...over,
  };
}

/** Adds the Debian package `hello` with `2.10-3` (full control data, two architectures, one coordinate) and `2.9-1` (a bare version). */
export function debFixture(fake: FakePackagesApi): void {
  fake.packages.push(DEB_PACKAGE);
  fake.versionList.push(
    debVersion('2.9-1', { architectures: ['amd64'], coordinates: ['stable/main'] }, { publishedAt: '2026-10-01T00:00:00.000Z', files: [] }),
    debVersion('2.10-3', {
      architectures: ['amd64', 'arm64'],
      coordinates: ['stable/main'],
      maintainer: 'Jane Maintainer <jane@example.com>',
      section: 'devel',
      priority: 'optional',
      installedSize: '280',
      homepage: 'https://www.gnu.org/software/hello/',
      depends: 'libc6 (>= 2.34)',
      source: 'hello (2.10-3)',
    }),
  );
}

/** The package of the Ansible fakes. It is not in the default list of {@link FakePackagesApi}; a test adds it with {@link ansibleFixture}. */
export const ANSIBLE_PACKAGE: PackageSummary = {
  id: 'pkg_01JBQ9ANSANS',
  format: 'ansible',
  name: 'acme.tools',
  description: 'Tools of the Acme playbooks',
  latestVersion: '1.2.0',
  keywords: ['tools', 'linux'],
  repositories: ['web/ansible-local'],
  createdAt: '2026-11-01T00:00:00.000Z',
  updatedAt: '2026-11-08T00:00:00.000Z',
  versionCount: 2,
  downloadCount: 0,
};

/**
 * An Ansible collection version as the view adapter describes it (formats/ansible.md R-ANS-026): `requiresAnsible`, the
 * dependencies on other collections, the authors and the links; the licences and the tags are the generic fields.
 */
function ansibleVersion(v: string, metadata: Record<string, unknown>, over: Partial<FakeVersion> = {}): FakeVersion {
  const path = `collections/acme/tools/acme-tools-${v}.tar.gz`;
  return {
    packageId: ANSIBLE_PACKAGE.id,
    format: 'ansible',
    name: 'acme.tools',
    version: v,
    prerelease: false,
    publishedAt: '2026-11-08T00:00:00.000Z',
    licenses: [],
    sizeBytes: 20_480,
    downloadCount: 0,
    hasReadme: false,
    metadata,
    repositories: [{ key: 'web/ansible-local', class: 'local', publishedAt: '2026-11-08T00:00:00.000Z' }],
    files: [
      {
        path,
        sizeBytes: 20_480,
        sha256: sha(0x4a1),
        downloadUrl: `${BASE}/api/ansible/web/ansible-local/v3/artifacts/collections/acme-tools-${v}.tar.gz`,
        contentUrl: `/api/v1/projects/web/repositories/ansible-local/content/${path}`,
      },
    ],
    readme: null,
    ...over,
  };
}

/** Adds the collection `acme.tools` with `1.2.0` (full manifest data) and `1.0.0` (a bare version). */
export function ansibleFixture(fake: FakePackagesApi): void {
  fake.packages.push(ANSIBLE_PACKAGE);
  fake.versionList.push(
    ansibleVersion('1.0.0', {}, { publishedAt: '2026-10-01T00:00:00.000Z' }),
    ansibleVersion('1.2.0', {
      requiresAnsible: '>=2.15.0',
      dependencies: { 'community.general': '>=8.0.0' },
      authors: ['Jane Maintainer'],
      documentationUrl: 'https://example.com/acme/tools/docs',
      homepage: 'https://example.com/acme/tools',
      repositoryUrl: 'https://example.com/acme/tools',
      bugsUrl: 'https://example.com/acme/tools/issues',
    }, { licenses: ['Apache-2.0', 'MIT'] }),
  );
}

/**
 * Gives `acme.tools@1.2.0` the documentation of {@link commonFixture} (or `fx`): the version announces it in `metadata.docs` and has a
 * README, as a version that the generator has read does (R-ANS-036). Call it after {@link ansibleFixture}.
 */
export function ansibleDocsFixture(fake: FakePackagesApi, fx: DocsFixture = commonFixture(), version = '1.2.0'): DocsFixture {
  const v = fake.versionList.find((x) => x.packageId === ANSIBLE_PACKAGE.id && x.version === version);
  if (!v) throw new Error(`no version ${version}`);
  fake.docFixtures.set(`${ANSIBLE_PACKAGE.id}@${version}`, fx);
  v.hasReadme = fx.files.some((f) => f.name === 'README.md');
  v.metadata = {
    ...v.metadata,
    docs: {
      state: fx.state,
      ...(fx.state === 'available' ? { source: fx.source, entries: fx.entries.length } : {}),
    },
  };
  return fx;
}

export class FakePackagesApi extends PackagesApi {
  packages: PackageSummary[] = [
    {
      id: 'pkg_01JBQ6V7S3K9',
      format: 'npm',
      name: '@team-a/ui',
      description: 'Shared UI components',
      latestVersion: '1.4.0',
      keywords: ['ui', 'components'],
      repositories: ['web/npm-local', 'web/npm-all'],
      createdAt: '2026-09-02T08:00:12.201Z',
      updatedAt: '2026-10-20T09:12:44.512Z',
      versionCount: 3,
      downloadCount: 1832,
    },
    {
      id: 'pkg_01JBQ7A1B2C3',
      format: 'npm',
      name: 'left-pad',
      description: 'Pads a string on the left',
      latestVersion: '1.3.0',
      keywords: ['string'],
      repositories: ['web/npm-remote', 'web/npm-all'],
      createdAt: '2026-08-11T12:00:00.000Z',
      updatedAt: '2026-08-11T12:00:00.000Z',
      versionCount: 1,
      downloadCount: 77,
    },
    {
      id: 'pkg_01JBQ7ZZZZZZ',
      format: 'nuget',
      name: 'Contoso.Logging',
      description: null,
      latestVersion: '2.3.1',
      keywords: [],
      repositories: ['web/nuget-local'],
      createdAt: '2026-10-01T00:00:00.000Z',
      updatedAt: '2026-10-25T00:00:00.000Z',
      versionCount: 2,
      downloadCount: 0,
    },
  ];
  versionList: FakeVersion[] = [
    version('pkg_01JBQ6V7S3K9', '@team-a/ui', '1.3.0', { publishedAt: '2026-09-02T08:00:12.201Z', downloadCount: 900 }),
    version('pkg_01JBQ6V7S3K9', '@team-a/ui', '1.4.0'),
    version('pkg_01JBQ6V7S3K9', '@team-a/ui', '1.5.0-beta.1', {
      publishedAt: '2026-10-24T10:00:00.000Z',
      downloadCount: 3,
      hasReadme: false,
      readme: null,
    }),
    version('pkg_01JBQ7A1B2C3', 'left-pad', '1.3.0', { hasReadme: false, readme: null }),
    nugetVersion('2.3.1'),
    nugetVersion('2.2.0', {
      publishedAt: '2026-10-01T00:00:00.000Z',
      hasReadme: false,
      readme: null,
      metadata: { listed: false, authors: 'Contoso Team', dependencyGroups: [] },
    }),
  ];
  listFilters: PackageFilters[] = [];
  /** Rejects the next `list` page once. */
  listError: ApiError | null = null;
  readmeCalls: string[] = [];
  /** The documentation by `<packageId>@<version>`; a version without an entry answers 404 (R-ANS-036). */
  docFixtures = new Map<string, DocsFixture>();
  /** Each call of the three documentation operations: `tree`, `tree?q=…`, `entry:<type>/<name>`, `file:<name>`. */
  docCalls: string[] = [];
  /** The refusal the next call of a documentation operation throws once. */
  docFailure: ApiError | null = null;
  /** The repository each documentation call asked for, in call order (`''`: none). */
  docRepositories: string[] = [];

  override list(filters: PackageFilters): PageFetcher<PackageSummary> {
    this.listFilters.push(filters);
    const q = filters.q?.toLowerCase();
    let items = this.packages
      .filter((p) => !filters.format?.length || filters.format.includes(p.format))
      .filter((p) => !filters.repository?.length || p.repositories.some((r) => filters.repository?.includes(r)))
      .filter((p) => !q || p.name.toLowerCase().includes(q) || (p.description ?? '').toLowerCase().includes(q))
      .filter((p) => !filters.namePrefix || p.name.startsWith(filters.namePrefix))
      .filter((p) => !filters.keyword || p.keywords.includes(filters.keyword))
      .filter((p) => !filters.createdFrom || p.createdAt >= filters.createdFrom)
      .filter((p) => !filters.createdTo || p.createdAt < filters.createdTo);
    const sort = filters.sort ?? 'name';
    const desc = sort.startsWith('-');
    const field = sort.replace('-', '') as 'name' | 'createdAt' | 'updatedAt';
    items = [...items].sort((a, b) => (desc ? -1 : 1) * a[field].localeCompare(b[field]));
    const fetch = pages(items);
    return async (cursor, limit) => {
      if (this.listError) {
        const e = this.listError;
        this.listError = null;
        throw e;
      }
      return fetch(cursor, limit);
    };
  }

  override async lookup(format: string, name: string): Promise<PackageSummary> {
    const found = this.packages.find((p) => p.format === format && p.name === name);
    if (!found) throw notFound();
    return found;
  }

  override versions(packageId: string, filters: VersionFilters = {}): PageFetcher<PackageVersionSummary> {
    const order = (a: PackageVersionSummary, b: PackageVersionSummary): number => {
      switch (filters.sort ?? '-publishedAt') {
        case 'publishedAt':
          return a.publishedAt.localeCompare(b.publishedAt);
        case '-version':
          return b.version.localeCompare(a.version, undefined, { numeric: true });
        case 'version':
          return a.version.localeCompare(b.version, undefined, { numeric: true });
        default:
          return b.publishedAt.localeCompare(a.publishedAt);
      }
    };
    const items = this.versionList
      .filter((v) => v.packageId === packageId)
      .filter((v) => filters.prerelease === undefined || v.prerelease === filters.prerelease)
      .map(
        (v): PackageVersionSummary => ({
          version: v.version,
          prerelease: v.prerelease,
          publishedAt: v.publishedAt,
          sizeBytes: v.sizeBytes,
          repositories: v.repositories.map((r) => r.key),
          downloadCount: v.downloadCount,
          ...(filters.include?.includes('digest') && typeof v.metadata['digest'] === 'string' ? { digest: v.metadata['digest'] } : {}),
        }),
      )
      .sort(order);
    return pages(items);
  }

  override async version(packageId: string, v: string): Promise<PackageVersion> {
    const found = this.versionList.find((x) => x.packageId === packageId && x.version === v);
    if (!found) throw notFound();
    const { readme: _readme, ...rest } = found;
    void _readme;
    return rest;
  }

  override async readme(packageId: string, v: string): Promise<PackageReadme | null> {
    this.readmeCalls.push(`${packageId}@${v}`);
    return this.versionList.find((x) => x.packageId === packageId && x.version === v)?.readme ?? null;
  }

  private doc(call: string, packageId: string, v: string, repository: string | undefined): DocsFixture | null {
    this.docCalls.push(call);
    this.docRepositories.push(repository ?? '');
    if (this.docFailure) {
      const e = this.docFailure;
      this.docFailure = null;
      throw e;
    }
    return this.docFixtures.get(`${packageId}@${v}`) ?? null;
  }

  override async docs(packageId: string, v: string, query: DocsQuery = {}): Promise<PackageDocTree | null> {
    const fx = this.doc(query.q ? `tree?q=${query.q}` : 'tree', packageId, v, query.repository);
    return fx ? docTree(fx, query.q) : null;
  }

  override async docEntry(packageId: string, v: string, type: string, name: string, repository?: string): Promise<PackageDocEntry> {
    const fx = this.doc(`entry:${type}/${name}`, packageId, v, repository);
    const found = fx?.entries.find((e) => e.type === type && e.name === name);
    if (!found) throw notFound();
    return found;
  }

  override async docFile(packageId: string, v: string, file: string, repository?: string): Promise<PackageDocFile> {
    const fx = this.doc(`file:${file}`, packageId, v, repository);
    const found = fx ? docFile(fx, file) : null;
    if (!found) throw notFound();
    return found;
  }
}

/** Single-quotes a value for a shell unless it is plain (R-API-030). */
export function shellQuote(value: string): string {
  return /^[A-Za-z0-9@/._+:=-]+$/.test(value) ? value : `'${value.replace(/'/g, `'\\''`)}'`;
}

export class FakeClientSetupApi extends ClientSetupApi {
  calls: { repository: string; query: ClientSetupQuery }[] = [];
  failure: ApiError | null = null;
  /** The virtual repository without a default deployment target has no publish snippets. */
  noTarget = new Set<string>(['web/npm-readonly']);
  /** Repositories the caller may read but not publish to. */
  readOnly = new Set<string>(['web/npm-remote']);
  ttl = 2_592_000;
  /**
   * The base URL each Bastion registration reported and the installation lists (R-BAS-007); `null` is a
   * registration whose address is not listed (409), a name that is not here is no registration (404).
   */
  bastionBases: Record<string, string | null> = { partners: 'https://packages.example.com', unlisted: null };

  override async get(repository: string, query: ClientSetupQuery): Promise<ClientSetup> {
    this.calls.push({ repository, query });
    if (this.failure) {
      const e = this.failure;
      this.failure = null;
      throw e;
    }
    if (repository === refOf(GENERIC_REPOSITORY)) {
      return curlSetup(repository, query, !this.readOnly.has(repository), this.ttl);
    }
    const nuget = NUGET_REPOSITORIES.find((r) => refOf(r) === repository);
    if (nuget) {
      const mayPublish = !this.readOnly.has(repository) && nuget.class !== 'remote' && !this.noTarget.has(repository);
      return nugetSetup(nuget, query, mayPublish, this.ttl);
    }
    const oci = OCI_REPOSITORIES.find((r) => refOf(r) === repository);
    if (oci) {
      const mayPublish = !this.readOnly.has(repository) && !this.noTarget.has(repository);
      return ociSetup(oci, query, mayPublish, this.ttl);
    }
    const ansible = ANSIBLE_REPOSITORIES.find((r) => refOf(r) === repository);
    if (ansible) {
      const mayPublish = !this.readOnly.has(repository) && !this.noTarget.has(repository);
      return ansibleSetup(ansible, query, mayPublish, this.ttl);
    }
    const repo = NPM_REPOSITORIES.find((r) => refOf(r) === repository);
    if (!repo) throw notFound();
    const credentials = query.credentials ?? 'placeholder';
    let base = BASE;
    if (query.bastion) {
      const reported = this.bastionBases[query.bastion];
      if (reported === undefined) throw notFound();
      if (reported === null) throw new ApiError(409, { code: 'bastion_base_url_unknown' }, null);
      base = reported;
    }
    const registryUrl = `${base}/api/npm/${repository}/`;
    const endpointPath = registryUrl.replace(/^https?:/, '');
    const target =
      query.bastion || this.readOnly.has(repository) || this.noTarget.has(repository)
        ? null
        : repo.class === 'virtual'
          ? 'web/npm-local'
          : repository;
    const token = credentials === 'placeholder' ? '{{token}}' : credentials === 'env' ? '${TROVE_TOKEN}' : null;
    const auth = (line: string): string => (token === null ? '' : line.replace('TOKEN', token));
    const snippets: ClientSnippet[] = [];
    const yarnBerry = query.client === 'yarn';
    const client = query.client;
    if (!['npm', 'pnpm', 'yarn', 'yarn-classic'].includes(client)) {
      throw new ApiError(400, { code: 'malformed_request', detail: 'unknown client' }, null);
    }
    if (yarnBerry) {
      snippets.push({
        id: 'yarn.configure.yarnrc',
        tab: 'configure',
        fileName: '.yarnrc.yml',
        language: 'yaml',
        content:
          `npmRegistryServer: "${registryUrl}"\n` +
          (token === null ? '' : `npmAlwaysAuth: true\nnpmAuthToken: "${token}"\n`),
      });
    } else {
      snippets.push({
        id: `${client}.configure.npmrc`,
        tab: 'configure',
        fileName: '.npmrc',
        language: 'ini',
        content:
          `registry=${registryUrl}\n` +
          auth(`${endpointPath}:_authToken=TOKEN\n`) +
          (query.bastion && client === 'npm' ? 'replace-registry-host=always\n' : ''),
      });
      if (query.scope) {
        snippets.push({
          id: `${client}.configure.npmrc-scoped`,
          tab: 'configure',
          fileName: '.npmrc',
          language: 'ini',
          content: `${query.scope}:registry=${registryUrl}\n` + auth(`${endpointPath}:_authToken=TOKEN\n`),
        });
      }
    }
    if (client === 'npm') {
      snippets.push({
        id: 'npm.configure.cli',
        tab: 'configure',
        fileName: null,
        language: 'shell',
        content: `npm config set registry=${registryUrl}\n` + auth(`npm config set "${endpointPath}:_authToken=TOKEN"\n`),
      });
    }
    const verb = client === 'npm' ? 'npm install' : client === 'pnpm' ? 'pnpm add' : 'yarn add';
    const spec = query.package
      ? shellQuote(query.version ? `${query.package}@${query.version}` : query.package)
      : '<package>';
    snippets.push({ id: `${client}.install`, tab: 'install', fileName: null, language: 'shell', content: `${verb} ${spec}` });
    if (target !== null) {
      // As the server renders them (npm format templates, R-API-030, Q-API-180): the registry of the
      // repository the caller chose, a virtual one forwards the publish to its deployment target.
      const publish: Record<string, [string, ClientSnippet['language'], string | null]> = {
        npm: [`npm publish --registry ${registryUrl}\n`, 'shell', null],
        pnpm: [`pnpm publish --registry ${registryUrl}\n`, 'shell', null],
        'yarn-classic': [`yarn publish --registry ${registryUrl}\n`, 'shell', null],
        yarn: [`npmPublishRegistry: "${registryUrl}"\n`, 'yaml', '.yarnrc.yml'],
      };
      const [content, language, fileName] = publish[client] ?? ['', 'text', null];
      snippets.push({ id: `${client}.publish`, tab: 'publish', fileName, language, content });
    }
    return {
      repository,
      format: 'npm',
      client,
      credentials,
      registryUrl,
      endpointPath,
      username: credentials === 'none' ? null : 'alice',
      publishTarget: target,
      tokenTtlSeconds: this.ttl,
      snippets,
    };
  }
}

/**
 * The NuGet snippets as the server renders them (internal/formats/nuget/setup.go, Q-NUGET-49): the
 * registry URL ends in a slash and the source is its `v3/index.json`; the ids are
 * `<client>.<section>[.<name>]`; the token is one spelling per shell (`${TROVE_TOKEN}`,
 * `%TROVE_TOKEN%`, `$env:TROVE_TOKEN`), replaced by the placeholder or by the variable of the
 * credential mode; with `none` the lines that hold a token go. The user name is the fixed `trove`.
 * A virtual repository publishes through itself, so the push names the repository the caller chose.
 */
function nugetSetup(repo: Repository, query: ClientSetupQuery, mayPublish: boolean, ttl: number): ClientSetup {
  const clients = ['dotnet', 'nuget', 'visual-studio'];
  if (!clients.includes(query.client)) {
    throw new ApiError(400, { code: 'malformed_request', detail: 'unknown client' }, null);
  }
  const credentials = query.credentials ?? 'placeholder';
  const registryUrl = `${BASE}/api/nuget/${refOf(repo)}/`;
  const source = `${registryUrl}v3/index.json`;
  const target = mayPublish ? (repo.class === 'virtual' ? 'web/nuget-local' : refOf(repo)) : null;
  const refs = {
    sh: credentials === 'placeholder' ? '{{token}}' : credentials === 'env' ? '${TROVE_TOKEN}' : null,
    cmd: credentials === 'placeholder' ? '{{token}}' : credentials === 'env' ? '%TROVE_TOKEN%' : null,
    ps: credentials === 'placeholder' ? '{{token}}' : credentials === 'env' ? '$env:TROVE_TOKEN' : null,
  };
  const auth = credentials !== 'none';
  const pkg = query.package ?? '<package>';
  const withVersion = query.package && query.version ? query.version : null;
  const snippets: ClientSnippet[] = [];
  const add = (id: string, tab: ClientSnippet['tab'], language: ClientSnippet['language'], content: string, fileName: string | null = null): void => {
    snippets.push({ id, tab, fileName, language, content });
  };

  const config =
    '<?xml version="1.0" encoding="utf-8"?>\n<configuration>\n  <packageSources>\n    <clear />\n' +
    `    <add key="trove" value="${source}" protocolVersion="3" />\n  </packageSources>\n` +
    (auth
      ? '  <packageSourceCredentials>\n    <trove>\n      <add key="Username" value="trove" />\n' +
        `      <add key="ClearTextPassword" value="${refs.cmd}" />\n    </trove>\n  </packageSourceCredentials>\n`
      : '') +
    '</configuration>\n';

  if (query.client === 'dotnet') {
    add(
      'dotnet.configure.shell',
      'configure',
      'shell',
      `dotnet nuget add source ${source} --name trove` + (auth ? ` \\\n  --username trove --password "${refs.sh}" --store-password-in-clear-text` : '') + '\n',
    );
    add(
      'dotnet.configure.powershell',
      'configure',
      'shell',
      `dotnet nuget add source ${source} --name trove` + (auth ? ` \`\n  --username trove --password "${refs.ps}" --store-password-in-clear-text` : '') + '\n',
    );
  }
  if (query.client === 'nuget') {
    add('nuget.configure.cmd', 'configure', 'shell', `nuget sources add -Name trove -Source ${source}` + (auth ? ` ^\n  -Username trove -Password ${refs.cmd}` : '') + '\n');
    add(
      'nuget.configure.powershell',
      'configure',
      'shell',
      `nuget sources add -Name trove -Source ${source}` + (auth ? ` \`\n  -Username trove -Password "${refs.ps}"` : '') + '\n',
    );
  }
  add(`${query.client}.configure.nugetconfig`, 'configure', 'xml', config, 'nuget.config');

  if (query.client === 'dotnet') {
    add('dotnet.install', 'install', 'shell', `dotnet add package ${pkg}${withVersion ? ` --version ${withVersion}` : ''}\n`);
  } else if (query.client === 'nuget') {
    add('nuget.install', 'install', 'shell', `nuget install ${pkg} -Source trove -OutputDirectory packages${withVersion ? ` -Version ${withVersion}` : ''}\n`);
  } else {
    add('visual-studio.install.packagereference', 'install', 'xml', `<PackageReference Include="${pkg}"${withVersion ? ` Version="${withVersion}"` : ''} />\n`);
  }

  if (target !== null && query.client !== 'visual-studio') {
    const feed = source;
    const key = (r: string | null, quote: boolean): string => (r === null ? '' : quote ? ` --api-key "${r}"` : '');
    if (query.client === 'dotnet') {
      const push = `dotnet nuget push bin/Release/Acme.Widgets.1.2.3.nupkg --source ${feed}`;
      add('dotnet.publish.shell', 'publish', 'shell', push + key(refs.sh, true) + '\n');
      add('dotnet.publish.powershell', 'publish', 'shell', push + key(refs.ps, true) + '\n');
    } else {
      const push = `nuget push Acme.Widgets.1.2.3.nupkg -Source ${feed}`;
      add('nuget.publish.cmd', 'publish', 'shell', push + (refs.cmd === null ? '' : ` -ApiKey ${refs.cmd}`) + '\n');
      add('nuget.publish.powershell', 'publish', 'shell', push + (refs.ps === null ? '' : ` -ApiKey "${refs.ps}"`) + '\n');
    }
  }
  return {
    repository: refOf(repo),
    format: 'nuget',
    client: query.client,
    credentials,
    registryUrl,
    endpointPath: source.replace(/^https?:/, ''),
    username: credentials === 'none' ? null : 'alice',
    publishTarget: target,
    tokenTtlSeconds: ttl,
    snippets,
  };
}

/**
 * The Ansible snippets as the server renders them (internal/formats/ansible/setup.go, R-ANS-025; the ids are
 * `<client>.<section>[.<name>]` and `snippet-ids.fixture.ts` pins them against the Go side). The server is named `trove` in an
 * `ansible.cfg` that holds no token, the token is the environment variable `ANSIBLE_GALAXY_SERVER_TROVE_TOKEN` in the spelling of each
 * shell, and a `~/.netrc` line is a command that names the host alone. The credential reference is rewritten by the mode of the request
 * the way the API does (`applyCredentials` of internal/api/v1/setup_snippets.go): the placeholder, the variable, or the line dropped.
 * `PowerShell` and `cmd` are reported as `shell`, the one language the document knows for them (Q-API-191).
 */
function ansibleSetup(repo: Repository, query: ClientSetupQuery, mayPublish: boolean, ttl: number): ClientSetup {
  if (query.client !== 'ansible-galaxy') {
    throw new ApiError(400, { code: 'malformed_request', detail: 'unknown client' }, null);
  }
  const credentials = query.credentials ?? 'placeholder';
  const registryUrl = `${BASE}/api/ansible/${refOf(repo)}/`;
  const host = new URL(BASE).hostname;
  const target = mayPublish ? refOf(repo) : null;
  const auth = credentials !== 'none';
  const sh = '${TROVE_TOKEN}';
  const cmd = '%TROVE_TOKEN%';
  const ps = '$env:TROVE_TOKEN';
  const variable = 'ANSIBLE_GALAXY_SERVER_TROVE_TOKEN';
  const anonymous = 'This repository is open to anonymous readers: no credential is needed to install from it.';
  const credential = (content: string): string => {
    if (credentials === 'env') return content;
    if (credentials === 'none') {
      return content
        .split(/(?<=\n)/)
        .filter((line) => ![sh, cmd, ps].some((ref) => line.includes(ref)))
        .join('');
    }
    return content.split(sh).join('{{token}}').split(cmd).join('{{token}}').split(ps).join('{{token}}');
  };
  const pkg = query.package ? shellQuote(query.package) : null;
  const snippets: ClientSnippet[] = [];
  const add = (id: string, tab: ClientSnippet['tab'], language: ClientSnippet['language'], content: string, fileName: string | null = null): void => {
    snippets.push({ id, tab, fileName, language, content: credential(content) });
  };
  add('ansible-galaxy.configure.ansible-cfg', 'configure', 'ini', `[galaxy]\nserver_list = trove\n\n[galaxy_server.trove]\nurl=${registryUrl}\n`, 'ansible.cfg');
  add(
    'ansible-galaxy.configure.token-env',
    'configure',
    'shell',
    auth ? `export ${variable}="${sh}"\n` : `# ${anonymous}\n`,
  );
  add(
    'ansible-galaxy.configure.token-env-powershell',
    'configure',
    'shell',
    auth ? `$env:${variable} = "${ps}"\n` : `# ${anonymous}\n`,
  );
  add('ansible-galaxy.configure.token-env-cmd', 'configure', 'shell', auth ? `set ${variable}=${cmd}\n` : `REM ${anonymous}\n`);
  add(
    'ansible-galaxy.configure.netrc',
    'configure',
    'shell',
    auth
      ? `touch ~/.netrc && chmod 600 ~/.netrc\nprintf 'machine %s login trove password %s\\n' ${host} "${sh}" >>~/.netrc\n`
      : '# This repository is open to anonymous readers: no ~/.netrc entry is needed.\n',
  );
  const version = query.package && query.version ? query.version : null;
  add('ansible-galaxy.install.collection', 'install', 'shell', `ansible-galaxy collection install ${pkg === null ? '<package>' : version ? `${pkg}:${shellQuote(version)}` : pkg}\n`);
  add(
    'ansible-galaxy.install.requirements',
    'install',
    'yaml',
    '---\ncollections:\n' +
      `  - name: ${query.package ?? '<package>'}${version ? `\n    version: "${version}"` : ''}\n` +
      '    source: trove\n' +
      '# Roles are served by remote and virtual repositories of a Galaxy only. Install them with\n' +
      '#   ansible-galaxy role install -r requirements.yml\n' +
      '# roles:\n' +
      '#   - name: <owner>.<role>\n',
    'requirements.yml',
  );
  if (target !== null) {
    add('ansible-galaxy.publish.collection', 'publish', 'shell', `ansible-galaxy collection publish ./acme-tools-1.0.0.tar.gz --server ${shellQuote(registryUrl)} --token=${sh}\n`);
  }
  return {
    repository: refOf(repo),
    format: 'ansible',
    client: query.client,
    credentials,
    registryUrl,
    endpointPath: registryUrl.replace(/^https?:/, ''),
    username: credentials === 'none' ? null : 'alice',
    publishTarget: target,
    tokenTtlSeconds: ttl,
    snippets,
  };
}

/**
 * The OCI snippets as the server renders them (internal/formats/oci/setup.go, R-OCI-030; the ids are `<client>.<section>[.<name>]`,
 * Q-API-160, and `snippet-ids.fixture.ts` pins them against the Go side): the image prefix is `<host>/<project>/<repository>`, not
 * the registry URL. With a credential the login reads the token from standard input; without one it is the interactive login and the
 * pull secret is a comment that asks for the snippet with a credential (the API drops the lines that hold the token reference).
 */
function ociSetup(repo: Repository, query: ClientSetupQuery, mayPublish: boolean, ttl: number): ClientSetup {
  if (!['docker', 'podman'].includes(query.client)) {
    throw new ApiError(400, { code: 'malformed_request', detail: 'unknown client' }, null);
  }
  const credentials = query.credentials ?? 'placeholder';
  const registryUrl = `${BASE}/v2/${refOf(repo)}/`;
  const prefix = `${BASE.replace(/^https?:\/\//, '')}/${refOf(repo)}`;
  const token = credentials === 'placeholder' ? '{{token}}' : credentials === 'env' ? '${TROVE_TOKEN}' : null;
  const target = mayPublish ? refOf(repo) : null;
  const tool = query.client;
  const pkg = query.package ? shellQuote(query.package) : '<package>';
  const spec = query.package && query.version ? `${pkg}:${shellQuote(query.version)}` : pkg;
  const snippets: ClientSnippet[] = [];
  const add = (id: string, tab: ClientSnippet['tab'], content: string): void => {
    snippets.push({ id, tab, fileName: null, language: 'shell', content });
  };
  add(
    `${tool}.configure.shell`,
    'configure',
    token === null
      ? `${tool} login ${prefix} --username trove\n`
      : `printf '%s' "${token}" | ${tool} login ${prefix} --username trove --password-stdin\n`,
  );
  if (tool === 'docker') {
    add(
      'docker.configure.kubernetes',
      'configure',
      token === null
        ? '# An image pull secret holds a credential: ask for the snippet with one.\n'
        : `printf '{"auths":{"%s":{"username":"trove","password":"%s"}}}' ${prefix} "${token}" | \\\n` +
            '  kubectl create secret generic trove-registry --type=kubernetes.io/dockerconfigjson --from-file=.dockerconfigjson=/dev/stdin\n',
    );
  }
  add(`${tool}.install`, 'install', `${tool} pull ${prefix}/${spec}\n`);
  if (target !== null) {
    add(`${tool}.publish`, 'publish', `${tool} tag myapp:1.0 ${prefix}/myapp:1.0\n${tool} push ${prefix}/myapp:1.0\n`);
    if (tool === 'docker') {
      add('docker.publish.buildx', 'publish', `docker buildx build --platform linux/amd64,linux/arm64 --tag ${prefix}/myapp:1.0 --push .\n`);
    }
  }
  return {
    repository: refOf(repo),
    format: 'oci',
    client: tool,
    credentials,
    registryUrl,
    endpointPath: registryUrl.replace(/^https?:/, ''),
    username: credentials === 'none' ? null : 'alice',
    publishTarget: target,
    tokenTtlSeconds: ttl,
    snippets,
  };
}

/** The curl snippets of a generic repository: the function of the configure tab and its calls. */
function curlSetup(repository: string, query: ClientSetupQuery, mayPublish: boolean, ttl: number): ClientSetup {
  if (query.client !== 'curl') {
    throw new ApiError(400, { code: 'malformed_request', detail: 'unknown client' }, null);
  }
  const credentials = query.credentials ?? 'placeholder';
  const registryUrl = `${BASE}/files/${repository}/`;
  const token = credentials === 'placeholder' ? '{{token}}' : credentials === 'env' ? '${TROVE_TOKEN}' : null;
  const header = token === null ? '' : `header = "Authorization: Bearer ${token}"\n`;
  const snippets: ClientSnippet[] = [
    {
      id: 'curl.configure.function',
      tab: 'configure',
      fileName: null,
      language: 'shell',
      content: `trove_curl() {\n  curl --silent --show-error --config - "$@" <<EOF\n${header}EOF\n}\n`,
    },
    {
      id: 'curl.install',
      tab: 'install',
      fileName: null,
      language: 'shell',
      content: `trove_curl --fail --remote-name "${registryUrl}<path>"\n`,
    },
  ];
  if (mayPublish) {
    snippets.push({
      id: 'curl.publish',
      tab: 'publish',
      fileName: null,
      language: 'shell',
      content: `trove_curl --fail --upload-file <file> "${registryUrl}<path>"\n`,
    });
  }
  return {
    repository,
    format: 'generic',
    client: 'curl',
    credentials,
    registryUrl,
    endpointPath: registryUrl.replace(/^https?:/, ''),
    username: credentials === 'none' ? null : 'alice',
    publishTarget: mayPublish ? repository : null,
    tokenTtlSeconds: ttl,
    snippets,
  };
}

export function providePackageFakes(): Provider[] {
  return [
    { provide: PackagesApi, useClass: FakePackagesApi },
    { provide: ClientSetupApi, useClass: FakeClientSetupApi },
  ];
}
