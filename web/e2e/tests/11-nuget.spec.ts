import { expect, expectAccessible, signInLocal, signOut, test, unique } from '../support';

// R-UI-047 journey "nuget" (WP3.7; R-UI-007, R-UI-018, R-UI-019, R-UI-021): the repository wizard creates a local NuGet
// repository and writes the one setting that differs from the default; the edit form reads it back; the client setup page
// offers the .NET CLI, nuget.exe and Visual Studio and shows, for each, the snippets of R-NUGET-026 with the index.json
// URL of the repository; the guides area offers NuGet as soon as the server lists clients. Every screen passes axe in the
// light and in the dark colour scheme. The journey creates its own repositories and publishes nothing: the package
// list and detail of a pushed package are covered by the component tier (package-detail.spec.ts) and by the NuGet
// client matrix of the end-to-end suite.

const localKey = unique('ui-nuget');
const virtualKey = unique('ui-nuget-all');
const wizardKey = unique('ui-nuget-wizard');

test.beforeAll(async ({ admin }) => {
  const local = await admin.post('/api/v1/repositories', {
    data: { key: localKey, class: 'local', format: 'nuget' },
  });
  expect(local.status(), await local.text()).toBe(201);
  const virtual = await admin.post('/api/v1/repositories', {
    data: {
      key: virtualKey,
      class: 'virtual',
      format: 'nuget',
      virtual: { members: [localKey], deployTarget: localKey },
    },
  });
  expect(virtual.status(), await virtual.text()).toBe(201);
});

test.afterAll(async ({ admin }) => {
  await admin.delete(`/api/v1/repositories/${virtualKey}`);
  await admin.delete(`/api/v1/repositories/${localKey}`);
  await admin.delete(`/api/v1/repositories/${wizardKey}`);
});

test('R-UI-018 R-UI-019 the NuGet clients, their snippets with the index.json URL, and the guide, in both colour schemes', async ({
  page,
  replicas,
  baseURL,
}) => {
  await signInLocal(page);
  const port = new URL(baseURL ?? '').port;
  const feed = (key: string) => `https://localhost:${port}/api/nuget/${key}/v3/index.json`;

  await test.step('the client setup page offers NuGet with its three clients', async () => {
    await page.goto(`/ui/en/setup?repository=${virtualKey}`);
    await expect(page.getByRole('heading', { name: 'Client setup', level: 1 })).toBeVisible();
    await expect(
      page
        .getByRole('group', { name: /Package type/ })
        .getByRole('radio', { name: 'NuGet', exact: true }),
    ).toBeChecked();
    const clientStep = page.getByRole('group', { name: /^2\s*Client$/ });
    for (const label of ['.NET CLI (dotnet)', 'NuGet CLI (nuget.exe)', 'Visual Studio']) {
      await expect(clientStep.getByRole('radio', { name: label, exact: true })).toBeVisible();
    }
  });

  await test.step('the .NET CLI: the source command with the clear-text flag and a nuget.config that clears other sources', async () => {
    const clientStep = page.getByRole('group', { name: /^2\s*Client$/ });
    await clientStep.getByRole('radio', { name: '.NET CLI (dotnet)', exact: true }).check();
    const configure = page.getByRole('tabpanel', { name: 'Configure' });
    // The server renders the shell form, the PowerShell form and the nuget.config, in that order; the source is
    // always the index.json URL, and the push names the repository the caller chose (a virtual one forwards it).
    await expect(configure.locator('.snippet pre').first()).toContainText(
      `dotnet nuget add source ${feed(virtualKey)} --name trove`,
    );
    await expect(configure.locator('.snippet pre').nth(1)).toContainText(
      `dotnet nuget add source ${feed(virtualKey)} --name trove`,
    );
    await expect(configure.locator('.snippet pre').nth(2)).toContainText('<clear />');
    await expect(configure.locator('.snippet pre').nth(2)).toContainText(
      `value="${feed(virtualKey)}" protocolVersion="3"`,
    );
    await page.getByRole('tab', { name: 'Publish' }).click();
    await expect(
      page.getByRole('tabpanel', { name: 'Publish' }).locator('.snippet pre').first(),
    ).toContainText(`--source ${feed(virtualKey)}`);
  });

  await test.step('nuget.exe and Visual Studio', async () => {
    const clientStep = page.getByRole('group', { name: /^2\s*Client$/ });
    await clientStep.getByRole('radio', { name: 'NuGet CLI (nuget.exe)', exact: true }).check();
    await page.getByRole('tab', { name: 'Configure' }).click();
    await expect(
      page
        .getByRole('tabpanel', { name: 'Configure' })
        .locator('.snippet pre')
        .filter({ hasText: 'nuget sources add' })
        .first(),
    ).toContainText(feed(virtualKey));
    await clientStep.getByRole('radio', { name: 'Visual Studio', exact: true }).check();
    await expect(
      page.getByRole('tabpanel', { name: 'Configure' }).locator('.snippet pre').first(),
    ).toContainText(`<add key="trove" value="${feed(virtualKey)}" protocolVersion="3" />`);
  });

  for (const scheme of ['light', 'dark'] as const) {
    await test.step(`axe on the setup page and the guide in the ${scheme} scheme`, async () => {
      await page.emulateMedia({ colorScheme: scheme });
      await page.goto(`/ui/en/setup?repository=${virtualKey}&client=dotnet`);
      await expect(page.getByRole('heading', { name: 'Client setup', level: 1 })).toBeVisible();
      await expectAccessible(page, `client setup, NuGet (${scheme})`);
      await page.goto(`/ui/en/guides?format=nuget&client=dotnet&repository=${virtualKey}`);
      await expect(page.getByRole('heading', { name: 'Guides', level: 1 })).toBeVisible();
      await expect(
        page
          .getByRole('group', { name: 'Package type' })
          .getByRole('radio', { name: 'NuGet', exact: true }),
      ).toBeChecked();
      await expectAccessible(page, `guides, NuGet (${scheme})`);
    });
  }

  await signOut(page);
  expect(replicas.size, `replicas that answered: ${[...replicas]}`).toBeGreaterThanOrEqual(2);
});

test('R-UI-021 R-ARCH-004 the NuGet settings in the wizard and on the edit form, rendered from the settings schema', async ({
  page,
  replicas,
  admin,
}) => {
  await signInLocal(page);
  const mode = page.getByRole('combobox', { name: 'deleteMode' });
  const nugetMember = async () => {
    const res = await admin.get(`/api/v1/repositories/${wizardKey}`);
    expect(res.status()).toBe(200);
    return ((await res.json()) as { nuget?: Record<string, unknown> }).nuget;
  };

  await test.step('the wizard offers deleteMode for a local repository only and writes the value that differs from the default', async () => {
    await page.goto('/ui/en/admin/repositories/new');
    await page.getByRole('radio', { name: /^Local/ }).check();
    await page.getByRole('button', { name: 'Next' }).click();
    await page.getByRole('radio', { name: /^NuGet/ }).check();
    await page.getByRole('button', { name: 'Next' }).click();
    await page.locator('#f-key').fill(wizardKey);
    await expect(page.getByText('The key is available.')).toBeVisible();
    await page.getByRole('button', { name: 'Next' }).click();
    await expect(page.getByRole('heading', { name: 'NuGet settings', level: 2 })).toBeVisible();
    await expect(page.getByRole('checkbox', { name: 'forwardSearch' })).toHaveCount(0);
    await expect(mode.locator('option:checked')).toHaveText('unlist'); // the options carry their index as value (format-settings.ts)
    await mode.selectOption('delete');
    for (const scheme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme: scheme });
      await expectAccessible(page, `repository wizard, NuGet settings (${scheme})`);
    }
    await page.getByRole('button', { name: 'Next' }).click();
    await expect(page.getByText('deleteMode: delete')).toBeVisible();
    await page.getByRole('button', { name: 'Create repository' }).click();
    await expect(page).toHaveURL(new RegExp(`/admin/repositories/${wizardKey}$`));
    expect(await nugetMember()).toEqual({ deleteMode: 'delete' });
  });

  await test.step('the edit form reads it back and setting the default back removes it from the document', async () => {
    await page.reload();
    await page.getByRole('tab', { name: 'NuGet settings' }).click();
    await expect(mode.locator('option:checked')).toHaveText('delete');
    await expectAccessible(page, 'repository detail, NuGet settings');
    await mode.selectOption('unlist');
    await page.getByRole('button', { name: 'Save changes' }).click();
    await expect(page.getByText('Repository saved.')).toBeVisible();
    await expect.poll(nugetMember).toBeUndefined();
  });

  await signOut(page);
  expect(replicas.size, `replicas that answered: ${[...replicas]}`).toBeGreaterThanOrEqual(2);
});
