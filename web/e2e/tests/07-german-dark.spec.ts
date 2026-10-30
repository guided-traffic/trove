import { expect, expectAccessible, signInLocal, signOut, test } from '../support';

// R-UI-047 internationalization and accessibility tiers in the browser (R-UI-044, R-UI-045, R-UI-046, E1.11): one
// journey in German with the dark colour scheme, so `color-contrast` is evaluated for the second locale and the second
// theme. The screens are those of the other journeys; the language switch of the user menu ends it.

test.use({ colorScheme: 'dark', locale: 'de-DE' });

test('R-UI-045 the screens in German, in the dark theme, and the switch back to English', async ({ page, replicas }) => {
  await signInLocal(page, '/ui/de');
  await expect(page.locator('html')).toHaveAttribute('lang', 'de');
  await expect(page).toHaveURL(/\/ui\/de\//);

  const screens: { route: string; heading: string }[] = [
    { route: '/ui/de/admin/repositories', heading: 'Repositories' },
    { route: '/ui/de/admin/repositories/new', heading: 'Neues Repository' },
    { route: '/ui/de/admin/groups', heading: 'Gruppen' },
    { route: '/ui/de/admin/system', heading: 'Systemstatus' },
    { route: '/ui/de/admin/audit', heading: 'Audit-Protokoll' },
  ];
  for (const { route, heading } of screens) {
    await page.goto(route);
    await expect(page.getByRole('heading', { name: heading, level: 1 })).toBeVisible();
    await expect(page.locator('html')).toHaveAttribute('lang', 'de');
    await expectAccessible(page, `${route} (de, dark)`);
  }

  // The language menu moves to the same screen in the other locale.
  await page.getByRole('button', { name: /Benutzermen/ }).click();
  await page.getByRole('menuitem', { name: 'Sprache' }).click();
  await page.getByRole('menuitem', { name: 'English' }).click();
  await expect(page).toHaveURL(/\/ui\/en\/admin\/audit/);
  await expect(page.getByRole('heading', { name: 'Audit log', level: 1 })).toBeVisible();
  await expect(page.locator('html')).toHaveAttribute('lang', 'en');
  await expectAccessible(page, 'audit log (en, dark)');

  await signOut(page);
  // The post-logout address carries no locale; the session that held the choice is gone, so the browser's language decides.
  await expect(page).toHaveURL(/\/ui\/(en|de)\/logged-out/);
  expect(replicas.size, `replicas that answered: ${[...replicas]}`).toBeGreaterThanOrEqual(2);
});
