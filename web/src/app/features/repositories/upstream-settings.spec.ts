import { TestBed } from '@angular/core/testing';
import { Router } from '@angular/router';
import { ApiError } from '../../api/problem';
import { FormatDescriptor, RepositoriesApi, Repository } from '../../api/repositories-api';
import {
  FakeRepositoriesApi,
  GENERIC_FORMAT,
  NUGET_FORMAT,
  PACKAGE_FORMAT,
  sampleUpstreamTestResult,
} from '../../api/testing/fakes-catalog';
import {
  button,
  expectAccessible,
  renderScreen,
  settle,
  storedText,
  text,
  type,
} from '../../testing/screen';
import { EMPTY_FORM, formOf, maskSecrets, settingsOf } from './repository-model';
import {
  EMPTY_UPSTREAM,
  UpstreamForm,
  initialUpstream,
  upstreamDefaultsOf,
  upstreamOf,
  upstreamTestBody,
} from './upstream-model';
import { RepositoryDetail } from './repository-detail';
import { RepositoryWizard } from './repository-wizard';
import { upstreamPointerLabel, upstreamTarget } from './upstream-settings';

const NPM: FormatDescriptor = {
  ...PACKAGE_FORMAT,
  mirror: true,
  defaultMetadataTtlSeconds: 7200,
  defaultNotFoundTtlSeconds: 1800,
};
/** A format whose remote cannot mirror, with its well-known upstream. */
const NUGET: FormatDescriptor = {
  ...NUGET_FORMAT,
  mirror: false,
  defaultMetadataTtlSeconds: 300,
  defaultNotFoundTtlSeconds: 1800,
};
/** A format with no well-known upstream. */
const PRIVATE_ONLY: FormatDescriptor = { ...NUGET, name: 'nuget', upstreamPresets: [] };

/** The terms of a description list with their descriptions. */
const facts = (root: Element): Record<string, string> =>
  Object.fromEntries([...root.querySelectorAll('dt')].map((dt) => [text(dt), text(dt.nextElementSibling!)]));

const SECRET = 'correct-horse-battery-staple';
const defaults = upstreamDefaultsOf(NPM);
const up = (over: Partial<UpstreamForm>): UpstreamForm => ({ ...initialUpstream(NPM), ...over });

describe('The upstream settings of a remote repository (F-54, R-UI-021, R-REPO-024, R-REPO-033)', () => {
  describe('the pure rules', () => {
    it('R-UI-021 a new remote starts with the format preset and writes nothing of the upstream', () => {
      expect(initialUpstream(NPM).url).toBe('https://registry.npmjs.org/');
      expect(initialUpstream(PRIVATE_ONLY)).toEqual(EMPTY_UPSTREAM);
      expect(upstreamOf(initialUpstream(NPM), defaults, 'sparse')).toEqual({});
      expect(settingsOf({ ...EMPTY_FORM, upstream: initialUpstream(NPM) }, { name: 'npm', schema: null, remote: 'sparse', mirror: false, upstream: defaults })['remote']).toBeUndefined();
    });

    it('R-UI-021 the URL is written when it names another upstream; an address that normalizes to the preset is not', () => {
      expect(upstreamOf(up({ url: 'https://npm.internal.example/' }), defaults, 'sparse')).toEqual({
        url: 'https://npm.internal.example/',
      });
      expect(upstreamOf(up({ url: 'HTTPS://Registry.NPMJS.org:443/' }), defaults, 'sparse')).toEqual({});
      // A format without a preset needs the URL whatever it is.
      expect(upstreamOf({ ...EMPTY_UPSTREAM, url: 'https://x.example/v3/index.json' }, upstreamDefaultsOf(PRIVATE_ONLY), 'sparse')).toEqual({
        url: 'https://x.example/v3/index.json',
      });
      // The edit form always names the stored URL, so the patch does not depend on how a removed URL reads.
      expect(upstreamOf(up({}), defaults, 'structural')).toEqual({
        url: 'https://registry.npmjs.org/',
        cache: {},
        availability: {},
      });
    });

    it('R-REPO-033 a typed password is sent as a value, a stored one is never part of a request, and removing it is null', () => {
      const basic = up({ authMode: 'basic', username: ' ci ', password: SECRET });
      expect(upstreamOf(basic, defaults, 'sparse')).toEqual({
        authMode: 'basic',
        username: 'ci',
        password: { value: SECRET },
      });
      const keep = up({ authMode: 'basic', username: 'ci', passwordStored: 'sealed' });
      expect(upstreamOf(keep, defaults, 'structural')).not.toHaveProperty('password');
      expect(upstreamOf({ ...keep, clearPassword: true }, defaults, 'structural')).toHaveProperty('password', null);
      // Bearer ignores the username; none writes no credential at all.
      expect(upstreamOf(up({ authMode: 'bearer', username: 'x', password: SECRET }), defaults, 'sparse')).toEqual({
        authMode: 'bearer',
        password: { value: SECRET },
      });
      expect(upstreamOf(up({ authMode: 'none', username: 'x', password: SECRET }), defaults, 'sparse')).toEqual({});
    });

    it('R-UI-021 Q-UI-80 the cache periods are written only where they differ from the format default; an offline switch writes availability', () => {
      expect(upstreamOf(up({ metadataTtl: 7200, notFoundTtl: 1800 }), defaults, 'sparse')).toEqual({});
      expect(upstreamOf(up({ metadataTtl: 60, notFoundTtl: null }), defaults, 'sparse')).toEqual({
        cache: { metadataTtlSeconds: 60 },
      });
      // A value that is no number stays text, so the server rejects it at the field.
      expect(upstreamOf(up({ notFoundTtl: 'soon' }), defaults, 'sparse')).toEqual({ cache: { notFoundTtlSeconds: 'soon' } });
      expect(upstreamOf(up({ offline: true }), defaults, 'sparse')).toEqual({ availability: { mode: 'offline' } });
    });

    it('R-REPO-033 a stored document reads back as a password that is set, never as a value', () => {
      const repo = {
        key: 'npm-remote',
        class: 'remote',
        format: 'npm',
        remote: {
          url: 'https://npm.internal.example/',
          authMode: 'basic',
          username: 'ci',
          password: { set: true },
          cache: { metadataTtlSeconds: 60, notFoundTtlSeconds: 1800 },
          availability: { mode: 'offline' },
        },
      } as unknown as Repository;
      expect(formOf(repo).upstream).toEqual({
        url: 'https://npm.internal.example/',
        authMode: 'basic',
        username: 'ci',
        password: '',
        passwordStored: 'sealed',
        clearPassword: false,
        metadataTtl: 60,
        notFoundTtl: 1800,
        offline: true,
      });
      const reference = { ...repo, remote: { ...repo.remote, password: { secretRef: { file: 'npm-token' } } } } as unknown as Repository;
      expect(formOf(reference).upstream.passwordStored).toBe('reference');
    });

    it('R-REPO-024 the test body carries the format and the URL the repository would use', () => {
      expect(upstreamTestBody(EMPTY_UPSTREAM, NPM)).toEqual({
        format: 'npm',
        remote: { url: 'https://registry.npmjs.org/' },
      });
      expect(upstreamTestBody(up({ url: 'https://npm.internal.example/', authMode: 'bearer', password: SECRET }), NPM)).toEqual({
        format: 'npm',
        remote: { url: 'https://npm.internal.example/', authMode: 'bearer', password: { value: SECRET } },
      });
      // A stored password cannot be part of an unsaved test.
      expect(upstreamTestBody(up({ authMode: 'bearer', passwordStored: 'sealed', clearPassword: true }), NPM).remote).not.toHaveProperty('password');
    });

    it('R-UI-043 a request shown on screen never shows a password value', () => {
      const shown = maskSecrets({ remote: { authMode: 'basic', password: { value: SECRET } }, list: [{ password: { value: SECRET } }] });
      expect(JSON.stringify(shown)).not.toContain(SECRET);
      expect(shown.remote.password).toEqual({ value: '<the password>' });
      expect(maskSecrets({ remote: { password: null } })).toEqual({ remote: { password: null } });
    });

    it('R-UI-044 server pointers of the upstream settings map to their control and label; the mirror pointers are not theirs', () => {
      expect(upstreamTarget('/remote/url')).toBe('f-remote-url');
      expect(upstreamTarget('/remote/password/value')).toBe('f-remote-password');
      expect(upstreamTarget('/remote/cache/metadataTtlSeconds')).toBe('f-remote-cache-metadataTtlSeconds');
      expect(upstreamTarget('/remote/availability/mode')).toBe('f-remote-availability-mode');
      expect(upstreamTarget('/remote/extraHosts/0/pattern')).toBe('f-remote-upstream');
      expect(upstreamTarget('/remote/mode')).toBeNull();
      expect(upstreamTarget('/remote/mirror/concurrency')).toBeNull();
      expect(upstreamTarget('/key')).toBeNull();
      expect(upstreamPointerLabel('/remote/url')).toBe('Upstream URL');
      expect(upstreamPointerLabel('/remote/mode')).toBeNull();
    });
  });

  describe('in the wizard', () => {
    type Wizard = Awaited<ReturnType<typeof renderScreen<RepositoryWizard>>>;
    const control = (root: ParentNode, id: string) => root.querySelector<HTMLInputElement>(`#${id}`)!;
    const check = async (r: Wizard, id: string, on = true) => {
      const box = control(r.root, id);
      box.checked = on;
      box.dispatchEvent(new Event('change', { bubbles: true }));
      await settle(r.fixture);
    };
    const tile = (r: Wizard, name: string) =>
      [...r.root.querySelectorAll<HTMLElement>('[role="option"]')].find(
        (t) => t.querySelector('.tile-name')?.textContent?.trim() === name,
      )!;

    async function remoteStep(formats: readonly FormatDescriptor[], format: string): Promise<Wizard> {
      const r = await renderScreen(RepositoryWizard, {
        inputs: { project: 'web' },
        setup: (get) => {
          (get(RepositoriesApi) as FakeRepositoriesApi).formatsResult = formats;
        },
      });
      vi.spyOn(TestBed.inject(Router), 'navigate').mockResolvedValue(true);
      tile(r, format).click();
      await settle(r.fixture);
      button(r.root, 'Next').click();
      await settle(r.fixture);
      r.root.querySelector<HTMLInputElement>('input[name="class"][value="remote"]')!.click();
      await settle(r.fixture);
      button(r.root, 'Next').click();
      await settle(r.fixture);
      type(control(r.root, 'f-key'), 'upstream-cache');
      await settle(r.fixture);
      button(r.root, 'Next').click();
      await settle(r.fixture);
      return r;
    }

    it('R-UI-021 F-54 the remote step shows the upstream settings with the preset URL and the defaults of the format; nothing is written while they stay', async () => {
      const r = await remoteStep([GENERIC_FORMAT, NPM], 'npm');
      const fake = r.get(RepositoriesApi) as FakeRepositoriesApi;
      expect(r.root.querySelector('trove-upstream-settings')).not.toBeNull();
      expect(control(r.root, 'f-remote-url').value).toBe('https://registry.npmjs.org/');
      expect(text(r.root.querySelector('#d-remote-url')!)).toContain('well-known upstream');
      expect(control(r.root, 'f-remote-authMode').value).toBe('none');
      expect(r.root.querySelector('#f-remote-password')).toBeNull();
      expect(control(r.root, 'f-remote-cache-metadataTtlSeconds').value).toBe('');
      expect(text(r.root.querySelector('#d-remote-metadata-ttl')!)).toContain('Default: 7200');
      expect(text(r.root.querySelector('#d-remote-notfound-ttl')!)).toContain('Default: 1800');
      expect(control(r.root, 'f-remote-availability-mode').checked).toBe(false);
      expect(text(r.root.querySelector('nav')!)).toContain('4. Access, upstream, npm and mirror settings');

      button(r.root, 'Next').click();
      await settle(r.fixture);
      expect(text(r.root)).toContain('https://registry.npmjs.org/');
      button(r.root, 'Create repository').click();
      await settle(r.fixture);
      expect((fake.repositories.find((x) => x.key === 'upstream-cache') as Repository).remote).toBeUndefined();
    });

    it('R-UI-021 F-54 a non-default upstream with credentials, periods and the offline switch is written as only what differs; the password is sealed and never shown', async () => {
      const r = await remoteStep([GENERIC_FORMAT, NPM], 'npm');
      const fake = r.get(RepositoriesApi) as FakeRepositoriesApi;
      type(control(r.root, 'f-remote-url'), 'https://npm.internal.example/registry/');
      type(control(r.root, 'f-remote-authMode'), 'basic');
      await settle(r.fixture);
      type(control(r.root, 'f-remote-username'), 'ci-bot');
      type(control(r.root, 'f-remote-password'), SECRET);
      type(control(r.root, 'f-remote-cache-metadataTtlSeconds'), '60');
      type(control(r.root, 'f-remote-cache-notFoundTtlSeconds'), '1800');
      await check(r, 'f-remote-availability-mode');
      expect(control(r.root, 'f-remote-password').type).toBe('password');

      button(r.root, 'Next').click();
      await settle(r.fixture);
      // The review and the request shown on screen name the member, never the secret.
      expect(r.root.textContent).not.toContain(SECRET);
      expect(r.root.textContent).toContain('<the password>');
      expect(storedText()).not.toContain(SECRET);

      button(r.root, 'Create repository').click();
      await settle(r.fixture);
      const created = fake.repositories.find((x) => x.key === 'upstream-cache') as Repository;
      expect(created.remote).toEqual({
        url: 'https://npm.internal.example/registry/',
        authMode: 'basic',
        username: 'ci-bot',
        password: { set: true },
        cache: { metadataTtlSeconds: 60 },
        availability: { mode: 'offline' },
      });
    });

    it('R-UI-021 the password field is write-only: it sits only under an authentication that has one', async () => {
      const r = await remoteStep([GENERIC_FORMAT, NPM], 'npm');
      type(control(r.root, 'f-remote-authMode'), 'bearer');
      await settle(r.fixture);
      expect(r.root.querySelector('#f-remote-username')).toBeNull();
      expect(control(r.root, 'f-remote-password')).not.toBeNull();
      expect(r.root.querySelector('#f-remote-clearPassword')).toBeNull();
      type(control(r.root, 'f-remote-authMode'), 'none');
      await settle(r.fixture);
      expect(r.root.querySelector('#f-remote-password')).toBeNull();
    });

    it('R-UI-021 Test connection posts the unsaved section and shows the result inline, in words', async () => {
      const r = await remoteStep([GENERIC_FORMAT, NPM], 'npm');
      const fake = r.get(RepositoriesApi) as FakeRepositoriesApi;
      type(control(r.root, 'f-remote-url'), 'https://npm.internal.example/');
      type(control(r.root, 'f-remote-authMode'), 'bearer');
      await settle(r.fixture);
      type(control(r.root, 'f-remote-password'), SECRET);
      await settle(r.fixture);
      button(r.root, 'Test connection').click();
      await settle(r.fixture);

      expect(fake.upstreamTests).toEqual([
        {
          format: 'npm',
          remote: { url: 'https://npm.internal.example/', authMode: 'bearer', password: { value: SECRET } },
        },
      ]);
      const result = r.root.querySelector('[data-testid="upstream-test-result"]')!;
      expect(text(result)).toContain('The upstream answered.');
      expect(result.className).toContain('ok');
      const verdict = facts(r.root.querySelector('.test-result')!);
      expect(verdict['HTTP status']).toBe('200');
      expect(verdict['Credentials']).toBe('accepted');
      expect(verdict['Duration']).toBe('184 ms');
      expect(verdict['Certificate']).toContain('CN=registry.npmjs.org');
      expect(r.root.textContent).not.toContain(SECRET);
      expect(storedText()).not.toContain(SECRET);
      await expectAccessible(r.root);
    });

    it('R-UI-021 R-REPO-024 an upstream that cannot be reached is a warning in words with the server reason; refused credentials are named', async () => {
      const r = await remoteStep([GENERIC_FORMAT, NPM], 'npm');
      const fake = r.get(RepositoriesApi) as FakeRepositoriesApi;
      fake.upstreamTestResult = sampleUpstreamTestResult({
        reachable: false,
        httpStatus: null,
        authenticated: null,
        tls: null,
        error: { code: 'dns_error', message: 'npm.internal.example does not resolve' },
      });
      button(r.root, 'Test connection').click();
      await settle(r.fixture);
      let result = r.root.querySelector('[data-testid="upstream-test-result"]')!;
      expect(text(result)).toContain('The upstream could not be reached.');
      expect(result.className).toContain('warn');
      expect(result.className).not.toContain('ok');
      expect(facts(r.root.querySelector('.test-result')!)['Reason']).toBe('npm.internal.example does not resolve dns_error');

      fake.upstreamTestResult = sampleUpstreamTestResult({ httpStatus: 401, authenticated: false });
      button(r.root, 'Test connection').click();
      await settle(r.fixture);
      result = r.root.querySelector('[data-testid="upstream-test-result"]')!;
      expect(text(result)).toContain('refused the credentials');
      expect(result.className).not.toContain('ok');
      expect(facts(r.root.querySelector('.test-result')!)['Credentials']).toBe('refused');
      await expectAccessible(r.root);
    });

    it('R-UI-021 a result goes when the settings change, and a failed test is an error notice', async () => {
      const r = await remoteStep([GENERIC_FORMAT, NPM], 'npm');
      const fake = r.get(RepositoriesApi) as FakeRepositoriesApi;
      button(r.root, 'Test connection').click();
      await settle(r.fixture);
      expect(r.root.querySelector('[data-testid="upstream-test-result"]')).not.toBeNull();
      type(control(r.root, 'f-remote-url'), 'https://other.example/');
      await settle(r.fixture);
      expect(r.root.querySelector('[data-testid="upstream-test-result"]')).toBeNull();

      fake.upstreamTestError = new ApiError(403, { code: 'forbidden' }, null);
      button(r.root, 'Test connection').click();
      await settle(r.fixture);
      expect(r.root.querySelector('.test-result [role="alert"]')?.textContent).toContain('permission');
    });

    it('R-UI-021 a format without a well-known upstream starts with an empty URL and says it is required', async () => {
      const r = await remoteStep([GENERIC_FORMAT, PRIVATE_ONLY], 'NuGet');
      expect(control(r.root, 'f-remote-url').value).toBe('');
      expect(text(r.root.querySelector('#d-remote-url')!)).toContain('no well-known upstream');
      type(control(r.root, 'f-remote-url'), 'https://feed.example.org/v3/index.json');
      await settle(r.fixture);
      const fake = r.get(RepositoriesApi) as FakeRepositoriesApi;
      button(r.root, 'Next').click();
      await settle(r.fixture);
      button(r.root, 'Create repository').click();
      await settle(r.fixture);
      expect((fake.repositories.find((x) => x.key === 'upstream-cache') as Repository).remote).toEqual({
        url: 'https://feed.example.org/v3/index.json',
      });
    });

    it('R-UI-021 a remote of a format that cannot mirror has the upstream settings and no Mirror section', async () => {
      const r = await remoteStep([GENERIC_FORMAT, NUGET], 'NuGet');
      expect(r.root.querySelector('trove-upstream-settings')).not.toBeNull();
      expect(r.root.querySelector('trove-mirror-settings')).toBeNull();
      expect(text(r.root.querySelector('nav')!)).toContain('Access, upstream and NuGet settings');
    });

    it('R-UI-021 Q-UI-122 the mirror bound notice follows the URL entered: the well-known upstream is refused, another one is saved with a warning', async () => {
      const r = await remoteStep([GENERIC_FORMAT, NPM], 'npm');
      const radio = r.root.querySelector<HTMLInputElement>('input[name="remote-mode"][value="mirror"]')!;
      radio.checked = true;
      radio.dispatchEvent(new Event('change', { bubbles: true }));
      await settle(r.fixture);
      expect(text(r.root.querySelector('#mirror-unbounded')!)).toContain('needs a bound');

      type(control(r.root, 'f-remote-url'), 'https://npm.internal.example/');
      await settle(r.fixture);
      expect(text(r.root.querySelector('#mirror-unbounded')!)).toContain('It is saved with a warning');

      type(control(r.root, 'f-remote-url'), 'https://registry.npmjs.org:443/');
      await settle(r.fixture);
      expect(text(r.root.querySelector('#mirror-unbounded')!)).toContain('needs a bound');
    });

    it('R-UI-021 a server error at the upstream URL appears at its field and in the summary, and the wizard stays on that step', async () => {
      const r = await remoteStep([GENERIC_FORMAT, NPM], 'npm');
      const fake = r.get(RepositoriesApi) as FakeRepositoriesApi;
      type(control(r.root, 'f-remote-url'), 'ftp://x');
      await settle(r.fixture);
      button(r.root, 'Next').click();
      await settle(r.fixture);
      fake.create = async () => {
        throw new ApiError(
          422,
          { code: 'validation_failed', errors: [{ pointer: '/remote/url', code: 'invalid_url', detail: 'http or https' }] },
          null,
        );
      };
      button(r.root, 'Create repository').click();
      await settle(r.fixture);
      expect(control(r.root, 'f-remote-url').getAttribute('aria-invalid')).toBe('true');
      const summary = r.root.querySelector<HTMLButtonElement>('[role="alert"] button')!;
      expect(summary.textContent?.trim()).toBe('Upstream URL');
      summary.click();
      expect(document.activeElement).toBe(control(r.root, 'f-remote-url'));
    });

    it('R-UI-021 choosing another format starts the upstream settings afresh with the new preset', async () => {
      const r = await remoteStep([GENERIC_FORMAT, NPM, NUGET], 'npm');
      type(control(r.root, 'f-remote-url'), 'https://other.example/');
      await settle(r.fixture);
      r.root.querySelector<HTMLButtonElement>('.step-link')!.click();
      await settle(r.fixture);
      tile(r, 'NuGet').click();
      await settle(r.fixture);
      button(r.root, 'Next').click();
      await settle(r.fixture);
      r.root.querySelector<HTMLInputElement>('input[name="class"][value="remote"]')!.click();
      await settle(r.fixture);
      button(r.root, 'Next').click();
      await settle(r.fixture);
      button(r.root, 'Next').click();
      await settle(r.fixture);
      expect(control(r.root, 'f-remote-url').value).toBe('https://api.nuget.org/v3/index.json');
    });
  });

  describe('on the edit form', () => {
    const stored = (remote: Record<string, unknown> = {}): Repository =>
      ({
        key: 'npm-remote',
        project: 'web',
        class: 'remote',
        format: 'npm',
        remote: {
          url: 'https://npm.internal.example/',
          authMode: 'basic',
          username: 'ci-bot',
          password: { set: true },
          cache: { metadataTtlSeconds: 60, notFoundTtlSeconds: 1800 },
          availability: { mode: 'online' },
          mode: 'pullThrough',
          mirror: {},
          ...remote,
        },
      }) as unknown as Repository;

    async function open(repo: Repository = stored()) {
      const r = await renderScreen(RepositoryDetail, {
        inputs: { project: repo.project ?? 'web', key: repo.key },
        setup: (get) => {
          const fake = get(RepositoriesApi) as FakeRepositoriesApi;
          fake.formatsResult = [GENERIC_FORMAT, NPM];
          fake.repositories.push(repo);
        },
      });
      [...r.root.querySelectorAll<HTMLElement>('[role="tab"]')]
        .find((t) => t.textContent?.trim() === 'Upstream')
        ?.click();
      await settle(r.fixture);
      return { r, fake: r.get(RepositoriesApi) as FakeRepositoriesApi };
    }
    const control = (root: ParentNode, id: string) => root.querySelector<HTMLInputElement>(`#${id}`)!;
    const doc = (fake: FakeRepositoriesApi) => fake.repositories.find((x) => x.key === 'npm-remote') as Repository;

    it('R-UI-021 F-54 a remote has an Upstream tab with the stored values; a local one has none', async () => {
      const { r } = await open();
      expect(control(r.root, 'f-remote-url').value).toBe('https://npm.internal.example/');
      expect(control(r.root, 'f-remote-authMode').value).toBe('basic');
      expect(control(r.root, 'f-remote-username').value).toBe('ci-bot');
      expect(control(r.root, 'f-remote-cache-metadataTtlSeconds').value).toBe('60');
      expect(control(r.root, 'f-remote-cache-notFoundTtlSeconds').value).toBe('1800');
      expect(control(r.root, 'f-remote-availability-mode').checked).toBe(false);
      expect(r.fixture.componentInstance.hasUnsavedChanges()).toBe(false);
      await expectAccessible(r.root);

      const local = await renderScreen(RepositoryDetail, {
        inputs: { project: 'web', key: 'npm-local' },
        setup: (get) => {
          const fake = get(RepositoriesApi) as FakeRepositoriesApi;
          fake.formatsResult = [GENERIC_FORMAT, NPM];
          fake.repositories.push({ key: 'npm-local', project: 'web', class: 'local', format: 'npm' });
        },
      });
      expect([...local.root.querySelectorAll('[role="tab"]')].map((t) => t.textContent?.trim())).not.toContain('Upstream');
    });

    it('R-REPO-033 the stored password is never read into the field: it is empty, the page says one is stored, and no value of it exists anywhere', async () => {
      const { r } = await open();
      const password = control(r.root, 'f-remote-password');
      expect(password.value).toBe('');
      expect(password.type).toBe('password');
      expect(text(r.root.querySelector('#d-remote-password')!)).toContain('A password is stored and is not shown');
      expect(r.root.textContent).not.toContain(SECRET);
      expect(r.root.querySelector('#f-remote-clearPassword')).not.toBeNull();
    });

    it('R-UI-021 R-REPO-033 changing the URL patches the URL only; leaving the password empty keeps it', async () => {
      const { r, fake } = await open();
      type(control(r.root, 'f-remote-url'), 'https://npm.other.example/');
      await settle(r.fixture);
      button(r.root, 'Save changes').click();
      await settle(r.fixture);
      expect(fake.patches[0]?.patch).toEqual({ remote: { url: 'https://npm.other.example/' } });
      expect(doc(fake).remote?.password).toEqual({ set: true });
    });

    it('R-REPO-033 a typed password is patched as a value and the API tab does not show it; after the save the field is empty again', async () => {
      const { r, fake } = await open();
      type(control(r.root, 'f-remote-password'), SECRET);
      await settle(r.fixture);
      const api = [...r.root.querySelectorAll<HTMLElement>('[role="tab"]')].find((t) => t.textContent?.trim() === 'API')!;
      api.click();
      await settle(r.fixture);
      expect(r.root.textContent).not.toContain(SECRET);
      expect(r.root.textContent).toContain('<the password>');
      button(r.root, 'Save changes').click();
      await settle(r.fixture);
      expect(fake.patches[0]?.patch).toEqual({ remote: { password: { value: SECRET } } });
      expect(doc(fake).remote?.password).toEqual({ set: true });
      expect(r.fixture.componentInstance.hasUnsavedChanges()).toBe(false);
      [...r.root.querySelectorAll<HTMLElement>('[role="tab"]')].find((t) => t.textContent?.trim() === 'Upstream')!.click();
      await settle(r.fixture);
      expect(control(r.root, 'f-remote-password').value).toBe('');
      expect(storedText()).not.toContain(SECRET);
    });

    it('R-REPO-033 Remove the stored password patches the member with null', async () => {
      const { r, fake } = await open();
      const box = control(r.root, 'f-remote-clearPassword');
      box.checked = true;
      box.dispatchEvent(new Event('change', { bubbles: true }));
      await settle(r.fixture);
      button(r.root, 'Save changes').click();
      await settle(r.fixture);
      expect(fake.patches[0]?.patch).toEqual({ remote: { password: null } });
      expect(doc(fake).remote?.password).toBeUndefined();
    });

    it('R-UI-021 Q-UI-80 a period set back to its default and the offline switch are patched as null and as availability', async () => {
      const { r, fake } = await open();
      type(control(r.root, 'f-remote-cache-metadataTtlSeconds'), '7200');
      const offline = control(r.root, 'f-remote-availability-mode');
      offline.checked = true;
      offline.dispatchEvent(new Event('change', { bubbles: true }));
      await settle(r.fixture);
      button(r.root, 'Save changes').click();
      await settle(r.fixture);
      expect(fake.patches[0]?.patch).toEqual({
        remote: { cache: { metadataTtlSeconds: null }, availability: { mode: 'offline' } },
      });
    });

    it('R-UI-021 Test connection from the edit form uses the unsaved section and does not claim to use the stored password', async () => {
      const { r, fake } = await open();
      expect(text(r.root.querySelector('.test')!)).toContain('does not use the stored password');
      button(r.root, 'Test connection').click();
      await settle(r.fixture);
      expect(fake.upstreamTests).toEqual([
        {
          format: 'npm',
          remote: {
            url: 'https://npm.internal.example/',
            authMode: 'basic',
            username: 'ci-bot',
            cache: { metadataTtlSeconds: 60 },
          },
        },
      ]);
      expect(r.root.querySelector('[data-testid="upstream-test-result"]')).not.toBeNull();
    });

    it('R-UI-021 R-UI-044 a server error at the URL appears at its field and in the summary', async () => {
      const { r, fake } = await open();
      fake.patch = async () => {
        throw new ApiError(
          422,
          { code: 'validation_failed', errors: [{ pointer: '/remote/url', code: 'invalid_url', detail: 'bad' }] },
          null,
        );
      };
      type(control(r.root, 'f-remote-url'), 'nope');
      await settle(r.fixture);
      button(r.root, 'Save changes').click();
      await settle(r.fixture);
      expect(control(r.root, 'f-remote-url').getAttribute('aria-invalid')).toBe('true');
      r.root.querySelector<HTMLButtonElement>('[role="alert"] button')!.click();
      expect(document.activeElement).toBe(control(r.root, 'f-remote-url'));
    });

    it('R-UI-021 Q-UI-122 on the edit form the mirror notice follows the URL typed, not only the stored one', async () => {
      const repo = stored({ url: 'https://registry.npmjs.org/', authMode: 'none', mode: 'pullThrough' });
      const { r } = await open(repo);
      [...r.root.querySelectorAll<HTMLElement>('[role="tab"]')].find((t) => t.textContent?.trim() === 'Mirror')!.click();
      await settle(r.fixture);
      const radio = r.root.querySelector<HTMLInputElement>('input[name="remote-mode"][value="mirror"]')!;
      radio.checked = true;
      radio.dispatchEvent(new Event('change', { bubbles: true }));
      await settle(r.fixture);
      expect(text(r.root.querySelector('#mirror-unbounded')!)).toContain('needs a bound');
      [...r.root.querySelectorAll<HTMLElement>('[role="tab"]')].find((t) => t.textContent?.trim() === 'Upstream')!.click();
      await settle(r.fixture);
      type(control(r.root, 'f-remote-url'), 'https://npm.internal.example/');
      await settle(r.fixture);
      [...r.root.querySelectorAll<HTMLElement>('[role="tab"]')].find((t) => t.textContent?.trim() === 'Mirror')!.click();
      await settle(r.fixture);
      expect(text(r.root.querySelector('#mirror-unbounded')!)).toContain('It is saved with a warning');
    });
  });
});
