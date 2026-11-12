import { e2eProject, expect, expectAccessible, repositories, repositoryApi, signInLocal, signOut, test, unique } from '../support';

// R-UI-047 journey "repository setup" (R-UI-080 to R-UI-088): the format chooser and the one form named after the format, the
// well-known registries of an OCI remote and the further host GitLab's brings, a virtual npm repository created with two members
// and the deployment target the form presets, a key that is red only after it was left, and the settings page of the created
// remote: the standard sections open, the details closed, and a description saved alone. No upstream is contacted: the remote is
// created and read back through the API's answers only.

const ociKey = unique('ui-oci-gitlab');
const localKey = unique('ui-setup-local');
const remoteKey = unique('ui-setup-remote');
const virtualKey = unique('ui-setup-virtual');

test.beforeAll(async ({ admin }) => {
  const local = await admin.post(repositories(), { data: { key: localKey, class: 'local', format: 'npm' } });
  expect(local.status(), await local.text()).toBe(201);
  const remote = await admin.post(repositories(), { data: { key: remoteKey, class: 'remote', format: 'npm' } });
  expect(remote.status(), await remote.text()).toBe(201);
});

test.afterAll(async ({ admin }) => {
  for (const key of [virtualKey, ociKey, remoteKey, localKey]) await admin.delete(repositoryApi(key));
});

test('R-UI-080 R-UI-081 R-UI-082 R-UI-086 R-UI-087 a repository is set up on one form named after its format, and its settings are sections', async ({
  page,
  admin,
  replicas,
}) => {
  await signInLocal(page);
  const key = page.locator('#f-key');

  await test.step('the chooser opens the form of OCI, titled after the format, with the key suggested and nothing red', async () => {
    await page.goto(`/ui/en/projects/${e2eProject}/repositories/new`);
    await page.getByRole('searchbox', { name: 'Search by package type' }).fill('docker');
    await page.getByRole('link', { name: 'OCI (Docker)' }).click();
    await expect(page.getByRole('heading', { name: 'New OCI (Docker) repository', level: 1 })).toBeVisible();
    await expect(page.getByRole('navigation', { name: 'Breadcrumb' })).toContainText('New OCI (Docker) repository');
    await expect(key).toHaveValue(/^oci-local/);
    await expect(page.locator('main .field-error')).toHaveCount(0);
    await expect(key).not.toHaveAttribute('aria-invalid', 'true');
  });

  await test.step('a wrong key is red only once it was left', async () => {
    await key.fill('A');
    await expect(page.locator('#e-key')).toHaveCount(0);
    await key.blur();
    await expect(page.locator('#e-key')).toHaveText('Use 2 to 64 lower-case letters and digits, with ., _, __ or - between them.');
    await key.fill(ociKey);
    await key.blur();
    await expect(page.locator('#e-key')).toHaveCount(0);
  });

  await test.step('an OCI remote offers four registries; GitLab sets its URL and its token host, and the create names only the URL', async () => {
    await page.getByRole('radio', { name: /^Remote/ }).check();
    const presets = page.locator('main [aria-pressed]');
    await expect(presets).toHaveText(['Docker Hub', 'Quay', 'GitHub (ghcr.io)', 'GitLab']);
    await expect(page.getByRole('button', { name: 'Docker Hub' })).toHaveAttribute('aria-pressed', 'true');
    await expect(page.getByRole('textbox', { name: 'Upstream URL' })).toHaveValue('https://registry-1.docker.io/');
    await page.getByRole('button', { name: 'GitLab' }).click();
    await expect(page.getByRole('textbox', { name: 'Upstream URL' })).toHaveValue('https://registry.gitlab.com/');
    await page.getByRole('button', { name: 'Upstream details' }).click();
    // The further hosts follow the preset: GitLab's token service receives the credential (R-REPO-062).
    await expect(page.locator('main .host-row input[type="text"]').first()).toHaveValue('gitlab.com');
    for (const scheme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme: scheme });
      await expectAccessible(page, `creation form of an OCI remote with GitLab (${scheme})`);
    }
    const created = page.waitForResponse((r) => r.request().method() === 'POST' && r.url().endsWith(repositories()));
    await page.getByRole('button', { name: 'Create repository' }).click();
    const res = await created;
    expect(res.status()).toBe(201);
    expect(res.request().postDataJSON()).toMatchObject({ key: ociKey, class: 'remote', format: 'oci', remote: { url: 'https://registry.gitlab.com/' } });
    expect(res.request().postDataJSON().remote.extraHosts).toBeUndefined();
    await expect(page).toHaveURL(new RegExp(`/projects/${e2eProject}/repositories/${ociKey}$`));
    const stored = (await (await admin.get(repositoryApi(ociKey))).json()) as { remote: { extraHosts: { pattern: string; sendCredentials: boolean }[] } };
    expect(stored.remote.extraHosts).toEqual([{ pattern: 'gitlab.com', sendCredentials: true }]);
  });

  await test.step('a virtual npm repository is created with two members, and the deployment target is the local one without being touched', async () => {
    await page.goto(`/ui/en/projects/${e2eProject}/repositories/new/npm`);
    await page.getByRole('radio', { name: /^Virtual/ }).check();
    await key.fill(virtualKey);
    const available = page.getByRole('listbox', { name: 'Available repositories' });
    for (const member of [localKey, remoteKey]) {
      await available.getByRole('option', { name: new RegExp(member) }).click();
      await page.getByRole('button', { name: 'Add', exact: true }).click();
    }
    await expect(page.getByRole('combobox', { name: 'Deployment target' })).toHaveValue(localKey);
    await expectAccessible(page, 'creation form of a virtual npm repository with two members');
    await page.getByRole('button', { name: 'Create repository' }).click();
    await expect(page).toHaveURL(new RegExp(`/projects/${e2eProject}/repositories/${virtualKey}$`));
    const stored = (await (await admin.get(repositoryApi(virtualKey))).json()) as { virtual: { members: string[]; deployTarget: string | null } };
    expect(stored.virtual.members).toEqual([localKey, remoteKey]);
    expect(stored.virtual.deployTarget).toBe(localKey);
  });

  await test.step('the settings of the OCI remote: key, description, visibility and upstream open, every other section closed, no tab', async () => {
    await page.goto(`/ui/en/projects/${e2eProject}/repositories/${ociKey}/settings`);
    await expect(page.getByRole('heading', { name: 'Settings', level: 1 })).toBeVisible();
    await expect(page.getByRole('tab')).toHaveCount(0);
    await expect(page.getByRole('textbox', { name: 'Key' })).toHaveAttribute('readonly', '');
    await expect(page.getByRole('heading', { name: 'Visibility', level: 2 })).toBeVisible();
    await expect(page.getByRole('textbox', { name: 'Upstream URL' })).toHaveValue('https://registry.gitlab.com/');
    const toggles = page.locator('main trove-disclosure .toggle');
    await expect(toggles).not.toHaveCount(0);
    for (const toggle of await toggles.all()) await expect(toggle).toHaveAttribute('aria-expanded', 'false');
    for (const scheme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme: scheme });
      await expectAccessible(page, `settings page of an OCI remote (${scheme})`);
    }
  });

  await test.step('saving a changed description sends the description and nothing else', async () => {
    await page.getByRole('textbox', { name: 'Description (optional)' }).fill('GitLab images, cached');
    const patched = page.waitForResponse((r) => r.request().method() === 'PATCH' && r.url().endsWith(repositoryApi(ociKey)));
    await page.getByRole('button', { name: 'Save changes', exact: true }).click();
    const res = await patched;
    expect(res.status()).toBe(200);
    expect(res.request().postDataJSON()).toEqual({ description: 'GitLab images, cached' });
    await expect(page.getByText('Repository saved.')).toBeVisible();
  });

  await signOut(page);
  expect(replicas.size, `replicas that answered: ${[...replicas]}`).toBeGreaterThanOrEqual(2);
});
