import { bobPassword, expect, expectAccessible, repositories, repositoryApi, signInLocal, signInWithKeycloak, test, unique, watchCsp } from '../support';

// R-UI-047 journey "projects" (R-UI-041, R-AUTHZ-026, Q-AUTHZ-175, Q-AUTHZ-177, Q-AUTHZ-182, Q-AUTHZ-207, Q-AUTHZ-217): the
// administrator creates a project with an Owner group in the dialog; bob, a member of that group and without any system
// role, sees only the project screens, finds the project in the filter of the top bar, reads the members and is refused
// the removal of the last Owner, creates a project policy with the principal lookup and a project service account, and
// issues a token for it that expires.
//
// The journey needs the deployed backend of phase 5a (the project API, `authz/me` `projects`, the delegated writes); it was
// written against the generated types and the fakes of the component tier and has not been run against a cluster yet.

const key = unique('ui-pr').replace(/[^a-z0-9-]/g, '').slice(0, 24);
const repoKey = unique('ui-pr-repo');
const groupName = unique('ui-pr-leads');
const policyName = 'readers';
const accountName = 'ci';

test.beforeAll(async ({ admin }) => {
  // Data is prepared through the API (INV-17): the group bob will belong to.
  const group = await admin.post('/api/v1/groups', { data: { name: groupName } });
  expect(group.status(), await group.text()).toBe(201);
});

test.afterAll(async ({ admin }) => {
  // A project is deleted when it is empty: the service account and the repository first, then the project
  // (its policies and members go with it).
  await admin.delete(`/api/v1/projects/${key}/service-accounts/${accountName}`);
  await admin.delete(repositoryApi(repoKey, key));
  await admin.delete(`/api/v1/projects/${key}`);
  await admin.delete(`/api/v1/groups/${groupName}`);
});

test('R-UI-041 an administrator creates a project; a member of its Owner group administers it without a system role', async ({ page, browser, admin }) => {
  // bob signs in first, so that he exists (R-AUTH-007) and is put into the group before the project is created.
  const bobContext = await browser.newContext({ ignoreHTTPSErrors: true, locale: 'en-US' });
  const bob = await bobContext.newPage();
  const bobCsp = await watchCsp(bob);
  await signInWithKeycloak(bob, 'bob', bobPassword);
  const member = await admin.patch(`/api/v1/groups/${groupName}/members`, { data: { add: ['bob'] } });
  expect(member.ok(), `add bob: ${member.status()} ${await member.text()}`).toBeTruthy();

  await signInLocal(page);

  await test.step('the administrator creates the project with its Owner group; a repository is created in it', async () => {
    await page.goto('/ui/en/admin/projects');
    await expect(page.getByRole('heading', { name: 'Projects', level: 1 })).toBeVisible();
    await expectAccessible(page, 'project list');
    await page.getByRole('button', { name: 'New project' }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByLabel('Key', { exact: true }).fill(key);
    await dialog.getByLabel('Display name', { exact: true }).fill('Journey project');
    // The first Owners are named in the dialog (Q-AUTHZ-229); members and roles are changed on the project's screen.
    await dialog.getByLabel('Find a user or group').fill(groupName.slice(0, 8));
    await dialog.getByRole('button', { name: `group:${groupName}` }).click();
    await expectAccessible(page, 'new project dialog');
    await dialog.getByRole('button', { name: 'Create project' }).click();

    await expect(page).toHaveURL(new RegExp(`/admin/projects/${key}$`));
    await expect(page.getByRole('heading', { name: 'Project', level: 1 })).toBeVisible();
    await expect(page.getByText(`group:${groupName}`).first()).toBeVisible();
    // The repository is created in the project (R-REPO-001), through the API as the journey's data.
    const created = await admin.post(repositories(key), { data: { key: repoKey, class: 'local', format: 'generic' } });
    expect(created.status(), await created.text()).toBe(201);
    await page.reload();
    await expect(page.getByRole('link', { name: repoKey })).toBeVisible();
    // A project that holds a repository cannot be deleted; the page says why.
    await expect(page.getByRole('button', { name: 'Delete project' })).toBeDisabled();
    await expectAccessible(page, 'project detail (administrator)');
  });

  await test.step('bob sees the project screens only, and the project in the filter of the top bar', async () => {
    await bob.goto('/ui/en/admin');
    await expect(bob.getByRole('heading', { name: 'Projects', level: 1 })).toBeVisible();
    const nav = bob.getByRole('navigation', { name: 'Main navigation' });
    await expect(nav.getByRole('link', { name: 'Projects' })).toBeVisible();
    for (const global of ['Users', 'Groups', 'Permission policies', 'System roles', 'Tokens', 'Audit log']) {
      await expect(nav.getByRole('link', { name: global, exact: true })).toHaveCount(0);
    }
    await expect(bob.getByRole('row', { name: new RegExp(key) })).toBeVisible();
    await expectAccessible(bob, 'project list (project Owner)');

    await bob.getByRole('button', { name: /All projects/ }).click();
    await bob.getByRole('menuitemradio', { name: new RegExp(`\\(${key}\\)`) }).click();
    await expect(bob.getByRole('button', { name: new RegExp(`${key}.*Project filter`) })).toBeVisible();
    await expectAccessible(bob, 'project filter selected');
  });

  await test.step('bob creates a project policy with the lookup of principals', async () => {
    await bob.getByRole('link', { name: key, exact: true }).click();
    await expect(bob.getByRole('heading', { name: 'Project', level: 1 })).toBeVisible();
    // An Owner creates repositories, edits the display name and description, and cannot delete the project.
    await expect(bob.getByRole('link', { name: 'Create repository' })).toBeVisible();
    await expect(bob.getByRole('button', { name: 'Edit project' })).toBeVisible();
    await expect(bob.getByRole('button', { name: 'Delete project' })).toHaveCount(0);
    // The members card shows the group as Owner; the last Owner cannot be removed (Q-AUTHZ-217).
    const members = bob.locator('trove-project-members');
    await expect(members.getByRole('rowheader', { name: `group:${groupName}` })).toBeVisible();
    await members.getByRole('button', { name: `Remove group:${groupName}` }).click();
    await expect(members.getByText('A project keeps at least one Owner')).toBeVisible();
    await expectAccessible(bob, 'project detail (Owner)');

    await bob.getByRole('link', { name: 'Create policy' }).click();
    await expect(bob.getByRole('heading', { name: 'New permission policy', level: 1 })).toBeVisible();
    await expect(bob.getByText('Project policy of')).toBeVisible();
    const name = bob.getByLabel('Name', { exact: true }).first();
    await name.fill(policyName);
    await bob.getByLabel('Find a user or group').fill(groupName.slice(0, 8));
    await bob.getByRole('button', { name: `group:${groupName}` }).click();
    await bob.getByRole('button', { name: new RegExp(repoKey) }).click();
    await expect(bob.getByText('No problems found.')).toBeVisible();
    await expect(bob.locator('.covers')).toContainText('Repositories covered now: 1');
    await expectAccessible(bob, 'project policy editor');
    await bob.getByRole('button', { name: 'Create policy' }).click();
    await expect(bob).toHaveURL(new RegExp(`/admin/permissions/${key}/${policyName}$`));
    await expect(bob.getByRole('heading', { name: 'Permission policy', level: 1 })).toBeVisible();

    await bob.getByRole('link', { name: 'Back to the project' }).click();
    await expect(bob.getByRole('link', { name: policyName })).toBeVisible();
  });

  await test.step('bob creates a project service account and issues a token that expires', async () => {
    await bob.getByRole('button', { name: 'Create service account' }).click();
    const dialog = bob.getByRole('dialog');
    await dialog.getByLabel('Name', { exact: true }).fill(accountName);
    await expectAccessible(bob, 'new project service account dialog');
    await dialog.getByRole('button', { name: 'Create service account' }).click();
    await expect(bob).toHaveURL(new RegExp(`/admin/service-accounts/${key}/${accountName}$`));
    await expect(bob.getByText('Service account of project')).toBeVisible();

    await bob.getByRole('button', { name: 'Create token' }).click();
    const form = bob.locator('trove-token-form');
    // An Owner's token always expires: no "never", and the cap is said.
    await expect(form.getByText('Never expires')).toHaveCount(0);
    await expect(form.getByText(/caps the lifetime of service-account tokens/)).toBeVisible();
    await form.getByLabel('Description').fill('browser journey');
    await form.getByLabel('Lifetime').selectOption({ label: '7 days' });
    await expectAccessible(bob, 'new project token form');
    await form.locator('button[type=submit]').click();

    const secret = bob.getByRole('region', { name: 'Your new token' });
    await expect(secret).toBeVisible();
    expect(await secret.locator('#secret-value').inputValue()).toMatch(/^trv_sat_/);
    await secret.getByRole('button', { name: 'I have copied it' }).click();
    await expect(bob.getByRole('row', { name: /browser journey/ })).toBeVisible();
    await expectAccessible(bob, 'project service account with a token');

    // The Owner revokes it again.
    await bob.getByRole('row', { name: /browser journey/ }).getByRole('button', { name: 'Revoke' }).click();
    await bob.getByRole('dialog').getByRole('button', { name: 'Revoke' }).click();
    await expect(bob.getByText('No tokens match.')).toBeVisible();
  });

  await test.step('the administrator sees the project with what bob created', async () => {
    await page.goto(`/ui/en/admin/projects/${key}`);
    await expect(page.getByRole('link', { name: policyName })).toBeVisible();
    await expect(page.getByRole('link', { name: accountName })).toBeVisible();
  });

  expect(await bobCsp(), 'securitypolicyviolation events on the page of bob').toEqual([]);
  await bobContext.close();
});
