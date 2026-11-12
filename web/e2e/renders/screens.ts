import { Page, expect } from '@playwright/test';
import { Kind, emptyProject, exposureRule, group, longDebKey, longNpmKey, mainProject, npmPackage, ociPackage, otherProject, projectServiceAccount, serviceAccount } from './seed';
import { gradedPackage } from './advisories';
import { routeTooMany } from './suggestions';

/**
 * Every screen of the app (web/src/app/app.routes.ts and the route files of the feature areas), with the parameters of the
 * seed and the kinds of user that reach it from the navigation. A screen's tabs (`role=tab` inside `main`) are found and
 * rendered one by one; a tab strip of links leads to other routes, which are screens of their own.
 */
export interface Screen {
  /** The file name's screen part and the contact sheet's group. */
  readonly id: string;
  readonly path: string;
  readonly kinds: readonly Kind[];
  /** Answers of the app's own API calls that are the screen's normal state, not a failure, each with its reason. */
  readonly expected?: readonly { readonly match: RegExp; readonly why: string }[];
  /**
   * A flow through the screen: each value it yields names a state that is rendered, after the actions before it ran. Without a
   * walk the screen is rendered as it opens and with each of its tabs.
   */
  readonly walk?: (page: Page) => AsyncGenerator<string>;
}

/**
 * The repository wizard (R-UI-021) for one format and class, as far as its review: every step as it opens, and the basic
 * settings once more before the key is typed. Nothing is created. A step whose **Next** stays disabled ends the walk there.
 */
function wizard(format: string, cls: 'Local' | 'Remote' | 'Virtual'): Screen['walk'] {
  return async function* (page) {
    const step = async (): Promise<string> => {
      const text = (await page.locator('[aria-current="step"]').allTextContents())[0] ?? 'step';
      return text.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 48);
    };
    // Next once it is enabled, then until the step has changed; false where it stays disabled.
    const next = async (): Promise<boolean> => {
      const button = page.getByRole('button', { name: 'Next', exact: true });
      if ((await button.count()) === 0) return false;
      const ready = await expect(button)
        .toBeEnabled({ timeout: 5_000 })
        .then(() => true)
        .catch(() => false);
      if (!ready) return false;
      const before = await step();
      await button.click();
      await expect.poll(step, { timeout: 10_000 }).not.toBe(before);
      return true;
    };
    yield await step();
    await page.getByRole('option', { name: format, exact: true }).click();
    if (!(await next())) return;
    yield await step();
    await page.getByRole('radio', { name: new RegExp(`^${cls}`) }).check();
    if (!(await next())) return;
    yield `${await step()}-untouched`;
    const project = page.getByLabel('Project', { exact: true });
    if ((await project.count()) > 0) await project.selectOption(mainProject);
    await page.locator('#f-key').fill(`renders-${format.toLowerCase().replace(/[^a-z]+/g, '')}-${cls.toLowerCase()}`);
    await page.locator('#f-key').blur();
    yield await step();
    for (let i = 0; i < 6; i++) {
      if (!(await next())) return;
      const name = await step();
      yield name;
      if (name.includes('review')) return;
    }
  };
}

/**
 * A list as it opens, then with its filter panel open: beside the panel the list has some 300 px less, and that width is the
 * one its columns and addresses have to fit (the owner's screen of 2026-11-12, a repository list at 1860 px).
 */
async function* filtersOpen(page: Page): AsyncGenerator<string> {
  yield '';
  await page.locator('main .filter-toggle').first().click();
  await expect(page.locator('main trove-filter-panel form')).toBeVisible();
  yield 'filters';
}

/** The list as it opens, then sorted by its Detected button: the click fails where the button cannot be reached. */
async function* sortByDetection(page: Page): AsyncGenerator<string> {
  yield '';
  await page.locator('th').getByRole('button', { name: 'Detected' }).click();
  await expect(page).toHaveURL(/sort=detectedAt/);
  yield 'sorted';
}

/** The suggestions' answer of a 429 and its seconds: the notice of R-UI-097 is a state of the field. */
const tooMany = [
  { match: /^GET \/api\/v1\/search\/suggestions\?\S* answered 429$/, why: 'the 429 notice is rendered on purpose' },
  { match: /^GET \/api\/v1\/auth\/whoami answered 401$/, why: 'an anonymous visitor has no session' },
];

/**
 * The search field of the top bar and its list (R-UI-095 to R-UI-097): the bar as it opens; the list opened empty; typing `web`
 * (every group of the main area); the start of the seed's longest key; a text with no match; `us` where the caller is an instance
 * administrator (Admin pages, Users); the filter menu and the chip **Packages**; the 429 notice. Below 36 rem the button opens the
 * sheet, and the sheet with suggestions is the state. The suggestions are the server's (R-API-060); only the 429 is routed.
 */
async function* searchField(page: Page): AsyncGenerator<string> {
  // The 429 of the previous width's last state stays on the page: the server answers again.
  await page.unroute('**/api/v1/search/suggestions?**');
  yield '';
  const narrow = (page.viewportSize()?.width ?? 1440) < 576;
  const field = page.locator('#global-search');
  const list = page.locator('#search-suggestions');
  const heading = (name: string | RegExp) => list.getByRole('group', { name, exact: typeof name === 'string' });
  if (narrow) await page.getByRole('button', { name: 'Search', exact: true }).click();
  else await field.click();
  await expect(list).toBeVisible();
  const admin = (await heading('Admin area').count()) > 0;
  yield 'search-empty';
  // An anonymous visitor reads the public repository collections of the platform project only.
  const anonymous = (await page.getByRole('link', { name: 'Sign in' }).count()) > 0;
  await field.fill(anonymous ? 'coll' : 'web');
  await expect(heading('Repositories')).toBeVisible();
  yield 'search-typing';
  // The other states are a signed-in user's; an anonymous visitor's list differs only in what it may read.
  if (narrow || anonymous) return;
  await field.fill(longNpmKey.slice(0, 24));
  await expect(list.locator('[role=option]').first()).toContainText('release-candidates');
  yield 'search-long';
  await field.fill('zzqx');
  await expect(page.locator('.search-status')).toContainText('Nothing you may read matches');
  yield 'search-none';
  if (admin) {
    await field.fill('us');
    await expect(heading(/^Admin pages/)).toBeVisible();
    yield 'search-admin';
  }
  await page.locator('.search-filter').click();
  await expect(page.locator('.search-filter-menu')).toBeVisible();
  yield 'search-filter-menu';
  await page.getByRole('menuitemradio', { name: 'Packages' }).click();
  await field.fill('acme');
  await expect(heading('Packages')).toBeVisible();
  yield 'search-chip';
  await routeTooMany(page, 30);
  await field.fill('acme-ui');
  await expect(page.locator('.search-status')).toContainText('Too many searches');
  yield 'search-limited';
}

/**
 * The search's store of frequently visited places (R-UI-096): three repositories and two projects opened first, then the start
 * page with **Recently opened** from the store, the field opened with its frequently visited groups, and the project switcher's
 * recent list.
 */
async function* frequentPlaces(page: Page): AsyncGenerator<string> {
  for (const path of [
    `/ui/projects/${mainProject}/repositories/npm-vendor`,
    `/ui/projects/${otherProject}/repositories/collections`,
    `/ui/projects/${mainProject}/repositories/npm-internal`,
  ]) {
    await page.goto(path);
    await expect(page.locator('main h1')).toBeVisible();
  }
  await page.goto('/ui/projects');
  await expect(page.locator('main .recent')).toBeVisible();
  yield '';
  const narrow = (page.viewportSize()?.width ?? 1440) < 576;
  if (narrow) await page.getByRole('button', { name: 'Search', exact: true }).click();
  else await page.locator('#global-search').click();
  await expect(page.locator('#search-suggestions').getByRole('group', { name: 'Frequently visited repositories' })).toBeVisible();
  yield 'search-frequent';
  await page.goto(`/ui/projects/${mainProject}/repositories`);
  if (narrow) await page.getByRole('button', { name: 'Open navigation' }).click();
  await page.locator('.switcher-button').click();
  await expect(page.locator('#project-switcher-panel')).toBeVisible();
  yield 'switcher';
}

/** The closed Advanced section at the end of a page, opened: where a page keeps its destructive action (the style guide). */
async function* advancedOpen(page: Page): AsyncGenerator<string> {
  yield '';
  await page.locator('main trove-disclosure').last().getByRole('button', { name: 'Advanced' }).click();
  await expect(page.locator('main trove-disclosure').last().getByRole('button', { name: /^(Delete|Revoke all)/ }).first()).toBeVisible();
  yield 'advanced';
}

/** The closed Visibility section of a settings page, opened: where the level of the installation or of a project is chosen (R-UI-053, R-UI-054). */
async function* visibilityOpen(page: Page): AsyncGenerator<string> {
  yield '';
  await page.locator('main trove-disclosure').filter({ hasText: 'Visibility' }).first().getByRole('button').first().click();
  await expect(page.locator('main trove-visibility-level').first()).toBeVisible();
  yield 'visibility';
}

/** The wizard asks whether the typed key is taken; a 404 means it is free. */
const keyFree = [{ match: /^GET \/api\/v1\/projects\/[^/]+\/repositories\/renders-[a-z-]+ answered 404$/, why: 'the key check: 404 is a free key' }];

const wizards: Screen[] = (['OCI (Docker)', 'npm', 'Ansible Galaxy'] as const).flatMap((format) =>
  (['Local', 'Remote', 'Virtual'] as const).flatMap((cls): Screen[] => {
    const id = `wizard-${format.split(' ')[0]!.toLowerCase()}-${cls.toLowerCase()}`;
    return [
      { id, path: `/ui/projects/${mainProject}/repositories/new`, kinds: ['power'], walk: wizard(format, cls), expected: keyFree },
      { id: `admin-${id}`, path: '/ui/admin/repositories/new', kinds: ['admin'], walk: wizard(format, cls), expected: keyFree },
    ];
  }),
);

/** An answer of the upstream test, as the server gives it (R-REPO-024, R-REPO-064), for a state of the upstream section. */
function testAnswer(over: Record<string, unknown>): Record<string, unknown> {
  return {
    reachable: true,
    httpStatus: 401,
    authenticated: null,
    durationMs: 212,
    tls: { peerSubject: 'CN=registry.example.com', notAfter: '2027-03-01T00:00:00Z' },
    outboundProxy: null,
    sourceAddress: null,
    tokenService: null,
    error: null,
    ...over,
  };
}

/**
 * The upstream section of a new remote repository (R-UI-082) for one format: the presets as it opens, then, where `full`, a preset
 * with credentials and the line that names their hosts, a typed URL, the four kinds of test result (answered by the route, so
 * no registry is contacted), and Upstream details open with two further hosts, one of 64 characters. Nothing is created.
 */
function upstreamWalk(format: string, preset: string | null, full: boolean): Screen['walk'] {
  return async function* (page) {
    const next = async (): Promise<void> => {
      const button = page.getByRole('button', { name: 'Next', exact: true });
      await expect(button).toBeEnabled();
      const before = await page.locator('[aria-current="step"]').allTextContents();
      await button.click();
      await expect.poll(() => page.locator('[aria-current="step"]').allTextContents()).not.toEqual(before);
    };
    await page.getByRole('option', { name: format, exact: true }).click();
    await next();
    await page.getByRole('radio', { name: /^Remote/ }).check();
    await next();
    const project = page.getByLabel('Project', { exact: true });
    if ((await project.count()) > 0) {
      // The select is held while the projects load; choose once it offers the project, and again until it shows it.
      await expect(project.locator(`option[value="${mainProject}"]`)).toHaveCount(1);
      await expect(async () => {
        await project.selectOption(mainProject);
        await expect(project).toHaveValue(mainProject, { timeout: 1_000 });
      }).toPass();
    }
    await page.locator('#f-key').fill(`renders-${format.toLowerCase().replace(/[^a-z]+/g, '')}-upstream`);
    await page.locator('#f-key').blur();
    await next();
    const upstream = page.locator('trove-upstream-settings');
    await expect(upstream).toBeVisible();
    yield 'presets';
    if (full && preset) {
      await upstream.getByRole('button', { name: preset, exact: true }).click();
      await page.locator('#f-remote-authMode').selectOption('basic');
      await page.locator('#f-remote-username').fill('ci-robot');
      await page.locator('#f-remote-password').fill('a-password-of-the-renders');
      yield 'preset-credentials';
      const answers: [string, Record<string, unknown>][] = [
        ['test-accepted', testAnswer({ authenticated: true, tokenService: { host: 'gitlab.com', allowed: true, receivesCredential: true } })],
        ['test-refused', testAnswer({ authenticated: false, tokenService: { host: 'gitlab.com', allowed: true, receivesCredential: true } })],
        [
          'test-unreachable',
          testAnswer({ reachable: false, httpStatus: null, tls: null, error: { code: 'dns_error', message: 'The name registry.gitlab.com does not resolve.' } }),
        ],
        [
          'test-token-foreign',
          testAnswer({
            tokenService: { host: 'gitlab.example-company.com', allowed: false, receivesCredential: false },
            error: {
              code: 'foreign_host_denied',
              message: "The registry's token service is on gitlab.example-company.com, which is neither the upstream nor one of the repository's further hosts; trove did not ask it.",
            },
          }),
        ],
      ];
      for (const [state, body] of answers) {
        await page.route('**/upstream-tests', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) }));
        await upstream.getByRole('button', { name: 'Test connection' }).click();
        await expect(upstream.locator('[data-testid="upstream-test-result"]')).toBeVisible();
        yield state;
        await page.unroute('**/upstream-tests');
      }
      await page.locator('#f-remote-url').fill('https://registry.example-company.com/team/');
      yield 'typed-url';
    }
    await upstream.getByRole('button', { name: 'Upstream details' }).click();
    const rows = upstream.locator('.host-row input[type="text"]');
    const n = (await rows.count()) + 2;
    await upstream.getByRole('button', { name: 'Add host' }).click();
    await upstream.getByRole('button', { name: 'Add host' }).click();
    await expect(rows).toHaveCount(n);
    await rows.nth(n - 2).fill('cdn.registry.example-company.com');
    await rows.nth(n - 1).fill('a-very-long-host-name-of-a-token-service.eu-central.example-co.com');
    yield 'details';
  };
}

const upstreams: Screen[] = [
  { id: 'upstream-oci', path: `/ui/projects/${mainProject}/repositories/new`, kinds: ['power'], walk: upstreamWalk('OCI (Docker)', 'GitLab', true), expected: keyFree },
  { id: 'upstream-npm', path: `/ui/projects/${mainProject}/repositories/new`, kinds: ['power'], walk: upstreamWalk('npm', null, false), expected: keyFree },
  { id: 'upstream-deb', path: `/ui/projects/${mainProject}/repositories/new`, kinds: ['power'], walk: upstreamWalk('Debian (APT)', null, false), expected: keyFree },
  { id: 'admin-upstream-oci', path: '/ui/admin/repositories/new', kinds: ['admin'], walk: upstreamWalk('OCI (Docker)', 'GitLab', true), expected: keyFree },
];

const all: readonly Kind[] = ['plain', 'power', 'admin'];
const owners: readonly Kind[] = ['power', 'admin'];
const admins: readonly Kind[] = ['admin'];
const P = `/ui/projects/${mainProject}`;
const O = `/ui/projects/${otherProject}`;

export const screens: readonly Screen[] = [
  // Without a session (Q-UI-125).
  { id: 'login', path: '/ui/login', kinds: ['anonymous'] },
  { id: 'logged-out', path: '/ui/logged-out', kinds: ['anonymous'] },

  // The start and the project's content (Q-UI-129): the plain user's main path.
  { id: 'home', path: '/ui/', kinds: all },
  { id: 'projects', path: '/ui/projects', kinds: all },
  // The search field and its list on the start page; anonymous while the seed makes a project public (R-UI-095).
  { id: 'search-field', path: '/ui/projects', kinds: ['anonymous', ...all], walk: searchField, expected: tooMany },
  // The start page and the project switcher read the search's store (R-UI-096); an anonymous visitor's start page (R-UI-095).
  { id: 'start-recent', path: '/ui/projects', kinds: ['plain', 'power'], walk: frequentPlaces },
  { id: 'projects-anonymous', path: '/ui/projects', kinds: ['anonymous'], expected: tooMany },
  // The results page of the search (R-UI-098): every tab with matches, a text without a match, a tab opened by `kind`.
  { id: 'search-results', path: '/ui/search?q=web', kinds: all },
  { id: 'search-results-none', path: '/ui/search?q=zzqx', kinds: ['plain'] },
  { id: 'search-results-kind-package', path: '/ui/search?q=acme&kind=package', kinds: all },
  { id: 'repositories', path: `${P}/repositories`, kinds: all, walk: filtersOpen },
  { id: 'repositories-long-keys', path: `${O}/repositories`, kinds: all, walk: filtersOpen },
  { id: 'repositories-empty-project', path: `/ui/projects/${emptyProject}/repositories`, kinds: admins },
  { id: 'repository-npm-local', path: `${P}/repositories/npm-internal`, kinds: all },
  { id: 'repository-npm-remote', path: `${P}/repositories/npmjs`, kinds: all },
  { id: 'repository-npm-virtual', path: `${P}/repositories/npm`, kinds: all },
  { id: 'repository-long-key', path: `${P}/repositories/${longNpmKey}`, kinds: all },
  { id: 'repository-generic', path: `${P}/repositories/assets`, kinds: all },
  { id: 'repository-oci', path: `${P}/repositories/images`, kinds: all },
  // The grades of R-VULN-038: S to E in the vendored packages, N/A in the internal ones and the images (advisories.ts).
  { id: 'repository-npm-vendor', path: `${P}/repositories/npm-vendor`, kinds: all },
  { id: 'repository-npm-vendor-by-grade', path: `${P}/repositories/npm-vendor?sort=-grade`, kinds: ['plain'] },
  { id: 'repository-empty', path: `${P}/repositories/scratch`, kinds: all },
  { id: 'repository-oci-remote', path: `${O}/repositories/docker-hub-remote`, kinds: all },
  { id: 'repository-deb', path: `${O}/repositories/${longDebKey}`, kinds: all },
  { id: 'repository-nuget-virtual', path: `${O}/repositories/nuget`, kinds: all },
  { id: 'repository-ansible', path: `${O}/repositories/collections`, kinds: all },
  { id: 'packages', path: `${P}/packages`, kinds: all, walk: filtersOpen },
  { id: 'packages-other-project', path: `${O}/packages`, kinds: all, walk: filtersOpen },
  { id: 'package-npm', path: `${P}/packages/${npmPackage.format}/${npmPackage.name}`, kinds: all },
  { id: 'package-oci', path: `${P}/packages/${ociPackage.format}/${ociPackage.name}`, kinds: all },
  { id: 'package-npm-graded', path: `${P}/packages/${gradedPackage.format}/${gradedPackage.name}`, kinds: all },
  { id: 'package-npm-fixed', path: `${P}/packages/npm/xml-stream-parser?version=0.9.2`, kinds: ['plain'] },
  // The tab's findings narrowed by a segment of the card's bar (Q-UI-379).
  { id: 'package-npm-graded-critical', path: `${P}/packages/${gradedPackage.format}/${gradedPackage.name}?tab=vulnerabilities&severity=critical`, kinds: ['plain'] },
  { id: 'browse', path: `${P}/browse`, kinds: all, walk: filtersOpen },
  { id: 'browse-folder', path: `${P}/browse/assets/releases/design-system/2.5.0`, kinds: all },
  { id: 'browse-file', path: `${P}/browse/assets/README.md`, kinds: all },
  { id: 'vulnerabilities', path: `${P}/vulnerabilities`, kinds: all, walk: filtersOpen },
  // A filter opens the panel; the list sorts by Detected at every width, at 390 px from the sort row above its stacked rows (Q-UI-391).
  { id: 'vulnerabilities-filtered', path: `${P}/vulnerabilities?severity=high`, kinds: admins, walk: sortByDetection },
  { id: 'members', path: `${P}/members`, kinds: all },
  { id: 'setup', path: '/ui/setup', kinds: all },
  { id: 'setup-repository', path: `/ui/setup?repository=${mainProject}/npm`, kinds: all },
  // The top bar's search ends on a page of the main area, never in the admin area (Q-UI-390); the field shows the text.
  { id: 'search', path: '/ui/search?q=npm', kinds: all },
  { id: 'search-none', path: '/ui/search?q=no-such-repository', kinds: ['admin'] },
  { id: 'guides', path: '/ui/guides', kinds: all },
  { id: 'guides-repository', path: `/ui/guides?repository=${mainProject}/npm`, kinds: all },
  { id: 'help', path: '/ui/help', kinds: all },
  { id: 'help-user-page', path: '/ui/help/user/getting-started', kinds: all },
  { id: 'profile', path: '/ui/profile', kinds: all },
  { id: 'profile-tokens', path: '/ui/profile/tokens', kinds: all },
  // The plain user holds a token (setup.ts): its revoke-all, last on the page behind Advanced.
  { id: 'profile-tokens-advanced', path: '/ui/profile/tokens', kinds: ['plain'], walk: advancedOpen },
  { id: 'profile-sessions', path: '/ui/profile/sessions', kinds: all },
  { id: 'not-found', path: '/ui/no-such-page', kinds: all },

  // What an Owner of the project maintains, and the settings a reader is offered in the list.
  { id: 'repository-settings-npm-local', path: `${P}/repositories/npm-internal/settings`, kinds: all },
  { id: 'repository-settings-npm-remote', path: `${P}/repositories/npmjs/settings`, kinds: owners },
  { id: 'repository-settings-npm-virtual', path: `${P}/repositories/npm/settings`, kinds: owners },
  { id: 'repository-settings-generic', path: `${P}/repositories/assets/settings`, kinds: owners },
  { id: 'repository-settings-generic-advanced', path: `${P}/repositories/assets/settings`, kinds: admins, walk: advancedOpen },
  { id: 'repository-settings-oci', path: `${P}/repositories/images/settings`, kinds: owners },
  { id: 'repository-wizard', path: `${P}/repositories/new`, kinds: owners },
  { id: 'policies', path: `${P}/policies`, kinds: owners },
  { id: 'policy-new', path: `${P}/policies/new`, kinds: owners },
  { id: 'project-service-accounts', path: `${P}/service-accounts`, kinds: owners },
  { id: 'project-service-account', path: `${P}/service-accounts/${projectServiceAccount}`, kinds: owners },
  { id: 'project-ci', path: `${P}/ci`, kinds: owners },
  // A project's mapping editor without an issuer in the address: the issuer select and the policy picker (Q-UI-334).
  { id: 'project-ci-mapping-new', path: `${P}/ci/mappings/new`, kinds: owners },
  { id: 'project-settings', path: `${P}/settings`, kinds: owners, walk: visibilityOpen },

  // The administrator's area (Q-UI-127).
  { id: 'repository-settings-oci-remote', path: `${O}/repositories/docker-hub-remote/settings`, kinds: admins },
  { id: 'repository-settings-deb', path: `${O}/repositories/${longDebKey}/settings`, kinds: admins },
  { id: 'repository-settings-nuget-remote', path: `${O}/repositories/nuget-org/settings`, kinds: admins },
  { id: 'repository-settings-ansible', path: `${O}/repositories/collections/settings`, kinds: admins },
  { id: 'help-admin-page', path: '/ui/help/admin/repositories', kinds: admins },
  { id: 'admin-dashboard', path: '/ui/admin/dashboard', kinds: admins },
  { id: 'admin-projects', path: '/ui/admin/projects', kinds: admins, walk: filtersOpen },
  { id: 'admin-repositories', path: '/ui/admin/repositories', kinds: admins, walk: filtersOpen },
  { id: 'admin-repository-wizard', path: '/ui/admin/repositories/new', kinds: admins },
  { id: 'admin-users', path: '/ui/admin/users', kinds: admins, walk: filtersOpen },
  { id: 'admin-user', path: '/ui/admin/users/sam', kinds: admins },
  // The break-glass account: no Advanced action, so the page asks the token list whether it offers Revoke all.
  { id: 'admin-user-break-glass', path: '/ui/admin/users/admin', kinds: admins },
  { id: 'admin-user-advanced', path: '/ui/admin/users/sam', kinds: admins, walk: advancedOpen },
  { id: 'admin-groups', path: '/ui/admin/groups', kinds: admins, walk: filtersOpen },
  { id: 'admin-group', path: `/ui/admin/groups/${group}`, kinds: admins },
  { id: 'admin-service-accounts', path: '/ui/admin/service-accounts', kinds: admins, walk: filtersOpen },
  { id: 'admin-service-account', path: `/ui/admin/service-accounts/${serviceAccount}`, kinds: admins },
  { id: 'admin-permissions', path: '/ui/admin/permissions', kinds: admins, walk: filtersOpen },
  { id: 'admin-permission-new', path: '/ui/admin/permissions/new', kinds: admins },
  { id: 'admin-roles', path: '/ui/admin/roles', kinds: admins },
  { id: 'admin-tokens', path: '/ui/admin/tokens', kinds: admins, walk: filtersOpen },
  { id: 'admin-ci', path: '/ui/admin/ci', kinds: admins },
  { id: 'admin-ci-new', path: '/ui/admin/ci/new', kinds: admins },
  { id: 'admin-ci-issuances', path: '/ui/admin/ci/issuances', kinds: admins, walk: filtersOpen },
  { id: 'admin-bastions', path: '/ui/admin/bastions', kinds: admins },
  { id: 'admin-endpoints', path: '/ui/admin/endpoints', kinds: admins },
  // The endpoint every installation has (R-EP-001).
  { id: 'admin-endpoint', path: '/ui/admin/endpoints/default', kinds: admins },
  { id: 'admin-endpoint-rule-new', path: '/ui/admin/endpoints/default/rules/new', kinds: admins },
  { id: 'admin-endpoint-rule', path: `/ui/admin/endpoints/default/rules/${exposureRule}`, kinds: admins, walk: advancedOpen },

  // The creation flow, step by step, for three formats and every class (the owner's rules of 2026-11-12, CLAUDE.md).
  ...wizards,
  // The upstream of a remote where it is entered: the presets, the credentials, Test connection, the details (R-UI-082).
  ...upstreams,
  { id: 'admin-security', path: '/ui/admin/security', kinds: admins },
  { id: 'admin-audit', path: '/ui/admin/audit', kinds: admins, walk: filtersOpen },
  { id: 'admin-advisory-sources', path: '/ui/admin/advisory-sources', kinds: admins },
  { id: 'admin-system', path: '/ui/admin/system', kinds: admins },
  { id: 'admin-system-jobs', path: '/ui/admin/system/jobs', kinds: admins },
  { id: 'admin-system-backups', path: '/ui/admin/system/backups', kinds: admins },
  { id: 'admin-settings-auth', path: '/ui/admin/settings/auth', kinds: admins },
  { id: 'admin-settings-general', path: '/ui/admin/settings/general', kinds: admins, walk: visibilityOpen },
];
