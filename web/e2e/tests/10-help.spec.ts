import { expect, expectAccessible, signInLocal, signOut, test } from '../support';

// R-UI-050 journey "help" (R-API-054, R-UI-044, R-UI-047, F-16): the "?" of the top bar opens the manual page of the
// current screen, `/help` lists the manual, and a page opens from the list. Axe runs, with `color-contrast`, on each
// screen in the light scheme in English and in the dark scheme in German (the manual's own text is English,
// Q-UI-89, and carries `lang="en"`; the frame is translated).

const manual = [
  { section: 'user', page: 'access-tokens', screen: '/profile/tokens' },
  { section: 'admin', page: 'sign-in-and-identity', screen: '/admin/groups' },
];

test.describe('light, English', () => {
  test.use({ colorScheme: 'light', locale: 'en-US' });

  test('R-UI-050 R-API-054 R-UI-044 the help area and the "?" of the current screen, with axe in the light theme', async ({ page, replicas }) => {
    await signInLocal(page);

    await test.step('/help lists the manual, and a page opens from the list', async () => {
      await page.goto('/ui/en/help');
      await expect(page.getByRole('heading', { name: 'Help', level: 1 })).toBeVisible();
      const cards = page.locator('a.help-card');
      await expect(cards.first()).toBeVisible();
      expect(await cards.count()).toBeGreaterThanOrEqual(2);
      await expectAccessible(page, '/help (en, light)');
      await cards.first().click();
      await expect(page).toHaveURL(/\/ui\/en\/help\/[a-z]+\/[a-z-]+$/);
      await expect(page.locator('article.help-article h1')).toBeVisible();
      await expectAccessible(page, 'a manual page opened from the list (en, light)');
    });

    for (const { section, page: slug, screen } of manual) {
      await test.step(`the "?" on ${screen} opens ${section}/${slug}`, async () => {
        await page.goto(`/ui/en${screen}`);
        // The "?" follows the route once its navigation has ended (the shell is up before the guard
        // of the first navigation answers), so the step waits for the link of this screen, then clicks.
        const help = page.getByRole('link', { name: 'Help' }).first();
        await expect(help).toHaveAttribute('href', new RegExp(`/help/${section}/${slug}$`));
        await help.click();
        await expect(page).toHaveURL(new RegExp(`/ui/en/help/${section}/${slug}$`));
        await expect(page.locator('article.help-article h1')).toBeVisible();
        await expectAccessible(page, `help from ${screen} (en, light)`);
      });
    }

    await signOut(page);
    expect(replicas.size, `replicas that answered: ${[...replicas]}`).toBeGreaterThanOrEqual(2);
  });
});

test.describe('dark, German', () => {
  test.use({ colorScheme: 'dark', locale: 'de-DE' });

  test('R-UI-050 R-API-054 R-UI-044 R-UI-045 the help area and the "?" of the current screen, with axe in the dark theme in German', async ({ page, replicas }) => {
    await signInLocal(page, '/ui/de');
    await expect(page.locator('html')).toHaveAttribute('lang', 'de');

    await test.step('/help lists the manual, and a page opens from the list', async () => {
      await page.goto('/ui/de/help');
      await expect(page.getByRole('heading', { name: 'Hilfe', level: 1 })).toBeVisible();
      const cards = page.locator('a.help-card');
      await expect(cards.first()).toBeVisible();
      await expectAccessible(page, '/help (de, dark)');
      await cards.first().click();
      await expect(page).toHaveURL(/\/ui\/de\/help\/[a-z]+\/[a-z-]+$/);
      await expect(page.locator('article.help-article h1')).toBeVisible();
      await expectAccessible(page, 'a manual page opened from the list (de, dark)');
    });

    for (const { section, page: slug, screen } of manual) {
      await test.step(`the "?" on ${screen} opens ${section}/${slug}`, async () => {
        await page.goto(`/ui/de${screen}`);
        // The "?" follows the route once its navigation has ended (the shell is up before the guard
        // of the first navigation answers), so the step waits for the link of this screen, then clicks.
        const help = page.getByRole('link', { name: 'Hilfe' }).first();
        await expect(help).toHaveAttribute('href', new RegExp(`/help/${section}/${slug}$`));
        await help.click();
        await expect(page).toHaveURL(new RegExp(`/ui/de/help/${section}/${slug}$`));
        await expect(page.locator('article.help-article h1')).toBeVisible();
        await expectAccessible(page, `help from ${screen} (de, dark)`);
      });
    }

    await signOut(page);
    expect(replicas.size, `replicas that answered: ${[...replicas]}`).toBeGreaterThanOrEqual(2);
  });
});
