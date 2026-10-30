import { expect, expectAccessible, signInLocal, signOut, test } from '../support';

// R-UI-047 journeys "audit entry" and "system status" (R-UI-036, R-UI-037, R-UI-038, E1.11). They read what the
// journeys before them did (the files run in order): the audit log has an entry for each action, and the system
// status screen shows the three `web` replicas of the scatter profile and the one that answered.

const actions: { action: string; actor?: string }[] = [
  { action: 'repository.created', actor: 'admin' },
  { action: 'group.created', actor: 'admin' },
  { action: 'group.members.changed', actor: 'admin' },
  { action: 'authz.policy.created', actor: 'admin' },
  { action: 'token.created', actor: 'alice' },
  { action: 'token.revoked', actor: 'alice' },
  { action: 'endpoint.exposure.created', actor: 'admin' },
];

test('R-UI-036 R-UI-037 the audit log has an entry for each action of the journeys', async ({ page, replicas }) => {
  await signInLocal(page);
  await page.goto('/ui/en/admin/audit');
  await expect(page.getByRole('heading', { name: 'Audit log', level: 1 })).toBeVisible();
  await expectAccessible(page, 'audit log');

  for (const { action, actor } of actions) {
    await test.step(`${action} is in the log`, async () => {
      await page.getByRole('combobox', { name: 'Action' }).fill(action);
      await page.getByRole('button', { name: 'Search' }).click();
      const table = page.getByRole('table', { name: 'Audit events, newest first' });
      const rows = table.getByRole('row').filter({ hasText: action });
      await expect(rows.first()).toBeVisible();
      if (actor) await expect(rows.first()).toContainText(actor);
    });
  }

  await test.step('an event opens with its details', async () => {
    const table = page.getByRole('table', { name: 'Audit events, newest first' });
    await table.getByRole('row').filter({ hasText: 'endpoint.exposure.created' }).first().getByRole('button').click();
    await expect(page.getByRole('heading', { name: 'Event', level: 2 })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Details', level: 3 })).toBeVisible();
    await expectAccessible(page, 'audit log with an open event');
  });

  await signOut(page);
  expect(replicas.size, `replicas that answered: ${[...replicas]}`).toBeGreaterThanOrEqual(2);
});

test('R-UI-037 R-UI-038 the system status shows every replica and which one answered', async ({ page, replicas }) => {
  await signInLocal(page);
  await page.goto('/ui/en/admin/system');
  await expect(page.getByRole('heading', { name: 'System status', level: 1 })).toBeVisible();
  await expect(page.getByRole('region', { name: 'Replicas by role' })).toBeVisible();
  await expect(page.getByRole('region', { name: /^web\s*live 3, ready 3, degraded 0/ }).first()).toBeVisible();
  await expectAccessible(page, 'system status');

  // "Answered by" names the replica that served the status request, and it is one of the three in the table. (Which one
  // it is, is not asserted: a screen makes a fixed number of API calls and the round robin of the edge's pod that holds
  // the browser's connection is per request, so a given call can reach the same replica on every load. That the
  // journey as a whole was scattered is the assertion on `replicas` below.)
  const answered = (await page.locator('dt', { hasText: 'Answered by' }).locator('xpath=following-sibling::dd[1]').innerText()).trim();
  expect(answered).toMatch(/^trove-web-/);
  await expect(page.getByRole('region', { name: /^web\s*live 3/ }).first()).toContainText(answered);
  await page.getByRole('button', { name: 'Refresh now' }).click();
  await page.reload();
  await expect(page.getByRole('heading', { name: 'System status', level: 1 })).toBeVisible();

  await page.goto('/ui/en/admin/system/jobs');
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
  await expectAccessible(page, 'jobs');

  await signOut(page);
  await expect(page).toHaveURL(/\/ui\/en\/logged-out/);
  expect(replicas.size, `replicas that answered: ${[...replicas]}`).toBeGreaterThanOrEqual(2);
});
