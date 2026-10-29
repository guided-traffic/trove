import { IdentityApi } from '../../api/identity-api';
import { TokensApi } from '../../api/tokens-api';
import { FakeIdentityApi, FakeTokensApi, SECRET_VALUE, problem } from '../../api/testing/access-fakes';
import { buttonByText, renderScreen, settle, storedText, textOf, typeInto } from '../../shared/access/testing';
import { seriousViolations } from '../../testing/axe';
import { ServiceAccountDetail } from './service-account-detail';
import { ServiceAccountList } from './service-account-list';

const overlay = (): HTMLElement => document.querySelector('.cdk-overlay-container') as HTMLElement;

describe('Service accounts (R-UI-026)', () => {
  it('R-UI-026 lists name, description and status with the hint when to use one, and passes axe', async () => {
    const { root } = await renderScreen(ServiceAccountList);
    expect(textOf(root)).toContain('CI systems that cannot sign in through OpenID Connect');
    const rows = root.querySelectorAll('tbody tr');
    expect(rows).toHaveLength(1);
    expect(textOf(rows[0]!)).toContain('ci-bot');
    expect(textOf(rows[0]!)).toContain('Active');
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
    await settle(fixture, 300);
    expect((get(IdentityApi) as FakeIdentityApi).calls).toContain('disableServiceAccount ci-bot revokeTokens=false');
    expect(textOf(root)).toContain('Disabled');
  });

  it('R-UI-026 issues a token on the Tokens section, shows it once and never stores it', async () => {
    const { fixture, root, get } = await renderScreen(ServiceAccountDetail, { inputs: { name: 'ci-bot' } });
    buttonByText(root, 'Create token').click();
    await settle(fixture);
    typeInto(root.querySelector('trove-token-form')!, 'Description', 'nightly deploy');
    // A service-account token may be non-expiring.
    expect(textOf(root)).toContain('Never expires');
    buttonByText(root.querySelector('trove-token-form')!, 'Create token').click();
    await settle(fixture);
    const tokens = get(TokensApi) as FakeTokensApi;
    expect(tokens.calls).toContain('createForServiceAccount ci-bot nightly deploy');
    const secret = root.querySelector('trove-token-secret') as HTMLElement;
    expect(secret).toBeTruthy();
    expect((secret.querySelector('input') as HTMLInputElement).value).toBe(SECRET_VALUE);
    expect(textOf(secret)).toContain('shown only once');
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
    expect(textOf(root)).toContain('longer than the maximum');
    expect(root.querySelector('trove-token-form')).toBeTruthy();
    expect(root.querySelector('trove-token-secret')).toBeNull();
  });
});
