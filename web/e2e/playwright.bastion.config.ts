import { defineConfig } from '@playwright/test';
import base from './playwright.config';

/**
 * The Bastions journey of R-BAS-007 (R-BAS-049). It needs the Bastions of deploy/dev/bastion.sh, so it is not part of the
 * journeys of `playwright.config.ts`; `TestBastion_R_BAS_007_Journey` (test/e2e/bastion_journey_test.go) starts it with
 * the environment of that config plus TROVE_E2E_BASTION_REGISTRATION (the registration whose replicas have made contact).
 * It signs in with the break-glass account and needs neither the test IdP nor its global setup.
 */
export default defineConfig({
  ...base,
  testDir: './bastion',
  globalSetup: undefined,
});
