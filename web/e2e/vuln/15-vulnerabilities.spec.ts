import { readFile } from 'node:fs/promises';
import { APIRequestContext } from '@playwright/test';
import { localNpmRepository } from '../npm-publish';
import { bobPassword, e2eProject, expect, expectAccessible, qualified, repositories, repositoryApi, signInLocal, signInWithKeycloak, test, unique } from '../support';

// R-UI-047 journeys "vulnerabilities" (R-VULN-022, R-VULN-023, R-VULN-029): the report, the finding, the triage and the
// package page against the test advisory source (deploy/dev/osvstub.yaml). They create their own local npm repositories and
// publish the fixture packages into them through the API (INV-17); the findings come from the commit hook of the
// publish, so each journey waits for them through the findings API before it opens the UI. Run by the e2e wave in the
// `scatter` profile with `npx playwright test -c playwright.vuln.config.ts` and TROVE_E2E_VULN=1; the three replicas answer
// at random, so a journey that passes has read its own writes on another replica (R-HA-011).

test.beforeAll(() => {
  if (!process.env['TROVE_E2E_VULN']) {
    throw new Error('TROVE_E2E_VULN is not set: the journeys need the scanning against the test advisory source (deploy/dev/osvstub.yaml)');
  }
});

interface Finding {
  readonly id: string;
  readonly severity: string;
  readonly advisory: string;
  readonly status: string;
}

const SEVERITY = { critical: 'Critical', high: 'High', medium: 'Medium', low: 'Low', unknown: 'Unknown', none: 'None' } as const;

/** Waits until the findings API shows `count` findings of the repository, which the commit hook of the publish wrote. */
async function findingsOf(admin: APIRequestContext, repo: string, count: number): Promise<Finding[]> {
  let items: Finding[] = [];
  await expect
    .poll(
      async () => {
        const res = await admin.get(`/api/v1/vulnerabilities/findings?repository=${qualified(repo)}`);
        items = ((await res.json()) as { items: Finding[] }).items;
        return items.length;
      },
      { message: `findings of ${repo}`, timeout: 60_000 },
    )
    .toBe(count);
  return items;
}

test('R-VULN-022 the Critical-style count filters the list, the sort and the finding keep the filter in the URL, and a dismissal lowers the count', async ({
  page,
  admin,
  replicas,
}) => {
  const repo = unique('vuln-a');
  await localNpmRepository(admin, repo, [['trove-test-vuln', '1.0.0']]);
  try {
    const [finding] = await findingsOf(admin, repo, 1);
    const label = SEVERITY[finding!.severity as keyof typeof SEVERITY];
    await signInLocal(page);

    await page.goto(`/ui/en/projects/${e2eProject}/vulnerabilities?repository=${qualified(repo)}`);
    await expect(page.getByRole('heading', { name: 'Vulnerabilities', level: 1 })).toBeVisible();
    const counts = page.getByRole('list', { name: 'Open findings' });
    const count = counts.getByRole('link', { name: new RegExp(`^1\\s*${label}`) });
    await expect(count).toBeVisible();
    for (const scheme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme: scheme });
      await expectAccessible(page, `the vulnerability report (${scheme})`);
    }
    await page.emulateMedia({ colorScheme: 'light' });

    await test.step('the count applies its filter and the URL holds it', async () => {
      await count.click();
      await expect(page).toHaveURL(new RegExp(`severity=${finding!.severity}`));
      await expect(page).toHaveURL(new RegExp(`repository=${e2eProject}(%2F|/)${repo}`));
      await expect(page.getByRole('row', { name: new RegExp(finding!.advisory) })).toBeVisible();
      await expect(page.getByRole('list', { name: 'Applied filters' })).toContainText(`Severity: ${label}`);
    });

    await test.step('sorting by detection keeps the filter', async () => {
      await page.locator('th').getByRole('button', { name: 'Detected' }).click();
      await expect(page).toHaveURL(/sort=detectedAt/);
      await expect(page).toHaveURL(new RegExp(`severity=${finding!.severity}`));
    });

    await test.step('the finding opens and is dismissed with a reason', async () => {
      await page.getByRole('link', { name: finding!.advisory }).first().click();
      await expect(page).toHaveURL(/\/vulnerabilities\/findings\//);
      await expect(page.getByRole('heading', { name: 'Finding', level: 1 })).toBeVisible();
      await expect(page.getByText('GitHub Advisory Database').first()).toBeVisible();
      await expectAccessible(page, 'a finding');
      await page.getByRole('button', { name: 'Dismiss' }).click();
      const dialog = page.getByRole('dialog');
      await dialog.getByLabel('Reason').selectOption('not_exploitable');
      await dialog.getByLabel('Comment').fill('The vulnerable function is not reachable from our code.');
      await expectAccessible(page, 'the dismiss dialog');
      await dialog.getByRole('button', { name: 'Dismiss', exact: true }).click();
      await expect(page.getByRole('definition').filter({ hasText: 'Not exploitable' })).toBeVisible();
      await expect(page.getByRole('cell', { name: 'Dismissed' }).first()).toBeVisible();
    });

    await test.step('back at the report the filter is kept and the count is one lower', async () => {
      await page.goBack();
      await expect(page).toHaveURL(new RegExp(`severity=${finding!.severity}`));
      await expect(counts.getByRole('link', { name: new RegExp(`^0\\s*${label}`) })).toBeVisible();
      await expect(page.getByText('No finding matches the filters.')).toBeVisible();
    });
    expect(replicas.size, `replicas that answered: ${[...replicas]}`).toBeGreaterThanOrEqual(2);
  } finally {
    await admin.delete(repositoryApi(repo));
  }
});

test('R-VULN-022 three findings are confirmed at once, the CSV holds the rows of the list, the page does not scroll sideways at 390 px, and a reader sees no selection', async ({
  page,
  browser,
  admin,
}) => {
  const repo = unique('vuln-b');
  const policy = unique('vuln-b-read');
  await localNpmRepository(admin, repo, [
    ['trove-test-vuln', '1.0.0'],
    ['trove-test-alias', '2.0.0'],
    ['left-pad', '1.3.0'],
  ]);
  const bobContext = await browser.newContext({ ignoreHTTPSErrors: true, locale: 'en-US' });
  try {
    await findingsOf(admin, repo, 3);
    // bob is created at his first sign-in, then gets read access to the repository and nothing else
    const bob = await bobContext.newPage();
    await signInWithKeycloak(bob, 'bob', bobPassword);
    const granted = await admin.post('/api/v1/permission-policies', {
      data: { name: policy, description: 'browser journey', resources: { repositories: [qualified(repo)] }, entries: [{ principal: 'user:bob', actions: ['read'] }] },
    });
    expect(granted.status(), await granted.text()).toBe(201);

    await signInLocal(page);
    await page.goto(`/ui/en/projects/${e2eProject}/vulnerabilities?repository=${qualified(repo)}&status=needs_triage`);
    const rows = page.locator('trove-data-table tbody tr');
    await expect(rows).toHaveCount(3);

    await test.step('select the three rows and confirm them', async () => {
      await page.getByRole('button', { name: 'Select all loaded findings' }).click();
      await expect(page.getByRole('status').filter({ hasText: '3 selected' })).toBeVisible();
      await page.getByRole('button', { name: 'Change status' }).click();
      const dialog = page.getByRole('dialog');
      await expect(dialog).toContainText('3 findings are selected');
      await expectAccessible(page, 'the bulk dialog');
      await dialog.getByRole('button', { name: 'Save' }).click();
      await expect(dialog).toContainText('3 changed, 0 failed, 0 unchanged');
      await dialog.getByRole('button', { name: 'Close' }).click();
      await expect(page.getByText('No finding matches the filters.')).toBeVisible();
    });

    await test.step('three rows show confirmed, and the CSV has the same rows', async () => {
      await page.goto(`/ui/en/projects/${e2eProject}/vulnerabilities?repository=${qualified(repo)}&status=confirmed`);
      await expect(rows).toHaveCount(3);
      for (const row of await rows.all()) await expect(row).toContainText('Confirmed');
      const download = page.waitForEvent('download');
      await page.getByRole('button', { name: 'Export CSV' }).click();
      const file = await (await download).path();
      const csv = (await readFile(file, 'utf8')).trim().split('\n');
      expect(csv[0]).toContain('findingId');
      expect(csv.length - 1).toBe(3);
    });

    await test.step('at 390 CSS px the counts and the list are usable without scrolling the page sideways', async () => {
      await page.setViewportSize({ width: 390, height: 844 });
      await page.reload();
      await expect(page.getByRole('list', { name: 'Open findings' })).toBeVisible();
      await expect(rows.first()).toBeVisible();
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
      expect(overflow, 'horizontal overflow of the page in CSS px').toBeLessThanOrEqual(0);
      await page.setViewportSize({ width: 1280, height: 800 });
    });

    await test.step('a reader without the triage capability sees the findings and no selection column', async () => {
      await bob.goto(`/ui/en/projects/${e2eProject}/vulnerabilities?repository=${qualified(repo)}`);
      await expect(bob.locator('trove-data-table tbody tr')).toHaveCount(3);
      await expect(bob.getByRole('button', { name: 'Select all loaded findings' })).toHaveCount(0);
      await expect(bob.getByRole('columnheader', { name: 'Select' })).toHaveCount(0);
      await expect(bob.getByRole('link', { name: 'Advisory sources' })).toHaveCount(0);
      await expectAccessible(bob, 'the report of a reader');
    });
  } finally {
    await bobContext.close();
    await admin.delete(`/api/v1/permission-policies/${policy}`);
    await admin.delete(repositoryApi(repo));
  }
});

test('R-VULN-023 R-VULN-022 the package page shows the finding on the affected version only; blocking it refuses downloads, shows "Blocked in" and lifting it reverts all three', async ({
  page,
  admin,
}) => {
  // trove-test-cvss: the other journeys and the client tests cache trove-test-vuln in npm-remote, which would be a second repository here
  const repo = unique('vuln-c');
  await localNpmRepository(admin, repo, [
    ['trove-test-cvss', '1.0.0'],
    ['trove-test-cvss', '1.0.1'],
  ]);
  const tarball = `/api/npm/${e2eProject}/${repo}/trove-test-cvss/-/trove-test-cvss-1.0.0.tgz`;
  try {
    const [finding] = await findingsOf(admin, repo, 1);
    expect((await admin.get(tarball)).status(), 'download before the block').toBe(200);
    await signInLocal(page);
    const packagePage = (version: string, tab: string) => `/ui/en/projects/${e2eProject}/packages/npm/trove-test-cvss?version=${version}&tab=${tab}`;

    await test.step('the tab shows one finding on 1.0.0 and none on 1.0.1', async () => {
      await page.goto(packagePage('1.0.0', 'vulnerabilities'));
      await expect(page.getByRole('tab', { name: 'Vulnerabilities (1)' })).toBeVisible();
      const panel = page.locator('trove-package-vulnerabilities');
      await expect(panel.getByRole('heading', { name: repo })).toBeVisible();
      await expect(panel.getByRole('link', { name: finding!.advisory })).toBeVisible();
      await expectAccessible(page, 'the package page, Vulnerabilities tab');
      await page.goto(packagePage('1.0.1', 'vulnerabilities'));
      await expect(page.locator('trove-package-vulnerabilities')).toContainText('No known vulnerabilities.');
    });

    await test.step('All versions shows a severity badge on 1.0.0 only', async () => {
      await page.goto(packagePage('1.0.0', 'versions'));
      const row = (v: string) => page.getByRole('row', { name: new RegExp(`^${v.replace('.', '\\.')}`) });
      await expect(row('1.0.0').locator('trove-severity')).toBeVisible();
      await expect(row('1.0.1').locator('trove-severity')).toHaveCount(0);
    });

    await test.step('confirm the finding, then block downloads', async () => {
      await page.goto(`/ui/en/projects/${e2eProject}/vulnerabilities/findings/${finding!.id}`);
      await page.getByRole('button', { name: 'Confirm' }).click();
      await page.getByRole('dialog').getByRole('button', { name: 'Save' }).click();
      await expect(page.getByRole('button', { name: 'Block downloads' })).toBeVisible();
      await page.getByRole('button', { name: 'Block downloads' }).click();
      const dialog = page.getByRole('dialog');
      await expect(dialog).toContainText(`Every client of ${qualified(repo)}`);
      await expectAccessible(page, 'the block dialog');
      await dialog.getByRole('button', { name: 'Block downloads' }).click();
      await expect(page.locator('.vuln-blocked')).toContainText(`Downloads of trove-test-cvss@1.0.0 are blocked in ${qualified(repo)}.`);
      await expect(page.locator('.vuln-blocked')).toContainText('blocked in triage');
    });

    await test.step('the row, the count, the package page and the download show the block', async () => {
      await page.goto(`/ui/en/projects/${e2eProject}/vulnerabilities?repository=${qualified(repo)}`);
      await expect(page.getByRole('row', { name: new RegExp(finding!.advisory) })).toContainText('Blocked (blocked in triage)');
      await expect(page.getByRole('list', { name: 'Open findings' }).getByRole('link', { name: /^1\s*Blocked versions/ })).toBeVisible();
      await page.goto(packagePage('1.0.0', 'vulnerabilities'));
      await expect(page.locator('.pkg-header .banner-warning')).toContainText(`Blocked in ${qualified(repo)}`);
      await expect(page.locator('trove-package-vulnerabilities .banner-warning')).toContainText('blocked in triage');
      expect((await admin.get(tarball)).status(), 'download after the block').toBe(403);
    });

    await test.step('lifting the block reverts all three', async () => {
      await page.goto(`/ui/en/projects/${e2eProject}/vulnerabilities/findings/${finding!.id}`);
      await page.getByRole('button', { name: 'Lift block' }).click();
      await page.getByRole('dialog').getByRole('button', { name: 'Save' }).click();
      await expect(page.locator('.vuln-blocked')).toHaveCount(0);
      await page.goto(packagePage('1.0.0', 'vulnerabilities'));
      await expect(page.locator('.pkg-header .banner-warning')).toHaveCount(0);
      await page.goto(`/ui/en/projects/${e2eProject}/vulnerabilities?repository=${qualified(repo)}`);
      await expect(page.getByRole('list', { name: 'Open findings' }).getByRole('link', { name: /^0\s*Blocked versions/ })).toBeVisible();
      expect((await admin.get(tarball)).status(), 'download after the block is lifted').toBe(200);
    });
  } finally {
    await admin.delete(repositoryApi(repo));
  }
});

test('R-VULN-022 the sources screen shows the state of the test source, and the advisory page lists where an advisory is used', async ({ page, admin }) => {
  const repo = unique('vuln-d');
  await localNpmRepository(admin, repo, [['trove-test-cvss', '1.0.0']]);
  try {
    const [finding] = await findingsOf(admin, repo, 1);
    await signInLocal(page);
    await page.goto('/ui/en/admin/advisory-sources');
    await expect(page.getByRole('heading', { name: 'Advisory sources', level: 1 })).toBeVisible();
    await expect(page.locator('trove-data-table tbody tr').filter({ hasText: 'OSV' }).first()).toContainText('Online');
    await expectAccessible(page, 'the advisory sources');

    await page.goto(`/ui/en/projects/${e2eProject}/vulnerabilities/advisories/npm/${finding!.advisory}`);
    await expect(page.getByRole('heading', { name: 'Advisory', level: 1 })).toBeVisible();
    await expect(page.getByRole('row', { name: new RegExp(repo) }).or(page.getByRole('row', { name: /trove-test-cvss/ })).first()).toBeVisible();
    await expectAccessible(page, 'an advisory');
  } finally {
    await admin.delete(repositoryApi(repo));
  }
});
