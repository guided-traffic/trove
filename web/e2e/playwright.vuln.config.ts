import { defineConfig } from '@playwright/test';
import base from './playwright.config';

/**
 * The vulnerability journeys of R-VULN-022 and R-VULN-023. They need the scanning switched on against the test advisory
 * source (`deploy/dev/osvstub.yaml`, R-VULN-029), so they are not part of the journeys of `playwright.config.ts`: the e2e
 * wave starts them with the environment of that config plus TROVE_E2E_VULN=1, in the `scatter` profile (three `web`
 * replicas, every request to another one). They sign in with the break-glass account and with bob (the test IdP), so the
 * global setup of the base config stays.
 */
export default defineConfig({
  ...base,
  testDir: './vuln',
});
