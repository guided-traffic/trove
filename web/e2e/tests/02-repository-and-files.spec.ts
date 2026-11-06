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
    await page.goto('/ui/en/admin/repositories');
    await expect(page.getByRole('heading', { name: 'Repositories', level: 1 })).toBeVisible();
    await expectAccessible(page, 'repository list');
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
    await page.getByLabel('Project', { exact: true }).selectOption(e2eProject);
    await page.locator('#f-key').fill(repoKey);
    await expect(page.getByText('The key is available.')).toBeVisible();
    await expectAccessible(page, 'repository wizard, step basic settings');
    await page.getByRole('button', { name: 'Next' }).click();
    await page.getByRole('button', { name: 'Next' }).click();
    await expect(page.getByRole('heading', { name: 'Review', level: 2 })).toBeVisible();
    await expectAccessible(page, 'repository wizard, review');
    await page.getByRole('button', { name: 'Create repository' }).click();
    await expect(page).toHaveURL(new RegExp(`/admin/repositories/${e2eProject}/${repoKey}$`));
    await expectAccessible(page, 'repository detail');
  });

  await test.step('the file tree shows it; a file is uploaded in the browser', async () => {
    await page.goto('/ui/en/browse');
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
    await page.goto(`/ui/en/browse/${e2eProject}/${repoKey}`);
    await expect(page.getByRole('table')).toContainText('hello.txt');
    await expectAccessible(page, 'repository contents');
    await page.getByRole('link', { name: 'hello.txt' }).click();
    await expect(page).toHaveURL(new RegExp(`/browse/${e2eProject}/${repoKey}/hello\\.txt$`));
  });

  await test.step('it is found by browsing and downloaded with the original bytes', async () => {
    await page.goto(`/ui/en/browse/${e2eProject}/${repoKey}`);
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
