import { alicePassword, breakGlassPassword, expect, expectAccessible, signInWithKeycloak, signOut, submitLocalForm, test } from '../support';

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
  await submitLocalForm(page, () => page.getByRole('button', { name: 'Sign in', exact: true }).last().click());
  await expect(page.locator('.user-name')).toHaveText('admin');
  await page.goto('/ui/en/admin/system');
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
  await page.reload();
  await expect(page.locator('.user-name')).toHaveText('admin');
  await signOut(page);
  await expect(page).toHaveURL(/\/ui\/en\/logged-out/);
  expect(replicas.size, `replicas that answered: ${[...replicas]}`).toBeGreaterThanOrEqual(2);
});

// R-AUTH-014 Q-AUTH-14 Q-AUTH-194: inside the rotation hold the local login answers 503 break_glass_rotating with a
// Retry-After; the sign-in waits for it and submits again. The hold is stood in for by two stubbed answers; the
// third request reaches the server, so the session is a real one. Any other 503 is not waited for.
test('R-AUTH-014 the local sign-in waits out the break-glass rotation hold', async ({ page }) => {
  let held = 0;
  await page.route('**/auth/local/login', async (route) => {
    if (held < 2) {
      held++;
      await route.fulfill({
        status: 503,
        headers: { 'Retry-After': '1' },
        contentType: 'application/problem+json',
        body: JSON.stringify({ status: 503, code: 'break_glass_rotating', title: 'Service Unavailable' }),
      });
      return;
    }
    await route.continue();
  });
  const started = Date.now();
  await page.goto('/ui/en/login?local=1');
  await page.getByLabel('Username').fill('admin');
  await page.getByLabel('Password').fill(breakGlassPassword);
  await submitLocalForm(page, () => page.getByRole('button', { name: 'Sign in', exact: true }).last().click());
  await expect(page.locator('.user-name')).toHaveText('admin');
  expect(held).toBe(2);
  expect(Date.now() - started, 'two waits of Retry-After 1 s plus the 1 s margin').toBeGreaterThanOrEqual(4000);
  await signOut(page);
});

test('R-AUTH-014 a 503 that is not the rotation hold is not waited for', async ({ page }) => {
  await page.route('**/auth/local/login', (route) =>
    route.fulfill({
      status: 503,
      headers: { 'Retry-After': '1' },
      contentType: 'application/problem+json',
      body: JSON.stringify({ status: 503, code: 'server_busy', title: 'Service Unavailable' }),
    }),
  );
  await page.goto('/ui/en/login?local=1');
  await page.getByLabel('Username').fill('admin');
  await page.getByLabel('Password').fill(breakGlassPassword);
  const started = Date.now();
  await submitLocalForm(page, () => page.getByRole('button', { name: 'Sign in', exact: true }).last().click());
  expect(Date.now() - started).toBeLessThan(3000);
  await expect(page.locator('.user-name')).toHaveCount(0);
});
