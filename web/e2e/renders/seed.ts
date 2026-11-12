import { createHash } from 'node:crypto';
import { gzipSync } from 'node:zlib';
import { APIRequestContext, APIResponse } from '@playwright/test';
import { advisoryArchive, advisoryMirror, gradedPackage, vendorPackages } from './advisories';

/**
 * The data of `make ui-renders` (docs/developer/web-ui.md#renders-of-every-screen): three projects, repositories of every
 * format and class with short and long keys, a few published packages and files, the three kinds of user of R-UI-067 and
 * an empty project and repository for the empty states. Everything is created through trove's public API and is
 * idempotent: a second run finds what the first made and adds nothing.
 */

/** The kinds of user the renders sign in as (R-UI-067), and the usernames of the profile's sign-in provider (test/devidp). */
export const kinds = { plain: 'sam', power: 'priya', admin: 'ada' } as const;
export type Kind = keyof typeof kinds | 'anonymous';

export interface RepoSeed {
  readonly project: string;
  readonly key: string;
  readonly format: 'npm' | 'nuget' | 'generic' | 'oci' | 'deb' | 'ansible';
  readonly class: 'local' | 'remote' | 'virtual';
  readonly description?: string;
  /** The members of a virtual repository, the first local one its deploy target. */
  readonly members?: readonly string[];
  /** The upstream of a remote repository whose format has no well-known one. */
  readonly url?: string;
  /** The member `vulnerabilities` of the repository document (R-VULN-002). */
  readonly vulnerabilities?: { readonly matchPublicAdvisories: 'auto' | 'always' | 'never' };
}

export const projects = [
  { key: 'web-frontend', displayName: 'Web frontend', description: "The customer portal's packages, container images and static assets." },
  { key: 'platform', displayName: 'Platform engineering', description: 'Base images, Debian packages, NuGet libraries and automation for every team.' },
  // Empty: the empty states of a project.
  { key: 'sandbox', displayName: 'Sandbox', description: '' },
] as const;

/** The project the power user owns, and in which the screens of a project are rendered. */
export const mainProject = 'web-frontend';
export const otherProject = 'platform';
export const emptyProject = 'sandbox';
/** A key long enough to show what a long key and its address do to a list (R-UI-065). */
export const longNpmKey = 'release-candidates-for-the-customer-portal-frontend';
export const longDebKey = 'debian-internal-packages-for-bookworm-and-trixie';

export const repositories: readonly RepoSeed[] = [
  { project: mainProject, key: 'npm-internal', format: 'npm', class: 'local', description: 'Packages built by the web team.' },
  { project: mainProject, key: 'npmjs', format: 'npm', class: 'remote', description: 'The public npm registry, cached.' },
  { project: mainProject, key: 'npm', format: 'npm', class: 'virtual', members: ['npm-internal', 'npmjs'] },
  {
    project: mainProject,
    key: longNpmKey,
    format: 'npm',
    class: 'local',
    description:
      'Release candidates of the customer portal before they are promoted; the release pipeline removes candidates older than four weeks every Monday.',
  },
  { project: mainProject, key: 'assets', format: 'generic', class: 'local', description: "Static files: fonts, icons and the design system's release archives." },
  { project: mainProject, key: 'images', format: 'oci', class: 'local' },
  // Vendored public packages, matched against the seed's advisories so that every grade occurs (advisories.ts, R-VULN-038).
  {
    project: mainProject,
    key: 'npm-vendor',
    format: 'npm',
    class: 'local',
    description: 'Public packages the web team vendors, checked against public advisories.',
    vulnerabilities: { matchPublicAdvisories: 'always' },
  },
  // Empty: the empty states of a repository.
  { project: mainProject, key: 'scratch', format: 'generic', class: 'local' },
  { project: otherProject, key: 'base-images', format: 'oci', class: 'local', description: 'Hardened base images.' },
  { project: otherProject, key: 'docker-hub-remote', format: 'oci', class: 'remote', description: 'Docker Hub, cached.' },
  { project: otherProject, key: 'containers', format: 'oci', class: 'virtual', members: ['base-images', 'docker-hub-remote'] },
  { project: otherProject, key: longDebKey, format: 'deb', class: 'local', description: 'Packages of the platform team for Debian 12 and 13.' },
  { project: otherProject, key: 'debian', format: 'deb', class: 'remote', description: 'deb.debian.org, cached.' },
  { project: otherProject, key: 'apt', format: 'deb', class: 'virtual', members: [longDebKey] },
  { project: otherProject, key: 'nuget-internal', format: 'nuget', class: 'local', description: 'Shared .NET libraries.' },
  // The advisory data of the profile, which reads it instead of the network (R-VULN-005, deploy/ui-dev/up.sh).
  { project: advisoryMirror.project, key: advisoryMirror.key, format: 'generic', class: 'local', description: 'Advisory data for the offline sync.' },
  { project: otherProject, key: 'nuget-org', format: 'nuget', class: 'remote' },
  { project: otherProject, key: 'nuget', format: 'nuget', class: 'virtual', members: ['nuget-internal', 'nuget-org'] },
  { project: otherProject, key: 'collections', format: 'ansible', class: 'local', description: 'Ansible collections of the platform team.' },
  { project: otherProject, key: 'galaxy', format: 'ansible', class: 'remote' },
  { project: otherProject, key: 'ansible', format: 'ansible', class: 'virtual', members: ['collections', 'galaxy'] },
];

/** Who is what: the project roles of R-AUTHZ-026 and the system role `admin` (R-AUTHZ-010); see R-UI-067. */
export const memberships: readonly { project: string; username: string; role: 'reader' | 'developer' | 'maintainer' | 'owner' }[] = [
  { project: mainProject, username: kinds.plain, role: 'reader' },
  { project: otherProject, username: kinds.plain, role: 'reader' },
  { project: mainProject, username: kinds.power, role: 'owner' },
  { project: otherProject, username: kinds.power, role: 'developer' },
];

/** The packages the power user publishes, through the formats' own routes. */
export const npmPackages: readonly { repo: string; name: string; version: string; readme?: string; dependencies?: Record<string, string> }[] = [
  {
    repo: 'npm-internal',
    name: '@acme/ui-kit',
    version: '1.0.0',
    readme: '# @acme/ui-kit\n\nThe components of the customer portal.\n',
  },
  {
    repo: 'npm-internal',
    name: '@acme/ui-kit',
    version: '1.1.0',
    readme: '# @acme/ui-kit\n\nThe components of the customer portal.\n\n## Install\n\n```shell\nnpm install @acme/ui-kit\n```\n\n## Use\n\nImport a component and give it the theme of the page.\n',
    dependencies: { '@acme/tokens': '^2.0.0', tslib: '^2.8.0' },
  },
  { repo: 'npm-internal', name: '@acme/eslint-config', version: '3.2.1' },
  { repo: 'npm-internal', name: 'customer-portal-api-client-generated-from-the-openapi-document', version: '0.4.0' },
  { repo: longNpmKey, name: '@acme/portal', version: '5.0.0-rc.3' },
];

export const genericFiles: readonly { repo: string; path: string; content: string }[] = [
  { repo: 'assets', path: 'README.md', content: '# Assets\n\nStatic files of the customer portal.\n' },
  { repo: 'assets', path: 'fonts/inter/Inter-Regular.woff2', content: 'font bytes' },
  { repo: 'assets', path: 'icons/sprite.svg', content: '<svg xmlns="http://www.w3.org/2000/svg"></svg>\n' },
  { repo: 'assets', path: 'releases/design-system/2.4.0/design-system-2.4.0.tar.gz', content: 'archive 2.4.0' },
  { repo: 'assets', path: 'releases/design-system/2.5.0/design-system-2.5.0.tar.gz', content: 'archive 2.5.0' },
  {
    repo: 'assets',
    path: 'releases/design-system/2.5.0/design-system-2.5.0-with-a-file-name-long-enough-to-wrap-a-narrow-column.tar.gz',
    content: 'archive 2.5.0 long',
  },
];

export const images: readonly { project: string; repo: string; name: string; tags: readonly string[] }[] = [
  { project: mainProject, repo: 'images', name: 'portal/web', tags: ['1.4.0', 'latest'] },
  { project: mainProject, repo: 'images', name: 'portal/api', tags: ['1.4.0'] },
];

/** The first package of the main project: the package page the renders open, with every tab. */
export const npmPackage = { format: 'npm', name: '@acme/ui-kit' } as const;
export const ociPackage = { format: 'oci', name: 'portal/web' } as const;

/** A thin client of the API with the cookie session of the caller, its CSRF token and the UI's origin (R-AUTH-012). */
export class Api {
  constructor(
    readonly ctx: APIRequestContext,
    private readonly csrf: string,
    private readonly origin: string,
  ) {}

  get csrfToken(): string {
    return this.csrf;
  }
  get originUrl(): string {
    return this.origin;
  }

  static async of(ctx: APIRequestContext, origin: string): Promise<Api> {
    const csrf = (await ctx.storageState()).cookies.find((c) => c.name === '__Host-trv_csrf')?.value ?? '';
    return new Api(ctx, csrf, origin);
  }

  private headers(extra: Record<string, string> = {}): Record<string, string> {
    return { 'X-CSRF-Token': this.csrf, Origin: this.origin, ...extra };
  }

  get(url: string): Promise<APIResponse> {
    return this.ctx.get(url);
  }
  post(url: string, data: unknown, extra?: Record<string, string>): Promise<APIResponse> {
    return this.ctx.post(url, { data, headers: this.headers(extra) });
  }
  put(url: string, data: unknown, extra?: Record<string, string>): Promise<APIResponse> {
    return this.ctx.put(url, { data, headers: this.headers(extra) });
  }
  patch(url: string, data: unknown, extra?: Record<string, string>): Promise<APIResponse> {
    return this.ctx.patch(url, { data, headers: this.headers({ 'Content-Type': 'application/merge-patch+json', ...extra }) });
  }
  delete(url: string): Promise<APIResponse> {
    return this.ctx.delete(url, { headers: this.headers() });
  }
}

/** Fails with the request and the answer unless the status is one of `ok`. */
export async function expectStatus(res: APIResponse, ok: readonly number[], what: string): Promise<void> {
  if (!ok.includes(res.status())) throw new Error(`${what}: ${res.status()} ${(await res.text()).slice(0, 600)}`);
}

/** The projects and their repositories, created by the break-glass account; an existing one is left as it is. */
export async function seedProjectsAndRepositories(admin: Api): Promise<void> {
  for (const p of projects) {
    const res = await admin.post('/api/v1/projects', { key: p.key, displayName: p.displayName, ...(p.description ? { description: p.description } : {}) });
    await expectStatus(res, [201, 409], `create project ${p.key}`);
  }
  // Locals first: a virtual repository names its members.
  const order = [...repositories].sort((a, b) => rank(a.class) - rank(b.class));
  for (const r of order) {
    const body: Record<string, unknown> = { key: r.key, class: r.class, format: r.format };
    if (r.description) body['description'] = r.description;
    if (r.class === 'remote' && r.url) body['remote'] = { url: r.url };
    if (r.vulnerabilities) body['vulnerabilities'] = r.vulnerabilities;
    if (r.class === 'virtual') {
      const target = r.members?.find((m) => repositories.find((x) => x.key === m)?.class === 'local');
      body['virtual'] = { members: r.members ?? [], deployTarget: target ?? null };
    }
    const res = await admin.post(`/api/v1/projects/${r.project}/repositories`, body);
    await expectStatus(res, [201, 409], `create repository ${r.project}/${r.key}`);
  }
}

function rank(c: RepoSeed['class']): number {
  return c === 'local' ? 0 : c === 'remote' ? 1 : 2;
}

/** The memberships and the administrator's role; the users exist from their first sign-in (R-AUTH-007). */
export async function seedGrants(admin: Api): Promise<void> {
  for (const m of memberships) {
    const res = await admin.put(`/api/v1/projects/${m.project}/members/${encodeURIComponent(`user:${m.username}`)}`, { role: m.role });
    await expectStatus(res, [200, 201], `member ${m.username} of ${m.project}`);
  }
  const bound = await admin.put(`/api/v1/role-bindings/admin/${encodeURIComponent(`user:${kinds.admin}`)}`, {});
  await expectStatus(bound, [200, 201], `role admin of ${kinds.admin}`);

  // A group and service accounts, so that the lists and pages of identities have content.
  await expectStatus(await admin.post('/api/v1/groups', { name: group, description: 'Everyone who builds the customer portal.' }), [201, 409], `group ${group}`);
  const members = await admin.ctx.patch(`/api/v1/groups/${group}/members`, {
    data: { add: [kinds.plain, kinds.power] },
    headers: { 'X-CSRF-Token': admin.csrfToken, Origin: admin.originUrl },
  });
  await expectStatus(members, [200], `members of ${group}`);
  await expectStatus(
    await admin.post('/api/v1/service-accounts', { name: serviceAccount, description: 'Reads the backups for the restore drill.' }),
    [201, 409],
    `service account ${serviceAccount}`,
  );
  await expectStatus(
    await admin.post(`/api/v1/projects/${mainProject}/service-accounts`, { name: projectServiceAccount, description: 'Publishes from the release pipeline.' }),
    [201, 409],
    `service account ${mainProject}/${projectServiceAccount}`,
  );
}

export const group = 'web-team';
export const serviceAccount = 'backup-reader';
export const projectServiceAccount = 'release-pipeline';

/** One ustar entry: a 512-byte header and the content padded to the block size. */
function tarEntry(name: string, content: Buffer): Buffer {
  const header = Buffer.alloc(512);
  const octal = (value: number, width: number): string => value.toString(8).padStart(width - 1, '0') + '\0';
  header.write(name, 0, 100, 'utf8');
  header.write(octal(0o644, 8), 100, 'ascii');
  header.write(octal(0, 8), 108, 'ascii');
  header.write(octal(0, 8), 116, 'ascii');
  header.write(octal(content.length, 12), 124, 'ascii');
  header.write(octal(0, 12), 136, 'ascii');
  header.write(' '.repeat(8), 148, 'ascii');
  header.write('0', 156, 'ascii');
  header.write('ustar\0' + '00', 257, 'ascii');
  const sum = header.reduce((a, b) => a + b, 0);
  header.write(sum.toString(8).padStart(6, '0') + '\0 ', 148, 'ascii');
  return Buffer.concat([header, content, Buffer.alloc((512 - (content.length % 512)) % 512)]);
}

/** The body of an npm publish, as the npm client sends it: the manifest and the tarball as an attachment. */
function npmPublishBody(edge: string, project: string, p: (typeof npmPackages)[number]): object {
  const manifest = {
    name: p.name,
    version: p.version,
    description: `${p.name}, published by the seed of the renders`,
    license: 'MIT',
    ...(p.dependencies ? { dependencies: p.dependencies } : {}),
    repository: { type: 'git', url: 'https://git.example.invalid/web/ui-kit.git' },
  };
  const entries = [tarEntry('package/package.json', Buffer.from(JSON.stringify(manifest, null, 2)))];
  if (p.readme) entries.push(tarEntry('package/README.md', Buffer.from(p.readme)));
  const tgz = gzipSync(Buffer.concat([...entries, Buffer.alloc(1024)]));
  const file = `${p.name.split('/').pop()}-${p.version}.tgz`;
  return {
    _id: p.name,
    name: p.name,
    description: manifest.description,
    'dist-tags': { [p.version.includes('-') ? 'next' : 'latest']: p.version },
    ...(p.readme ? { readme: p.readme } : {}),
    versions: {
      [p.version]: {
        ...manifest,
        ...(p.readme ? { readme: p.readme } : {}),
        _id: `${p.name}@${p.version}`,
        dist: {
          integrity: `sha512-${createHash('sha512').update(tgz).digest('base64')}`,
          shasum: createHash('sha1').update(tgz).digest('hex'),
          tarball: `${edge}/api/npm/${project}/${p.repo}/${p.name}/-/${file}`,
        },
      },
    },
    access: null,
    _attachments: { [file]: { content_type: 'application/octet-stream', data: tgz.toString('base64'), length: tgz.length } },
  };
}

/**
 * The packages and files, published by the power user with a personal access token through the formats' routes, as a
 * client does. A version that exists already answers a conflict, which is fine.
 */
export async function seedContent(ctx: APIRequestContext, edge: string, token: string): Promise<void> {
  const auth = { Authorization: `Bearer ${token}` };
  for (const p of [...npmPackages, ...vendorPackages]) {
    const url = `/api/npm/${mainProject}/${p.repo}/${p.name.replace('/', '%2f')}`;
    const res = await ctx.put(url, { data: npmPublishBody(edge, mainProject, p), headers: auth });
    await expectStatus(res, [200, 201, 409], `npm publish ${p.name}@${p.version}`);
  }
  for (const f of genericFiles) {
    const url = `/files/${mainProject}/${f.repo}/${f.path.split('/').map(encodeURIComponent).join('/')}`;
    const head = await ctx.head(url, { headers: auth });
    if (head.status() === 200) continue;
    const res = await ctx.put(url, { data: Buffer.from(f.content), headers: { ...auth, 'Content-Type': 'application/octet-stream' } });
    await expectStatus(res, [200, 201], `upload ${f.repo}/${f.path}`);
  }
  for (const image of images) await pushImage(ctx, edge, token, image);
  const archive = `/files/${advisoryMirror.project}/${advisoryMirror.key}/osv/npm/all.zip`;
  const upload = await ctx.put(archive, { data: advisoryArchive(), headers: { ...auth, 'Content-Type': 'application/zip' } });
  await expectStatus(upload, [200, 201], `upload the advisory archive to ${advisoryMirror.project}/${advisoryMirror.key}`);
}

/**
 * Runs the sync of the OSV source as the break-glass account and waits until the evaluation has graded the package of grade E,
 * so that the screens show the grades (R-VULN-038). An archive that is already imported is not imported again, and the grade is
 * there already.
 */
export async function syncAdvisories(admin: Api): Promise<void> {
  const run = await admin.post('/api/v1/system/tasks/vuln-sync-osv/run', {});
  await expectStatus(run, [202, 409], 'run the advisory sync');
  const list = await admin.get(`/api/v1/packages?repository=${mainProject}/npm-vendor&format=npm&q=${gradedPackage.name}`);
  await expectStatus(list, [200], 'find the graded package');
  const item = ((await list.json()) as { items: { id: string; name: string }[] }).items.find((p) => p.name === gradedPackage.name);
  if (!item) throw new Error(`${gradedPackage.name} was not published`);
  for (let i = 0; i < 180; i++) {
    const res = await admin.get(`/api/v1/packages/${item.id}/versions/${gradedPackage.version}/vulnerabilities`);
    if (res.ok()) {
      const report = (await res.json()) as { repositories: { grade?: { grade: string } }[] };
      if (report.repositories.some((r) => r.grade?.grade === 'E')) return;
    }
    await new Promise((r) => setTimeout(r, 1000));
  }
  throw new Error(`the advisory archive was not imported and evaluated within three minutes: ${gradedPackage.name} has no grade E`);
}

/** Pushes a small image (one layer, a config, a manifest per tag) through `/v2/` with a registry token (R-OCI-005). */
async function pushImage(ctx: APIRequestContext, edge: string, pat: string, image: (typeof images)[number]): Promise<void> {
  const repo = `${image.project}/${image.repo}/${image.name}`;
  const bearer = await registryToken(ctx, edge, pat, repo);
  const auth = { Authorization: `Bearer ${bearer}` };
  const layer = gzipSync(tarEntry('etc/acme-release', Buffer.from(`${image.name}\n`)));
  const config = Buffer.from(
    JSON.stringify({ architecture: 'amd64', os: 'linux', config: { Labels: { 'org.opencontainers.image.title': image.name } }, rootfs: { type: 'layers', diff_ids: [] } }),
  );
  const digestOf = (b: Buffer): string => `sha256:${createHash('sha256').update(b).digest('hex')}`;
  for (const blob of [layer, config]) {
    const digest = digestOf(blob);
    if ((await ctx.head(`/v2/${repo}/blobs/${digest}`, { headers: auth })).status() === 200) continue;
    const start = await ctx.post(`/v2/${repo}/blobs/uploads/`, { headers: auth });
    await expectStatus(start, [202], `start upload ${repo}`);
    const location = new URL(start.headers()['location'] ?? '', edge);
    location.searchParams.set('digest', digest);
    const put = await ctx.put(location.pathname + location.search, {
      data: blob,
      headers: { ...auth, 'Content-Type': 'application/octet-stream' },
    });
    await expectStatus(put, [201], `upload blob ${repo}`);
  }
  const manifest = Buffer.from(
    JSON.stringify({
      schemaVersion: 2,
      mediaType: 'application/vnd.oci.image.manifest.v1+json',
      config: { mediaType: 'application/vnd.oci.image.config.v1+json', digest: digestOf(config), size: config.length },
      layers: [{ mediaType: 'application/vnd.oci.image.layer.v1.tar+gzip', digest: digestOf(layer), size: layer.length }],
      annotations: { 'org.opencontainers.image.description': `The ${image.name} image of the renders` },
    }),
  );
  for (const tag of image.tags) {
    const res = await ctx.put(`/v2/${repo}/manifests/${tag}`, {
      data: manifest,
      headers: { ...auth, 'Content-Type': 'application/vnd.oci.image.manifest.v1+json' },
    });
    await expectStatus(res, [201], `push ${repo}:${tag}`);
  }
}

/** The handshake of a registry client: the challenge of `/v2/`, then the token service with the PAT as the password. */
async function registryToken(ctx: APIRequestContext, edge: string, pat: string, repo: string): Promise<string> {
  const challenge = (await ctx.get('/v2/')).headers()['www-authenticate'] ?? '';
  const realm = /realm="([^"]+)"/.exec(challenge)?.[1];
  const service = /service="([^"]+)"/.exec(challenge)?.[1];
  if (!realm || !service) throw new Error(`/v2/ sent no Bearer challenge: ${challenge}`);
  const url = new URL(realm, edge);
  url.searchParams.set('service', service);
  url.searchParams.set('scope', `repository:${repo}:pull,push`);
  const res = await ctx.get(url.pathname + url.search, {
    headers: { Authorization: `Basic ${Buffer.from(`${kinds.power}:${pat}`).toString('base64')}` },
  });
  await expectStatus(res, [200], `registry token for ${repo}`);
  const body = (await res.json()) as { token?: string; access_token?: string };
  const token = body.token ?? body.access_token;
  if (!token) throw new Error('the token service sent no token');
  return token;
}
