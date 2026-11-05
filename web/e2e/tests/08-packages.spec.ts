import { createHash } from 'node:crypto';
import { gzipSync } from 'node:zlib';
import { expect, expectAccessible, openFilter, signInLocal, signOut, test, unique } from '../support';

// R-UI-047 journey "packages" (E2.8; R-UI-006 to R-UI-009, R-UI-016, R-UI-017): a version published to an npm local
// repository through the npm protocol appears in the packages list, its README renders sanitized, and the client setup
// page shows, verbatim, the configure snippets the npm client matrix of E2.1 writes (setupSnippet in
// test/e2e/npm_matrix_test.go). A second journey creates an npm repository with the wizard and edits its npm settings
// in the form rendered from the format's settings schema (R-UI-021, R-ARCH-004).

const repoKey = unique('ui-npm');
const wizardKey = unique('ui-npm-wizard');
const mirrorKey = unique('ui-npm-mirror');
const pkgName = unique('ui-readme');
const version = '1.0.0';

// Hostile HTML next to the Markdown: the server's sanitizer must drop it and the page must run none of it (a CSP
// violation would fail the journey through the page fixture as well).
const readme = [
  `# ${pkgName}`,
  '',
  'Published by the trove browser journey.',
  '',
  '## Usage',
  '',
  '```js',
  `const value = require('${pkgName}');`,
  '```',
  '',
  '<script>window.troveReadmeRan = "script";</script>',
  '<img src="x" onerror="window.troveReadmeRan = \'onerror\'">',
  '<a href="javascript:window.troveReadmeRan = \'link\'">a link that runs script</a>',
  '<iframe src="https://example.com/"></iframe>',
  '',
].join('\n');

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
function publishBody(): object {
  const description = 'A package of the trove browser journey';
  const manifest = { name: pkgName, version, description, license: 'MIT' };
  const tgz = gzipSync(
    Buffer.concat([
      tarEntry('package/package.json', Buffer.from(JSON.stringify(manifest, null, 2))),
      tarEntry('package/README.md', Buffer.from(readme)),
      Buffer.alloc(1024),
    ]),
  );
  return {
    _id: pkgName,
    name: pkgName,
    description,
    'dist-tags': { latest: version },
    versions: {
      [version]: {
        ...manifest,
        _id: `${pkgName}@${version}`,
        dist: {
          integrity: `sha512-${createHash('sha512').update(tgz).digest('base64')}`,
          shasum: createHash('sha1').update(tgz).digest('hex'),
          tarball: `https://localhost/api/npm/${repoKey}/${pkgName}/-/${pkgName}-${version}.tgz`,
        },
      },
    },
    access: null,
    _attachments: {
      [`${pkgName}-${version}.tgz`]: {
        content_type: 'application/octet-stream',
        data: tgz.toString('base64'),
        length: tgz.length,
      },
    },
  };
}

/**
 * The configure snippet of a client as setupSnippet renders npm's template for the matrix: the registry URL of the
 * edge on `localhost`, the keyed credential line with ${TROVE_TOKEN}, and the always-auth switches, because the
 * repository is not anonymously readable.
 */
function matrixSnippet(client: string, edgePort: string): { file: string; content: string } {
  const registry = `https://localhost:${edgePort}/api/npm/${repoKey}/`;
  const npmrc = `registry=${registry}\n${registry.replace(/^https:/, '')}:_authToken=\${TROVE_TOKEN}\n`;
  switch (client) {
    case 'npm':
      return { file: '.npmrc', content: npmrc };
    case 'pnpm':
      return { file: '~/.npmrc', content: npmrc };
    case 'yarn-classic':
      return { file: '.npmrc', content: `${npmrc}always-auth=true\n` };
    default:
      return {
        file: '.yarnrc.yml',
        content: `npmRegistryServer: "${registry}"\nnpmAlwaysAuth: true\nnpmAuthToken: "\${TROVE_TOKEN}"\n`,
      };
  }
}

const clients = [
  { id: 'npm', label: 'npm' },
  { id: 'pnpm', label: 'pnpm' },
  { id: 'yarn', label: 'Yarn Berry (2 and later)' },
  { id: 'yarn-classic', label: 'Yarn Classic (1.x)' },
];

test.beforeAll(async ({ admin }) => {
  const created = await admin.post('/api/v1/repositories', { data: { key: repoKey, class: 'local', format: 'npm' } });
  expect(created.status(), await created.text()).toBe(201);
  const published = await admin.put(`/api/npm/${repoKey}/${pkgName}`, { data: publishBody() });
  expect(published.status(), await published.text()).toBe(201);
});

test.afterAll(async ({ admin }) => {
  await admin.delete(`/api/v1/repositories/${repoKey}`);
  await admin.delete(`/api/v1/repositories/${wizardKey}`);
  await admin.delete(`/api/v1/repositories/${mirrorKey}`);
});

test('E2.8 R-UI-006 R-UI-008 R-UI-016 the published version in the list, its sanitized README and the setup snippets of the npm matrix', async ({
  page,
  replicas,
  baseURL,
}) => {
  await signInLocal(page);

  await test.step('the published version appears in the packages list', async () => {
    await page.goto('/ui/en/packages');
    await expect(page.getByRole('heading', { name: 'Packages', level: 1 })).toBeVisible();
    await expectAccessible(page, 'package list');
    await openFilter(page, 'Repository');
    await page.getByRole('checkbox', { name: repoKey, exact: true }).check();
    await page.getByRole('button', { name: 'Search' }).click();
    const row = page.getByRole('table', { name: 'Packages' }).getByRole('row', { name: new RegExp(pkgName) });
    await expect(row.getByRole('link', { name: pkgName })).toBeVisible();
    await expect(row).toContainText(version);
    await expectAccessible(page, 'package list filtered by the repository');
  });

  await test.step('the README renders, and what the sanitizer must drop is gone', async () => {
    await page.getByRole('link', { name: pkgName }).click();
    await expect(page.getByRole('heading', { name: pkgName, level: 1 }).first()).toBeVisible();
    await expect(page.getByRole('tab', { name: 'README', selected: true })).toBeVisible();
    const rendered = page.locator('trove-package-readme .readme');
    await expect(rendered.getByRole('heading', { name: 'Usage', level: 2 })).toHaveAttribute(
      'id',
      'user-content-usage',
    );
    await expect(rendered.locator('pre code')).toContainText(`require('${pkgName}')`);
    await expect(rendered).toContainText('Published by the trove browser journey.');
    await expect(rendered.locator('script, iframe, [onerror], a[href^="javascript:" i]')).toHaveCount(0);
    expect(
      await page.evaluate(() => (window as unknown as { troveReadmeRan?: string }).troveReadmeRan),
    ).toBeUndefined();
    await expectAccessible(page, 'package detail with the README');
  });

  await test.step('the client setup page shows the configure snippet of every matrix client verbatim', async () => {
    await page.getByRole('link', { name: 'Set up your client for this repository' }).click();
    await expect(page.getByRole('heading', { name: 'Client setup', level: 1 })).toBeVisible();
    await expect(page.getByRole('radio', { name: 'Reference an environment variable' })).toBeChecked();
    const port = new URL(baseURL ?? '').port;
    const clientStep = page.getByRole('group', { name: /^2\s*Client$/ });
    for (const client of clients) {
      await clientStep.getByRole('radio', { name: client.label, exact: true }).check();
      const expected = matrixSnippet(client.id, port);
      const file = new RegExp(`^${expected.file.replace(/[.~/]/g, '\\$&')}$`);
      const snippet = page
        .getByRole('tabpanel', { name: 'Configure' })
        .locator('.snippet')
        .filter({ has: page.locator('p.target code', { hasText: file }) });
      await expect(snippet, `${client.id}: the snippet for ${expected.file}`).toHaveCount(1);
      await expect
        .poll(() => snippet.locator('pre').textContent(), { message: `${client.id} snippet` })
        .toBe(expected.content);
    }
    await expectAccessible(page, 'client setup');
  });

  await signOut(page);
  expect(replicas.size, `replicas that answered: ${[...replicas]}`).toBeGreaterThanOrEqual(2);
});

test('R-UI-021 R-ARCH-004 the npm settings in the wizard and on the edit form, rendered from the settings schema', async ({
  page,
  replicas,
  admin,
}) => {
  await signInLocal(page);
  const guard = page.getByRole('checkbox', { name: 'latestGuard' });
  const npmMember = async () => {
    const res = await admin.get(`/api/v1/repositories/${wizardKey}`);
    expect(res.status()).toBe(200);
    return ((await res.json()) as { npm?: Record<string, unknown> }).npm;
  };

  await test.step('the wizard offers the settings of a local npm repository and writes the one that differs from the default', async () => {
    await page.goto('/ui/en/admin/repositories/new');
    await page.getByRole('option', { name: 'npm' }).click();
    await page.getByRole('button', { name: 'Next' }).click();
    await page.getByRole('radio', { name: /^Local/ }).check();
    await page.getByRole('button', { name: 'Next' }).click();
    await page.locator('#f-key').fill(wizardKey);
    await expect(page.getByText('The key is available.')).toBeVisible();
    await page.getByRole('button', { name: 'Next' }).click();
    await expect(page.getByRole('heading', { name: 'npm settings', level: 2 })).toBeVisible();
    await expect(page.getByRole('checkbox', { name: 'serveRemovedVersions' })).toHaveCount(0);
    await expect(guard).toBeChecked();
    await guard.uncheck();
    await expectAccessible(page, 'repository wizard, step access and npm settings');
    await page.getByRole('button', { name: 'Next' }).click();
    await expect(page.getByText('latestGuard: false')).toBeVisible();
    await page.getByRole('button', { name: 'Create repository' }).click();
    await expect(page).toHaveURL(new RegExp(`/admin/repositories/${wizardKey}$`));
    expect(await npmMember()).toEqual({ latestGuard: false });
  });

  await test.step('the edit form reads it back and setting the default back removes it from the document', async () => {
    await page.reload();
    await page.getByRole('tab', { name: 'npm settings' }).click();
    await expect(guard).not.toBeChecked();
    await expectAccessible(page, 'repository detail, npm settings');
    await guard.check();
    await page.getByRole('button', { name: 'Save changes' }).click();
    await expect(page.getByText('Repository saved.')).toBeVisible();
    await expect.poll(npmMember).toBeUndefined();
  });

  await signOut(page);
  expect(replicas.size, `replicas that answered: ${[...replicas]}`).toBeGreaterThanOrEqual(2);
});

// R-UI-021 R-REPO-050 R-REPO-052 Q-UI-117: a remote repository of a format with `mirror: true` (npm) has the Mirror section in
// the wizard and on a tab of the edit form. The mirror mode itself is refused by the server until the npm lister is built, so
// the journey asserts what the section renders and its defaults, and creates the remote in the default mode `pullThrough`; a
// later wave asserts saving `mode: mirror`.
test('R-UI-021 R-REPO-050 the Mirror section of a remote npm repository in the wizard and on the edit form', async ({
  page,
  replicas,
  admin,
}) => {
  await signInLocal(page);
  const mode = (name: RegExp) => page.getByRole('radio', { name });
  const interval = page.getByRole('textbox', { name: 'Pass interval in seconds', exact: true });

  await test.step('the wizard shows the section for a remote npm repository, in mode pull-through with no members', async () => {
    await page.goto('/ui/en/admin/repositories/new');
    await page.getByRole('option', { name: 'npm' }).click();
    await page.getByRole('button', { name: 'Next' }).click();
    await expect(page.getByRole('radio', { name: /^Virtual/ })).toBeEnabled();
    await page.getByRole('radio', { name: /^Remote/ }).check();
    await page.getByRole('button', { name: 'Next' }).click();
    await page.locator('#f-key').fill(mirrorKey);
    await expect(page.getByText('The key is available.')).toBeVisible();
    await page.getByRole('button', { name: 'Next' }).click();

    await expect(page.getByRole('heading', { name: 'Mirror', level: 2 })).toBeVisible();
    await expect(page.getByRole('navigation').getByText('4. Access, upstream, npm and mirror settings')).toBeVisible();
    await expect(mode(/^Pull-through/)).toBeChecked();
    await expect(mode(/^Mirror/)).not.toBeChecked();
    await expect(interval).toHaveCount(0);
    for (const scheme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme: scheme });
      await expectAccessible(page, `repository wizard, Mirror section, pull-through (${scheme})`);
    }
  });

  await test.step('mode mirror shows the members with their defaults and the bound notice of the well-known upstream', async () => {
    await mode(/^Mirror/).check();
    await expect(interval).toBeVisible();
    await expect(interval).toHaveValue('');
    await expect(page.getByText('300 to 604800. Default: 3600.')).toBeVisible();
    await expect(page.getByRole('textbox', { name: 'Concurrent fetches' })).toHaveValue('');
    await expect(page.getByRole('checkbox', { name: 'Include prerelease versions' })).toBeChecked();
    await expect(page.getByRole('checkbox', { name: 'Paused' })).not.toBeChecked();
    await expect(page.getByRole('combobox', { name: 'Discovery' })).toHaveValue('auto');
    await expect(page.getByText("A mirror of the format's well-known upstream needs a bound.")).toBeVisible();
    for (const scheme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme: scheme });
      await expectAccessible(page, `repository wizard, Mirror section, mirror (${scheme})`);
    }
    // The pattern the server asks for removes the notice; it is typed here, not sent.
    await page.getByRole('textbox', { name: 'Include patterns' }).fill('@types/*');
    await expect(page.getByText("A mirror of the format's well-known upstream needs a bound.")).toHaveCount(0);
    await page.getByRole('textbox', { name: 'Include patterns' }).fill('');
    await mode(/^Pull-through/).check();
    await expect(interval).toHaveCount(0);
  });

  await test.step('the create writes no mirror member of its own; the server fills the defaults', async () => {
    await page.getByRole('button', { name: 'Next' }).click();
    await expect(page.getByText('Pull-through (no mirror).')).toBeVisible();
    await page.getByRole('button', { name: 'Create repository' }).click();
    await expect(page).toHaveURL(new RegExp(`/admin/repositories/${mirrorKey}$`));
    const res = await admin.get(`/api/v1/repositories/${mirrorKey}`);
    expect(res.status()).toBe(200);
    const remote = ((await res.json()) as { remote?: { mode?: string; mirror?: Record<string, unknown> } }).remote;
    expect(remote?.mode).toBe('pullThrough');
    expect(remote?.mirror).toMatchObject({ paused: false, intervalSeconds: 3600, concurrency: 4, discovery: 'auto' });
  });

  await test.step('the edit form has a Mirror tab with the mode read back and nothing to save', async () => {
    await page.getByRole('tab', { name: 'Mirror' }).click();
    await expect(mode(/^Pull-through/)).toBeChecked();
    await expect(page.getByRole('button', { name: 'Save changes' })).toBeDisabled();
    await expectAccessible(page, 'repository detail, Mirror tab');
  });

  await signOut(page);
  expect(replicas.size, `replicas that answered: ${[...replicas]}`).toBeGreaterThanOrEqual(2);
});
