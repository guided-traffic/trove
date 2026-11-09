import { e2eProject, expect, expectAccessible, repositories, signInLocal, test, unique } from '../support';

// R-UI-047 journey "general settings" (R-UI-033, R-OPS-024, R-OPS-025): an administrator saves a banner and it shows on the
// sign-in page of an anonymous browser and, after a reload, on every screen; a javascript link in its message is shown as text.
// Switching the read-only maintenance mode on asks first, then every write answers 503 with the message, reads still work, and
// switching it off lifts the refusal. Whatever the journey sets is put back through the API at the end, so the installation the
// following journeys share is not left read-only.

const bannerText = 'Planned work on Saturday, see [status](https://status.example.com/) or [bad](javascript:alert(1)).';

test.afterEach(async ({ admin }) => {
  await admin.put('/api/v1/system/maintenance', { data: { mode: 'normal' } });
  await admin.put('/api/v1/system/banner', { data: { enabled: false } });
});

test('R-UI-033 R-OPS-025 a saved banner shows on the sign-in page and on every screen, and its javascript link is plain text', async ({
  page,
  browser,
  baseURL,
}) => {
  await signInLocal(page);
  await page.goto('/ui/en/admin/settings/general');
  await expect(page.getByRole('heading', { name: 'General settings', level: 1 })).toBeVisible();
  await expect(page.getByTestId('base-url')).not.toBeEmpty();
  await expect(page.getByTestId('base-url-source')).toContainText('configuration file');

  await test.step('the banner is saved', async () => {
    await page.getByLabel('Show the banner').check();
    await page.getByLabel('Severity').selectOption('warning');
    await page.getByLabel('Title (optional)').fill('Planned work');
    await page.getByLabel('Banner message').fill(bannerText);
    await page.getByRole('button', { name: 'Save banner' }).click();
    await expect(page.getByText('Banner saved.')).toBeVisible();
  });

  await test.step('it shows on another screen of the signed-in user', async () => {
    await page.goto(`/ui/en/projects/${e2eProject}/packages`);
    const notice = page.getByRole('region', { name: 'System notice' });
    await expect(notice).toContainText('Warning: Planned work.');
    await expect(notice.getByRole('link', { name: 'status' })).toHaveAttribute('href', 'https://status.example.com/');
    await expect(notice).toContainText('[bad](javascript:alert(1))');
    await expect(notice.locator('a[href^="javascript:" i]')).toHaveCount(0);
  });

  await test.step('it shows on the sign-in page for a browser without a session', async () => {
    const anonymous = await browser.newContext({ baseURL, ignoreHTTPSErrors: true });
    const other = await anonymous.newPage();
    await other.goto('/ui/en/login?auto_sign_in=false');
    await expect(other.getByRole('region', { name: 'System notice' })).toContainText('Planned work on Saturday');
    await anonymous.close();
  });

  await test.step('switching it off removes it', async () => {
    await page.goto('/ui/en/admin/settings/general');
    await page.getByLabel('Show the banner').uncheck();
    await page.getByRole('button', { name: 'Save banner' }).click();
    await expect(page.getByText('Banner saved.')).toBeVisible();
    await expect(page.getByRole('region', { name: 'System notice' })).toHaveCount(0);
  });

  await expectAccessible(page, 'general settings');
});

test('R-UI-033 R-OPS-024 the read-only maintenance mode asks first, refuses writes with the message and is switched off again', async ({
  page,
  admin,
}) => {
  await signInLocal(page);
  await page.goto('/ui/en/admin/settings/general');
  const message = `Storage migration ${unique('m')}`;

  await test.step('switching it on asks for confirmation', async () => {
    await page.getByLabel('Read-only maintenance mode', { exact: true }).check();
    await page.getByLabel('Maintenance message').fill(message);
    await page.getByRole('button', { name: 'Save maintenance mode' }).click();
    const dialog = page.getByRole('dialog', { name: 'Switch to read-only maintenance mode?' });
    await expect(dialog).toBeVisible();
    await dialog.getByRole('button', { name: 'Switch on' }).click();
    await expect(page.getByTestId('mode-state')).toContainText('Read-only mode is on.');
    await expect(page.getByRole('region', { name: 'Maintenance notice' })).toContainText(message);
  });

  await test.step('a write answers 503 with the message and a Retry-After, a read still works', async () => {
    await expect
      .poll(async () => (await admin.post(repositories(), { data: { key: unique('ui-blocked'), class: 'local', format: 'npm' } })).status(), {
        message: 'every replica refuses the write',
      })
      .toBe(503);
    const refused = await admin.post(repositories(), { data: { key: unique('ui-blocked'), class: 'local', format: 'npm' } });
    expect(refused.status()).toBe(503);
    expect(refused.headers()['retry-after']).toBeTruthy();
    expect(await refused.text()).toContain(message);
    expect((await admin.get(repositories())).status()).toBe(200);
  });

  await test.step('switching it off lifts the refusal', async () => {
    await page.getByLabel('Read-only maintenance mode', { exact: true }).uncheck();
    await page.getByRole('button', { name: 'Save maintenance mode' }).click();
    await expect(page.getByTestId('mode-state')).toContainText('Normal mode');
    await expect(page.getByRole('region', { name: 'Maintenance notice' })).toHaveCount(0);
    const key = unique('ui-unblocked');
    await expect
      .poll(async () => (await admin.post(repositories(), { data: { key, class: 'local', format: 'npm' } })).status(), {
        message: 'every replica accepts the write again',
      })
      .toBeLessThan(500);
    await admin.delete(`${repositories()}/${key}`);
  });
});
