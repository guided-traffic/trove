import { defineConfig } from '@playwright/test';
import base from './playwright.config';

/**
 * The browser journey of the upgrade tests (R-HA-020, R-HA-064): one journey, run again and again by
 * `TestUpgrade_R_HA_064_AcrossTheCut` and `TestUpgrade_R_HA_020_RollingFromThePreviousRelease` while the release is upgraded
 * under it. Each run is one iteration of the loop, so its artifacts go to a directory of its own
 * (UPGRADE_OUTPUT_DIR, set by the test): the traces of a failed iteration must not be removed by the next one. The
 * journey signs in with the break-glass account only, so the global setup is the edge-only one of this directory.
 */
export default defineConfig({
  ...base,
  testDir: './upgrade',
  globalSetup: './upgrade/global-setup.ts',
  outputDir: process.env['UPGRADE_OUTPUT_DIR'] ?? '../../.out/playwright/upgrade',
  reporter: [['list']],
});
