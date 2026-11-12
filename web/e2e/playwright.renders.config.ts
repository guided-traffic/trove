import { defineConfig, devices } from '@playwright/test';
import { authFile, edge } from './renders/paths';

/**
 * `make ui-renders` (docs/developer/web-ui.md#renders-of-every-screen): every screen of the UI as pictures to judge, against
 * the UI development profile with the seed of renders/seed.ts. Local only, no CI job. `deploy/ui-dev/renders.sh` starts
 * what is not running (the services, trove with the profile's sign-in provider, the dev server) and runs this
 * configuration; by hand: `npx playwright test -c e2e/playwright.renders.config.ts [--grep <screen>]` in web/.
 *
 * UI_RENDERS_EDGE      the dev server, default https://localhost:4200
 * UI_RENDERS_WORKERS   browsers at a time, default 3 (the host is shared)
 */
export default defineConfig({
  testDir: './renders',
  outputDir: '../../.out/ui-renders/.results',
  workers: Number(process.env['UI_RENDERS_WORKERS'] ?? 3),
  fullyParallel: true,
  retries: 0,
  forbidOnly: true,
  // Three widths, two schemes and every tab of a screen, each settled before its picture.
  timeout: 300_000,
  expect: { timeout: 15_000 },
  globalSetup: './renders/setup.ts',
  globalTeardown: './renders/sheet.ts',
  reporter: [['list']],
  use: {
    baseURL: edge,
    // The dev server's certificate is signed by the profile's CA, which no browser store holds.
    ignoreHTTPSErrors: true,
    locale: 'en-US',
    timezoneId: 'UTC',
    actionTimeout: 15_000,
    navigationTimeout: 60_000,
    trace: 'off',
    screenshot: 'off',
    // The scrollbars are shown, as on a desktop with a mouse: one beside a table's rows takes the table's width.
    launchOptions: { ignoreDefaultArgs: ['--hide-scrollbars'] },
  },
  projects: (['anonymous', 'plain', 'power', 'admin'] as const).map((kind) => ({
    name: kind,
    grep: new RegExp(`@${kind}\\b`),
    use: { ...devices['Desktop Chrome'], ...(kind === 'anonymous' ? {} : { storageState: authFile(kind) }) },
  })),
});
