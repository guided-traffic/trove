import { expect, signInLocal, signOut, test } from '../support';

// The journey that TestUpgrade_R_HA_064_AcrossTheCut and TestUpgrade_R_HA_020_RollingFromThePreviousRelease run in a loop
// while the release is upgraded under it (R-HA-020, R-HA-064). Every iteration is one fresh browser: the break-glass sign-in
// (a session created by one release must be understood by the other, R-HA-020), the screens that read through both
// families (the repository list is the management API, the status screen is the replica registry), a reload, and the sign-out.
// It uses only what the UI of every release since the cut offers: the sign-in form, the user name and the two screens.

test('R-HA-020 R-HA-064 R-UI-047 the UI signs in, lists the repositories and the replicas, and signs out during an upgrade', async ({ page }) => {
  // A hashed asset of one release is not served by the pods of the other (Q-HA-99): a page load that spans the window of an
  // upgrade can meet a 404 for `main-<hash>.js` and stay blank. The journey names such an answer on its standard output,
  // which is how the test tells that failure from any other.
  page.on('response', (r) => {
    if (r.status() === 404 && /\/ui\/[a-z]{2}(-[A-Z]{2})?\/[^?]*-[A-Za-z0-9_-]{8}\.(js|css)$/.test(r.url())) {
      console.log(`ASSET-SKEW ${r.url()} ${r.headers()['x-trove-replica'] ?? ''}`);
    }
  });
  await signInLocal(page);

  await page.goto('/ui/en/admin/repositories');
  await expect(page.getByRole('heading', { name: 'Repositories', level: 1 })).toBeVisible();
  await expect(page.getByText('npm-local').first()).toBeVisible();

  await page.goto('/ui/en/admin/system');
  await expect(page.getByRole('heading', { name: 'System status', level: 1 })).toBeVisible();

  // a reload is another request, on another replica, and the session holds
  await page.reload();
  await expect(page.locator('.user-name')).toHaveText('admin');

  await signOut(page);
  await expect(page).toHaveURL(/\/ui\/en\/logged-out/);
});
