import { readFile } from 'node:fs/promises';
import { e2eProject, expect, expectAccessible, repositories, repositoryApi, signInLocal, signOut, test, unique } from '../support';

// R-UI-047 journey "repository wizard" with the file tree (R-UI-010, R-UI-011, R-UI-012, R-UI-020, E1.11): the
// administrator creates a repository with the wizard, uploads a file in the browser, browses to it and
// downloads it, with the requests spread over three replicas.

const repoKey = unique('ui-files');
const content = `trove browser journey ${repoKey}\n`;

test.afterAll(async ({ admin }) => {
  await admin.delete(repositoryApi(repoKey));
});

test('R-UI-010 R-UI-011 R-UI-012 wizard, upload, browse and download', async ({ page, replicas }) => {
  await signInLocal(page);

  await test.step('the wizard creates a local generic repository', async () => {
    await page.goto(`/ui/en/projects/${e2eProject}/repositories`);
    await expect(page.getByRole('heading', { name: 'Repositories', level: 1 })).toBeVisible();
    await expect(page.getByRole('navigation', { name: 'Breadcrumb' })).toContainText(e2eProject);
    await expectAccessible(page, 'repository list of the project');
    await page.getByRole('link', { name: 'Create repository' }).click();
    await expect(page.getByRole('heading', { name: 'New repository', level: 1 })).toBeVisible();
    // The format is the first step (R-UI-021, Q-UI-112): a grid of tiles under a search field.
    await expect(page.getByRole('heading', { name: 'Choose the repository format', level: 2 })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Next' })).toBeDisabled();
    await expectAccessible(page, 'repository wizard, step format');
    await page.getByRole('searchbox', { name: 'Search by package type' }).fill('gener');
    await expect(page.getByRole('option')).toHaveCount(1);
    await page.getByRole('option', { name: 'Generic' }).click();
    await page.getByRole('button', { name: 'Next' }).click();

    // The class step offers the classes of the format; generic has only local, the others are shown unavailable.
    await expect(page.getByRole('radio', { name: /^Local/ })).toBeEnabled();
    await expect(page.getByRole('radio', { name: /^Remote/ })).toBeDisabled();
    await expect(page.getByText('Generic does not implement the class Virtual.')).toBeVisible();
    await expectAccessible(page, 'repository wizard, step class');
    await page.getByRole('radio', { name: /^Local/ }).check();
    await page.getByRole('button', { name: 'Next' }).click();
    // Inside a project the project is given, not chosen (Q-UI-129).
    await expect(page.getByLabel('Project', { exact: true })).toHaveCount(0);
    // The breadcrumb and the switcher name the project as well: the sentence of the step is what is asserted.
    await expect(page.locator('p.subject')).toHaveText(`In the project ${e2eProject}`);
    await page.locator('#f-key').fill(repoKey);
    await expect(page.getByText('The key is available.')).toBeVisible();
    await expectAccessible(page, 'repository wizard, step basic settings');
    await page.getByRole('button', { name: 'Next' }).click();
    await page.getByRole('button', { name: 'Next' }).click();
    await expect(page.getByRole('heading', { name: 'Review', level: 2 })).toBeVisible();
    await expectAccessible(page, 'repository wizard, review');
    await page.getByRole('button', { name: 'Create repository' }).click();
    await expect(page).toHaveURL(new RegExp(`/projects/${e2eProject}/repositories/${repoKey}$`));
    await expectAccessible(page, 'repository detail');
  });

  await test.step('the file tree shows it; a file is uploaded in the browser', async () => {
    await page.goto(`/ui/en/projects/${e2eProject}/browse`);
    await expectAccessible(page, 'file tree');
    await page.getByLabel('Filter repositories').fill(repoKey);
    await page.getByRole('treeitem', { name: new RegExp(repoKey) }).click();
    await expect(page.getByRole('navigation', { name: 'Location' })).toContainText(repoKey);
    await expectAccessible(page, 'file tree, a repository selected');
    await page.getByRole('button', { name: 'Upload files' }).click();
    await expect(page.getByRole('heading', { name: 'Upload files' })).toBeVisible();
    await page.locator('input[type=file]').setInputFiles({
      name: 'hello.txt',
      mimeType: 'text/plain',
      buffer: Buffer.from(content),
    });
    await expectAccessible(page, 'upload dialog');
    await page.getByRole('button', { name: 'Upload', exact: true }).click();
    await expect(page.getByText('uploaded', { exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'Close' }).click();
  });

  await test.step('the folder view of the repository lists the file in the right pane', async () => {
    await page.goto(`/ui/en/projects/${e2eProject}/browse/${repoKey}`);
    await expect(page.getByRole('table')).toContainText('hello.txt');
    await expectAccessible(page, 'repository contents');
    await page.getByRole('link', { name: 'hello.txt' }).click();
    await expect(page).toHaveURL(new RegExp(`/projects/${e2eProject}/browse/${repoKey}/hello\\.txt$`));
  });

  await test.step('it is found by browsing and downloaded with the original bytes', async () => {
    await page.goto(`/ui/en/projects/${e2eProject}/browse/${repoKey}`);
    await page.getByRole('treeitem', { name: /hello\.txt/ }).click();
    await expect(page.getByRole('navigation', { name: 'Location' })).toContainText('hello.txt');
    await expect(page.getByText('SHA-256')).toBeVisible();
    await expectAccessible(page, 'file details');
    const download = page.waitForEvent('download');
    await page.getByRole('link', { name: 'Download' }).click();
    const file = await download;
    expect(file.suggestedFilename()).toBe('hello.txt');
    expect(await readFile(await file.path(), 'utf8')).toBe(content);
  });

  await signOut(page);
  expect(replicas.size, `replicas that answered: ${[...replicas]}`).toBeGreaterThanOrEqual(2);
});

test('R-UI-065 R-UI-066 the repository list of a project shows the client address in a read-only field, the one of the admin area a copy button, and each puts it on the clipboard', async ({ page, context, admin }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  const key = unique('ui-address');
  const created = await admin.post(repositories(), {
    data: { key, class: 'local', format: 'generic', description: 'client address field' },
  });
  expect(created.ok(), await created.text()).toBe(true);
  try {
    const listed = await admin.get(`${repositories()}?q=${key}`);
    const row = ((await listed.json()) as { items: { key: string; addresses: string[] }[] }).items.find((r) => r.key === key);
    expect(row?.addresses[0], 'the API gives the repository a client address').toBeTruthy();
    const address = row!.addresses[0]!;

    await signInLocal(page);
    await page.goto(`/ui/en/projects/${e2eProject}/repositories?q=${key}`);
    const field = page.getByRole('textbox', { name: `Client address of ${key}` });
    await expect(field).toHaveValue(address);
    await expect(field).toHaveJSProperty('readOnly', true);
    await page.getByRole('button', { name: `Copy client address of ${key}` }).click();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(address);
    await expectAccessible(page, 'repository list with the client address field');

    // The admin area's list has no field: a quiet copy button beside the row's menu carries the address (Q-UI-330).
    await page.evaluate(() => navigator.clipboard.writeText(''));
    await page.goto(`/ui/en/admin/repositories?q=${key}`);
    await expect(page.getByRole('textbox', { name: `Client address of ${key}` })).toHaveCount(0);
    await page.getByRole('button', { name: `Copy client address ${address}` }).click();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(address);
    await signOut(page);
  } finally {
    await admin.delete(repositoryApi(key));
  }
});
