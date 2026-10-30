import { request } from '@playwright/test';
import { alicePassword, expect, expectAccessible, signInWithKeycloak, signOut, test } from '../support';

// R-UI-047 journey "token create and revoke" (R-UI-029, R-AUTH-029, R-AUTH-031, R-AUTH-042, E1.11): alice creates a
// personal token on the profile screen, sees its value once, uses it, revokes it, and the revocation holds on every
// replica within the bound of R-AUTH-042.

test('R-UI-029 a personal token is shown once, works, and stops working when it is revoked', async ({ page, baseURL, replicas }) => {
  await signInWithKeycloak(page, 'alice', alicePassword);
  await page.goto('/ui/en/profile/tokens');
  await expect(page.getByRole('heading', { name: 'Personal tokens', level: 1 })).toBeVisible();
  await expectAccessible(page, 'personal tokens, empty');

  await page.getByRole('button', { name: 'Create token' }).click();
  await expect(page.getByRole('heading', { name: 'New token', level: 2 })).toBeVisible();
  await page.getByLabel('Description').fill('browser journey');
  await expectAccessible(page, 'new token form');
  await page.locator('trove-token-form button[type=submit]').click();

  // The value is shown in this answer only.
  const secret = page.getByRole('region', { name: 'Your new token' });
  await expect(secret).toBeVisible();
  const value = await secret.locator('#secret-value').inputValue();
  expect(value).toMatch(/^trv_/);
  await expectAccessible(page, 'the new token value');

  const probe = async (token: string) => {
    const ctx = await request.newContext({ baseURL, ignoreHTTPSErrors: true, extraHTTPHeaders: { Authorization: `Bearer ${token}` } });
    try {
      return (await ctx.get('/api/v1/tokens/current')).status();
    } finally {
      await ctx.dispose();
    }
  };
  // Every request goes to another replica: the token is accepted on all of them.
  for (let i = 0; i < 6; i++) expect(await probe(value), `use ${i + 1} of the new token`).toBe(200);

  await secret.getByRole('button', { name: 'I have copied it' }).click();
  await expect(secret).toBeHidden();
  await page.reload();
  await expect(page.getByRole('cell', { name: 'browser journey' })).toBeVisible();
  // The list never shows the value again.
  await expect(page.getByText(value)).toHaveCount(0);
  await expectAccessible(page, 'personal tokens, one token');

  await page.getByRole('row', { name: /browser journey/ }).getByRole('button', { name: 'Revoke' }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Revoke' }).click();
  await expect(page.getByText('No tokens match.')).toBeVisible();

  // auth.revocation_bound is 2 s: a read after that is refused on every replica.
  await expect.poll(() => probe(value), { timeout: 10_000, intervals: [500] }).toBe(401);
  for (let i = 0; i < 6; i++) expect(await probe(value), `use ${i + 1} after the revocation`).toBe(401);

  await page.getByRole('checkbox', { name: 'Show revoked and expired tokens' }).check();
  await expect(page.getByRole('row', { name: /browser journey/ })).toContainText('Revoked');
  await expectAccessible(page, 'personal tokens, one revoked');

  await signOut(page);
  expect(replicas.size, `replicas that answered: ${[...replicas]}`).toBeGreaterThanOrEqual(2);
});
