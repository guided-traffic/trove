import { Provider } from '@angular/core';
import {
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
import { Page, PageFetcher } from '../pagination';
import { ApiError } from '../problem';
import { Repository } from '../repositories-api';

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
  class: 'local',
  format: 'generic',
  description: 'Installers and build outputs',
};

/** The npm repositories of the fakes: a local, a remote, a virtual with and one without a target. */
export const NPM_REPOSITORIES: readonly Repository[] = [
  { key: 'npm-local', class: 'local', format: 'npm', description: 'Internal packages' },
  { key: 'npm-remote', class: 'remote', format: 'npm', description: 'Public registry cache' },
  { key: 'npm-all', class: 'virtual', format: 'npm', description: 'Everything for npm clients' },
  { key: 'npm-readonly', class: 'virtual', format: 'npm', description: 'Read-only view' },
];

/** The NuGet repositories of the fakes: a local, a remote and a virtual one that deploys to the local one. */
export const NUGET_REPOSITORIES: readonly Repository[] = [
  { key: 'nuget-local', class: 'local', format: 'nuget', description: 'Internal packages' },
  { key: 'nuget-remote', class: 'remote', format: 'nuget', description: 'nuget.org cache' },
  { key: 'nuget-all', class: 'virtual', format: 'nuget', description: 'Everything for NuGet clients' },
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
      { key: 'npm-local', class: 'local', publishedAt: '2026-10-20T09:12:44.512Z' },
      { key: 'npm-all', class: 'virtual', publishedAt: '2026-10-20T09:12:44.512Z' },
    ],
    files: [
      {
        path: file,
        sizeBytes: 48213,
        sha256: sha(0x5be0c1d2),
        downloadUrl: `${BASE}/api/npm/npm-local/${file}`,
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
 * verbatim and `authors` is the one string of the nuspec.
 */
function nugetVersion(v: string, over: Partial<FakeVersion> = {}): FakeVersion {
  const file = `contoso.logging.${v}.nupkg`;
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
      listed: true,
      authors: 'Contoso Team',
      projectUrl: 'https://example.com/contoso/logging',
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
    repositories: [{ key: 'nuget-local', class: 'local', publishedAt: '2026-10-25T00:00:00.000Z' }],
    files: [
      {
        path: file,
        sizeBytes: 20480,
        sha256: sha(0x7ac3),
        downloadUrl: `${BASE}/api/nuget/nuget-local/v3/flatcontainer/contoso.logging/${v}/${file}`,
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

export class FakePackagesApi extends PackagesApi {
  packages: PackageSummary[] = [
    {
      id: 'pkg_01JBQ6V7S3K9',
      format: 'npm',
      name: '@team-a/ui',
      description: 'Shared UI components',
      latestVersion: '1.4.0',
      keywords: ['ui', 'components'],
      repositories: ['npm-local', 'npm-all'],
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
      repositories: ['npm-remote', 'npm-all'],
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
      repositories: ['nuget-local'],
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
}

/** Single-quotes a value for a shell unless it is plain (R-API-030). */
export function shellQuote(value: string): string {
  return /^[A-Za-z0-9@/._+:=-]+$/.test(value) ? value : `'${value.replace(/'/g, `'\\''`)}'`;
}

export class FakeClientSetupApi extends ClientSetupApi {
  calls: { repository: string; query: ClientSetupQuery }[] = [];
  failure: ApiError | null = null;
  /** The virtual repository without a default deployment target has no publish snippets. */
  noTarget = new Set<string>(['npm-readonly']);
  /** Repositories the caller may read but not publish to. */
  readOnly = new Set<string>(['npm-remote']);
  ttl = 2_592_000;

  override async get(repository: string, query: ClientSetupQuery): Promise<ClientSetup> {
    this.calls.push({ repository, query });
    if (this.failure) {
      const e = this.failure;
      this.failure = null;
      throw e;
    }
    if (repository === GENERIC_REPOSITORY.key) {
      return curlSetup(repository, query, !this.readOnly.has(repository), this.ttl);
    }
    const nuget = NUGET_REPOSITORIES.find((r) => r.key === repository);
    if (nuget) {
      const mayPublish = !this.readOnly.has(repository) && nuget.class !== 'remote' && !this.noTarget.has(repository);
      return nugetSetup(nuget, query, mayPublish, this.ttl);
    }
    const repo = NPM_REPOSITORIES.find((r) => r.key === repository);
    if (!repo) throw notFound();
    const credentials = query.credentials ?? 'placeholder';
    const registryUrl = `${BASE}/api/npm/${repository}/`;
    const endpointPath = registryUrl.replace(/^https?:/, '');
    const target =
      this.readOnly.has(repository) || this.noTarget.has(repository)
        ? null
        : repo.class === 'virtual'
          ? 'npm-local'
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
        content: `registry=${registryUrl}\n` + auth(`${endpointPath}:_authToken=TOKEN\n`),
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
 * The NuGet snippets (R-NUGET-026, R-UI-018): always the `index.json` URL, a `nuget.config` that clears
 * the other sources, the token as the password and, on a repository that receives uploads, the push
 * with the token as the API key. A virtual repository publishes to its default target, `nuget-local`.
 */
function nugetSetup(repo: Repository, query: ClientSetupQuery, mayPublish: boolean, ttl: number): ClientSetup {
  const clients = ['dotnet', 'nuget', 'visual-studio'];
  if (!clients.includes(query.client)) {
    throw new ApiError(400, { code: 'malformed_request', detail: 'unknown client' }, null);
  }
  const credentials = query.credentials ?? 'placeholder';
  const feed = (key: string): string => `${BASE}/api/nuget/${key}/v3/index.json`;
  const url = feed(repo.key);
  const target = mayPublish ? (repo.class === 'virtual' ? 'nuget-local' : repo.key) : null;
  const token = credentials === 'placeholder' ? '{{token}}' : credentials === 'env' ? '${TROVE_TOKEN}' : null;
  const user = credentials === 'none' ? null : 'alice';
  const pkg = query.package ?? '<id>';
  const ver = query.package && query.version ? ` --version ${query.version}` : query.package ? '' : ' --version <version>';
  const snippets: ClientSnippet[] = [];
  const name = 'trove';
  if (query.client === 'dotnet') {
    snippets.push({
      id: 'dotnet.configure.add-source',
      tab: 'configure',
      fileName: null,
      language: 'shell',
      content:
        `dotnet nuget add source "${url}" \\\n  --name ${name}` +
        (token === null ? '\n' : ` --username "${user}" --password "${token}" --store-password-in-clear-text\n`),
    });
  }
  if (query.client !== 'visual-studio') {
    snippets.push({
      id: `${query.client}.configure.nuget-config`,
      tab: 'configure',
      fileName: 'nuget.config',
      language: 'xml',
      content:
        '<?xml version="1.0" encoding="utf-8"?>\n<configuration>\n  <packageSources>\n    <clear />\n' +
        `    <add key="${name}" value="${url}" />\n  </packageSources>\n` +
        (token === null
          ? ''
          : `  <packageSourceCredentials>\n    <${name}>\n      <add key="Username" value="${user}" />\n` +
            `      <add key="ClearTextPassword" value="%TROVE_TOKEN%" />\n    </${name}>\n  </packageSourceCredentials>\n`) +
        '</configuration>\n',
    });
  }
  if (query.client === 'nuget') {
    snippets.push({
      id: 'nuget.configure.sources-add',
      tab: 'configure',
      fileName: null,
      language: 'shell',
      content: `nuget sources add -Name ${name} -Source "${url}"` + (token === null ? '\n' : ` -Username "${user}" -Password "${token}"\n`),
    });
  }
  if (query.client === 'visual-studio') {
    snippets.push({
      id: 'visual-studio.configure.source',
      tab: 'configure',
      fileName: null,
      language: 'text',
      content: `Name: ${name}\nSource: ${url}\n` + (token === null ? '' : `User name: ${user}\nPassword: ${token}\n`),
    });
  }
  const install: Record<string, string> = {
    dotnet: `dotnet add package ${pkg}${ver}\ndotnet restore\n`,
    nuget: `nuget install ${pkg}${query.version ? ` -Version ${query.version}` : ''} -Source "${url}"\n`,
    'visual-studio': `Install-Package ${pkg}${query.version ? ` -Version ${query.version}` : ''} -Source ${name}\n`,
  };
  snippets.push({ id: `${query.client}.install`, tab: 'install', fileName: null, language: 'shell', content: install[query.client] ?? '' });
  if (target !== null && query.client !== 'visual-studio') {
    const push =
      query.client === 'dotnet'
        ? `dotnet nuget push <id>.<version>.nupkg --source "${feed(target)}"` + (token === null ? '' : ` --api-key "${token}"`)
        : `nuget push <id>.<version>.nupkg -Source "${feed(target)}"` + (token === null ? '' : ` -ApiKey "${token}"`);
    snippets.push({ id: `${query.client}.publish`, tab: 'publish', fileName: null, language: 'shell', content: push + '\n' });
  }
  return {
    repository: repo.key,
    format: 'nuget',
    client: query.client,
    credentials,
    registryUrl: url,
    endpointPath: url.replace(/^https?:/, ''),
    username: user,
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
