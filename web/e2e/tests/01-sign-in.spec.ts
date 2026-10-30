import { alicePassword, breakGlassPassword, expect, expectAccessible, signInWithKeycloak, signOut, test } from '../support';

// R-UI-047 journey "sign-in" (R-UI-004, R-AUTH-009, R-AUTH-013, E1.11): the authorization code flow against the
// test IdP and the break-glass form, each with requests that land on different replicas.

test('R-UI-004 R-AUTH-009 sign-in with the identity provider and sign-out', async ({ page, replicas }) => {
  await signInWithKeycloak(page, 'alice', alicePassword);
  await expect(page).toHaveURL(/\/ui\/en\/browse/);
  await expect(page.locator('.user-name')).toHaveText('alice');
  await expectAccessible(page, 'start page after the sign-in');

  // A reload is another request, on another replica, and the session holds.
  await page.reload();
  await expect(page.locator('.user-name')).toHaveText('alice');

  await signOut(page);
  await expect(page).toHaveURL(/\/ui\/en\/logged-out/);
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
  await expectAccessible(page, 'signed out');

  // The session is gone on every replica, and so is the IdP's (RP-initiated logout, R-AUTH-013): a protected route
  // leaves for the IdP, which asks for the password again instead of signing alice in silently.
  for (let i = 0; i < 3; i++) {
    await page.goto('/ui/en/admin/repositories');
    await expect(page).toHaveURL(/trove-keycloak\.trove\.svc:8443\/realms\/trove\//);
    await expect(page.locator('#username')).toBeVisible();
  }
  expect(replicas.size, `replicas that answered: ${[...replicas]}`).toBeGreaterThanOrEqual(2);
});

test('R-UI-004 R-AUTH-022 the break-glass administrator signs in with the local form', async ({ page, replicas }) => {
  await page.goto('/ui/en/login?local=1');
  await expect(page.getByRole('heading', { name: 'Local administrator' })).toBeVisible();
  await expectAccessible(page, 'sign-in with the local form');
  await page.getByLabel('Username').fill('admin');
  await page.getByLabel('Password').fill(breakGlassPassword);
  await page.getByRole('button', { name: 'Sign in', exact: true }).last().click();
  await expect(page.locator('.user-name')).toHaveText('admin');
  await page.goto('/ui/en/admin/system');
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
  await page.reload();
  await expect(page.locator('.user-name')).toHaveText('admin');
  await signOut(page);
  await expect(page).toHaveURL(/\/ui\/en\/logged-out/);
  expect(replicas.size, `replicas that answered: ${[...replicas]}`).toBeGreaterThanOrEqual(2);
});
