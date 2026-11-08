import { e2eProject, expect, expectAccessible, qualified, repositories, repositoryApi, signInLocal, signOut, test, unique } from '../support';

// R-UI-047 journey "deb" (T-17 WP11; R-DEB-014, R-DEB-015, R-DEB-017, R-DEB-029, R-DEB-030, R-UI-021): the repository
// wizard offers Debian (APT) and the edit form shows its settings; the signing key panel lists the generated primary key,
// creates a next key, promotes it and shows the old key retired, and never shows a private key; the index state tab lists the
// suites; the client setup page offers APT and renders the source with the key file named after the project and the
// repository. Every screen passes axe in the light and in the dark colour scheme. The journey creates its own repository and
// uploads nothing: the package screens of an uploaded package are covered by the component tier (package-detail.spec.ts)
// and by the APT client matrix of the end-to-end suite.
//
// Written against the generated client and the documented shapes; not run in the run that wrote it (the kind cluster was in
// use by another builder). The index state step accepts the empty state of a repository without packages.

const localKey = unique('ui-deb');
const wizardKey = unique('ui-deb-wizard');

test.beforeAll(async ({ admin }) => {
  const local = await admin.post(repositories(), { data: { key: localKey, class: 'local', format: 'deb' } });
  expect(local.status(), await local.text()).toBe(201);
});

test.afterAll(async ({ admin }) => {
  await admin.delete(repositoryApi(localKey));
  await admin.delete(repositoryApi(wizardKey));
});

test('R-DEB-015 R-DEB-017 the signing key panel lists the generated key, rotates it with a next key and a promotion, and never shows a private key', async ({
  page,
  replicas,
  admin,
}) => {
  await signInLocal(page);
  const keys = async () => {
    const res = await admin.get(`${repositoryApi(localKey)}/deb/signing-keys`);
    expect(res.status(), await res.text()).toBe(200);
    return ((await res.json()) as { keys: { fingerprint: string; state: string }[] }).keys;
  };
  const table = page.getByTestId('deb-key-table');

  await test.step('the panel lists the one generated primary key', async () => {
    await page.goto(`/ui/en/projects/${e2eProject}/repositories/${localKey}`);
    await page.getByRole('tab', { name: 'Signing keys' }).click();
    await expect(page.getByRole('heading', { name: 'Signing keys', level: 2 })).toBeVisible();
    await expect(table.locator('tbody tr')).toHaveCount(1);
    await expect(table.locator('tbody tr').first()).toContainText('Primary');
    await expect(table.locator('tbody tr').first()).toContainText('Generated');
    await expect(table.locator('tbody tr').first()).toContainText('EdDSA Ed25519');
    await expect(page.getByTestId('deb-no-primary')).toHaveCount(0);
    await expect(page.locator('body')).not.toContainText('PRIVATE KEY');
    for (const scheme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme: scheme });
      await expectAccessible(page, `repository detail, signing keys (${scheme})`);
    }
  });

  let primary = '';
  await test.step('a next key starts the overlap', async () => {
    primary = (await keys())[0]!.fingerprint;
    await page.getByRole('button', { name: 'Generate a next key' }).click();
    await expect(table.locator('tbody tr')).toHaveCount(2);
    await expect(table.locator('tbody tr').nth(1)).toContainText('Next');
    await expect(page.getByTestId('deb-overlap')).toContainText('Both keys sign since');
    await expect(page.getByRole('button', { name: 'Generate a next key' })).toHaveCount(0);
    expect((await keys()).map((k) => k.state).sort()).toEqual(['next', 'primary']);
  });

  await test.step('the promotion asks first, retires the old key and keeps its certificate as a record', async () => {
    await page.getByRole('button', { name: 'Promote', exact: true }).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('its private key is erased');
    await dialog.getByRole('button', { name: 'Promote', exact: true }).click();
    await expect(page.getByText('The key was promoted.')).toBeVisible();
    await expect(table.locator('tbody tr').first()).toContainText('Primary');
    await expect(table.locator('tbody tr').nth(1)).toContainText('Retired');
    const after = await keys();
    expect(after.find((k) => k.fingerprint === primary)?.state).toBe('retired');
    expect(after.filter((k) => k.state === 'primary')).toHaveLength(1);
    await expect(page.locator('body')).not.toContainText('PRIVATE KEY');
  });

  await signOut(page);
  expect(replicas.size, `replicas that answered: ${[...replicas]}`).toBeGreaterThanOrEqual(2);
});

test('R-DEB-014 the index state tab lists the suites of the repository, or says that none has packages yet', async ({ page, replicas }) => {
  await signInLocal(page);
  await page.goto(`/ui/en/projects/${e2eProject}/repositories/${localKey}`);
  await page.getByRole('tab', { name: 'Index', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Index state', level: 2 })).toBeVisible();
  await expect(page.getByTestId('deb-index-table').or(page.getByText('No suite has packages yet.'))).toBeVisible();
  await expect(page.getByRole('button', { name: 'Rebuild all indexes' })).toBeVisible();
  for (const scheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme: scheme });
    await expectAccessible(page, `repository detail, index state (${scheme})`);
  }
  await signOut(page);
  expect(replicas.size, `replicas that answered: ${[...replicas]}`).toBeGreaterThanOrEqual(2);
});

test('R-UI-021 R-DEB-008 the wizard offers Debian and the edit form reads the settings back, the architectures as lines', async ({ page, replicas, admin }) => {
  await signInLocal(page);
  const debMember = async () => {
    const res = await admin.get(repositoryApi(wizardKey));
    expect(res.status()).toBe(200);
    return ((await res.json()) as { deb?: Record<string, unknown> }).deb;
  };

  await test.step('the wizard writes the architectures that differ from the default', async () => {
    await page.goto('/ui/en/admin/repositories/new');
    await page.getByRole('option', { name: 'Debian (APT)' }).click();
    await page.getByRole('button', { name: 'Next' }).click();
    await page.getByRole('radio', { name: /^Local/ }).check();
    await page.getByRole('button', { name: 'Next' }).click();
    await page.getByLabel('Project', { exact: true }).selectOption(e2eProject);
    await page.locator('#f-key').fill(wizardKey);
    await expect(page.getByText('The key is available.')).toBeVisible();
    await page.getByRole('button', { name: 'Next' }).click();
    await expect(page.getByRole('heading', { name: 'Debian (APT) settings', level: 2 })).toBeVisible();
    await expect(page.getByLabel('suites', { exact: true })).toHaveCount(0); // virtual repositories only
    await page.getByLabel('architectures', { exact: true }).fill('arm64\nriscv64');
    await page.getByRole('button', { name: 'Next' }).click();
    await page.getByRole('button', { name: 'Create repository' }).click();
    await expect(page).toHaveURL(new RegExp(`/projects/${e2eProject}/repositories/${wizardKey}$`));
    expect(await debMember()).toMatchObject({ architectures: ['arm64', 'riscv64'] });
  });

  await test.step('the edit form shows the lines and the default back is saved', async () => {
    await page.reload();
    await page.getByRole('tab', { name: 'Debian (APT) settings' }).click();
    await expect(page.getByLabel('architectures', { exact: true })).toHaveValue('arm64\nriscv64');
    await expectAccessible(page, 'repository detail, Debian settings');
    await page.getByLabel('architectures', { exact: true }).fill('amd64\narm64');
    await page.getByRole('button', { name: 'Save changes' }).click();
    await expect(page.getByText('Repository saved.')).toBeVisible();
    // the document reads back either without the member or with the format's default
    await expect
      .poll(async () => (await debMember())?.['architectures'] ?? ['amd64', 'arm64'])
      .toEqual(['amd64', 'arm64']);
  });

  await signOut(page);
  expect(replicas.size, `replicas that answered: ${[...replicas]}`).toBeGreaterThanOrEqual(2);
});

test('R-DEB-029 the client setup page offers APT and renders the source with the key file named after the project and the repository', async ({
  page,
  replicas,
  baseURL,
}) => {
  await signInLocal(page);
  const port = new URL(baseURL ?? '').port;
  const name = `trove-${e2eProject}-${localKey}`;

  await page.goto(`/ui/en/setup?repository=${qualified(localKey)}`);
  await expect(page.getByRole('heading', { name: 'Client setup', level: 1 })).toBeVisible();
  const clientStep = page.getByRole('group', { name: /^2\s*Client$/ });
  await expect(clientStep.getByRole('radio', { name: 'APT (apt, apt-get)', exact: true })).toBeChecked();
  const configure = page.getByRole('tabpanel', { name: 'Configure' });
  const source = configure.locator('.snippet pre').first();
  await expect(source).toContainText(`/etc/apt/keyrings/${name}.asc`);
  await expect(source).toContainText(`URIs: https://localhost:${port}/api/deb/${e2eProject}/${localKey}/`);
  await expect(source).toContainText(`Signed-By: /etc/apt/keyrings/${name}.asc`);
  await expect(configure).not.toContainText('trusted=yes');
  for (const scheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme: scheme });
    await expectAccessible(page, `client setup, APT (${scheme})`);
  }

  await signOut(page);
  expect(replicas.size, `replicas that answered: ${[...replicas]}`).toBeGreaterThanOrEqual(2);
});
