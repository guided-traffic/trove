import { expect, expectAccessible, signInLocal, signOut, test, unique } from '../support';

// R-UI-047 journeys "mirror" (R-REPO-059, R-UI-021, Q-UI-104, Q-UI-105, Q-UI-119 to Q-UI-124) and "upstream settings" (F-54,
// R-UI-021, R-REPO-024, R-REPO-033). Both use the counting npm test upstream of the end-to-end tests (test/npmstub): trove
// reaches it as https://npmstub.<ns>.svc, which is the only thing the browser needs of it, since the journeys type that
// address into the form and trove, not the browser, talks to the stub. The run that starts these journeys exports
// TROVE_E2E_NPMSTUB_HOST (deploy/dev/e2e.sh with TROVE_E2E_NPMSTUB=1, which the sets that run TestE1_11Journeys set).
//
// A mirror pass needs a process that claims the queue `mirror` (R-REPO-053); the journey of the panel does not wait for a
// pass to end: it asserts the status, the pause and the resume, which the API answers whatever the worker does, and the
// answer to a run request, which is a queued job or the running job that is already there (a new mirror starts its first
// pass by itself).

function env(name: string): string {
  const v = process.env[name];
  if (!v) throw new Error(`${name} is not set; the journey runs through TestE1_11Journeys with the npm test upstream deployed (TROVE_E2E_NPMSTUB=1)`);
  return v;
}

const stubUrl = `https://${env('TROVE_E2E_NPMSTUB_HOST')}/`;
const mirrorKey = unique('ui-mirror');
const upstreamKey = unique('ui-upstream');
const secret = `journey-secret-${Date.now().toString(36)}`;

test.afterAll(async ({ admin }) => {
  await admin.delete(`/api/v1/repositories/${mirrorKey}`);
  await admin.delete(`/api/v1/repositories/${upstreamKey}`);
});

type Remote = {
  url?: string;
  authMode?: string;
  username?: string;
  password?: { set?: boolean };
  mode?: string;
  mirror?: { paused?: boolean; intervalSeconds?: number; maxBytes?: number | null };
  cache?: { metadataTtlSeconds?: number };
  availability?: { mode?: string };
};

/** Opens the wizard of a remote npm repository at its class-specific step, with the key typed. */
async function remoteWizard(page: import('@playwright/test').Page, key: string): Promise<void> {
  await page.goto('/ui/en/admin/repositories/new');
  await page.getByRole('option', { name: 'npm' }).click();
  await page.getByRole('button', { name: 'Next' }).click();
  await page.getByRole('radio', { name: /^Remote/ }).check();
  await page.getByRole('button', { name: 'Next' }).click();
  await page.locator('#f-key').fill(key);
  await expect(page.getByText('The key is available.')).toBeVisible();
  await page.getByRole('button', { name: 'Next' }).click();
  await expect(page.getByRole('heading', { name: 'Upstream', level: 2 })).toBeVisible();
}

test('R-REPO-059 R-UI-021 Q-UI-104 Q-UI-105 the status of a mirror, pause, resume and a pass on request', async ({ page, replicas, admin }) => {
  await signInLocal(page);
  const remoteOf = async () => ((await (await admin.get(`/api/v1/repositories/${mirrorKey}`)).json()) as { remote?: Remote }).remote;
  const state = page.getByTestId('mirror-state');

  await test.step('the wizard creates an npm remote in mode mirror against the test upstream, bounded by a storage limit', async () => {
    await remoteWizard(page, mirrorKey);
    await page.getByRole('textbox', { name: 'Upstream URL' }).fill(stubUrl);
    await page.getByRole('radio', { name: /^Mirror/ }).check();
    // A mirror of another upstream than the format's well-known one is saved with a warning when it has no bound (Q-UI-118).
    await expect(page.getByText('It is saved with a warning.')).toBeVisible();
    await page.getByRole('textbox', { name: 'Storage limit in bytes' }).fill('104857600');
    await expect(page.getByText('It is saved with a warning.')).toHaveCount(0);
    await page.getByRole('textbox', { name: 'Pass interval in seconds', exact: true }).fill('300');
    await page.getByRole('button', { name: 'Next' }).click();
    await expect(page.getByText('Mirror mode: a background pass stores what the upstream offers.')).toBeVisible();
    await page.getByRole('button', { name: 'Create repository' }).click();
    await expect(page).toHaveURL(new RegExp(`/admin/repositories/${mirrorKey}$`));
    const remote = await remoteOf();
    expect(remote?.mode).toBe('mirror');
    expect(remote?.url).toBe(stubUrl);
    expect(remote?.mirror).toMatchObject({ intervalSeconds: 300, maxBytes: 104857600, paused: false });
  });

  await test.step('the Mirror tab shows the status above the settings, with the facts of every part, in words', async () => {
    await page.getByRole('tab', { name: 'Mirror' }).click();
    await expect(page.getByRole('heading', { name: 'Mirror status', level: 2 })).toBeVisible();
    // A new mirror starts its first pass by itself, so the state is one of these; the word, not the colour, says which.
    await expect(state.locator('strong')).toHaveText(/^(Scheduled|Running|Waiting|Bound reached|Failing)$/);
    for (const heading of ['Last complete pass', 'Next pass', 'Last error', 'Rate-limit waits', 'Bound']) {
      await expect(page.getByRole('heading', { name: heading, level: 3 })).toBeVisible();
    }
    await expect(page.getByTestId('mirror-bound')).toContainText('100.0 MiB');
    await expect(page.getByRole('heading', { name: 'Mirror', level: 2, exact: true })).toBeVisible();
    // The status sits above the settings.
    const status = await page.getByRole('heading', { name: 'Mirror status', level: 2 }).boundingBox();
    const settings = await page.getByRole('heading', { name: 'Mirror', level: 2, exact: true }).boundingBox();
    expect(status!.y).toBeLessThan(settings!.y);
    for (const scheme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme: scheme });
      await expectAccessible(page, `repository detail, Mirror tab with the status (${scheme})`);
    }
  });

  await test.step('Pause patches the document with If-Match and the state reads Paused; Resume reverts it', async () => {
    const patched = page.waitForResponse((r) => r.request().method() === 'PATCH' && r.url().endsWith(`/api/v1/repositories/${mirrorKey}`));
    await page.getByRole('button', { name: 'Pause', exact: true }).click();
    const res = await patched;
    expect(res.status()).toBe(200);
    expect(res.request().headers()['if-match']).toBeTruthy();
    expect(res.request().postDataJSON()).toEqual({ remote: { mirror: { paused: true } } });
    await expect(state.locator('strong')).toHaveText('Paused');
    await expect(page.getByRole('button', { name: 'Resume', exact: true })).toBeVisible();
    await expect(page.getByTestId('mirror-next')).toContainText('No pass is planned.');
    expect((await remoteOf())?.mirror?.paused).toBe(true);
    await expectAccessible(page, 'repository detail, Mirror tab, paused');

    // A run request on a paused mirror is refused in words (409 mirror_paused), not as an error page.
    await page.getByRole('button', { name: 'Run a pass now' }).click();
    await expect(page.getByText('The mirror is paused. Resume it to run a pass.')).toBeVisible();

    await page.getByRole('button', { name: 'Resume', exact: true }).click();
    await expect(state.locator('strong')).not.toHaveText('Paused');
    await expect(page.getByRole('button', { name: 'Pause', exact: true })).toBeVisible();
    expect((await remoteOf())?.mirror?.paused).toBe(false);
  });

  await test.step('Run a pass now and Run a full pass answer with the queued job or the one that is running, with a link to the jobs', async () => {
    const queuedOrRunning = /A pass is queued\.|A full pass is queued\.|A pass is already queued or running\./;
    await page.getByRole('button', { name: 'Run a pass now' }).click();
    const notice = page.getByRole('region', { name: 'Mirror status' }).getByRole('status');
    await expect(notice).toContainText(queuedOrRunning);
    await expect(notice.getByRole('link', { name: 'Open the mirror jobs' })).toHaveAttribute('href', /\/admin\/system\/jobs\?kind=mirror-pass$/);

    await page.getByRole('button', { name: 'More ways to run a pass' }).click();
    await page.getByRole('menuitem', { name: 'Run a full pass' }).click();
    await expect(notice).toContainText(queuedOrRunning);
    await expectAccessible(page, 'repository detail, Mirror tab, after a run request');

    await notice.getByRole('link', { name: 'Open the mirror jobs' }).click();
    await expect(page).toHaveURL(/\/admin\/system\/jobs\?kind=mirror-pass$/);
    await expect(page.getByRole('heading', { name: 'Jobs', level: 1 })).toBeVisible();
  });

  await signOut(page);
  expect(replicas.size, `replicas that answered: ${[...replicas]}`).toBeGreaterThanOrEqual(2);
});

test('F-54 R-UI-021 R-REPO-024 R-REPO-033 a remote with a non-default upstream, write-only credentials and Test connection', async ({ page, replicas, admin }) => {
  await signInLocal(page);
  const remoteOf = async () => {
    const res = await admin.get(`/api/v1/repositories/${upstreamKey}`);
    return { text: await res.text(), remote: (JSON.parse(await res.text()) as { remote?: Remote }).remote };
  };

  await test.step('the wizard offers the upstream with the preset of npm, the credentials, the periods and the offline switch', async () => {
    await remoteWizard(page, upstreamKey);
    await expect(page.getByRole('textbox', { name: 'Upstream URL' })).toHaveValue('https://registry.npmjs.org/');
    await expect(page.getByText('The format\'s well-known upstream is')).toBeVisible();
    await expect(page.getByRole('combobox', { name: 'Authentication' })).toHaveValue('none');
    await expect(page.getByRole('textbox', { name: 'Metadata cache period in seconds' })).toHaveValue('');
    await expect(page.getByText('Default: 7200.')).toBeVisible();
    await expect(page.getByRole('checkbox', { name: 'Offline' })).not.toBeChecked();
    for (const scheme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme: scheme });
      await expectAccessible(page, `repository wizard, Upstream section (${scheme})`);
    }
  });

  await test.step('a non-default upstream with a password and a period is tested before it is saved, and the result is shown in words', async () => {
    await page.getByRole('textbox', { name: 'Upstream URL' }).fill(stubUrl);
    await page.getByRole('combobox', { name: 'Authentication' }).selectOption('basic');
    await page.getByRole('textbox', { name: 'Username' }).fill('journey');
    await page.getByLabel('Password or token').fill(secret);
    await page.getByRole('textbox', { name: 'Metadata cache period in seconds' }).fill('600');
    await page.getByRole('button', { name: 'Test connection' }).click();
    const result = page.getByTestId('upstream-test-result');
    await expect(result).toContainText('The upstream answered.');
    await expect(page.locator('.test-result')).toContainText('accepted');
    await expectAccessible(page, 'repository wizard, Upstream section, test result');
  });

  await test.step('the review and the request name the password member and never its value; the create seals it', async () => {
    await page.getByRole('button', { name: 'Next' }).click();
    await page.getByText('Show as API request').click();
    await expect(page.getByText('<the password>').first()).toBeVisible();
    await expect(page.locator('body')).not.toContainText(secret);
    await page.getByRole('button', { name: 'Create repository' }).click();
    await expect(page).toHaveURL(new RegExp(`/admin/repositories/${upstreamKey}$`));
    const { text, remote } = await remoteOf();
    expect(text).not.toContain(secret);
    expect(remote).toMatchObject({
      url: stubUrl,
      authMode: 'basic',
      username: 'journey',
      password: { set: true },
      cache: { metadataTtlSeconds: 600 },
      mode: 'pullThrough',
    });
  });

  await test.step('the edit form reads the upstream back with an empty password field; the stored password survives a save of another member', async () => {
    await page.getByRole('tab', { name: 'Upstream' }).click();
    await expect(page.getByRole('textbox', { name: 'Upstream URL' })).toHaveValue(stubUrl);
    await expect(page.getByRole('textbox', { name: 'Username' })).toHaveValue('journey');
    await expect(page.getByLabel('Password or token')).toHaveValue('');
    await expect(page.getByText('A password is stored and is not shown.')).toBeVisible();
    await expect(page.getByRole('textbox', { name: 'Metadata cache period in seconds' })).toHaveValue('600');
    await expect(page.locator('body')).not.toContainText(secret);
    await expectAccessible(page, 'repository detail, Upstream tab');

    await page.getByRole('checkbox', { name: 'Offline' }).check();
    await page.getByRole('button', { name: 'Test connection' }).click();
    await expect(page.getByTestId('upstream-test-result')).toContainText('The upstream could not be reached.');
    await expect(page.locator('.test-result')).toContainText('offline');
    await page.getByRole('button', { name: 'Save changes' }).click();
    await expect(page.getByText('Repository saved.')).toBeVisible();
    const saved = await remoteOf();
    expect(saved.remote?.availability?.mode).toBe('offline');
    expect(saved.remote?.password).toEqual({ set: true });
    expect(saved.text).not.toContain(secret);

    await page.getByRole('checkbox', { name: 'Offline' }).uncheck();
    await page.getByRole('button', { name: 'Save changes' }).click();
    await expect.poll(async () => (await remoteOf()).remote?.availability?.mode).toBe('online');
  });

  await signOut(page);
  expect(replicas.size, `replicas that answered: ${[...replicas]}`).toBeGreaterThanOrEqual(2);
});
