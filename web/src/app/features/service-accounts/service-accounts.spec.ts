import { IdentityApi } from '../../api/identity-api';
import { TokensApi } from '../../api/tokens-api';
import { FakeIdentityApi, FakeTokensApi, SECRET_VALUE, problem, sampleToken } from '../../api/testing/access-fakes';
import { AuthzMe, SessionApi } from '../../api/session-api';
import { FakeSessionApi } from '../../api/testing/fakes';
import { buttonByText, renderScreen, settle, storedText, text, typeInto, sleep } from '../../testing/screen';
import { seriousViolations } from '../../testing/axe';
import { ServiceAccountDetail } from './service-account-detail';
import { ServiceAccountList } from './service-account-list';

const overlay = (): HTMLElement => document.querySelector('.cdk-overlay-container') as HTMLElement;

describe('Service accounts (R-UI-026)', () => {
  it('R-UI-026 lists name, description and status with the hint when to use one, and passes axe', async () => {
    const { root } = await renderScreen(ServiceAccountList);
    expect(text(root)).toContain('CI systems that cannot sign in through OpenID Connect');
    const rows = root.querySelectorAll('tbody tr');
    expect(rows).toHaveLength(1);
    expect(text(rows[0]!)).toContain('ci-bot');
    expect(text(rows[0]!)).toContain('Active');
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-UI-026 creates a service account with an idempotency key', async () => {
    const { fixture, root, get } = await renderScreen(ServiceAccountList);
    buttonByText(root, 'New service account').click();
    await settle(fixture);
    const dialog = overlay().querySelector('trove-service-account-create-dialog') as HTMLElement;
    typeInto(dialog, 'Name', 'release-bot');
    buttonByText(dialog, 'Create service account').click();
    await settle(fixture);
    const api = get(IdentityApi) as FakeIdentityApi;
    expect(api.calls).toContain('createServiceAccount release-bot');
    expect(api.keys).toHaveLength(1);
  });

  it('R-UI-026 disables an account and asks whether to revoke its tokens (default: keep)', async () => {
    const { fixture, root, get } = await renderScreen(ServiceAccountDetail, { inputs: { name: 'ci-bot' } });
    buttonByText(root, 'Disable').click();
    await settle(fixture);
    buttonByText(overlay().querySelector('trove-confirm-dialog') as HTMLElement, 'Disable').click();
    await sleep(fixture, 300);
    expect((get(IdentityApi) as FakeIdentityApi).calls).toContain('disableServiceAccount ci-bot revokeTokens=false');
    expect(text(root)).toContain('Disabled');
  });

  it('R-API-012 saving the description sends If-Match; a 412 opens the conflict dialog', async () => {
    const { fixture, root, get } = await renderScreen(ServiceAccountDetail, { inputs: { name: 'ci-bot' } });
    const api = get(IdentityApi) as FakeIdentityApi;
    typeInto(root, 'Description', 'Deploy and release jobs');
    await settle(fixture);
    buttonByText(root, 'Save changes').click();
    await settle(fixture);
    expect(api.ifMatch).toEqual(['"1"']);
    api.bumpRevision('service-account:ci-bot');
    api.bumpRevision('service-account:ci-bot');
    typeInto(root, 'Description', 'Something else');
    await settle(fixture);
    buttonByText(root, 'Save changes').click();
    await settle(fixture);
    expect(overlay().querySelector('trove-conflict-dialog')).toBeTruthy();
  });

  it('R-UI-026 issues a token on the Tokens section, shows it once and never stores it', async () => {
    const { fixture, root, get } = await renderScreen(ServiceAccountDetail, { inputs: { name: 'ci-bot' } });
    buttonByText(root, 'Create token').click();
    await settle(fixture);
    typeInto(root.querySelector('trove-token-form')!, 'Description', 'nightly deploy');
    // A service-account token may be non-expiring.
    expect(text(root)).toContain('Never expires');
    buttonByText(root.querySelector('trove-token-form')!, 'Create token').click();
    await settle(fixture);
    const tokens = get(TokensApi) as FakeTokensApi;
    expect(tokens.calls).toContain('createForServiceAccount ci-bot nightly deploy');
    const secret = root.querySelector('trove-token-secret') as HTMLElement;
    expect(secret).toBeTruthy();
    expect((secret.querySelector('input') as HTMLInputElement).value).toBe(SECRET_VALUE);
    expect(text(secret)).toContain('shown only once');
    // R-UI-043: the value is in the component only.
    expect(storedText()).not.toContain('trv_');
    expect(await seriousViolations(root)).toEqual([]);
    buttonByText(secret, 'I have copied it').click();
    await settle(fixture);
    expect(root.querySelector('trove-token-secret')).toBeNull();
    expect(root.innerHTML).not.toContain(SECRET_VALUE);
  });

  it('R-UI-042 a refused create keeps the form and shows the error', async () => {
    const { fixture, root, get } = await renderScreen(ServiceAccountDetail, { inputs: { name: 'ci-bot' } });
    buttonByText(root, 'Create token').click();
    await settle(fixture);
    typeInto(root.querySelector('trove-token-form')!, 'Description', 'x');
    (get(TokensApi) as FakeTokensApi).failure = problem(422, 'ttl_exceeds_maximum');
    buttonByText(root.querySelector('trove-token-form')!, 'Create token').click();
    await settle(fixture);
    expect(text(root)).toContain('longer than the maximum');
    expect(root.querySelector('trove-token-form')).toBeTruthy();
    expect(root.querySelector('trove-token-secret')).toBeNull();
  });

  const asRole = (roles: AuthzMe['roles'], capabilities: string[]) => ({
    inputs: { name: 'ci-bot' },
    capabilities,
    setup: (api: <A>(t: abstract new (...a: never[]) => A) => A) => {
      const me = api(SessionApi) as FakeSessionApi;
      me.meResult = { roles, capabilities };
      (api(TokensApi) as FakeTokensApi).tokenList = [
        sampleToken({ id: 'sat-1', description: 'nightly', type: 'sat', owner: { kind: 'service_account', name: 'ci-bot' } }),
      ];
    },
  });

  it('R-AUTH-027 a security administrator (identities:write, no admin role) sees no Create token or Rotate for a service account', async () => {
    const { root } = await renderScreen(
      ServiceAccountDetail,
      asRole(['security-admin'], ['identities:read', 'identities:write', 'tokens:read', 'tokens:revoke', 'endpoints:read']),
    );
    expect(root.textContent).not.toContain('Create token');
    expect(root.textContent).not.toContain('Rotate');
    // Revoking is a capability (tokens:revoke) the server checks as well.
    expect(root.querySelector('trove-token-table')).toBeTruthy();
  });

  it('R-AUTH-027 the admin role sees Create token and Rotate', async () => {
    const { root } = await renderScreen(
      ServiceAccountDetail,
      asRole(['admin'], ['identities:read', 'identities:write', 'tokens:read', 'tokens:revoke']),
    );
    expect(root.textContent).toContain('Create token');
    expect(root.textContent).toContain('Rotate');
  });

  it('R-UI-026 Q-UI-333 the tokens are a quiet section like the page\'s other sections, with the token list inside', async () => {
    const { root } = await renderScreen(ServiceAccountDetail, { inputs: { name: 'ci-bot' } });
    expect(root.querySelector('section.tokens, h2#sa-tokens')).toBeNull();
    const tokens = [...root.querySelectorAll('trove-disclosure')].find((d) => d.querySelector('.heading')?.textContent?.includes('Tokens'))!;
    expect(tokens.querySelector('trove-token-manager')).toBeTruthy();
  });

  it('R-EP-011 the token form binds a token to the endpoints chosen, and leaves it unbound otherwise', async () => {
    const { fixture, root, get } = await renderScreen(ServiceAccountDetail, { inputs: { name: 'ci-bot' } });
    buttonByText(root, 'Create token').click();
    await settle(fixture);
    const form = root.querySelector('trove-token-form') as HTMLElement;
    expect(text(form)).toContain('Endpoints');
    typeInto(form, 'Description', 'customer acme');
    const boxes = Array.from(form.querySelectorAll<HTMLInputElement>('.endpoints input[type="checkbox"]'));
    expect(boxes.map((b) => b.closest('trove-checkbox')?.textContent?.trim())).toEqual(['default', 'customers']);
    boxes[1]!.click();
    await settle(fixture);
    buttonByText(form, 'Create token').click();
    await settle(fixture);
    expect((get(TokensApi) as FakeTokensApi).lastRequest?.scope).toEqual({ inherit: true, endpoints: ['customers'] });
  });

  describe('Q-UI-220 administrator rights in the token form of a service account', () => {
    const withAccount = (isAdmin: boolean, roles: AuthzMe['roles'], project: string | null = null) => ({
      inputs: project ? { name: 'ci-bot', project } : { name: 'ci-bot' },
      capabilities: ['identities:write', 'tokens:read'],
      setup: (api: <A>(t: abstract new (...a: never[]) => A) => A) => {
        const me = api(SessionApi) as FakeSessionApi;
        me.meResult = { roles, capabilities: ['identities:write', 'tokens:read'] };
        const ids = api(IdentityApi) as FakeIdentityApi;
        ids.accounts = ids.accounts.map((a) => ({ ...a, isAdmin, project }));
      },
    });
    const radios = (root: HTMLElement): string[] =>
      [...root.querySelectorAll('trove-token-form input[type="radio"]')].map((r) => text(r.closest('label')!));

    it('an administrator is offered the Administrator choice for an account that holds the role, and it sends admin with inherit', async () => {
      const { fixture, root, get } = await renderScreen(ServiceAccountDetail, withAccount(true, ['admin']));
      expect(text(root.querySelector('[data-testid="sa-admin"]')!)).toBe('Held by this account');
      buttonByText(root, 'Create token').click();
      await settle(fixture);
      expect(radios(root)[0]).toContain('Administrator: everything on this installation');
      typeInto(root.querySelector('trove-token-form')!, 'Description', 'ops');
      root.querySelector<HTMLInputElement>('trove-token-form input[type="radio"]')!.click();
      await settle(fixture);
      buttonByText(root.querySelector('trove-token-form')!, 'Create token').click();
      await settle(fixture);
      expect((get(TokensApi) as FakeTokensApi).lastRequest?.scope).toEqual({ inherit: true, admin: true });
    });

    it('an account without the role is offered no Administrator choice: it would take effect nowhere', async () => {
      const { fixture, root } = await renderScreen(ServiceAccountDetail, withAccount(false, ['admin']));
      expect(text(root.querySelector('[data-testid="sa-admin"]')!)).toBe('Not held');
      buttonByText(root, 'Create token').click();
      await settle(fixture);
      expect(radios(root).join('|')).not.toContain('Administrator');
    });

    it('a project account holds no system role: no choice and no role line', async () => {
      const { root } = await renderScreen(ServiceAccountDetail, withAccount(true, ['admin'], 'web'));
      expect(root.querySelector('[data-testid="sa-admin"]')).toBeNull();
    });
  });

  it('Q-UI-222 the page is one column: no card, the description edited in place with Save changes, Advanced last', async () => {
    const { fixture, root } = await renderScreen(ServiceAccountDetail, { inputs: { name: 'ci-bot' } });
    expect(root.querySelector('.settings > .card')).toBeNull();
    const save = buttonByText(root, 'Save changes') as HTMLButtonElement;
    expect(save.disabled).toBe(true);
    typeInto(root, 'Description', 'Changed');
    await settle(fixture);
    expect(save.disabled).toBe(false);
    expect(root.querySelector<HTMLInputElement>('input[name="id"]')!.readOnly).toBe(true);
    const headings = [...root.querySelectorAll('.toggle')].map((e) => text(e));
    expect(headings[headings.length - 1]).toMatch(/Advanced$/);
  });

  it('R-EP-011 without endpoints:read the form has no endpoint field', async () => {
    const { fixture, root } = await renderScreen(
      ServiceAccountDetail,
      asRole(['admin'], ['identities:write', 'tokens:read']),
    );
    buttonByText(root, 'Create token').click();
    await settle(fixture);
    expect(text(root.querySelector('trove-token-form')!)).not.toContain('Endpoints');
  });
});
