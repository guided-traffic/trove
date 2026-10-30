import { ClientSetupApi } from '../../api/packages-api';
import { ApiError } from '../../api/problem';
import { RepositoriesApi } from '../../api/repositories-api';
import { SessionApi } from '../../api/session-api';
import { FakeSessionApi } from '../../api/testing/fakes';
import { FakeRepositoriesApi, GENERIC_FORMAT, PACKAGE_FORMAT } from '../../api/testing/fakes-catalog';
import { FakeClientSetupApi, NPM_REPOSITORIES } from '../../api/testing/fakes-packages';
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
  inputs: Record<string, unknown> = { repository: 'npm-all' },
  options: { capabilities?: readonly string[]; anonymous?: boolean; setup?: (get: Opened['get']) => void } = {},
) {
  const r = await renderScreen(ClientSetupPage, {
    inputs,
    capabilities: options.capabilities,
    setup: (get) => {
      const repos = get(RepositoriesApi) as FakeRepositoriesApi;
      repos.formatsResult = [GENERIC_FORMAT, PACKAGE_FORMAT];
      repos.repositories = [...NPM_REPOSITORIES];
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
    const r = await open({});
    expect([...r.root.querySelectorAll('input[name="client"]')].map((i) => (i as HTMLInputElement).value)).toEqual([
      'npm',
      'pnpm',
      'yarn',
      'yarn-classic',
    ]);
    const repositories = [...r.root.querySelectorAll<HTMLInputElement>('input[name="repository"]')].map((i) => i.value);
    expect(repositories).toEqual(['npm-all', 'npm-readonly', 'npm-local', 'npm-remote']);
    const t = text(r.root);
    expect(t).toContain('recommended');
    expect(t).toContain('Read only');
    expect(t).toContain('Publishing goes to its default deployment target');
    await expectAccessible(r.root);
  });

  it('R-UI-016 a link from a repository opens the page prepared: the repository and client come from the query', async () => {
    const r = await open({ repository: 'npm-local', client: 'pnpm' });
    expect(radio(r, 'repository', 'npm-local').checked).toBe(true);
    expect(radio(r, 'client', 'pnpm').checked).toBe(true);
    expect(r.setup.calls.at(-1)).toMatchObject({ repository: 'npm-local', query: { client: 'pnpm' } });
  });

  it('R-UI-016 nothing is requested before a repository is chosen', async () => {
    const r = await open({});
    expect(r.setup.calls).toEqual([]);
  });

  it('R-UI-016 acceptance: the default choice produces a .npmrc with the literal ${TROVE_TOKEN} and shows no secret', async () => {
    const r = await open();
    expect(r.setup.calls.at(-1)?.query.credentials).toBe('env');
    const npmrc = snippets(r)[0]!;
    expect(npmrc).toContain('//trove.example.net/api/npm/npm-all/:_authToken=${TROVE_TOKEN}');
    expect(text(r.root)).not.toMatch(/trv_(pat|sat|cit)_/);
    expect(radio(r, 'credentials', 'env').checked).toBe(true);
    const link = [...r.root.querySelectorAll('a')].find((a) => text(a).includes('Create a token to put into the variable'));
    expect(link?.getAttribute('href')).toBe('/profile/tokens');
  });

  it('R-UI-016 R-UI-017 the npm client shows the registry and credential key with the trailing slash and the install and publish commands', async () => {
    const r = await open();
    expect(snippets(r).some((s) => s.startsWith('registry=https://trove.example.net/api/npm/npm-all/\n'))).toBe(true);
    await tab(r, 'Install');
    expect(snippets(r)).toContain('npm install <package>');
    await tab(r, 'Publish');
    expect(snippets(r)).toContain('npm publish --registry https://trove.example.net/api/npm/npm-local/');
  });

  it('R-UI-017 Yarn Berry gets a .yarnrc.yml with npmRegistryServer, npmAlwaysAuth and npmAuthToken, and its publish registry', async () => {
    const r = await open({ repository: 'npm-all', client: 'yarn' });
    const yml = snippets(r)[0]!;
    expect(yml).toContain('npmRegistryServer: "https://trove.example.net/api/npm/npm-all/"');
    expect(yml).toContain('npmAlwaysAuth: true');
    expect(yml).toContain('npmAuthToken: "${TROVE_TOKEN}"');
    await tab(r, 'Publish');
    expect(snippets(r).some((s) => s.includes('npmPublishRegistry: "https://trove.example.net/api/npm/npm-local/"'))).toBe(true);
  });

  it('R-UI-017 Yarn Classic and pnpm use the .npmrc and their own add and publish commands', async () => {
    const classic = await open({ repository: 'npm-all', client: 'yarn-classic' });
    expect(snippets(classic)[0]).toContain('registry=https://trove.example.net/api/npm/npm-all/');
    await tab(classic, 'Install');
    expect(snippets(classic)).toContain('yarn add <package>');
    await tab(classic, 'Publish');
    expect(snippets(classic)).toContain('yarn publish --registry https://trove.example.net/api/npm/npm-local/');

    const pnpm = await open({ repository: 'npm-all', client: 'pnpm' });
    await tab(pnpm, 'Install');
    expect(snippets(pnpm)).toContain('pnpm add <package>');
  });

  it('R-UI-017 the Publish tab holds the GitLab CI snippet of the generic exchange', async () => {
    const r = await open();
    await tab(r, 'Publish');
    expect(snippets(r).some((s) => s.includes('TROVE_ID_TOKEN'))).toBe(true);
    expect(text(r.root)).toContain('npm 10, pnpm 10 on npm 10 and Yarn 1');
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
      expect(request.description).toMatch(/^Client setup: npm, npm-all, \d{4}-\d{2}-\d{2}$/);
      expect(request.ttlSeconds).toBe(2_592_000);
      expect(request.scope).toEqual({ repositories: [{ repositories: ['npm-all'], actions: ['read'] }] });
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
        { repositories: ['npm-all'], actions: ['read'] },
        { repositories: ['npm-local'], actions: ['read', 'deploy'] },
      ]);

      const readOnly = await open({ repository: 'npm-remote' });
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
      r.fixture.componentRef.setInput('repository', 'npm-local');
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
    const r = await open({ repository: 'npm-all' }, { anonymous: true });
    expect(r.setup.calls.at(-1)?.query.credentials).toBe('none');
    expect(r.root.querySelector('input[name="credentials"]')).toBeNull();
    expect(text(r.root)).toContain('You are not signed in.');
    expect(snippets(r)[0]).not.toContain('_authToken');
    await expectAccessible(r.root);
  });

  it('R-UI-016 a virtual repository without a default deployment target shows an explanation instead of the publish snippets, and links the members for those who may read the configuration', async () => {
    const r = await open({ repository: 'npm-readonly' });
    await tab(r, 'Publish');
    expect(text(r.root)).toContain('no default deployment target');
    expect(snippets(r)).toEqual([]);
    const link = [...r.root.querySelectorAll('a')].find((a) => text(a).includes('see its members'));
    expect(link?.getAttribute('href')).toBe('/admin/repositories/npm-readonly');

    const plain = await open({ repository: 'npm-readonly' }, { capabilities: [] });
    await tab(plain, 'Publish');
    expect([...plain.root.querySelectorAll('a')].some((a) => text(a).includes('see its members'))).toBe(false);
  });

  it('R-UI-016 a repository the user cannot publish to says so on the Publish tab', async () => {
    const r = await open({ repository: 'npm-remote' });
    await tab(r, 'Publish');
    expect(text(r.root)).toContain('You cannot publish to this repository');
  });

  it('R-UI-016 only readable repositories of the chosen format are offered; a user who reads none gets an empty state', async () => {
    const none = await open({}, { setup: (get) => ((get(RepositoriesApi) as FakeRepositoriesApi).repositories = []) });
    expect(text(none.root)).toContain('You can read no repository of this package type.');
    expect(none.root.querySelector('input[name="repository"]')).toBeNull();
  });

  it('R-UI-016 a failing setup answer is shown with a retry', async () => {
    const r = await open({ repository: 'npm-all' }, { setup: (get) => ((get(ClientSetupApi) as FakeClientSetupApi).failure = new ApiError(503, { code: 'unavailable' }, null)) });
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
});
