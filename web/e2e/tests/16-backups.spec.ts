import { expect, expectAccessible, signInLocal, signOut, test } from '../support';

// R-UI-047 journey "backups" (R-UI-048, R-API-050). The screen reads the backup job and changes nothing: the journey opens
// it as the administrator, checks the sections that exist whether or not a run has happened yet (the CronJob of the chart
// creates the first one on its schedule, so a fresh installation has none), and records every request the browser sends
// while the screen is open and the copy button is clicked: none may use a method other than GET or HEAD.
//
// What a run in progress, an interrupted run and a lease-expired run look like, and the overdue rule, are covered by the
// component tier with fixtures (web/src/app/features/system/backups/backups-page.spec.ts); the scenarios that produce those
// states on a cluster (drain during a run, kill -9) belong to the backup suite of test/e2e.

test('R-UI-048 the backups screen shows the status, the runs, the stored backups and the command, and sends only GET requests', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  await signInLocal(page);
  const methods: string[] = [];
  page.on('request', (request) => {
    if (new URL(request.url()).pathname.startsWith('/api/')) methods.push(`${request.method()} ${new URL(request.url()).pathname}`);
  });
  await page.goto('/ui/en/admin/system');
  await page.getByRole('link', { name: 'Backups', exact: true }).click();
  await expect(page).toHaveURL(/\/ui\/en\/admin\/system\/backups$/);
  await expect(page.getByRole('heading', { name: 'Backups', level: 1 })).toBeVisible();

  await test.step('the status says when the last backup was made, or that there is none', async () => {
    const status = page.getByRole('region', { name: 'Status', exact: true });
    await expect(status.getByText('Expected interval')).toBeVisible();
    await expect(status.getByText('Last success')).toBeVisible();
    await expect(status.getByText(/ago|No backup yet/).first()).toBeVisible();
  });

  await test.step('the runs are filtered by operation and state', async () => {
    const runs = page.getByRole('region', { name: 'Runs', exact: true });
    await expect(runs.getByRole('combobox', { name: 'Operation' })).toBeVisible();
    await runs.getByRole('combobox', { name: 'State' }).selectOption('failed');
    await runs.getByRole('button', { name: 'Search' }).click();
    await expect(page).toHaveURL(/state=failed/);
    await runs.getByRole('combobox', { name: 'State' }).selectOption('');
    await runs.getByRole('button', { name: 'Search' }).click();
    await expect(page).not.toHaveURL(/state=/);
    await expect(runs.getByRole('table', { name: 'Backup runs, newest first' }).or(runs.getByText('No backup run matches the filters.'))).toBeVisible();
  });

  await test.step('the stored backups say which run recorded them, or that no run has listed the target', async () => {
    const stored = page.getByRole('region', { name: 'Stored backups', exact: true });
    await expect(stored.getByText(/As recorded by run|No run has listed the target yet\./).first()).toBeVisible();
    await expect(stored.getByText('Expected interval')).toBeVisible();
  });

  await test.step('the panel shows the command and copies it; no control starts a backup', async () => {
    const panel = page.getByRole('region', { name: 'Start a backup now', exact: true });
    const command = (await panel.locator('pre').innerText()).trim();
    expect(command).toMatch(/^kubectl -n \S+ create job --from=cronjob\/\S+ \S+-manual-\d{10}$/);
    expect(command.split(' ').at(-1)!.length).toBeLessThanOrEqual(63);
    await panel.getByRole('button', { name: 'Copy the command' }).click();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(command);
    await expect(page.getByRole('button', { name: /^(Start|Run|Create|Delete|Verify|Restore)\b/ })).toHaveCount(0);
  });

  await expectAccessible(page, 'backups');

  await test.step('every request of the visit was a GET', async () => {
    expect(methods.length).toBeGreaterThan(0);
    expect(methods.filter((m) => !m.startsWith('GET ') && !m.startsWith('HEAD '))).toEqual([]);
    expect(methods.some((m) => m === 'GET /api/v1/system/backups/summary')).toBe(true);
    expect(methods.some((m) => m.startsWith('GET /api/v1/system/backups') && m !== 'GET /api/v1/system/backups/summary')).toBe(true);
  });

  await signOut(page);
});
