import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { Browser, chromium, expect, request } from '@playwright/test';
import { Api, expectStatus, kinds, mainProject, seedContent, seedGrants, seedProjectsAndRepositories } from './seed';
import { authFile, edge, outDir, stateDir } from './paths';

/**
 * The global setup of `make ui-renders`: waits for the edge (the dev server of the UI development profile), seeds the data
 * through the API as the break-glass account, signs the three kinds of user in through the profile's sign-in provider and
 * keeps their sessions for the renders, and publishes the packages and files as the power user.
 */
export default async function setup(): Promise<void> {
  // A run of every screen starts from an empty folder; a run of some (--grep) replaces their pictures and keeps the rest,
  // so the contact sheet shows both.
  if (process.env['UI_RENDERS_CLEAN'] === '1') rmSync(outDir, { recursive: true, force: true });
  mkdirSync(join(outDir, '.auth'), { recursive: true });
  await waitForEdge();

  const password = readFileSync(join(stateDir, 'secrets', 'break-glass-password'), 'utf8').trim();
  const ctx = await request.newContext({ baseURL: edge, ignoreHTTPSErrors: true, extraHTTPHeaders: { Origin: edge } });
  await expectStatus(await ctx.post('/auth/local/login', { data: { username: 'admin', password } }), [204], 'break-glass sign-in');
  const admin = await Api.of(ctx, edge);
  await seedProjectsAndRepositories(admin);

  const browser = await chromium.launch();
  try {
    for (const kind of ['plain', 'power', 'admin'] as const) await signIn(browser, kind);
    await seedGrants(admin);
    await publishAsPowerUser(browser);
  } finally {
    await browser.close();
    await ctx.dispose();
  }
}

/** The edge answers once trove behind the dev server does; `deploy/ui-dev/renders.sh` started both or found them running. */
async function waitForEdge(): Promise<void> {
  const ctx = await request.newContext({ baseURL: edge, ignoreHTTPSErrors: true });
  try {
    for (let i = 0; i < 120; i++) {
      const res = await ctx.get('/api/v1/auth/login-options').catch(() => null);
      if (res?.ok()) {
        const options = (await res.json()) as { providers: { id: string; available: boolean }[] };
        if (options.providers.some((p) => p.id === 'ui-dev' && p.available)) return;
      }
      await new Promise((r) => setTimeout(r, 1000));
    }
    throw new Error(`${edge} did not offer the sign-in provider ui-dev within two minutes: is make ui-dev-backend running?`);
  } finally {
    await ctx.dispose();
  }
}

/**
 * Signs one kind of user in through the provider's page and keeps the session (`.out/ui-renders/.auth/<kind>.json`). A
 * stored theme is cleared, so the browser's colour scheme decides, as "Follow the system" does (R-API-021).
 */
async function signIn(browser: Browser, kind: keyof typeof kinds): Promise<void> {
  const context = await browser.newContext({ baseURL: edge, ignoreHTTPSErrors: true });
  try {
    const page = await context.newPage();
    await page.goto('/ui/login');
    await page.getByRole('button', { name: /UI development users/ }).click();
    await page.getByRole('button', { name: new RegExp(`\\(${kinds[kind]}\\)`) }).click();
    // The login returns to /ui/en/, which the dev server does not serve (it serves one locale at /ui/): the session is
    // set by then, so the start page is opened by hand.
    await page.waitForURL((url) => url.origin === edge && url.pathname.startsWith('/ui/'));
    await page.goto('/ui/');
    await expect(page.getByRole('button', { name: /User menu/ })).toBeVisible({ timeout: 30_000 });
    const api = await Api.of(context.request, edge);
    const prefs = await api.get('/api/v1/users/me/preferences');
    await expectStatus(prefs, [200], `preferences of ${kinds[kind]}`);
    const reset = await api.patch('/api/v1/users/me/preferences', { theme: null }, { 'If-Match': prefs.headers()['etag'] ?? '*' });
    await expectStatus(reset, [200], `reset the theme of ${kinds[kind]}`);
    await context.storageState({ path: authFile(kind) });
  } finally {
    await context.close();
  }
}

/** The power user's packages and files, with a personal access token of an hour that is revoked afterwards. */
async function publishAsPowerUser(browser: Browser): Promise<void> {
  const context = await browser.newContext({ baseURL: edge, ignoreHTTPSErrors: true, storageState: authFile('power') });
  try {
    const api = await Api.of(context.request, edge);
    // The membership was granted a moment ago; the epoch of R-AUTHZ-032 makes every replica decide with it.
    const me = await api.get('/api/v1/authz/me');
    await expectStatus(me, [200], 'authz/me');
    const created = await api.post('/api/v1/tokens', { description: 'make ui-renders seed', ttlSeconds: 3600, scope: { inherit: true } });
    await expectStatus(created, [201], 'create the seed token');
    const token = (await created.json()) as { id: string; value?: string; token?: string };
    const value = token.value ?? token.token;
    if (!value) throw new Error('the token answer carries no value');
    try {
      await seedContent(context.request, edge, value);
    } finally {
      await expectStatus(await api.delete(`/api/v1/tokens/${token.id}`), [204, 404], 'revoke the seed token');
    }
    writeFileSync(join(outDir, '.auth', 'seeded'), `${mainProject}\n`);
  } finally {
    await context.close();
  }
}
