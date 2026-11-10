import { e2eProject, expect, expectAccessible, qualified, repositories, repositoryApi, signInLocal, signOut, test, unique } from '../support';

// R-UI-047 journey "ansible" (R-ANS-025, R-ANS-026, R-ANS-012, R-UI-049): the client setup page offers ansible-galaxy and renders
// the ansible.cfg with the address of the repository and no token; the guide of the repository (its "How to use" tab) shows the
// sections for a local repository, with the configure snippet of the setup page and the netrc line the roles need; the edit form
// has the tabs for the import tasks and the tombstones, which say so when there is nothing yet. Every screen passes axe in the
// light and in the dark colour scheme. The journey creates its own repository and publishes nothing: the screens of a published
// collection are covered by the component tier (package-detail.spec.ts) and by the ansible-galaxy matrix of the end-to-end suite.

const localKey = unique('ui-ansible');

test.beforeAll(async ({ admin }) => {
  const local = await admin.post(repositories(), { data: { key: localKey, class: 'local', format: 'ansible' } });
  expect(local.status(), await local.text()).toBe(201);
});

test.afterAll(async ({ admin }) => {
  await admin.delete(repositoryApi(localKey));
});

test('R-ANS-025 R-UI-049 the guide of a local repository shows its sections, and its configure snippet is the one of the setup page', async ({
  page,
  replicas,
  baseURL,
}) => {
  await signInLocal(page);
  const port = new URL(baseURL ?? '').port;
  const address = `https://localhost:${port}/api/ansible/${e2eProject}/${localKey}/`;
  let guideConfigure = '';

  await test.step('the setup page offers ansible-galaxy and renders the server without a token', async () => {
    await page.goto(`/ui/en/setup?repository=${qualified(localKey)}`);
    await expect(page.getByRole('heading', { name: 'Client setup', level: 1 })).toBeVisible();
    const clientStep = page.getByRole('group', { name: /^2\s*Client$/ });
    await expect(clientStep.getByRole('radio', { name: 'Ansible Galaxy (ansible-galaxy)', exact: true })).toBeChecked();
    const configure = page.getByRole('tabpanel', { name: 'Configure' });
    const cfg = configure.locator('.snippet pre').first();
    await expect(cfg).toContainText('server_list = trove');
    await expect(cfg).toContainText(`url=${address}`);
    await expect(cfg).not.toContainText('token');
    await expect(configure).toContainText('ANSIBLE_GALAXY_SERVER_TROVE_TOKEN');
    for (const scheme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme: scheme });
      await expectAccessible(page, `client setup, ansible-galaxy (${scheme})`);
    }
    guideConfigure = (await cfg.textContent()) ?? '';
  });

  await test.step('the How to use tab shows the guide for a local repository and the same configure snippet', async () => {
    await page.goto(`/ui/en/projects/${e2eProject}/repositories/${localKey}/settings`);
    await page.getByRole('tab', { name: 'How to use' }).click();
    await expect(page.getByRole('heading', { name: 'What a local repository is', level: 2 })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Which credential ansible-galaxy sends', level: 2 })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Remove a version, import tasks and tombstones', level: 2 })).toBeVisible();
    await expect(page.locator('[data-section="ansible.configure"] .snippet pre').first()).toHaveText(guideConfigure);
    await expect(page.locator('body')).toContainText('~/.netrc');
    for (const scheme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme: scheme });
      await expectAccessible(page, `repository detail, how to use, ansible-galaxy (${scheme})`);
    }
  });

  await signOut(page);
  expect(replicas.size, `replicas that answered: ${[...replicas]}`).toBeGreaterThanOrEqual(2);
});

test('R-ANS-026 R-ANS-012 the edit form has the import tasks and the tombstones, each with its empty line', async ({ page, replicas }) => {
  await signInLocal(page);
  await page.goto(`/ui/en/projects/${e2eProject}/repositories/${localKey}/settings`);

  await test.step('the import tasks tab says that nothing was published', async () => {
    await page.getByRole('tab', { name: 'Import tasks', exact: true }).click();
    await expect(page.getByRole('heading', { name: 'Import tasks', level: 2 })).toBeVisible();
    await expect(page.getByTestId('ansible-imports-table')).toContainText('No collection has been published to this repository yet');
    for (const scheme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme: scheme });
      await expectAccessible(page, `repository detail, import tasks (${scheme})`);
    }
  });

  await test.step('the tombstones tab says that no number is blocked', async () => {
    await page.getByRole('tab', { name: 'Tombstones', exact: true }).click();
    await expect(page.getByRole('heading', { name: 'Tombstones', level: 2 })).toBeVisible();
    await expect(page.getByTestId('ansible-tombstones-table')).toContainText('no number is blocked');
    for (const scheme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme: scheme });
      await expectAccessible(page, `repository detail, tombstones (${scheme})`);
    }
  });

  await signOut(page);
  expect(replicas.size, `replicas that answered: ${[...replicas]}`).toBeGreaterThanOrEqual(2);
});
