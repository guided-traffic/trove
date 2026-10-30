import { expect, expectAccessible, signInLocal, signOut, test, unique } from '../support';

// R-UI-047 journey "exposure rule" on the endpoint screens (R-UI-039, R-EP-017, R-EP-015, E1.11): an exposure rule is
// created on the endpoint `customers` with the preview, and the rule shows up in the endpoint's table.

const repoKey = unique('ui-exposed');
const ruleName = unique('ui-docs');

test.beforeAll(async ({ admin }) => {
  const created = await admin.post('/api/v1/repositories', { data: { key: repoKey, class: 'local', format: 'generic' } });
  expect(created.status(), await created.text()).toBe(201);
  for (const path of ['docs/guide.txt', 'docs/faq.txt', 'private/plan.txt']) {
    const put = await admin.put(`/files/${repoKey}/${path}`, { data: `${path}\n` });
    expect(put.ok(), `PUT ${path}: ${put.status()}`).toBeTruthy();
  }
});

test.afterAll(async ({ admin }) => {
  await admin.delete(`/api/v1/endpoints/customers/exposure-rules/${ruleName}`);
  await admin.delete(`/api/v1/repositories/${repoKey}`);
});

test('R-UI-039 R-EP-017 an exposure rule with its preview', async ({ page, replicas }) => {
  await signInLocal(page);

  await page.goto('/ui/en/admin/endpoints');
  await expect(page.getByRole('heading', { name: 'Endpoints', level: 1 })).toBeVisible();
  await expectAccessible(page, 'endpoint list');
  await page.getByRole('link', { name: 'customers' }).click();
  await expect(page.getByRole('heading', { name: 'Endpoint', level: 1, exact: true })).toBeVisible();
  await expect(page.getByText('customers').first()).toBeVisible();
  await expectAccessible(page, 'endpoint detail');

  await page.goto('/ui/en/admin/endpoints/customers/rules/new');
  await expect(page.getByRole('heading', { name: 'New exposure rule', level: 1 })).toBeVisible();
  await expectAccessible(page, 'exposure rule editor, empty');
  await page.getByLabel('Name', { exact: true }).fill(ruleName);
  await page.getByLabel('Repository', { exact: true }).selectOption({ label: `${repoKey} (generic)` });
  await page.getByLabel('Include (one per line)').first().fill('docs/**');

  // The preview is the dry run of the endpoint with the rule as typed, before anything is saved.
  const preview = page.getByRole('region', { name: 'Preview' });
  await preview.getByLabel('Paths to test (one per line)').fill('docs/guide.txt\nprivate/plan.txt');
  const verdicts = preview.getByRole('table', { name: 'Verdict per path' });
  await expect(verdicts.getByRole('row', { name: /docs\/guide\.txt/ })).toContainText(ruleName);
  await expect(verdicts.getByRole('row', { name: /private\/plan\.txt/ })).not.toContainText(ruleName);
  await expectAccessible(page, 'exposure rule editor with the preview');

  await page.getByRole('button', { name: 'Create rule' }).click();
  await expect(page).toHaveURL(new RegExp(`/admin/endpoints/customers/rules/${ruleName}$`));
  // On another replica the rule is read back at once (R-API-052): the endpoint lists it, the editor shows what was saved.
  await page.goto('/ui/en/admin/endpoints/customers');
  await expect(page.getByRole('link', { name: ruleName })).toBeVisible();
  await expectAccessible(page, 'endpoint detail with the rule');
  await page.getByRole('link', { name: ruleName }).click();
  await expect(page.getByLabel('Include (one per line)').first()).toHaveValue('docs/**');
  await expectAccessible(page, 'exposure rule editor, saved rule');

  await signOut(page);
  expect(replicas.size, `replicas that answered: ${[...replicas]}`).toBeGreaterThanOrEqual(2);
});
