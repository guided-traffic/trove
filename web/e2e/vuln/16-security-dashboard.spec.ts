import { APIRequestContext } from '@playwright/test';
import { localNpmRepository, npmPublishBody } from '../npm-publish';
import { bobPassword, e2eProject, expect, expectAccessible, qualified, repositories, repositoryApi, signInLocal, signInWithKeycloak, test, unique } from '../support';

// R-UI-047 journey "security dashboard" (Q-UI-130, R-VULN-020, R-VULN-021, R-VULN-022): the auditor's instance-wide view in
// the admin area over two projects, against the test advisory source (deploy/dev/osvstub.yaml). It creates its own second
// project and local npm repositories and publishes the fixture packages through the API (INV-17); the findings come from the
// commit hook of the publish. Run with `npx playwright test -c playwright.vuln.config.ts` and TROVE_E2E_VULN=1 in the
// `scatter` profile (R-HA-011).

test.beforeAll(() => {
  if (!process.env['TROVE_E2E_VULN']) {
    throw new Error('TROVE_E2E_VULN is not set: the journeys need the scanning against the test advisory source (deploy/dev/osvstub.yaml)');
  }
});

interface Finding {
  readonly id: string;
  readonly project: string;
  readonly repository: string;
  readonly package: string;
  readonly advisory: string;
  readonly aliases: readonly string[];
  readonly status: string;
}

/** A local npm repository in `project`, with the packages published into it. */
async function localNpmRepositoryIn(admin: APIRequestContext, project: string, key: string, packages: readonly (readonly [string, string])[]): Promise<void> {
  const created = await admin.post(repositories(project), { data: { key, class: 'local', format: 'npm', vulnerabilities: { matchPublicAdvisories: 'always' } } });
  expect(created.status(), await created.text()).toBe(201);
  for (const [name, version] of packages) {
    const published = await admin.put(`/api/npm/${project}/${key}/${name}`, { data: npmPublishBody(key, name, version) });
    expect(published.status(), `publish ${name}@${version}: ${await published.text()}`).toBe(201);
  }
}

/** Waits until the findings API shows `count` findings of the project, which the commit hook of the publish wrote. */
async function findingsOfProject(admin: APIRequestContext, project: string, count: number): Promise<Finding[]> {
  let items: Finding[] = [];
  await expect
    .poll(
      async () => {
        const res = await admin.get(`/api/v1/vulnerabilities/findings?project=${project}`);
        items = ((await res.json()) as { items: Finding[] }).items;
        return items.length;
      },
      { message: `findings of project ${project}`, timeout: 60_000 },
    )
    .toBe(count);
  return items;
}

test('Q-UI-130 the auditor searches every project by alias, package and project, filters by project, and the counts follow', async ({
  page,
  admin,
  replicas,
}) => {
  const shop = unique('sec');
  const repoA = unique('sec-a');
  const repoB = unique('sec-b');
  const created = await admin.post('/api/v1/projects', { data: { key: shop, displayName: 'Security journey' } });
  expect(created.status(), await created.text()).toBe(201);
  try {
    // trove-test-alias 2.0.0 carries GHSA-aaaa-bbbb-cccc with the alias CVE-2026-0001 (test/osvstub/fixture.go)
    await localNpmRepository(admin, repoA, [['trove-test-alias', '2.0.0']]);
    await localNpmRepositoryIn(admin, shop, repoB, [
      ['trove-test-alias', '2.0.0'],
      ['trove-test-cvss', '1.0.0'],
    ]);
    const mine = await findingsOfProject(admin, shop, 2);
    const aliasFinding = mine.find((f) => f.package === 'trove-test-alias')!;
    expect(aliasFinding.aliases, 'the aliases of the finding').toContain('CVE-2026-0001');
    await signInLocal(page);

    await test.step('the dashboard is the admin area\'s Security entry and shows the findings of both projects', async () => {
      await page.goto('/ui/en/admin/security?package=trove-test-alias');
      await expect(page.getByRole('heading', { name: 'Security dashboard', level: 1 })).toBeVisible();
      await expect(page.getByRole('link', { name: 'Security', exact: true }).first()).toBeVisible();
      const rows = page.locator('trove-data-table tbody tr').filter({ hasText: 'trove-test-alias' });
      await expect(rows.filter({ hasText: shop })).toHaveCount(1);
      await expect(rows.filter({ hasText: e2eProject })).not.toHaveCount(0);
      // Q-UI-380: the project is the link of each qualified repository name, not a column of its own.
      await expect(page.getByRole('columnheader', { name: 'Project' })).toHaveCount(0);
      await expect(rows.filter({ hasText: shop }).locator('td.qualified-name').getByRole('link', { name: shop })).toBeVisible();
      for (const scheme of ['light', 'dark'] as const) {
        await page.emulateMedia({ colorScheme: scheme });
        await expectAccessible(page, `the security dashboard (${scheme})`);
      }
      await page.emulateMedia({ colorScheme: 'light' });
    });

    await test.step('a search by an alias, typed in lower case, finds the finding of both projects', async () => {
      await page.goto('/ui/en/admin/security');
      const search = page.getByRole('search', { name: 'Search findings' });
      await search.getByLabel('Advisory ID').fill('cve-2026-0001');
      await search.getByRole('button', { name: 'Search' }).click();
      await expect(page).toHaveURL(/advisory=CVE-2026-0001/);
      await expect(page.getByRole('list', { name: 'Applied filters' })).toContainText('Advisory: CVE-2026-0001');
      const rows = page.locator('trove-data-table tbody tr');
      await expect(rows.filter({ hasText: shop })).toHaveCount(1);
      await expect(rows.filter({ hasText: qualified(repoA) })).toHaveCount(1);
      await expect(rows.filter({ hasText: 'GHSA-aaaa-bbbb-cccc' })).not.toHaveCount(0);
      await expect(rows.filter({ hasText: 'trove-test-cvss' })).toHaveCount(0);
    });

    await test.step('the project field narrows the list, the counts and the URL to one project', async () => {
      const search = page.getByRole('search', { name: 'Search findings' });
      await search.getByLabel('Project key').fill(shop);
      await search.getByRole('button', { name: 'Search' }).click();
      await expect(page).toHaveURL(new RegExp(`project=${shop}`));
      const rows = page.locator('trove-data-table tbody tr');
      await expect(rows).toHaveCount(1);
      await expect(rows.first()).toContainText(qualified(repoB, shop));
      await expect(page.getByRole('list', { name: 'Applied filters' })).toContainText(`Project: ${shop}`);
    });

    await test.step('the package search with the project alone lists that project\'s findings, and the counts cover that project', async () => {
      await page.goto(`/ui/en/admin/security?project=${shop}`);
      const rows = page.locator('trove-data-table tbody tr');
      await expect(rows).toHaveCount(2);
      for (const row of await rows.all()) await expect(row).toContainText(shop);
      const search = page.getByRole('search', { name: 'Search findings' });
      await search.getByLabel('Package starts with').fill('trove-test-cvss');
      await search.getByRole('button', { name: 'Search' }).click();
      await expect(rows).toHaveCount(1);
      await expect(rows.first()).toContainText('trove-test-cvss');
      // the total of the counts is the number of open findings of the project
      await page.goto(`/ui/en/admin/security?project=${shop}`);
      const counts = page.getByRole('list', { name: 'Open findings' });
      await expect(counts.getByRole('link', { name: /^2\s*Needs triage/ })).toBeVisible();
    });

    await test.step('a project cell narrows the list to its project, and a finding opens inside the admin area', async () => {
      await page.goto('/ui/en/admin/security?package=trove-test-alias');
      await page.locator('trove-data-table tbody tr').filter({ hasText: shop }).getByRole('link', { name: shop, exact: true }).click();
      await expect(page).toHaveURL(new RegExp(`project=${shop}`));
      await page.getByRole('link', { name: 'GHSA-aaaa-bbbb-cccc' }).first().click();
      await expect(page).toHaveURL(/\/admin\/security\/findings\//);
      await expect(page.getByRole('heading', { name: 'Finding', level: 1 })).toBeVisible();
      await expect(page.getByRole('link', { name: 'All findings' })).toHaveAttribute('href', /\/admin\/security$/);
      await expectAccessible(page, 'a finding opened from the dashboard');
    });
    expect(replicas.size, `replicas that answered: ${[...replicas]}`).toBeGreaterThanOrEqual(2);
  } finally {
    await admin.delete(repositoryApi(repoA));
    await admin.delete(repositoryApi(repoB, shop));
    await admin.delete(`/api/v1/projects/${shop}`);
  }
});

test('Q-UI-130 R-VULN-013 an auditor who is a member of no project reads the findings of every project and triages one from the dashboard', async ({
  browser,
  admin,
}) => {
  const shop = unique('sec');
  const repoB = unique('sec-c');
  const created = await admin.post('/api/v1/projects', { data: { key: shop, displayName: 'Security journey' } });
  expect(created.status(), await created.text()).toBe(201);
  const bobContext = await browser.newContext({ ignoreHTTPSErrors: true, locale: 'en-US' });
  try {
    await localNpmRepositoryIn(admin, shop, repoB, [['trove-test-cvss', '1.0.0']]);
    await findingsOfProject(admin, shop, 1);
    const bob = await bobContext.newPage();
    await signInWithKeycloak(bob, 'bob', bobPassword);
    // bob is created at his first sign-in; the auditor role reads every repository's metadata and triages
    const bound = await admin.put('/api/v1/role-bindings/auditor/user%3Abob');
    expect(bound.status(), await bound.text()).toBeLessThan(300);

    await test.step('the admin area starts at the dashboard for him and lists the project he is no member of', async () => {
      await bob.goto('/ui/en/admin');
      await expect(bob.getByRole('heading', { name: /Security dashboard|Dashboard/, level: 1 })).toBeVisible();
      await bob.goto(`/ui/en/admin/security?project=${shop}`);
      const rows = bob.locator('trove-data-table tbody tr');
      await expect(rows).toHaveCount(1);
      await expect(rows.first()).toContainText('trove-test-cvss');
      await expectAccessible(bob, 'the dashboard of an auditor');
    });

    await test.step('he confirms the finding, and the list and the counts follow', async () => {
      await bob.getByRole('button', { name: 'Select all loaded findings' }).click();
      await bob.getByRole('button', { name: 'Change status' }).click();
      const dialog = bob.getByRole('dialog');
      await dialog.getByRole('button', { name: 'Save' }).click();
      await expect(dialog).toContainText('1 changed, 0 failed, 0 unchanged');
      await dialog.getByRole('button', { name: 'Close' }).click();
      await bob.goto(`/ui/en/admin/security?project=${shop}&status=confirmed`);
      await expect(bob.locator('trove-data-table tbody tr')).toHaveCount(1);
      await expect(bob.locator('trove-data-table tbody tr').first()).toContainText('Confirmed');
    });
  } finally {
    await bobContext.close();
    await admin.delete('/api/v1/role-bindings/auditor/user%3Abob');
    await admin.delete(repositoryApi(repoB, shop));
    await admin.delete(`/api/v1/projects/${shop}`);
  }
});
