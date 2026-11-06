import { createHash } from 'node:crypto';
import { gzipSync } from 'node:zlib';
import { APIRequestContext, expect } from '@playwright/test';
import { e2eProject, repositories } from './support';

/** One ustar entry: a 512-byte header and the content padded to the block size. */
function tarEntry(name: string, content: Buffer): Buffer {
  const header = Buffer.alloc(512);
  const octal = (value: number, width: number) => value.toString(8).padStart(width - 1, '0') + '\0';
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

/** The body of an npm publish, the way the npm client sends it: the manifest and the tarball as an attachment. */
export function npmPublishBody(repoKey: string, name: string, version: string): object {
  const manifest = { name, version, description: 'A package of the trove browser journeys', license: 'MIT' };
  const tgz = gzipSync(
    Buffer.concat([tarEntry('package/package.json', Buffer.from(JSON.stringify(manifest, null, 2))), Buffer.alloc(1024)]),
  );
  return {
    _id: name,
    name,
    description: manifest.description,
    'dist-tags': { latest: version },
    versions: {
      [version]: {
        ...manifest,
        _id: `${name}@${version}`,
        dist: {
          integrity: `sha512-${createHash('sha512').update(tgz).digest('base64')}`,
          shasum: createHash('sha1').update(tgz).digest('hex'),
          tarball: `https://localhost/api/npm/${e2eProject}/${repoKey}/${name}/-/${name}-${version}.tgz`,
        },
      },
    },
    access: null,
    _attachments: {
      [`${name}-${version}.tgz`]: { content_type: 'application/octet-stream', data: tgz.toString('base64'), length: tgz.length },
    },
  };
}

/**
 * Creates a local npm repository and publishes the given `name@version` pairs into it, through the API (INV-17). The repository
 * declares that it holds public packages (`matchPublicAdvisories: always`, R-VULN-010): the first publish of a name makes an
 * automatic claim, and a claimed name of a repository without the setting is an internal name, which no public advisory applies to.
 */
export async function localNpmRepository(admin: APIRequestContext, key: string, packages: readonly (readonly [string, string])[]): Promise<void> {
  const created = await admin.post(repositories(), { data: { key, class: 'local', format: 'npm', vulnerabilities: { matchPublicAdvisories: 'always' } } });
  expect(created.status(), await created.text()).toBe(201);
  for (const [name, version] of packages) {
    const published = await admin.put(`/api/npm/${e2eProject}/${key}/${name}`, { data: npmPublishBody(key, name, version) });
    expect(published.status(), `publish ${name}@${version}: ${await published.text()}`).toBe(201);
  }
}
