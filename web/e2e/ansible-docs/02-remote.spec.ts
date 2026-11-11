import { Page } from '@playwright/test';
import { e2eProject, expect, expectAccessible, signInLocal, signOut, test } from '../support';

// R-UI-047 journey "ansible remote documentation" (R-UI-060, R-UI-061, R-UI-064, R-ANS-046): the Documentation tab of a collection that a
// remote repository took from the counting Galaxy test upstream, whose README holds script. Run by TestAnsibleDocsRemoteScreens.

function env(name: string): string {
  const v = process.env[name];
  if (!v) throw new Error(`${name} is not set: run the journey through TestAnsibleDocsRemoteScreens`);
  return v;
}

const remote = { name: env('TROVE_E2E_ANSIBLE_DOCS_REMOTE_PACKAGE'), version: env('TROVE_E2E_ANSIBLE_DOCS_REMOTE_VERSION') };

function pageUrl(p: { name: string; version: string }): string {
  return `/ui/en/projects/${e2eProject}/packages/ansible/${p.name}?version=${p.version}`;
}

const tab = (page: Page, name: string) => page.getByRole('tab', { name, exact: false });
const treeRow = (page: Page, name: RegExp | string) => page.getByRole('treeitem', { name });
const details = (page: Page) => page.locator('.pane-details');

async function bothSchemes(page: Page, screen: string): Promise<void> {
  for (const scheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme: scheme });
    await expectAccessible(page, `${screen} (${scheme})`);
  }
  await page.emulateMedia({ colorScheme: 'light' });
}

test('R-UI-060 R-UI-061 R-UI-064 R-ANS-046 a collection a remote repository took from the upstream: its documentation without script, no Import log, a filter without documentation and a role', async ({
  page,
}) => {
  await signInLocal(page);
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto(pageUrl(remote));

  await expect(tab(page, 'Documentation')).toHaveAttribute('aria-selected', 'true');
  await expect(tab(page, 'Contents').first()).toBeVisible();
  await expect(page.getByRole('tab', { name: 'Import log' })).toHaveCount(0);

  await test.step('the README of the upstream holds text, and no script, event handler or javascript: link', async () => {
    await expect(details(page)).toContainText(remote.name);
    await expect(page.locator('[data-testid="docs-source"]')).toContainText('Taken from the upstream');
    await expect(details(page).locator('script')).toHaveCount(0);
    await expect(details(page).locator('[onerror]')).toHaveCount(0);
    await expect(details(page).locator('a[href^="javascript"]')).toHaveCount(0);
    await bothSchemes(page, 'remote documentation, README');
  });

  await test.step('a filter without documentation is muted and its page says so', async () => {
    await treeRow(page, /^Plugins/).click();
    await treeRow(page, /^filter/).click();
    await expect(treeRow(page, /is_cidr/)).toContainText('no documentation');
    await treeRow(page, /is_cidr/).click();
    await expect(page.getByTestId('doc-no-docs')).toHaveText('The upstream sent no documentation for this plugin.');
  });

  await test.step('the role page is its README, with no script', async () => {
    await treeRow(page, /^Roles/).click();
    await treeRow(page, /webserver/).click();
    await expect(details(page).locator('h1')).toHaveText('webserver');
    await expect(page.locator('#doc-parameters')).toHaveCount(0);
    await expect(details(page).locator('script')).toHaveCount(0);
    await expect(details(page).locator('a[href^="javascript"]')).toHaveCount(0);
  });

  await test.step('the module of the upstream has its parameters', async () => {
    await treeRow(page, /^Modules/).click();
    await treeRow(page, /ping_host/).click();
    await expect(page.getByRole('table', { name: 'Parameters of this entry' }).locator('th code', { hasText: /^host$/ })).toBeVisible();
    await bothSchemes(page, 'remote documentation, module page');
  });

  await test.step('the Metadata tab has what the upstream\'s version document carries: Requires Ansible, the licence, the authors, the links and the dependencies', async () => {
    await tab(page, 'Metadata').click();
    const facts = page.getByTestId('ansible-facts');
    await expect(facts).toContainText('Requires Ansible');
    await expect(facts).toContainText('>=2.15.0');
    await expect(facts).toContainText('GPL-3.0-or-later');
    await expect(page.locator('.pkg-tabs')).toContainText('trove test');
    await expect(page.getByRole('link', { name: `https://example.invalid/acme/tools/docs` })).toBeVisible();
    await expect(page.locator('.pkg-tabs dt', { hasText: 'Docs site' })).toBeVisible();
    // the dependency is a collection this installation holds (the install pulled it), so it links to its page
    const dependency = page.locator('.pkg-tabs dl.facts dt', { hasText: /^\s*acme\.base\s*$/ });
    await expect(dependency).toBeVisible();
    await expect(dependency.locator('a')).toHaveAttribute('href', /\/packages\/ansible\/acme\.base$/);
    await expect(dependency.locator('xpath=following-sibling::dd[1]')).toHaveText('>=1.0.0');
    await bothSchemes(page, 'remote collection, metadata');
  });

  await signOut(page);
});

