import { defineConfig } from '@playwright/test';
import base from './playwright.config';

/**
 * The journey of the documentation of Ansible collections (R-UI-060 to R-UI-064). It reads a collection that
 * `TestAnsibleDocsJourney` (test/e2e/ansible_docs_test.go) published with the real ansible-galaxy and one that a
 * remote repository took from the counting Galaxy test upstream, whose names it passes in TROVE_E2E_ANSIBLE_DOCS_*, so
 * it is not part of the journeys of `playwright.config.ts`. It signs in with the break-glass account, so the global
 * setup of the base config stays.
 */
export default defineConfig({
  ...base,
  testDir: './ansible-docs',
});
