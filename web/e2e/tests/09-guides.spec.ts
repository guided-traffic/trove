import { expect, expectAccessible, signInLocal, signOut, test, unique } from '../support';

// R-UI-047 journey "guides" (E2.13; R-UI-049, R-API-053): the guide of a virtual npm repository opens from the side
// bar and from the repository's "How to use" tab, shows the sections of npm's guide for the repository's class, and its
// configure snippet is, byte for byte, the snippet the client setup page shows for the same repository and client. The
// journey creates its own local and virtual repository: the journeys do not own the matrix's npm-all.

const localKey = unique('ui-guide-local');
const virtualKey = unique('ui-guide-all');

test.beforeAll(async ({ admin }) => {
  const local = await admin.post('/api/v1/repositories', { data: { key: localKey, class: 'local', format: 'npm' } });
  expect(local.status(), await local.text()).toBe(201);
  const virtual = await admin.post('/api/v1/repositories', {
    data: { key: virtualKey, class: 'virtual', format: 'npm', virtual: { members: [localKey], deployTarget: localKey } },
  });
  expect(virtual.status(), await virtual.text()).toBe(201);
});

test.afterAll(async ({ admin }) => {
  await admin.delete(`/api/v1/repositories/${virtualKey}`);
  await admin.delete(`/api/v1/repositories/${localKey}`);
});

test('E2.13 R-UI-049 the guide of a virtual repository, and its configure snippet equals the setup page', async ({
  page,
  replicas,
}) => {
  await signInLocal(page);

  await test.step('the side bar opens the guides, which offer npm and its four clients', async () => {
    await page.goto('/ui/en/packages');
    await page.getByRole('navigation', { name: 'Main navigation' }).getByRole('link', { name: 'Guides' }).click();
    await expect(page.getByRole('heading', { name: 'Guides', level: 1 })).toBeVisible();
    for (const label of ['npm', 'pnpm', 'Yarn Berry (2 and later)', 'Yarn Classic (1.x)']) {
      await expect(page.getByRole('group', { name: 'Client' }).getByRole('radio', { name: label, exact: true })).toBeVisible();
    }
    await expect(page.getByRole('heading', { name: 'What a virtual repository is', level: 2 })).toBeVisible();
    await expectAccessible(page, 'guides');
  });

  let guideSnippet = '';
  await test.step('the repository screen shows the guide of the repository in its How to use tab', async () => {
    await page.goto(`/ui/en/admin/repositories/${virtualKey}`);
    await page.getByRole('tab', { name: 'How to use' }).click();
    await expect(page.getByRole('heading', { name: 'What a virtual repository is', level: 2 })).toBeVisible();
    await expect(page.getByText(`For ${virtualKey} that is ${localKey}.`)).toBeVisible();
    const snippet = page.locator('[data-section="npm.configure"] .snippet pre');
    await expect(snippet).toHaveCount(1);
    guideSnippet = (await snippet.textContent()) ?? '';
    expect(guideSnippet).toContain(`/api/npm/${virtualKey}/:_authToken=\${TROVE_TOKEN}`);
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
