import { alicePassword, bobPassword, expect, expectAccessible, signInLocal, signInWithKeycloak, signOut, test, unique, watchCsp } from '../support';

// R-UI-047 journeys "policy edit" and the effect of a grant (R-UI-025, R-UI-026, R-AUTH-018, R-API-052, E1.11): the
// administrator puts bob into a new group and writes a permission policy for it, in the editor with its
// validation preview; bob sees the repository only after that, on any replica.

const repoKey = unique('ui-access');
const groupName = unique('ui-readers');
const policyName = unique('ui-readers-read');

test.beforeAll(async ({ admin }) => {
  // Data is prepared through the API (INV-17): a repository with one file that nobody but the administrator may read.
  const created = await admin.post('/api/v1/repositories', { data: { key: repoKey, class: 'local', format: 'generic' } });
  expect(created.status(), await created.text()).toBe(201);
  const put = await admin.put(`/files/${repoKey}/docs/readme.txt`, { data: 'readable once granted\n' });
  expect(put.ok(), `PUT readme.txt: ${put.status()}`).toBeTruthy();
});

test.afterAll(async ({ admin }) => {
  await admin.delete(`/api/v1/permission-policies/${policyName}`);
  await admin.delete(`/api/v1/groups/${groupName}`);
  await admin.delete(`/api/v1/repositories/${repoKey}`);
});

test('R-UI-025 R-UI-026 a group, a policy with its preview, and the effect for bob', async ({ page, browser, replicas }) => {
  // bob signs in first: he is created just in time (R-AUTH-007) and has no grant on the repository yet.
  const bobContext = await browser.newContext({ ignoreHTTPSErrors: true, locale: 'en-US' });
  const bob = await bobContext.newPage();
  const bobCsp = await watchCsp(bob);
  const bobReplicas = new Set<string>();
  bob.on('response', (r) => {
    const id = r.headers()['x-trove-replica'];
    if (id) bobReplicas.add(id);
  });
  await signInWithKeycloak(bob, 'bob', bobPassword);
  const bobSearch = async () => {
    await bob.goto('/ui/en/browse');
    await bob.getByLabel('Repository key').fill(repoKey);
    await bob.getByRole('button', { name: 'Search' }).click();
  };
  await bobSearch();
  await expect(bob.getByText('No repository matches.')).toBeVisible();

  await signInLocal(page);

  await test.step('a group is created and bob is made a member', async () => {
    await page.goto('/ui/en/admin/groups');
    await expect(page.getByRole('heading', { name: 'Groups', level: 1 })).toBeVisible();
    await expectAccessible(page, 'group list');
    await page.getByRole('button', { name: 'New group' }).click();
    await page.getByRole('dialog').getByLabel('Name', { exact: true }).fill(groupName);
    await expectAccessible(page, 'new group dialog');
    await page.getByRole('button', { name: 'Create group' }).click();
    await expect(page).toHaveURL(new RegExp(`/admin/groups/${groupName}$`));
    await expect(page.getByRole('heading', { name: 'Group', level: 1 })).toBeVisible();
    await page.getByLabel('Add a member').fill('bob');
    await page.getByRole('button', { name: 'Add', exact: true }).click();
    await expect(page.getByRole('link', { name: 'bob' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Members (1)' })).toBeVisible();
    await expectAccessible(page, 'group detail');
  });

  await test.step('the policy editor validates while it is typed and previews what is covered', async () => {
    await page.goto('/ui/en/admin/permissions');
    await expectAccessible(page, 'policy list');
    await page.getByRole('link', { name: 'New policy' }).click();
    await expect(page.getByRole('heading', { name: 'New permission policy', level: 1 })).toBeVisible();
    // An empty draft is invalid, and the preview says where.
    await expect(page.getByText('The policy has problems:')).toBeVisible();

    await page.getByLabel('Name', { exact: true }).first().fill(policyName);
    await page.getByRole('button', { name: 'Add principal' }).click();
    const entry = page.getByRole('group', { name: 'Entry 1' });
    await entry.getByLabel('Principal', { exact: true }).fill(`group:${groupName}`);
    await entry.getByRole('checkbox', { name: 'read' }).check();
    await page.getByLabel('Repository selectors').fill(repoKey);
    await expect(page.locator('.covers')).toContainText('Repositories covered now: 1');
    await expect(page.getByText('No problems found.')).toBeVisible();
    await expect(page.locator('ul.covered')).toContainText(repoKey);

    // "Try it" evaluates the draft only.
    const preview = page.getByRole('complementary', { name: 'Preview' });
    await preview.getByLabel('Principal', { exact: true }).fill(`group:${groupName}`);
    await preview.getByLabel('Repository', { exact: true }).fill(repoKey);
    await preview.getByRole('button', { name: 'Explain' }).click();
    await expect(page.getByText('Allowed by this draft')).toBeVisible();
    await expectAccessible(page, 'policy editor with the preview');

    await page.getByRole('button', { name: 'Create policy' }).click();
    await expect(page).toHaveURL(new RegExp(`/admin/permissions/${policyName}$`));
    await expectAccessible(page, 'policy detail');
  });

  await test.step('bob sees the repository now, and its file', async () => {
    await bobSearch();
    await expect(bob.getByRole('treeitem', { name: new RegExp(repoKey) })).toBeVisible();
    await bob.goto(`/ui/en/browse/${repoKey}`);
    await bob.getByRole('treeitem', { name: /docs/ }).click();
    await expect(bob.getByRole('heading', { name: 'Details' })).toBeVisible();
    await expectAccessible(bob, 'the file tree of a consumer');
    // The same read on more requests, which land on other replicas, shows the grant everywhere.
    for (let i = 0; i < 4; i++) {
      await bobSearch();
      await expect(bob.getByRole('treeitem', { name: new RegExp(repoKey) })).toBeVisible();
    }
  });

  await test.step('removing the membership takes the grant away again', async () => {
    await page.goto(`/ui/en/admin/groups/${groupName}`);
    await page.getByRole('row', { name: /bob/ }).getByRole('button', { name: /Remove/ }).click();
    await expect(page.getByRole('heading', { name: 'Members (0)' })).toBeVisible();
    await bobSearch();
    await expect(bob.getByText('No repository matches.')).toBeVisible();
  });

  await signOut(bob);
  await signOut(page);
  const seen = new Set([...replicas, ...bobReplicas]);
  expect(seen.size, `replicas that answered: ${[...seen]}`).toBeGreaterThanOrEqual(2);
  expect(await bobCsp(), 'securitypolicyviolation events on bob\'s pages').toEqual([]);
  await bobContext.close();
});
