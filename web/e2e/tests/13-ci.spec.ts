import { bobPassword, expect, expectAccessible, openFilter, qualified, repositories, repositoryApi, signInLocal, signInWithKeycloak, test, unique, watchCsp } from '../support';

// R-UI-047 journey "ci" (R-UI-035, R-UI-036, R-CI-019, R-CI-020, Q-CI-26): against the real backend and the issuer double of
// test/oidcstub, an administrator registers the double as a CI issuer (with the CA of its certificate), creates the reference
// mapping of R-CI-028 in the editor, tests a claim set in the claim tester (a pasted ID token is decoded in the browser),
// reads the issuance of an exchange and revokes it; an Owner of a project manages a mapping of its project. Every screen is
// checked with axe in both colour schemes.
//
// TestE1_11Journeys mints the two ID tokens (a protected tag of acme/billing/invoice-api, as docs/spec/gitlab-ci-oidc.md writes
// it) and passes them with the issuer URL: TROVE_E2E_OIDCSTUB_ISSUER, TROVE_E2E_CI_TOKEN_PASTE, TROVE_E2E_CI_TOKEN_EXCHANGE.

function env(name: string): string {
  const v = process.env[name];
  if (!v) throw new Error(`${name} is not set; the journey runs through TestE1_11Journeys`);
  return v;
}

const issuerUrl = env('TROVE_E2E_OIDCSTUB_ISSUER');
const pasteToken = env('TROVE_E2E_CI_TOKEN_PASTE');
const exchangeToken = env('TROVE_E2E_CI_TOKEN_EXCHANGE');
const caRef = 'file:/var/run/secrets/trove-dev/keycloak-ca.crt'; // the dev CA, mounted into the replicas (deploy/dev/values-dev.yaml)

const run = unique('ui-ci')
  .replace(/[^a-z0-9-]/g, '')
  .slice(0, 20);
const issuerName = `${run}-gl`;
const npmRepo = `${run}-npm`;
const nugetRepo = `${run}-nuget`;
const policy = `${run}-read`;
const projectKey = `${run}-prj`;
const projectRepo = `${run}-prj-repo`;
const projectGroup = `${run}-leads`;
const projectMapping = `${run}-publish`; // a project mapping's name is unique within the project (R-CI-009)

// The condition of the reference mapping (docs/spec/gitlab-ci-oidc.md, Examples).
const match = {
  all: [
    { claim: 'namespace_id', equals: '4711' },
    { claim: 'ref_type', equals: 'tag' },
    { claim: 'ref_protected', equals: true },
    { claim: 'pipeline_source', in: ['push', 'web'] },
  ],
};

async function deleteIssuersOf(admin: import('@playwright/test').APIRequestContext): Promise<void> {
  // An issuer URL is unique: an issuer of the reference setup of the end-to-end tests (or of an earlier run) holds it.
  const list = await admin.get('/api/v1/ci/issuers');
  expect(list.ok(), await list.text()).toBeTruthy();
  for (const i of ((await list.json()) as { items: { name: string; issuer: string }[] }).items) {
    if (i.issuer === issuerUrl) {
      const gone = await admin.delete(`/api/v1/ci/issuers/${i.name}?cascade=true`);
      expect([204, 404], await gone.text()).toContain(gone.status());
    }
  }
}

test.beforeAll(async ({ admin }) => {
  await deleteIssuersOf(admin);
  for (const [key, format] of [
    [npmRepo, 'npm'],
    [nugetRepo, 'nuget'],
  ] as const) {
    const r = await admin.post(repositories(), { data: { key, class: 'local', format } });
    expect(r.status(), await r.text()).toBe(201);
  }
  const p = await admin.post('/api/v1/permission-policies', {
    data: {
      name: policy,
      description: 'journey',
      resources: { repositories: [qualified(npmRepo), qualified(nugetRepo)] },
      entries: [],
    },
  });
  expect(p.status(), await p.text()).toBe(201);
});

test.afterAll(async ({ admin }) => {
  await admin.delete(`/api/v1/ci/issuers/${issuerName}?cascade=true`);
  await admin.delete(repositoryApi(projectRepo, projectKey));
  await admin.delete(`/api/v1/projects/${projectKey}`);
  await admin.delete(`/api/v1/groups/${projectGroup}`);
  await admin.delete(`/api/v1/permission-policies/${policy}`);
  for (const key of [npmRepo, nugetRepo])
    await admin.delete(repositoryApi(key));
});

/** axe on the screen in both colour schemes; the page ends in the light scheme. */
async function accessible(page: import('@playwright/test').Page, screen: string): Promise<void> {
  // The focus leaves the field first: a focused field's label is mid-transition for a moment after the scheme changes, and axe
  // would measure a blended colour.
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  await page.mouse.move(2, 2); // no field under the pointer: a hovered field has a darker background
  for (const scheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme: scheme });
    await page.waitForTimeout(600); // the colour transitions of the switch end before axe measures
    await expectAccessible(page, `${screen} (${scheme})`);
  }
  await page.emulateMedia({ colorScheme: 'light' });
}

test('R-UI-035 R-UI-036 R-CI-019 an administrator registers the issuer, creates the reference mapping, tests a claim set, reads and revokes an issuance; an Owner of a project manages a mapping of its project', async ({
  page,
  browser,
  admin,
  replicas,
}) => {
  // bob signs in first, so that he exists, and joins the group that will administer the project
  const bobContext = await browser.newContext({ ignoreHTTPSErrors: true, locale: 'en-US' });
  const bob = await bobContext.newPage();
  const bobCsp = await watchCsp(bob);
  await signInWithKeycloak(bob, 'bob', bobPassword);
  const group = await admin.post('/api/v1/groups', { data: { name: projectGroup } });
  expect(group.status(), await group.text()).toBe(201);
  const member = await admin.patch(`/api/v1/groups/${projectGroup}/members`, {
    data: { add: ['bob'] },
  });
  expect(member.ok(), await member.text()).toBeTruthy();

  await signInLocal(page);

  await test.step('the issuer list is empty, and the new issuer is registered with the CA of the double', async () => {
    await page.goto('/ui/en/admin/ci');
    await expect(page.getByRole('heading', { name: 'CI identity', level: 1 })).toBeVisible();
    await expect(page.getByText('Not available yet')).toHaveCount(0);
    await accessible(page, 'CI issuers');
    await page.getByRole('link', { name: 'New issuer' }).click();
    await expect(page.getByRole('heading', { name: 'New CI issuer', level: 1 })).toBeVisible();
    await page.getByLabel('Name', { exact: true }).fill(issuerName);
    await page.getByLabel('Issuer URL').fill(issuerUrl);
    await page.getByLabel('Description').fill('browser journey: the issuer double');
    await accessible(page, 'new CI issuer');
    await page.getByText('More settings').click();
    await page.getByLabel('CA bundle').fill(caRef);
    await accessible(page, 'new CI issuer, more settings');
    await page.getByRole('button', { name: 'Create issuer' }).click();
    // the discovery document and the key set were fetched before the save (validate), so the keys tab shows the keys in use
    await expect(page).toHaveURL(new RegExp(`/admin/ci/${issuerName}\\?tab=keys`));
    await expect(page.getByRole('heading', { name: 'Signing keys' })).toBeVisible();
    await expect(page.getByText('In use').first()).toBeVisible();
    await expect(page.getByText('Last error')).toBeVisible();
    await accessible(page, 'issuer keys');
  });

  await test.step('the reference mapping is created in the editor', async () => {
    await page.getByRole('tab', { name: 'Mappings' }).click();
    await page.getByRole('link', { name: 'New mapping' }).click();
    await expect(page.getByRole('heading', { name: 'New mapping', level: 1 })).toBeVisible();
    await page.getByLabel('Name', { exact: true }).fill('acme-release-publish');
    await page
      .getByLabel('Description')
      .fill('Protected tags in acme/billing publish their own packages');
    await page.getByLabel('Priority').fill('100');
    await page.getByRole('button', { name: 'JSON', exact: true }).click();
    await page.getByLabel('Condition as JSON').fill(JSON.stringify(match, null, 2));
    await expect(page.getByText('Reads as:')).toBeVisible();
    await page.getByLabel('Add a policy').selectOption(policy);
    await page.getByRole('button', { name: 'Add inline grant' }).click();
    const grant = page.getByRole('group', { name: /Inline grant 1|grant 1/i }).first();
    await grant.getByLabel('Repositories').fill(qualified(npmRepo));
    await grant.getByLabel('Include').fill('@{{ namespace_path | slug }}/*');
    await grant.getByRole('checkbox', { name: 'read' }).check();
    await grant.getByRole('checkbox', { name: 'deploy' }).check();
    await page.getByLabel('Maximum token lifetime (seconds)').fill('1800');
    await accessible(page, 'new mapping');
    await page.getByRole('button', { name: 'Create mapping' }).click();
    await expect(page).toHaveURL(
      new RegExp(`/admin/ci/${issuerName}/mappings/acme-release-publish$`),
    );
    await expect(page.getByLabel('Name', { exact: true })).toHaveValue('acme-release-publish');
    await accessible(page, 'saved mapping');

    // the CI snippet of the mapping
    await page.getByRole('button', { name: 'CI snippet' }).click();
    await expect(page.locator('pre.snippet')).toBeVisible();
    await accessible(page, 'mapping with the CI snippet');
  });

  await test.step('the claim tester decodes a pasted ID token in the browser and shows the grants', async () => {
    await page.goto(`/ui/en/admin/ci/${issuerName}/test`);
    await expect(page.getByRole('heading', { name: 'Claim tester', level: 1 })).toBeVisible();
    // the token is not sent anywhere: the request carries its claims
    const evaluated = page.waitForRequest(
      (r) => r.url().endsWith(`/api/v1/ci/issuers/${issuerName}/evaluate`) && r.method() === 'POST',
    );
    await page.getByLabel('Claims as JSON, or paste an ID token').fill(pasteToken);
    await expect(
      page.getByText('The pasted token was decoded in your browser and discarded.'),
    ).toBeVisible();
    await page.getByRole('button', { name: 'Evaluate' }).click();
    const body = (await evaluated).postData() ?? '';
    expect(body).not.toContain(pasteToken);
    expect(body).toMatch(/"namespace_id"\s*:\s*"4711"/);
    await expect(page.getByText('Matched:')).toBeVisible();
    await expect(page.getByText('acme-release-publish').first()).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Resulting grants' })).toBeVisible();
    await expect(page.getByText(`@acme-billing/*`).first()).toBeVisible();
    await accessible(page, 'claim tester');
  });

  let tokenId = '';
  await test.step('a job exchanges its ID token; the issuance is read and revoked', async () => {
    // the exchange is public and carries no session: the job's request, made with the ID token the test minted
    const exchange = await page.request.post('/api/v1/auth/oidc/token', {
      form: {
        grant_type: 'urn:ietf:params:oauth:grant-type:token-exchange',
        subject_token_type: 'urn:ietf:params:oauth:token-type:id_token',
        subject_token: exchangeToken,
      },
    });
    const answer = (await exchange.json()) as {
      access_token?: string;
      mappings?: string[];
      reason?: string;
    };
    expect(exchange.status(), JSON.stringify(answer)).toBe(200);
    expect(answer.mappings).toContain('acme-release-publish');
    expect(answer.access_token).toMatch(/^trv_cit_/);

    await page.goto(`/ui/en/admin/ci/${issuerName}?tab=issuances`);
    await expect(page.getByRole('heading', { name: 'Issued tokens' }).first()).toBeVisible();
    const row = page.getByRole('row', { name: /acme-release-publish/ }).first();
    await expect(row).toBeVisible();
    await expect(row.getByText('Active')).toBeVisible();
    await expect(row).toContainText('acme/billing/invoice-api');
    await accessible(page, 'issuances of the issuer');
    await row.getByRole('button', { name: 'Details' }).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog.getByRole('heading', { name: 'Issued token' })).toBeVisible();
    await expect(dialog.getByText('Claims at the time of the exchange')).toBeVisible();
    await expect(dialog).toContainText('v1.4.0');
    tokenId = (await dialog.locator('dt:has-text("Token ID") + dd').innerText()).trim();
    expect(tokenId).toMatch(/^[0-9a-f-]{36}$/);
    await accessible(page, 'issuance detail');
    await dialog.getByRole('button', { name: 'Close' }).click();

    // the token works until it is revoked: the list of the issuances filters to the job and revokes what matches
    const token = answer.access_token!;
    const probe = await page.request.get('/api/v1/tokens/current', {
      headers: { Authorization: `Bearer ${token}` },
    });
    expect(probe.status()).toBe(200);
    const jobId = (
      JSON.parse(Buffer.from(exchangeToken.split('.')[1] ?? '', 'base64url').toString()) as {
        job_id: string;
      }
    ).job_id;
    await page.goto('/ui/en/admin/ci/issuances');
    await openFilter(page, 'Issuer');
    await page
      .getByRole('search', { name: 'Filters' })
      .getByLabel('Issuer', { exact: true })
      .fill(issuerName);
    await openFilter(page, 'Job ID');
    await page.getByRole('search', { name: 'Filters' }).getByLabel('Job ID').fill(jobId);
    await page
      .getByRole('search', { name: 'Filters' })
      .getByRole('button', { name: 'Apply filters' })
      .click();
    await expect(page.getByRole('button', { name: 'Revoke matching tokens' })).toBeEnabled();
    await page.getByRole('button', { name: 'Revoke matching tokens' }).click();
    const confirm = page.getByRole('dialog');
    await expect(confirm.getByRole('heading', { name: /Revoke 1 tokens\?/ })).toBeVisible();
    await accessible(page, 'bulk revoke confirmation');
    await confirm.getByRole('button', { name: /Revoke 1 tokens/ }).click();
    await expect(page.getByText('1 tokens revoked.')).toBeVisible();
    await expect(
      page.getByRole('row', { name: /acme-release-publish/ }).getByText('Revoked'),
    ).toBeVisible();
    // within the bound of R-AUTH-029 the revoked token fails on every replica
    await expect
      .poll(
        async () =>
          (
            await page.request.get('/api/v1/tokens/current', {
              headers: { Authorization: `Bearer ${token}` },
            })
          ).status(),
        { timeout: 5_000, intervals: [200] },
      )
      .toBe(401);
    await accessible(page, 'issuances after the revocation');
  });

  await test.step('an Owner manages a mapping of the project', async () => {
    const created = await admin.post('/api/v1/projects', {
      data: {
        key: projectKey,
        displayName: 'CI journey project',
      },
    });
    expect(created.status(), await created.text()).toBe(201);
    // The repository is created in the project (R-REPO-001); the group becomes its Owner (Q-AUTHZ-207).
    const repo = await admin.post(repositories(projectKey), { data: { key: projectRepo, class: 'local', format: 'generic' } });
    expect(repo.status(), await repo.text()).toBe(201);
    const owner = await admin.put(`/api/v1/projects/${projectKey}/members/group%3A${projectGroup}`, { data: { role: 'owner' } });
    expect(owner.ok(), await owner.text()).toBeTruthy();
    // the grant reaches every replica within the bound of R-AUTHZ-028
    await expect
      .poll(
        async () => {
          const me = (await (await bob.request.get('/api/v1/authz/me')).json()) as {
            projects?: { key: string }[];
          };
          return (me.projects ?? []).some((p) => p.key === projectKey);
        },
        { timeout: 30_000, intervals: [500] },
      )
      .toBe(true);

    await bob.goto(`/ui/en/projects/${projectKey}/ci`);
    await expect(bob.getByRole('heading', { name: 'CI identity', level: 1 })).toBeVisible();
    await expect(bob.getByText('Not available yet')).toHaveCount(0);
    await expect(bob.getByText('Issuers you can use')).toBeVisible();
    await expect(bob.getByRole('rowheader', { name: issuerName })).toBeVisible();
    await accessible(bob, 'project CI overview');

    await bob.getByRole('link', { name: 'New mapping' }).click();
    await expect(bob.getByRole('heading', { name: 'New mapping', level: 1 })).toBeVisible();
    await bob.getByLabel('Issuer', { exact: true }).selectOption(issuerName);
    await bob.getByLabel('Name', { exact: true }).fill(projectMapping);
    await bob.getByLabel('Priority').fill('300');
    await bob.getByRole('button', { name: 'JSON', exact: true }).click();
    await bob.getByLabel('Condition as JSON').fill(
      JSON.stringify({
        all: [
          { claim: 'project_id', equals: '1234' },
          { claim: 'ref_protected', equals: true },
        ],
      }),
    );
    await bob.getByRole('button', { name: 'Add inline grant' }).click();
    const grant = bob.getByRole('group', { name: /Inline grant 1|grant 1/i }).first();
    await grant.getByRole('button', { name: projectRepo }).click();
    await grant.getByLabel('Include').fill('journey-*');
    await grant.getByRole('checkbox', { name: 'read' }).check();
    await accessible(bob, 'new project mapping');
    await bob.getByRole('button', { name: 'Create mapping' }).click();
    await expect(bob).toHaveURL(new RegExp(`/projects/${projectKey}/ci/mappings/${projectMapping}(\\?|$)`));
    await bob.goto(`/ui/en/projects/${projectKey}/ci`);
    const mappingRow = bob.getByRole('row', { name: new RegExp(projectMapping) });
    await expect(mappingRow).toBeVisible();
    await accessible(bob, 'project CI overview with a mapping');

    // the project's claim tester evaluates the project's tier
    await bob.getByRole('link', { name: 'Test with claims' }).first().click();
    await expect(bob.getByRole('heading', { name: 'Claim tester', level: 1 })).toBeVisible();
    await bob.getByLabel('Issuer', { exact: true }).selectOption(issuerName);
    await bob.getByLabel('Claims as JSON, or paste an ID token').fill(pasteToken);
    await bob.getByRole('button', { name: 'Evaluate' }).click();
    await expect(bob.getByText('Matched:')).toBeVisible();
    await expect(bob.getByText(projectMapping).first()).toBeVisible();
    await accessible(bob, 'project claim tester');

    // delete it again
    await bob.goto(`/ui/en/projects/${projectKey}/ci`);
    await bob
      .getByRole('row', { name: new RegExp(projectMapping) })
      .getByRole('button', { name: 'Delete' })
      .click();
    const confirm = bob.getByRole('dialog');
    await confirm.locator('#confirm-name').fill(projectMapping);
    await confirm.getByRole('button', { name: 'Delete mapping' }).click();
    await expect(bob.getByRole('row', { name: new RegExp(projectMapping) })).toHaveCount(0);
  });

  expect(replicas.size, 'the journey was scattered over the replicas').toBeGreaterThan(1);
  expect(await bobCsp(), 'securitypolicyviolation events on the page of bob').toEqual([]);
  await bobContext.close();
});
