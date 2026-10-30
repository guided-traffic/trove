import AxeBuilder from '@axe-core/playwright';
import { APIRequestContext, expect, Page, request, test as base } from '@playwright/test';

/** What a journey needs besides the page: the management API as the administrator, and the replicas seen. */
interface Fixtures {
  /** The management API with the administrator token: data is prepared and cleaned up here, not in the UI (INV-17). */
  admin: APIRequestContext;
  /** The `X-Trove-Replica` values of every response the page received; filled while the test runs. */
  replicas: Set<string>;
}

export const alicePassword = required('TROVE_E2E_ALICE_PASSWORD');
export const bobPassword = required('TROVE_E2E_BOB_PASSWORD');
export const breakGlassPassword = required('TROVE_E2E_BREAKGLASS_PASSWORD');

function required(name: string): string {
  const v = process.env[name];
  if (!v) throw new Error(`${name} is not set`);
  return v;
}

/** A name that no earlier run used, so a rerun needs no cleanup to start. */
export function unique(prefix: string): string {
  return `${prefix}-${Date.now().toString(36)}`;
}

/**
 * `test` with two additions every journey wants. A CSP violation on any page fails the test (R-UI-047 security
 * tier), and the replicas that answered are collected so that a journey can assert that it was really scattered.
 */
export const test = base.extend<Fixtures>({
  admin: async ({ baseURL }, use) => {
    const ctx = await request.newContext({
      baseURL,
      ignoreHTTPSErrors: true,
      extraHTTPHeaders: { Authorization: `Bearer ${required('TROVE_E2E_ADMIN_TOKEN')}` },
    });
    await use(ctx);
    await ctx.dispose();
  },

  replicas: async ({ page }, use) => {
    const seen = new Set<string>();
    page.on('response', (r) => {
      const id = r.headers()['x-trove-replica'];
      if (id) seen.add(id);
    });
    await use(seen);
  },

  page: async ({ page }, use) => {
    const violations = await watchCsp(page);
    await use(page);
    expect(await violations(), 'securitypolicyviolation events').toEqual([]);
  },
});

/**
 * Collects the `securitypolicyviolation` events of every document the page loads (R-UI-047 security tier); the
 * returned function reads them. The `page` fixture asserts there are none; a second page of a journey (another
 * signed-in user) does the same with this function.
 */
export async function watchCsp(page: Page): Promise<() => Promise<string[]>> {
  const seen: string[] = [];
  await page.exposeFunction('troveCspViolation', (text: string) => seen.push(text));
  await page.addInitScript(() => {
    document.addEventListener('securitypolicyviolation', (e) => {
      void (window as unknown as { troveCspViolation(t: string): void }).troveCspViolation(
        `${e.violatedDirective} blocked ${e.blockedURI} at ${e.sourceFile}:${e.lineNumber}`,
      );
    });
  });
  return async () => seen;
}

export { expect };

/**
 * Runs axe-core on the page as it is and fails on a violation of impact serious or critical. This is the browser
 * tier of Q-UI-11: `color-contrast` and the document-level rules (landmarks, `lang`, title) count here, which
 * the jsdom tier cannot evaluate.
 */
export async function expectAccessible(page: Page, screen: string): Promise<void> {
  // A dialog or a menu that is still fading in has a blended colour: wait for the transitions that end.
  await page.evaluate(() =>
    Promise.all(
      document
        .getAnimations()
        .filter((a) => a.effect?.getComputedTiming().iterations !== Infinity)
        .map((a) => a.finished.catch(() => undefined)),
    ),
  );
  const result = await new AxeBuilder({ page }).analyze();
  const bad = result.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical');
  expect(
    bad.map((v) => `${v.impact} ${v.id}: ${v.help} (${v.nodes.length}) ${v.nodes.map((n) => n.target.join(' ')).slice(0, 3).join(' | ')}`),
    `axe on ${screen}`,
  ).toEqual([]);
}

/**
 * Signs in through the test IdP (alice or bob) and waits for the UI to show the session. The dev profile sets
 * `autoRedirect`, so the sign-in route leaves for the IdP's form at once; the sign-in screen itself is
 * checked in the journey of the local form, which suppresses the redirect.
 */
export async function signInWithKeycloak(page: Page, user: 'alice' | 'bob', password: string, ui = '/ui/en'): Promise<void> {
  await page.goto(`${ui}/login`);
  await page.locator('#username').fill(user);
  await page.locator('#password').fill(password);
  await page.locator('#kc-login').click();
  await expect(page.getByRole('button', { name: /User menu|Benutzermen/ })).toBeVisible();
}

/** Signs in as the break-glass administrator with the local form and waits for the session. */
export async function signInLocal(page: Page, ui = '/ui/en'): Promise<void> {
  await page.goto(`${ui}/login?local=1`);
  await page.getByLabel(/Username|Benutzername/).fill('admin');
  await page.getByLabel(/Password|Passwort/).fill(breakGlassPassword);
  await page.locator('form.local button[type=submit]').click();
  await expect(page.locator('.user-name')).toHaveText('admin');
}

/** Signs out through the user menu; the IdP ends its session and sends the browser back to the signed-out page. */
export async function signOut(page: Page): Promise<void> {
  await page.getByRole('button', { name: /User menu|Benutzermenü/ }).click();
  await page.getByRole('menuitem', { name: /Sign out|Abmelden/ }).click();
}
