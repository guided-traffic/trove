import { expect, expectAccessible, signInLocal, signOut, test } from '../support';

// R-UI-051 journey "avatar" (R-API-055, R-UI-002, R-UI-044): the signed-in user has the generated robot; an uploaded PNG
// replaces it in the top bar without a reload, a reload keeps it, and the removal brings the robot back. No request leaves
// the origin while it happens.

// A 64 by 64 pixel PNG of one colour: above the 32 pixel minimum and far under 1 MiB.
const png = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAEAAAABACAIAAAAlC+aJAAAAT0lEQVR42u3PQQkAAAgEsEtiS1Naygi+hcEKLNXzWgQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQELgtNcOE8DKrYzgAAAABJRU5ErkJggg==',
  'base64',
);

test('R-UI-051 R-API-055 a user uploads an avatar, sees it in the top bar, and removes it again', async ({ page }) => {
  await signInLocal(page);
  const origin = new URL(page.url()).origin;
  const foreign: string[] = [];
  page.on('request', (r) => {
    if (/^https?:/.test(r.url()) && new URL(r.url()).origin !== origin) foreign.push(r.url());
  });
  const topBar = page.locator('header .user-button .avatar img');

  await page.goto('/ui/en/profile');
  await expect(page.getByRole('heading', { name: 'Public avatar', level: 2 })).toBeVisible();
  await expect(topBar).toHaveAttribute('src', /^data:image\/svg\+xml/);
  await expect(page.getByRole('button', { name: 'Remove avatar' })).toHaveCount(0);
  await expectAccessible(page, 'profile with the generated avatar');

  await test.step('a file of another type is refused in the browser, nothing is sent', async () => {
    let puts = 0;
    page.on('request', (r) => {
      if (r.method() === 'PUT' && r.url().includes('/users/me/avatar')) puts++;
    });
    await page.locator('trove-profile-avatar input[type="file"]').setInputFiles({
      name: 'a.gif',
      mimeType: 'image/gif',
      buffer: Buffer.from('GIF89a'),
    });
    await expect(page.getByRole('alert').filter({ hasText: 'Only PNG, JPEG and WebP' })).toBeVisible();
    expect(puts).toBe(0);
  });

  await test.step('a PNG is stored, shown here and in the top bar without a reload', async () => {
    await page.locator('trove-profile-avatar input[type="file"]').setInputFiles({ name: 'me.png', mimeType: 'image/png', buffer: png });
    await expect(topBar).toHaveAttribute('src', /^\/api\/v1\/avatars\/[a-f0-9]{64}$/);
    await expect(page.locator('trove-profile-avatar img')).toHaveAttribute('src', /^\/api\/v1\/avatars\/[a-f0-9]{64}$/);
    await expect(page.getByRole('button', { name: 'Remove avatar' })).toBeVisible();
    // The browser loaded the image from trove: it has a width.
    await expect.poll(() => topBar.evaluate((img: HTMLImageElement) => img.naturalWidth)).toBe(256);
    await expectAccessible(page, 'profile with the uploaded avatar');
  });

  await test.step('a reload keeps it', async () => {
    await page.reload();
    await expect(topBar).toHaveAttribute('src', /^\/api\/v1\/avatars\/[a-f0-9]{64}$/);
  });

  await test.step('removing it brings the robot back', async () => {
    await page.getByRole('button', { name: 'Remove avatar' }).click();
    await page.getByRole('dialog').getByRole('button', { name: 'Remove avatar' }).click();
    await expect(topBar).toHaveAttribute('src', /^data:image\/svg\+xml/);
    await expect(page.getByRole('button', { name: 'Remove avatar' })).toHaveCount(0);
  });

  expect(foreign, 'requests to another origin').toEqual([]);
  await signOut(page);
});
