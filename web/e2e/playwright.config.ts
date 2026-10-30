import { defineConfig, devices } from '@playwright/test';

/**
 * The browser journeys of R-UI-047 (E1.11). They run against the chart-installed build in the `scatter`
 * profile of the dev cluster (three `web` replicas, every request to another one, R-ARCH-032) and are
 * started by `TestE1_11Journeys` (test/e2e/e1_11_scatter_test.go), which supplies the environment below.
 * By hand: see docs/developer/e2e.md.
 *
 * TROVE_E2E_EDGE_URL            the edge, e.g. https://localhost:28080
 * TROVE_E2E_KEYCLOAK_ADDR       host:port of a forward to the test IdP's Service (port 8443); the browser maps
 *                               the issuer's name trove-keycloak.trove.svc to it. Unset: the name must resolve.
 * TROVE_E2E_ALICE_PASSWORD, TROVE_E2E_BOB_PASSWORD, TROVE_E2E_BREAKGLASS_PASSWORD
 * TROVE_E2E_ADMIN_TOKEN         a bearer token of an administrator: journeys prepare and clean up through the API
 * NODE_EXTRA_CA_CERTS           the dev CA; global-setup.ts verifies both TLS endpoints against it
 */
const edge = process.env['TROVE_E2E_EDGE_URL'] ?? 'https://localhost:8080';
const keycloak = process.env['TROVE_E2E_KEYCLOAK_ADDR'];

export default defineConfig({
  testDir: './tests',
  outputDir: '../../.out/playwright/results',
  // One worker, in file order: the journeys share the installation, and the sign-in limits of R-AUTH-038 count
  // per client address. A journey does not test timing, so there are no retries: a failure is a finding.
  workers: 1,
  fullyParallel: false,
  retries: 0,
  forbidOnly: true,
  timeout: 120_000,
  expect: { timeout: 15_000 },
  globalSetup: './global-setup.ts',
  // PLAYWRIGHT_JSON_OUTPUT_NAME names the JSON report that TestE1_11Journeys reads; by hand it is left out.
  reporter: [
    ['list'],
    ['html', { outputFolder: '../../.out/playwright/report', open: 'never' }],
    ...(process.env['PLAYWRIGHT_JSON_OUTPUT_NAME'] ? [['json'] as ['json']] : []),
  ],
  use: {
    baseURL: edge,
    // The chain is verified by global-setup.ts against the dev CA (Chromium reads neither
    // NODE_EXTRA_CA_CERTS nor a CA that is in no system store).
    ignoreHTTPSErrors: true,
    locale: 'en-US',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    actionTimeout: 15_000,
    navigationTimeout: 30_000,
  },
  projects: [
    {
      name: 'chromium',
      use: {
        ...devices['Desktop Chrome'],
        launchOptions: {
          args: keycloak ? [`--host-resolver-rules=MAP trove-keycloak.trove.svc ${keycloak}`] : [],
        },
      },
    },
  ],
});
