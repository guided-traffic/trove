import { e2eProject, expect, expectAccessible, repositories, repositoryApi, signInLocal, signOut, test, unique } from '../support';

// R-UI-047 journey "guides" (E2.13; R-UI-049, R-API-053): the side bar opens the guides with every format that has
// clients; the guide of a virtual npm repository opens from there and from the repository's "How to use" tab, shows the
// sections of npm's guide for the repository's class, and its configure snippet is, byte for byte, the snippet the
// client setup page shows for the same repository and client. The journey creates its own local and virtual
// repository: the journeys do not own the matrix's npm-all.

const localKey = unique('ui-guide-local');
const virtualKey = unique('ui-guide-all');

test.beforeAll(async ({ admin }) => {
  const local = await admin.post(repositories(), { data: { key: localKey, class: 'local', format: 'npm' } });
  expect(local.status(), await local.text()).toBe(201);
  const virtual = await admin.post(repositories(), {
    data: { key: virtualKey, class: 'virtual', format: 'npm', virtual: { members: [localKey], deployTarget: localKey } },
  });
  expect(virtual.status(), await virtual.text()).toBe(201);
});

test.afterAll(async ({ admin }) => {
  await admin.delete(repositoryApi(virtualKey));
  await admin.delete(repositoryApi(localKey));
});

test('E2.13 R-UI-049 the guide of a virtual repository, and its configure snippet equals the setup page', async ({
  page,
  replicas,
}) => {
  await signInLocal(page);

  await test.step('the user menu opens the guides, which offer generic with curl and npm with its four clients', async () => {
    await page.goto(`/ui/en/projects/${e2eProject}/packages`);
    await page.getByRole('button', { name: /User menu/ }).click();
    await page.getByRole('menuitem', { name: 'Guides', exact: true }).click();
    await expect(page.getByRole('heading', { name: 'Guides', level: 1 })).toBeVisible();
    const formats = page.getByRole('group', { name: 'Package type' });
    // The catalog is ordered by name, so the first format (Debian, since T-17) opens checked; generic is chosen here.
    await expect(formats.getByRole('radio', { name: 'Debian (APT)', exact: true })).toBeChecked();
    await formats.getByRole('radio', { name: 'Generic', exact: true }).check();
    await expect(page.getByRole('group', { name: 'Client' }).getByRole('radio', { name: 'curl', exact: true })).toBeVisible();
    await formats.getByRole('radio', { name: 'npm', exact: true }).check();
    for (const label of ['npm', 'pnpm', 'Yarn Berry (2 and later)', 'Yarn Classic (1.x)']) {
      await expect(page.getByRole('group', { name: 'Client' }).getByRole('radio', { name: label, exact: true })).toBeVisible();
    }
    await expect(page.getByRole('heading', { name: 'What a virtual repository is', level: 2 })).toBeVisible();
    await expectAccessible(page, 'guides');
  });

  let guideSnippet = '';
  await test.step('the repository settings show the guide of the repository in its How to use tab', async () => {
    await page.goto(`/ui/en/projects/${e2eProject}/repositories/${virtualKey}/settings`);
    await page.getByRole('tab', { name: 'How to use' }).click();
    await expect(page.getByRole('heading', { name: 'What a virtual repository is', level: 2 })).toBeVisible();
    await expect(page.getByText(new RegExp(`For (${e2eProject}/)?${virtualKey} that is (${e2eProject}/)?${localKey}\\.`))).toBeVisible();
    const snippet = page.locator('[data-section="npm.configure"] .snippet pre');
    await expect(snippet).toHaveCount(1);
    guideSnippet = (await snippet.textContent()) ?? '';
    expect(guideSnippet).toContain(`/api/npm/${e2eProject}/${virtualKey}/:_authToken=\${TROVE_TOKEN}`);
    await expectAccessible(page, 'repository detail, how to use');
  });

  await test.step('the client setup page shows the same configure snippet', async () => {
    await page.getByRole('link', { name: 'Open the client setup' }).click();
    await expect(page.getByRole('heading', { name: 'Client setup', level: 1 })).toBeVisible();
    await expect(page.getByRole('radio', { name: 'Reference an environment variable' })).toBeChecked();
    const setup = page.getByRole('tabpanel', { name: 'Configure' }).locator('.snippet pre');
    await expect.poll(() => setup.first().textContent()).toBe(guideSnippet);
  });

  await test.step('the setup page links back to the guide of the same repository and client', async () => {
    await page.getByRole('link', { name: /^Read the guide/ }).click();
    await expect(page.getByRole('heading', { name: 'Guides', level: 1 })).toBeVisible();
    await expect(page.getByRole('radio', { name: virtualKey })).toBeChecked();
    await expect.poll(() => page.locator('[data-section="npm.configure"] .snippet pre').first().textContent()).toBe(guideSnippet);
  });

  await signOut(page);
  expect(replicas.size, `replicas that answered: ${[...replicas]}`).toBeGreaterThanOrEqual(2);
});
