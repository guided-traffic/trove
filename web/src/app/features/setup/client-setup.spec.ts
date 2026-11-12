import { ClientSetupApi } from '../../api/packages-api';
import { ApiError } from '../../api/problem';
import { RepositoriesApi } from '../../api/repositories-api';
import { SessionApi } from '../../api/session-api';
import { FakeSessionApi } from '../../api/testing/fakes';
import { FakeRepositoriesApi, GENERIC_FORMAT, NUGET_FORMAT, OCI_FORMAT, PACKAGE_FORMAT } from '../../api/testing/fakes-catalog';
import { FakeClientSetupApi, GENERIC_REPOSITORY, NPM_REPOSITORIES, NUGET_REPOSITORIES, OCI_REPOSITORIES } from '../../api/testing/fakes-packages';
import { FakeTokensApi } from '../../api/testing/access-fakes';
import { TokensApi } from '../../api/tokens-api';
import {
  button,
  expectAccessible,
  renderScreen,
  settle,
  storedText,
  text,
  type,
} from '../../testing/screen';
import { ClientSetupPage, fillToken } from './client-setup';
import { base62, crc32 } from './token-shape';

type Opened = Awaited<ReturnType<typeof open>>;

/** A syntactically valid token (prefix, 43 base62 digits, CRC-32) that the server never issued. */
function validToken(fill = 'x'): string {
  const body = 'trv_pat_' + fill.repeat(43);
  return body + base62(crc32(body), 6);
}

async function open(
  inputs: Record<string, unknown> = { repository: 'web/npm-all' },
  options: { capabilities?: readonly string[]; anonymous?: boolean; setup?: (get: Opened['get']) => void } = {},
) {
  const r = await renderScreen(ClientSetupPage, {
    inputs,
    capabilities: options.capabilities,
    setup: (get) => {
      const repos = get(RepositoriesApi) as FakeRepositoriesApi;
      repos.formatsResult = [GENERIC_FORMAT, PACKAGE_FORMAT, NUGET_FORMAT];
      repos.repositories = [...NPM_REPOSITORIES, ...NUGET_REPOSITORIES];
      if (options.anonymous) {
        (get(SessionApi) as FakeSessionApi).whoamiResult = new ApiError(401, { code: 'unauthenticated' }, null);
      }
      options.setup?.(get as Opened['get']);
    },
  });
  return {
    ...r,
    setup: r.get(ClientSetupApi) as FakeClientSetupApi,
    tokens: r.get(TokensApi) as FakeTokensApi,
  };
}

const snippets = (r: Opened) => [...r.root.querySelectorAll<HTMLElement>('.snippet pre')].map((p) => p.textContent ?? '');
const radio = (r: Opened, name: string, value: string) =>
  r.root.querySelector<HTMLInputElement>(`input[name="${name}"][value="${value}"]`)!;

async function choose(r: Opened, name: string, value: string) {
  const input = radio(r, name, value);
  input.checked = true;
  input.dispatchEvent(new Event('change', { bubbles: true }));
  await settle(r.fixture);
}

const tabLabels = (r: Opened) => [...r.root.querySelectorAll<HTMLElement>('[role="tab"]')].map((t) => text(t));

async function tab(r: Opened, label: string) {
  [...r.root.querySelectorAll<HTMLElement>('[role="tab"]')].find((t) => text(t) === label)!.click();
  await settle(r.fixture);
}

describe('Client setup', () => {
  describe('token replacement', () => {
    it('R-UI-016 R-UI-043 the browser replaces every {{token}} of a snippet and nothing else', () => {
      expect(fillToken('a={{token}}\nb={{token}}', 'T')).toBe('a=T\nb=T');
      expect(fillToken('no placeholder', 'T')).toBe('no placeholder');
      expect(fillToken('x={{token}}', null)).toBe('x={{token}}');
    });

    it('R-UI-016 the replacement is literal: $& or $1 in a token do not expand', () => {
      expect(fillToken('x={{token}}', 'a$&b$1')).toBe('x=a$&b$1');
    });
  });

  it('R-UI-016 offers format, client and repository in steps; virtual repositories come first and are marked recommended, each repository states its publish availability', async () => {
    const r = await open({ format: 'npm' });
    expect([...r.root.querySelectorAll('input[name="client"]')].map((i) => (i as HTMLInputElement).value)).toEqual([
      'npm',
      'pnpm',
      'yarn',
      'yarn-classic',
    ]);
    const repositories = [...r.root.querySelectorAll<HTMLInputElement>('input[name="repository"]')].map((i) => i.value);
    expect(repositories).toEqual(['web/npm-all', 'web/npm-readonly', 'web/npm-local', 'web/npm-remote']);
    const t = text(r.root);
    expect(t).toContain('recommended');
    expect(t).toContain('Read only');
    expect(t).toContain('Publishing goes to its default deployment target');
    await expectAccessible(r.root);
  });

  it('R-UI-016 R-GENERIC-016 the generic format is offered with curl, first in the order of the catalog, and its snippets come with their explanations', async () => {
    const withGeneric = (get: Opened['get']) =>
      void (get(RepositoriesApi) as FakeRepositoriesApi).repositories.push({ ...GENERIC_REPOSITORY });
    const first = await open({}, { setup: withGeneric });
    expect([...first.root.querySelectorAll<HTMLInputElement>('input[name="format"]')].map((i) => i.value)).toEqual(['generic', 'npm', 'nuget']);
    expect(radio(first, 'format', 'generic').checked).toBe(true);
    expect([...first.root.querySelectorAll<HTMLInputElement>('input[name="client"]')].map((i) => i.value)).toEqual(['curl']);

    const r = await open({ repository: 'web/generic-local' }, { setup: withGeneric });
    expect(r.root.querySelector('.chosen')?.textContent).toContain('web/generic-local');
    expect(r.setup.calls.at(-1)).toMatchObject({ repository: 'web/generic-local', query: { client: 'curl', credentials: 'env' } });
    const [configure] = snippets(r);
    expect(configure).toContain('trove_curl() {');
    expect(configure).toContain('header = "Authorization: Bearer ${TROVE_TOKEN}"');
    expect(text(r.root)).toContain('hands curl the credential on standard input');
    await tab(r, 'Install');
    expect(snippets(r)).toContain('trove_curl --fail --remote-name "https://trove.example.net/files/web/generic-local/<path>"\n');
    await tab(r, 'Publish');
    expect(snippets(r)).toContain('trove_curl --fail --upload-file <file> "https://trove.example.net/files/web/generic-local/<path>"\n');
    expect(text(r.root)).toContain('also needs the overwrite permission');
    await expectAccessible(r.root);

    await choose(r, 'credentials', 'create');
    expect(snippets(r)[0]).toContain('header = "Authorization: Bearer {{token}}"');
  });

  it('R-UI-016 a link from a repository opens the page prepared: the repository and client come from the query, and are not asked again', async () => {
    const r = await open({ repository: 'web/npm-local', client: 'pnpm' });
    // The format and the repository are chosen by the link (Q-UI-316): one line, with Change.
    expect(text(r.root.querySelector('.chosen')!)).toContain('web/npm-local');
    expect(r.root.querySelector('input[name="repository"]')).toBeNull();
    // The client is the one choice, a group named Client without a step number (the journeys find it by that name).
    const group = radio(r, 'client', 'pnpm').closest('fieldset')!;
    expect(text(group.querySelector('legend')!)).toBe('Client');
    expect(r.root.querySelectorAll('.step-no').length).toBe(0);
    expect(radio(r, 'client', 'pnpm').checked).toBe(true);
    expect(r.setup.calls.at(-1)).toMatchObject({ repository: 'web/npm-local', query: { client: 'pnpm' } });
    [...r.root.querySelectorAll<HTMLButtonElement>('.chosen button')].find((b) => text(b) === 'Change')!.click();
    await settle(r.fixture);
    expect(radio(r, 'repository', 'web/npm-local').checked).toBe(true);
  });

  it('R-UI-016 nothing is requested before a repository is chosen', async () => {
    const r = await open({});
    expect(r.setup.calls).toEqual([]);
  });

  it('R-UI-016 acceptance: the default choice produces a .npmrc with the literal ${TROVE_TOKEN} and shows no secret', async () => {
    const r = await open();
    expect(r.setup.calls.at(-1)?.query.credentials).toBe('env');
    const npmrc = snippets(r)[0]!;
    expect(npmrc).toContain('//trove.example.net/api/npm/web/npm-all/:_authToken=${TROVE_TOKEN}');
    expect(text(r.root)).not.toMatch(/trv_(pat|sat|cit)_/);
    expect(radio(r, 'credentials', 'env').checked).toBe(true);
    const link = [...r.root.querySelectorAll('a')].find((a) => text(a).includes('Create a token to put into the variable'));
    expect(link?.getAttribute('href')).toBe('/profile/tokens');
  });

  it('R-UI-016 R-UI-017 the npm client shows the registry and credential key with the trailing slash and the install and publish commands', async () => {
    const r = await open();
    expect(snippets(r).some((s) => s.startsWith('registry=https://trove.example.net/api/npm/web/npm-all/\n'))).toBe(true);
    await tab(r, 'Install');
    expect(snippets(r)).toContain('npm install <package>');
    await tab(r, 'Publish');
    expect(snippets(r)).toContain('npm publish --registry https://trove.example.net/api/npm/web/npm-all/\n');
  });

  it('R-UI-017 Yarn Berry gets a .yarnrc.yml with npmRegistryServer, npmAlwaysAuth and npmAuthToken, and its publish registry', async () => {
    const r = await open({ repository: 'web/npm-all', client: 'yarn' });
    const yml = snippets(r)[0]!;
    expect(yml).toContain('npmRegistryServer: "https://trove.example.net/api/npm/web/npm-all/"');
    expect(yml).toContain('npmAlwaysAuth: true');
    expect(yml).toContain('npmAuthToken: "${TROVE_TOKEN}"');
    await tab(r, 'Publish');
    expect(snippets(r).some((s) => s.includes('npmPublishRegistry: "https://trove.example.net/api/npm/web/npm-all/"'))).toBe(true);
  });

  it('R-UI-017 Yarn Classic and pnpm use the .npmrc and their own add and publish commands', async () => {
    const classic = await open({ repository: 'web/npm-all', client: 'yarn-classic' });
    expect(snippets(classic)[0]).toContain('registry=https://trove.example.net/api/npm/web/npm-all/');
    await tab(classic, 'Install');
    expect(snippets(classic)).toContain('yarn add <package>');
    await tab(classic, 'Publish');
    expect(snippets(classic)).toContain('yarn publish --registry https://trove.example.net/api/npm/web/npm-all/\n');

    const pnpm = await open({ repository: 'web/npm-all', client: 'pnpm' });
    await tab(pnpm, 'Install');
    expect(snippets(pnpm)).toContain('pnpm add <package>');
  });

  it('R-UI-016 each snippet shows its target file name and has a copy button; whole configuration files can be downloaded', async () => {
    const r = await open();
    const first = r.root.querySelector('.snippet')!;
    expect(text(first.querySelector('.target')!)).toBe('File: .npmrc');
    expect(first.querySelector('trove-copy-button button')?.getAttribute('aria-label')).toBe('Copy snippet');
    expect(text(first)).toContain('Download');
    const cli = [...r.root.querySelectorAll('.snippet')].find((s) => text(s).includes('npm config set'))!;
    expect(cli.querySelector('.target')).toBeNull();
    expect(text(cli)).not.toContain('Download');
  });

  it('R-UI-016 R-UI-043 a download goes through a Blob URL that is revoked right after the click', async () => {
    const created: Blob[] = [];
    const revoked: string[] = [];
    URL.createObjectURL = (blob: Blob | MediaSource) => {
      created.push(blob as Blob);
      return 'blob:trove/test';
    };
    URL.revokeObjectURL = (url: string) => void revoked.push(url);
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined);
    try {
      const r = await open();
      button(r.root.querySelector('.snippet')!, 'Download').click();
      expect(created).toHaveLength(1);
      expect(await created[0]!.text()).toContain('${TROVE_TOKEN}');
      expect(click).toHaveBeenCalledOnce();
      expect(revoked).toEqual(['blob:trove/test']);
    } finally {
      click.mockRestore();
    }
  });

  describe('create a token for this setup', () => {
    it('R-UI-016 R-UI-043 creates a read-only token scoped to the repository with the lifetime of the server, shows it once and writes it into the snippets', async () => {
      const r = await open();
      await choose(r, 'credentials', 'create');
      expect(r.setup.calls.at(-1)?.query.credentials).toBe('placeholder');
      expect(snippets(r)[0]).toContain(':_authToken={{token}}');
      expect(text(r.root)).toContain('Until a token is set');

      button(r.root, 'Create token').click();
      await settle(r.fixture);

      const request = r.tokens.lastRequest!;
      expect(request.description).toMatch(/^Client setup: npm, web\/npm-all, \d{4}-\d{2}-\d{2}$/);
      expect(request.ttlSeconds).toBe(2_592_000);
      expect(request.scope).toEqual({ repositories: [{ repositories: ['web/npm-all'], actions: ['read'] }] });
      const shown = r.root.querySelector('.token-value')?.textContent ?? '';
      expect(shown).toMatch(/^trv_pat_/);
      expect(snippets(r)[0]).toContain(`:_authToken=${shown}`);
      expect(snippets(r).join('')).not.toContain('{{token}}');
      expect(text(r.root)).toContain('This token is shown once.');
      expect(r.root.textContent).not.toContain('Until a token is set');
      await expectAccessible(r.root);
    });

    it('R-UI-016 the token may also publish to the deployment target, but only when the user asks and the answer names a target', async () => {
      const r = await open();
      await choose(r, 'credentials', 'create');
      const withPublish = [...r.root.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')].find((i) =>
        text(i.parentElement!).includes('may also publish'),
      )!;
      withPublish.checked = true;
      withPublish.dispatchEvent(new Event('change', { bubbles: true }));
      await settle(r.fixture);
      button(r.root, 'Create token').click();
      await settle(r.fixture);
      expect(r.tokens.lastRequest?.scope?.repositories).toEqual([
        { repositories: ['web/npm-all'], actions: ['read'] },
        { repositories: ['web/npm-local'], actions: ['read', 'deploy'] },
      ]);

      const readOnly = await open({ repository: 'web/npm-remote' });
      await choose(readOnly, 'credentials', 'create');
      expect(text(readOnly.root)).not.toContain('may also publish');
    });

    it('R-UI-016 R-UI-043 the value is in no storage and no URL; another repository, another mode or leaving the page discards it', async () => {
      const r = await open();
      await choose(r, 'credentials', 'create');
      button(r.root, 'Create token').click();
      await settle(r.fixture);
      const token = r.root.querySelector('.token-value')!.textContent!;
      expect(token).toMatch(/^trv_/);
      expect(storedText()).not.toContain('trv_');
      expect(location.href).not.toContain('trv_');

      // The router binds the query parameter; the spec sets the input the router would set.
      r.fixture.componentRef.setInput('repository', 'web/npm-local');
      await settle(r.fixture);
      expect(r.root.querySelector('.token-value')).toBeNull();
      expect(r.root.textContent).not.toContain(token);
      button(r.root, 'Create token').click();
      await settle(r.fixture);
      const second = r.root.querySelector('.token-value')!.textContent!;
      await choose(r, 'credentials', 'env');
      expect(r.root.textContent).not.toContain(second);

      await choose(r, 'credentials', 'create');
      button(r.root, 'Create token').click();
      await settle(r.fixture);
      const third = r.root.querySelector('.token-value')!.textContent!;
      // Leaving the route destroys the component, which forgets the value (the fixture's element
      // may outlive destroy() in the test document, so the component's own signal is asked).
      const held = r.fixture.componentInstance as unknown as { token: () => string | null };
      expect(held.token()).toBe(third);
      r.fixture.destroy();
      expect(held.token()).toBeNull();
      expect(storedText()).not.toContain(third);
    });

    it('R-UI-042 R-UI-043 a token created for one repository never fills the snippets of another, and is revoked', async () => {
      const r = await open();
      await choose(r, 'credentials', 'create');
      // The answer is held back until the repository has changed.
      let release!: () => void;
      const gate = new Promise<void>((resolve) => (release = resolve));
      const create = r.tokens.create.bind(r.tokens);
      r.tokens.create = async (request, key) => {
        await gate;
        return create(request, key);
      };
      button(r.root, 'Create token').click();
      await settle(r.fixture);

      r.fixture.componentRef.setInput('repository', 'web/npm-local');
      await settle(r.fixture);
      release();
      await settle(r.fixture);

      expect(r.root.querySelector('.token-value')).toBeNull();
      const held = r.fixture.componentInstance as unknown as { token: () => string | null };
      expect(held.token()).toBeNull();
      expect(r.tokens.calls.filter((c) => c.startsWith('revoke '))).toHaveLength(1);
      expect(r.tokens.lastRequest?.scope?.repositories?.[0]?.repositories).toEqual(['web/npm-all']);
    });

    it('R-UI-016 a failed creation (recent sign-in needed, no right) is shown and no token appears', async () => {
      const r = await open();
      await choose(r, 'credentials', 'create');
      r.tokens.failure = new ApiError(403, { code: 'forbidden' }, 'rid');
      button(r.root, 'Create token').click();
      await settle(r.fixture);
      expect(r.root.querySelector('[role="alert"]')).not.toBeNull();
      expect(r.root.querySelector('.token-value')).toBeNull();
    });

    it('R-UI-042 a double click creates one token', async () => {
      const r = await open();
      await choose(r, 'credentials', 'create');
      const create = button(r.root, 'Create token');
      create.click();
      create.click();
      await settle(r.fixture);
      expect(r.tokens.keys).toHaveLength(1);
    });
  });

  describe('use a token I already have', () => {
    async function paste(r: Opened, value: string) {
      await choose(r, 'credentials', 'paste');
      type(r.root.querySelector<HTMLInputElement>('#setup-token')!, value);
      await settle(r.fixture);
    }

    it('R-UI-016 R-UI-043 the field is a password field with a reveal toggle, autocomplete off and no spellcheck', async () => {
      const r = await open();
      await choose(r, 'credentials', 'paste');
      const field = r.root.querySelector<HTMLInputElement>('#setup-token')!;
      expect(field.type).toBe('password');
      expect(field.getAttribute('autocomplete')).toBe('off');
      expect(field.getAttribute('spellcheck')).toBe('false');
      const toggle = button(r.root, 'Show');
      expect(toggle.getAttribute('aria-pressed')).toBe('false');
      toggle.click();
      await settle(r.fixture);
      expect(r.root.querySelector<HTMLInputElement>('#setup-token')!.type).toBe('text');
      expect(button(r.root, 'Hide').getAttribute('aria-pressed')).toBe('true');
      await expectAccessible(r.root);
    });

    it('R-UI-016 a valid token is written into the snippets and sent nowhere: no request to the token service, none to the setup service with it', async () => {
      const r = await open();
      const token = validToken();
      const callsBefore = r.setup.calls.length;
      await paste(r, token);
      expect(snippets(r)[0]).toContain(`:_authToken=${token}`);
      expect(text(r.root)).toContain('looks like a trove token');
      expect(r.tokens.calls).toEqual([]);
      expect(r.tokens.keys).toEqual([]);
      // The setup answer was asked once more for the other credential mode, never with the value.
      for (const c of r.setup.calls.slice(callsBefore)) expect(JSON.stringify(c)).not.toContain(token);
    });

    it('R-UI-016 acceptance: a token with a wrong checksum shows a validation error and sends no request', async () => {
      const r = await open();
      const good = validToken();
      const bad = good.slice(0, -1) + (good.endsWith('a') ? 'b' : 'a');
      const callsBefore = r.setup.calls.length;
      await paste(r, bad);
      expect(text(r.root)).toContain('The checksum does not match.');
      expect(r.root.querySelector('#setup-token')?.getAttribute('aria-invalid')).toBe('true');
      expect(snippets(r)[0]).toContain(':_authToken={{token}}');
      expect(snippets(r).join('')).not.toContain(bad);
      expect(r.tokens.calls).toEqual([]);
      for (const c of r.setup.calls.slice(callsBefore)) expect(JSON.stringify(c)).not.toContain(bad);
      await expectAccessible(r.root);
    });

    it('R-UI-016 names what is wrong with a pasted value: prefix, length, characters', async () => {
      const r = await open();
      await paste(r, 'ghp_' + 'a'.repeat(36));
      expect(text(r.root)).toContain('starts with trv_pat_, trv_sat_ or trv_cit_');
      await paste(r, validToken().slice(0, 40));
      expect(text(r.root)).toContain('57 characters');
      await paste(r, 'trv_pat_' + '-'.repeat(49));
      expect(text(r.root)).toContain('only letters and digits');
    });

    it('R-UI-043 a pasted value is in no storage, and a value that fails the check is not kept at all', async () => {
      const r = await open();
      const token = validToken('q');
      await paste(r, token);
      expect(storedText()).not.toContain(token);
      expect(storedText()).not.toContain('trv_');
      await paste(r, token.slice(0, -1));
      expect(snippets(r).join('')).not.toContain(token.slice(0, -1));
    });

    it('R-UI-043 the pasted value is gone after switching to another mode and back', async () => {
      const r = await open();
      await paste(r, validToken());
      await choose(r, 'credentials', 'env');
      await choose(r, 'credentials', 'paste');
      expect(r.root.querySelector<HTMLInputElement>('#setup-token')!.value).toBe('');
    });
  });

  it('R-UI-016 an anonymous viewer skips the credentials step and gets snippets without authentication lines', async () => {
    const r = await open({ repository: 'web/npm-all' }, { anonymous: true });
    expect(r.setup.calls.at(-1)?.query.credentials).toBe('none');
    expect(r.root.querySelector('input[name="credentials"]')).toBeNull();
    expect(text(r.root)).toContain('You are not signed in.');
    expect(snippets(r)[0]).not.toContain('_authToken');
    await expectAccessible(r.root);
  });

  it('R-UI-016 a virtual repository without a default deployment target shows who can change it the reason instead of the publish snippets, and links the members', async () => {
    const r = await open({ repository: 'web/npm-readonly' });
    await tab(r, 'Publish');
    expect(text(r.root)).toContain('no default deployment target');
    expect(snippets(r)).toEqual([]);
    const link = [...r.root.querySelectorAll('a')].find((a) => text(a).includes('see its members'));
    expect(link?.getAttribute('href')).toBe('/projects/web/repositories/npm-readonly/settings');

    // Who can neither publish nor change the repository gets no Publish tab (Q-UI-316).
    const plain = await open({ repository: 'web/npm-readonly' }, { capabilities: [] });
    expect(tabLabels(plain)).toEqual(['Configure', 'Install']);
  });

  it('R-UI-016 a repository the user cannot publish to has no Publish tab', async () => {
    const r = await open({ repository: 'web/npm-remote' });
    expect(tabLabels(r)).toEqual(['Configure', 'Install']);
    expect(text(r.root)).not.toContain('You cannot publish');
  });

  it('R-UI-016 the credential is one line with Create a token beside it; the other ways are More options, closed until opened', async () => {
    const r = await open({ repository: 'web/npm-local' });
    expect(text(r.root.querySelector('.credential-line')!)).toContain('The snippets read your token from the variable TROVE_TOKEN.');
    const more = r.root.querySelector('trove-disclosure.more-options')!;
    const toggle = more.querySelector<HTMLButtonElement>('button[aria-expanded]')!;
    expect(text(toggle)).toContain('More options');
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
    expect(more.querySelector('.collapse-region')?.classList).not.toContain('open');
    expect(more.querySelector('input[name="credentials"][value="create"]')).toBeTruthy();
    toggle.click();
    await settle(r.fixture);
    await choose(r, 'credentials', 'paste');
    expect(text(r.root.querySelector('.credential-line')!)).toContain('The snippets hold the token you pasted.');
    expect(toggle.getAttribute('aria-expanded')).toBe('true');
    await expectAccessible(r.root);
  });

  it('R-UI-016 only readable repositories of the chosen format are offered; a user who reads none gets an empty state', async () => {
    const none = await open({}, { setup: (get) => ((get(RepositoriesApi) as FakeRepositoriesApi).repositories = []) });
    expect(text(none.root)).toContain('You can read no repository of this package type.');
    expect(none.root.querySelector('input[name="repository"]')).toBeNull();
  });

  it('R-UI-016 a failing setup answer is shown with a retry', async () => {
    const r = await open({ repository: 'web/npm-all' }, { setup: (get) => ((get(ClientSetupApi) as FakeClientSetupApi).failure = new ApiError(503, { code: 'unavailable' }, null)) });
    expect(text(r.root)).toContain('temporarily unavailable');
    button(r.root, 'Retry').click();
    await settle(r.fixture);
    expect(snippets(r).length).toBeGreaterThan(0);
  });

  it('R-UI-016 every snippet with a known id carries a translated explanation', async () => {
    const r = await open();
    expect(text(r.root)).toContain('Save this as .npmrc in your project.');
    await tab(r, 'Install');
    expect(text(r.root)).toContain('Installs a package through this repository');
  });

  describe('NuGet', () => {
    it('R-UI-018 NuGet is offered with the .NET CLI, nuget.exe and Visual Studio, in the order of the table', async () => {
      const r = await open({ format: 'nuget' });
      expect([...r.root.querySelectorAll<HTMLInputElement>('input[name="client"]')].map((i) => i.value)).toEqual([
        'dotnet',
        'nuget',
        'visual-studio',
      ]);
      expect([...r.root.querySelectorAll<HTMLInputElement>('input[name="repository"]')].map((i) => i.value)).toEqual([
        'web/nuget-all',
        'web/nuget-local',
        'web/nuget-remote',
      ]);
      await expectAccessible(r.root);
    });

    it('R-UI-018 R-NUGET-026 the .NET CLI source uses the index.json URL, the environment reference and the clear-text flag; the nuget.config clears the other sources', async () => {
      const r = await open({ repository: 'web/nuget-all', client: 'dotnet' });
      expect(r.setup.calls.at(-1)).toMatchObject({ repository: 'web/nuget-all', query: { client: 'dotnet', credentials: 'env' } });
      const [shell, powershell, config] = snippets(r);
      expect(shell).toContain('dotnet nuget add source https://trove.example.net/api/nuget/web/nuget-all/v3/index.json --name trove');
      expect(shell).toContain('--password "${TROVE_TOKEN}" --store-password-in-clear-text');
      expect(powershell).toContain('--password "$env:TROVE_TOKEN"');
      expect(config).toContain('<clear />');
      expect(config).toContain('protocolVersion="3"');
      expect(config).toContain('<add key="ClearTextPassword" value="%TROVE_TOKEN%" />');
      expect(text(r.root)).toContain('stored in clear text');
      expect(text(r.root)).toContain('removes all other package sources');
      expect(text(r.root)).not.toMatch(/trv_(pat|sat|cit)_/);
      await expectAccessible(r.root);
    });

    it('R-UI-018 install and publish: the push names the repository the caller chose, with the token as the API key', async () => {
      const r = await open({ repository: 'web/nuget-all', client: 'dotnet' });
      await tab(r, 'Install');
      expect(snippets(r)[0]).toContain('dotnet add package <package>');
      await tab(r, 'Publish');
      expect(snippets(r)[0]).toContain(
        'dotnet nuget push bin/Release/Acme.Widgets.1.2.3.nupkg --source https://trove.example.net/api/nuget/web/nuget-all/v3/index.json --api-key "${TROVE_TOKEN}"',
      );
      expect(text(r.root)).toContain('The token travels as the API key.');
    });

    it('R-UI-018 nuget.exe and Visual Studio each get their own source instructions', async () => {
      const exe = await open({ repository: 'web/nuget-local', client: 'nuget' });
      expect(snippets(exe)[0]).toContain('nuget sources add -Name trove -Source https://trove.example.net/api/nuget/web/nuget-local/v3/index.json');
      await tab(exe, 'Publish');
      expect(snippets(exe)[0]).toContain('nuget push Acme.Widgets.1.2.3.nupkg -Source');

      const vs = await open({ repository: 'web/nuget-local', client: 'visual-studio' });
      expect(snippets(vs)[0]).toContain('<add key="trove" value="https://trove.example.net/api/nuget/web/nuget-local/v3/index.json" protocolVersion="3" />');
      await tab(vs, 'Install');
      expect(snippets(vs)[0]).toContain('<PackageReference Include="<package>" />');
      await expectAccessible(vs.root);
    });

    it('R-UI-018 a remote NuGet repository cannot be published to: no Publish tab', async () => {
      const r = await open({ repository: 'web/nuget-remote', client: 'dotnet' });
      expect(tabLabels(r)).toEqual(['Configure', 'Install']);
    });
  });

  describe('OCI', () => {
    const oci = (inputs: Record<string, unknown>, options: { anonymous?: boolean } = {}) =>
      open(inputs, {
        ...options,
        setup: (get) => {
          const repos = get(RepositoriesApi) as FakeRepositoriesApi;
          repos.formatsResult = [GENERIC_FORMAT, PACKAGE_FORMAT, NUGET_FORMAT, OCI_FORMAT];
          repos.repositories = [...NPM_REPOSITORIES, ...OCI_REPOSITORIES];
        },
      });

    it('R-UI-016 R-OCI-030 OCI is offered with the Docker CLI and Podman, in the order of the table', async () => {
      const r = await oci({ format: 'oci' });
      expect([...r.root.querySelectorAll<HTMLInputElement>('input[name="client"]')].map((i) => i.value)).toEqual(['docker', 'podman']);
      expect([...r.root.querySelectorAll<HTMLInputElement>('input[name="repository"]')].map((i) => i.value)).toEqual(['web/oci-local']);
      expect(text(r.root)).toContain('Docker CLI (with Buildx)');
      await expectAccessible(r.root);
    });

    it('R-UI-016 R-OCI-030 the login reads the token from standard input, names the image prefix and not the registry URL, and the Kubernetes secret follows', async () => {
      const r = await oci({ repository: 'web/oci-local', client: 'docker' });
      expect(r.setup.calls.at(-1)).toMatchObject({ repository: 'web/oci-local', query: { client: 'docker', credentials: 'env' } });
      const [login, secret] = snippets(r);
      expect(login).toContain(`printf '%s' "\${TROVE_TOKEN}" | docker login trove.example.net/web/oci-local --username trove --password-stdin`);
      expect(login).not.toContain('/v2');
      expect(secret).toContain('kubectl create secret generic trove-registry --type=kubernetes.io/dockerconfigjson');
      const t = text(r.root);
      expect(t).toContain('A token is read from standard input');
      expect(t).toContain('creates an image pull secret that holds the token');
      expect(t).not.toMatch(/trv_(pat|sat|cit)_/);
      await expectAccessible(r.root);
    });

    it('R-UI-016 R-OCI-030 install pulls by tag, publish tags and pushes, and Buildx pushes a multi-platform image', async () => {
      const r = await oci({ repository: 'web/oci-local', client: 'docker' });
      await tab(r, 'Install');
      expect(snippets(r)[0]).toContain('docker pull trove.example.net/web/oci-local/<package>');
      expect(text(r.root)).toContain('Replace <package> with the image name.');
      await tab(r, 'Publish');
      const [push, buildx] = snippets(r);
      expect(push).toContain('docker tag myapp:1.0 trove.example.net/web/oci-local/myapp:1.0');
      expect(push).toContain('docker push trove.example.net/web/oci-local/myapp:1.0');
      expect(buildx).toContain('docker buildx build --platform linux/amd64,linux/arm64');
      expect(text(r.root)).toContain('Names a local image below this repository');
      expect(text(r.root)).toContain('one multi-platform image');
    });

    it('R-UI-016 R-OCI-030 Podman has its own login and push and no Buildx or Kubernetes snippet', async () => {
      const r = await oci({ repository: 'web/oci-local', client: 'podman' });
      expect(snippets(r)).toHaveLength(1);
      expect(snippets(r)[0]).toContain('podman login trove.example.net/web/oci-local --username trove --password-stdin');
      await tab(r, 'Publish');
      expect(snippets(r)).toHaveLength(1);
      expect(snippets(r)[0]).toContain('podman push trove.example.net/web/oci-local/myapp:1.0');
    });

    it('R-UI-016 R-OCI-030 an anonymous viewer gets the interactive login and a comment in place of the pull secret, and no token line', async () => {
      const r = await oci({ repository: 'web/oci-local', client: 'docker' }, { anonymous: true });
      expect(r.setup.calls.at(-1)?.query.credentials).toBe('none');
      const [login, secret] = snippets(r);
      expect(login).toBe('docker login trove.example.net/web/oci-local --username trove\n');
      expect(secret).toBe('# An image pull secret holds a credential: ask for the snippet with one.\n');
      expect(text(r.root)).toContain('without a token the client asks for the password');
      expect(text(r.root)).toContain('A snippet without a token is only a comment');
      expect(snippets(r).join('')).not.toContain('--password-stdin');
      await expectAccessible(r.root);
    });
  });
});
