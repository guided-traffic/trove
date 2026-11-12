import { createHash } from 'node:crypto';
import { gzipSync } from 'node:zlib';
import type { Page } from '@playwright/test';
import { bobPassword, expect, expectAccessible, repositories, repositoryApi, signInLocal, signInWithKeycloak, test, unique } from '../support';

// R-UI-047 journey "search" (T-22; R-UI-095 to R-UI-099): the one search field of the top bar. bob, a reader of a project
// with an npm local repository and a published package, finds both by a part of the key with the text bold, opens the
// repository by keyboard, and Enter without a pick opens the results page of the main area. The break-glass administrator
// (system role admin) gets the Admin area group last; Enter without a pick stays in the main area, and only picking the marked
// entry opens /admin/users. `/` with the shortcut preference on and off, Ctrl+K either way; at 390 px the button opens the sheet.
//
// Written against the contract (getSearchSuggestions, R-API-060) and the component tier's fakes; it needs the server's
// suggestions (R-API-061, R-API-062) and the repository list's `name` (R-API-063) to pass, and has not been run on a cluster yet.

const project = unique('ui-srch').replace(/[^a-z0-9-]/g, '').slice(0, 24);
const repoKey = unique('srch-vendor');
const part = repoKey.slice(0, 9);
const pkgName = unique('srch-kit');
const version = '1.0.0';

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

/** The body of an npm publish, the way the npm client sends it. */
function publishBody(): object {
  const manifest = { name: pkgName, version, description: 'A package of the search journey', license: 'MIT' };
  const tgz = gzipSync(Buffer.concat([tarEntry('package/package.json', Buffer.from(JSON.stringify(manifest))), Buffer.alloc(1024)]));
  return {
    _id: pkgName,
    name: pkgName,
    'dist-tags': { latest: version },
    versions: {
      [version]: {
        ...manifest,
        _id: `${pkgName}@${version}`,
        dist: {
          integrity: `sha512-${createHash('sha512').update(tgz).digest('base64')}`,
          shasum: createHash('sha1').update(tgz).digest('hex'),
          tarball: `https://localhost/api/npm/${project}/${repoKey}/${pkgName}/-/${pkgName}-${version}.tgz`,
        },
      },
    },
    _attachments: { [`${pkgName}-${version}.tgz`]: { content_type: 'application/octet-stream', data: tgz.toString('base64'), length: tgz.length } },
  };
}

const field = (page: Page) => page.getByRole('combobox', { name: 'Search' });
const list = (page: Page) => page.getByRole('listbox', { name: 'Suggestions' });

/** Every link of the page: none may lead into the admin area (R-UI-099). */
async function adminLinks(page: Page): Promise<string[]> {
  const hrefs = await page.locator('main a[href]').evaluateAll((as) => as.map((a) => a.getAttribute('href') ?? ''));
  return hrefs.filter((h) => /\/admin(\/|$|\?)/.test(h));
}

test.beforeAll(async ({ admin }) => {
  const created = await admin.post('/api/v1/projects', { data: { key: project, displayName: 'Search journey' } });
  expect(created.status(), await created.text()).toBe(201);
  const repo = await admin.post(repositories(project), { data: { key: repoKey, class: 'local', format: 'npm' } });
  expect(repo.status(), await repo.text()).toBe(201);
  const published = await admin.put(`/api/npm/${project}/${repoKey}/${pkgName}`, { data: publishBody() });
  expect(published.status(), await published.text()).toBe(201);
});

test.afterAll(async ({ admin }) => {
  await admin.delete(repositoryApi(repoKey, project));
  await admin.delete(`/api/v1/projects/${project}`);
});

test('R-UI-095 R-UI-097 R-UI-098 a reader finds a repository by a part of its key, opens it by keyboard, and Enter opens the results page', async ({ page, admin }) => {
  await signInWithKeycloak(page, 'bob', bobPassword);
  const member = await admin.put(`/api/v1/projects/${project}/members/${encodeURIComponent('user:bob')}`, { data: { role: 'reader' } });
  expect(member.ok(), await member.text()).toBeTruthy();

  await test.step('a part of the key suggests the repository and the package, the text bold', async () => {
    await page.goto('/ui/en/projects');
    await field(page).click();
    await field(page).fill(part);
    const repos = list(page).getByRole('group', { name: 'Repositories', exact: true });
    const option = repos.getByRole('option', { name: new RegExp(`^${repoKey} · ${project}`) });
    await expect(option).toBeVisible();
    await expect(option.locator('b').first()).toHaveText(part);
    await expect(list(page).getByRole('group', { name: 'Packages', exact: true }).getByRole('option', { name: new RegExp(pkgName) })).toBeVisible();
    for (const scheme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme: scheme });
      await expectAccessible(page, `search list (${scheme})`);
    }
    await page.emulateMedia({ colorScheme: 'light' });
  });

  await test.step('Down and Enter open the repository', async () => {
    const target = await list(page).getByRole('option', { name: new RegExp(`^${repoKey} · `) }).getAttribute('id');
    for (let i = 0; i < 30 && (await field(page).getAttribute('aria-activedescendant')) !== target; i++) {
      await field(page).press('ArrowDown');
    }
    await field(page).press('Enter');
    await expect(page).toHaveURL(new RegExp(`/projects/${project}/repositories/${repoKey}`));
  });

  await test.step('Enter without a pick opens the results page with the repository and no link into the admin area', async () => {
    await field(page).click();
    await field(page).fill(part);
    await expect(list(page).getByRole('option').first()).toBeVisible();
    await field(page).press('Enter');
    await expect(page).toHaveURL(/\/search\?q=/);
    await expect(page.getByRole('heading', { name: 'Search', level: 1 })).toBeVisible();
    await expect(page.getByRole('link', { name: repoKey })).toBeVisible();
    expect(await adminLinks(page)).toEqual([]);
    await expectAccessible(page, 'search results');
  });
});

test('R-UI-096 R-UI-099 the administrator gets the Admin area group last; only picking a marked entry opens the admin area', async ({ page }) => {
  await signInLocal(page);
  await page.goto('/ui/en/projects');

  await test.step('the empty list ends with the Admin area group, each entry marked', async () => {
    await field(page).click();
    const groups = list(page).getByRole('group');
    await expect(groups.last()).toHaveAccessibleName('Admin area');
    await expect(groups.last().getByRole('option', { name: 'Users, Admin area' })).toBeVisible();
  });

  await test.step('users shows the admin page Users marked; Enter without a pick stays in the main area', async () => {
    await field(page).fill('users');
    const entry = list(page).getByRole('option', { name: 'Users, Admin area', exact: true });
    await expect(entry).toBeVisible();
    await field(page).press('Enter');
    await expect(page).toHaveURL(/\/search\?q=users$/);
    expect(await adminLinks(page)).toEqual([]);
  });

  await test.step('picking the marked entry opens /admin/users', async () => {
    await field(page).click();
    await field(page).fill('users');
    const entry = list(page).getByRole('option', { name: 'Users, Admin area', exact: true });
    const target = await entry.getAttribute('id');
    // Down until the marked entry is the active one: the focus stays in the field (aria-activedescendant).
    for (let i = 0; i < 30 && (await field(page).getAttribute('aria-activedescendant')) !== target; i++) {
      await field(page).press('ArrowDown');
    }
    await expect(field(page)).toHaveAttribute('aria-activedescendant', target!);
    await field(page).press('Enter');
    await expect(page).toHaveURL(/\/admin\/users$/);
  });
});

test('R-UI-095 / focuses the field while the preference is on; Ctrl+K always', async ({ page }) => {
  await signInWithKeycloak(page, 'bob', bobPassword);
  const preference = async (on: boolean) => {
    await page.goto('/ui/en/profile');
    const box = page.getByRole('checkbox', { name: /Keyboard shortcuts with a single key/ });
    if ((await box.isChecked()) !== on) await box.click();
    await page.getByRole('button', { name: 'Save preferences' }).click();
    await expect(box).toBeChecked({ checked: on });
  };
  await preference(true);
  await page.locator('main h1').click();
  await page.keyboard.press('/');
  await expect(field(page)).toBeFocused();
  await expect(field(page)).toHaveValue('');
  await preference(false);
  await page.locator('main h1').click();
  await page.keyboard.press('/');
  await expect(field(page)).not.toBeFocused();
  await page.keyboard.press('Control+k');
  await expect(field(page)).toBeFocused();
  await preference(true);
});

test('R-UI-095 at 390 px the search button opens the sheet; Escape closes it and the focus is on the button again', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await signInWithKeycloak(page, 'bob', bobPassword);
  await page.goto('/ui/en/projects');
  const button = page.getByRole('button', { name: 'Search', exact: true });
  await button.click();
  const sheet = page.getByRole('dialog', { name: 'Search' });
  await expect(sheet).toBeVisible();
  await expect(field(page)).toBeFocused();
  await expectAccessible(page, 'search sheet');
  await field(page).press('Escape');
  await field(page).press('Escape');
  await expect(sheet).toBeHidden();
  await expect(button).toBeFocused();
});
