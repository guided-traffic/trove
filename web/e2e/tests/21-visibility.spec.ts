import { bobPassword, expect, expectAccessible, signInLocal, signInWithKeycloak, test, unique, watchCsp } from '../support';
import { npmPublishBody } from '../npm-publish';

// R-UI-047 journey "visibility" (R-UI-053 to R-UI-057, R-AUTHZ-030, R-AUTHZ-033, R-AUTHZ-034): an administrator sets the installation's
// default level and a project's level and confirms a lowering that names the repository it lowers; the Owner of a public project sets the
// artifacts of a repository to Public and its wiki to Internal; an anonymous visitor finds the public project, opens a package of it and
// sees no README tab and makes no README request; an external user's start page holds the public project and no internal one.
//
// The journey needs the deployed backend of T-18 (the visibility handlers) and an installation whose default endpoint admits anonymous
// requests (`endpoints.default.auth.anonymous`, R-EP-010); it was written against the generated types and the fakes of the component tier.

const suffix = unique('v').replace(/[^a-z0-9]/g, '').slice(0, 10);
const publicProject = `vis-pub-${suffix}`;
const internalProject = `vis-int-${suffix}`;
const defaultProject = `vis-def-${suffix}`;
// The start page names a project by its display name, with the key under it (Q-UI-311): its link is found by that name.
const displayName = (key: string): string => `Journey ${key}`;
const publicRepo = 'npm-public';
const wikiRepo = 'npm-wiki';
const pkgName = `vis-readme-${suffix}`;
const version = '1.0.0';
const readme = `# ${pkgName}\n\nA README that only internal users may read.\n`;

const merge = { 'Content-Type': 'application/merge-patch+json', 'If-Match': '*' };

test.beforeAll(async ({ admin }) => {
  const project = async (key: string, visibility: string) => {
    const created = await admin.post('/api/v1/projects', { data: { key, displayName: displayName(key), description: '', visibility } });
    expect(created.status(), await created.text()).toBe(201);
  };
  await project(publicProject, 'public');
  await project(internalProject, 'internal');
  for (const [key, visibility] of [
    [publicRepo, { artifacts: 'public', wiki: 'internal' }],
    [wikiRepo, { artifacts: 'internal', wiki: 'internal' }],
  ] as const) {
    const created = await admin.post(`/api/v1/projects/${publicProject}/repositories`, { data: { key, class: 'local', format: 'npm', visibility } });
    expect(created.status(), await created.text()).toBe(201);
  }
  const published = await admin.put(`/api/npm/${publicProject}/${publicRepo}/${pkgName}`, {
    data: npmPublishBody(publicRepo, pkgName, version, readme, publicProject),
  });
  expect(published.status(), await published.text()).toBe(201);
});

test.afterAll(async ({ admin }) => {
  await admin.patch('/api/v1/users/bob', { data: { external: false }, headers: merge });
  await admin.patch('/api/v1/settings/visibility', { data: { defaultLevel: 'private' }, headers: merge });
  for (const [project, repos] of [
    [publicProject, [publicRepo, wikiRepo]],
    [internalProject, []],
    [defaultProject, []],
  ] as const) {
    for (const repo of repos) await admin.delete(`/api/v1/projects/${project}/repositories/${repo}`);
    await admin.delete(`/api/v1/projects/${project}`);
  }
});

test('R-UI-053 R-UI-054 an administrator sets the default level, and lowers a project after the confirmation that names the repository', async ({ page, admin }) => {
  await signInLocal(page);

  await test.step('the default level is saved and a project created afterwards has the Internal icon', async () => {
    await page.goto('/ui/en/admin/settings/general');
    await page.getByRole('button', { name: 'Visibility', exact: true }).click();
    await page.getByRole('radio', { name: /^Internal/ }).check();
    await page.getByRole('button', { name: 'Save changes (visibility)' }).click();
    await expect(page.getByText('Default visibility saved.')).toBeVisible();
    await expectAccessible(page, 'general settings, visibility');
    const created = await admin.post('/api/v1/projects', { data: { key: defaultProject, displayName: 'Journey default', description: '' } });
    expect(created.status(), await created.text()).toBe(201);
    await page.goto('/ui/en/admin/projects');
    const row = page.getByRole('row', { name: new RegExp(defaultProject) });
    await expect(row.getByRole('img', { name: 'Internal' })).toBeVisible();
  });

  await test.step('the public project is listed as public and lists what is public in the general settings', async () => {
    await page.goto('/ui/en/admin/settings/general');
    await page.getByRole('button', { name: 'Visibility', exact: true }).click();
    await expect(page.getByTestId('public-objects').getByRole('link', { name: publicProject, exact: true })).toBeVisible();
    await expect(page.getByTestId('public-objects').getByRole('link', { name: `${publicProject}/${publicRepo}` })).toBeVisible();
  });

  await test.step('lowering the project to Internal asks first and names the repository it lowers', async () => {
    await page.goto(`/ui/en/projects/${internalProject}/settings`);
    // an internal project holds no repository that a lowering would reach
    await page.getByRole('button', { name: 'Visibility', exact: true }).click();
    await expect(page.getByRole('radio', { name: /^Internal/ })).toBeChecked();

    await page.goto(`/ui/en/projects/${publicProject}/settings`);
    await page.getByRole('button', { name: 'Visibility', exact: true }).click();
    await page.getByRole('radio', { name: /^Internal/ }).check();
    await page.getByRole('button', { name: 'Save changes (visibility)' }).click();
    const dialog = page.getByRole('dialog', { name: /Lower project/ });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByRole('listitem')).toHaveText([new RegExp(`^${publicRepo}: Public / Internal → Internal$`)]);
    await expectAccessible(page, 'lowering confirmation');
    await dialog.getByRole('button', { name: 'Lower the visibility' }).click();
    await expect(page.getByText(/Visibility saved\./)).toBeVisible();

    await page.goto(`/ui/en/projects/${publicProject}/repositories`);
    const row = page.getByRole('row', { name: new RegExp(publicRepo) });
    await expect(row.getByRole('img', { name: 'Internal' })).toBeVisible();
  });

  await test.step('the project is raised to Public again for the journeys that follow', async () => {
    const project = await admin.get(`/api/v1/projects/${publicProject}`);
    const raised = await admin.put(`/api/v1/projects/${publicProject}/visibility`, {
      data: { level: 'public' },
      headers: { 'If-Match': project.headers()['etag'] ?? '*' },
    });
    expect(raised.status(), await raised.text()).toBe(200);
    const repo = await admin.patch(`/api/v1/projects/${publicProject}/repositories/${publicRepo}`, {
      data: { visibility: { artifacts: 'public', wiki: 'internal' } },
      headers: merge,
    });
    expect(repo.status(), await repo.text()).toBe(200);
  });
});

test('R-UI-055 the Owner of a public project sets the artifacts of a repository Public and its wiki Internal', async ({ page, browser, admin }) => {
  // bob signs in first, so that he exists (R-AUTH-007); he becomes Owner of the public project.
  const context = await browser.newContext({ ignoreHTTPSErrors: true, locale: 'en-US' });
  const bob = await context.newPage();
  const csp = await watchCsp(bob);
  await signInWithKeycloak(bob, 'bob', bobPassword);
  const member = await admin.put(`/api/v1/projects/${publicProject}/members/${encodeURIComponent('user:bob')}`, { data: { role: 'owner' } });
  expect(member.ok(), await member.text()).toBeTruthy();
  void page;

  await test.step('in the public project both groups offer Public; the wiki cannot be above the artifacts', async () => {
    await bob.goto(`/ui/en/projects/${publicProject}/repositories/${wikiRepo}/settings`);
    await bob.getByRole('tab', { name: 'Access' }).click();
    const artifacts = bob.getByRole('group', { name: 'Artifacts' });
    const wiki = bob.getByRole('group', { name: 'Wiki' });
    // the project is public, so the artifacts may be Public; the wiki is at most as open as the artifacts (Internal now)
    await expect(artifacts.getByRole('radio', { name: /^Public/ })).toBeEnabled();
    await expect(wiki.getByRole('radio', { name: /^Public/ })).toBeDisabled();
    await artifacts.getByRole('radio', { name: /^Private/ }).check();
    await expect(wiki.getByRole('radio', { name: /^Internal/ })).toBeDisabled();
    await expect(wiki.getByRole('radio', { name: /^Private/ })).toBeChecked();
    await artifacts.getByRole('radio', { name: /^Public/ }).check();
    await expect(wiki.getByRole('radio', { name: /^Public/ })).toBeEnabled();
    await wiki.getByRole('radio', { name: /^Internal/ }).check();
    await expectAccessible(bob, 'repository access, visibility');
    await bob.getByRole('button', { name: 'Save changes (visibility)' }).click();
    await expect(bob.getByText('Visibility saved.')).toBeVisible();
  });

  await test.step('the repository list shows the Public icon', async () => {
    await bob.goto(`/ui/en/projects/${publicProject}/repositories`);
    await expect(bob.getByRole('row', { name: new RegExp(wikiRepo) }).getByRole('img', { name: 'Public' })).toBeVisible();
    const stored = await admin.get(`/api/v1/projects/${publicProject}/repositories/${wikiRepo}`);
    expect((await stored.json()).visibility).toEqual({ artifacts: 'public', wiki: 'internal' });
  });

  expect(await csp(), 'securitypolicyviolation events').toEqual([]);
  await context.close();
});

test('R-UI-057 an anonymous visitor finds the public project and a package without a README tab, and no README is requested', async ({ browser, baseURL }) => {
  const context = await browser.newContext({ baseURL, ignoreHTTPSErrors: true, locale: 'en-US' });
  const visitor = await context.newPage();
  const readmeRequests: string[] = [];
  visitor.on('request', (r) => {
    if (/\/readme(\?|$)/.test(r.url())) readmeRequests.push(r.url());
  });

  await visitor.goto('/ui/en/projects');
  await expect(visitor.getByRole('link', { name: displayName(publicProject), exact: true })).toBeVisible();
  await expect(visitor.getByRole('link', { name: displayName(internalProject), exact: true })).toHaveCount(0);
  await expect(visitor.getByRole('link', { name: 'Sign in' })).toBeVisible();
  await expectAccessible(visitor, 'start page of an anonymous visitor');

  await visitor.goto(`/ui/en/projects/${publicProject}/packages`);
  await visitor.getByRole('link', { name: pkgName }).click();
  await expect(visitor.getByRole('heading', { name: pkgName, level: 1 }).first()).toBeVisible();
  await expect(visitor.getByRole('tab', { name: 'Metadata' })).toBeVisible();
  await expect(visitor.getByRole('tab', { name: 'README' })).toHaveCount(0);
  expect(readmeRequests, 'README requests of an anonymous visitor').toEqual([]);
  await expectAccessible(visitor, 'package page of an anonymous visitor');
  await context.close();
});

test('R-UI-056 R-AUTHZ-030 an external user finds the public project on the start page and no internal one', async ({ browser, admin }) => {
  const context = await browser.newContext({ ignoreHTTPSErrors: true, locale: 'en-US' });
  const bob = await context.newPage();
  await signInWithKeycloak(bob, 'bob', bobPassword);
  // an internal user finds the internal project without being a member of it
  await bob.goto('/ui/en/projects');
  await expect(bob.getByRole('link', { name: displayName(internalProject), exact: true })).toBeVisible();

  const flagged = await admin.patch('/api/v1/users/bob', { data: { external: true }, headers: merge });
  expect(flagged.ok(), await flagged.text()).toBeTruthy();

  await expect
    .poll(async () => {
      await bob.goto('/ui/en/projects');
      return bob.getByRole('link', { name: displayName(internalProject), exact: true }).count();
    }, { message: 'the external flag takes effect within the revocation bound' })
    .toBe(0);
  await expect(bob.getByRole('link', { name: displayName(publicProject), exact: true })).toBeVisible();
  await expectAccessible(bob, 'start page of an external user');
  await context.close();
});
