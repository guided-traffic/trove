import { TokensApi } from '../../api/tokens-api';
import { FakeTokensApi, SECRET_VALUE, sampleToken } from '../../api/testing/access-fakes';
import { Notifier } from '../../core/notifier';
import { buttonByText, openFilters, renderScreen, settle, storedText, text, typeInto, sleep } from '../../testing/screen';
import { seriousViolations } from '../../testing/axe';
import { TokenAdmin } from './token-admin';

const overlay = (): HTMLElement => document.querySelector('.cdk-overlay-container') as HTMLElement;

describe('Token administration (R-UI-030)', () => {
  it('R-UI-030 lists all active tokens with owner, type, scope and state, and passes axe', async () => {
    const { root } = await renderScreen(TokenAdmin);
    const rows = root.querySelectorAll('tbody tr');
    expect(rows).toHaveLength(1);
    expect(text(rows[0]!)).toContain('laptop npm');
    expect(text(rows[0]!)).toContain('alice');
    expect(text(rows[0]!)).toContain('Active');
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-EP-011 a token bound to an endpoint that is not configured says so, and passes axe', async () => {
    const { root } = await renderScreen(TokenAdmin, {
      setup: (api) => {
        (api(TokensApi) as FakeTokensApi).tokenList = [
          sampleToken({ id: 't1', description: 'bound', unknownEndpoints: ['retired-ep'] }),
          sampleToken({ id: 't2', description: 'plain' }),
        ];
      },
    });
    const rows = root.querySelectorAll('tbody tr');
    expect(text(rows[0]!)).toContain('Bound to an endpoint that is not configured:');
    expect(text(rows[0]!)).toContain('retired-ep');
    expect(text(rows[1]!)).not.toContain('not configured');
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('Q-UI-221 Q-AUTH-209 the list of other people\'s tokens says which administrator tokens have inactive rights', async () => {
    const adminScope = { inherit: true, admin: true, repositories: [], system: [] };
    const { root } = await renderScreen(TokenAdmin, {
      setup: (api) => {
        (api(TokensApi) as FakeTokensApi).tokenList = [
          sampleToken({ id: 't1', description: 'lost the role', scope: adminScope, ownerIsAdmin: false }),
          sampleToken({ id: 't2', description: 'still admin', scope: adminScope, ownerIsAdmin: true }),
          sampleToken({ id: 't3', description: 'plain', ownerIsAdmin: false }),
        ];
      },
    });
    const rows = root.querySelectorAll('tbody tr');
    expect(text(rows[0]!)).toContain('Administrator rights inactive: the owner is no administrator now');
    expect(text(rows[1]!)).not.toContain('Administrator rights inactive');
    expect(text(rows[2]!)).not.toContain('Administrator rights inactive');
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-UI-030 the state filter "All" also lists revoked tokens', async () => {
    const { fixture, root } = await renderScreen(TokenAdmin);
    await openFilters(fixture, root);
    typeInto(root, 'State', 'all');
    buttonByText(root, 'Apply filters').click();
    await settle(fixture);
    expect(root.querySelectorAll('tbody tr')).toHaveLength(2);
    expect(text(root)).toContain('Revoked');
  });

  it('R-UI-030 "unused for N days" becomes lastUsedBefore N days ago', async () => {
    const { fixture, root, get } = await renderScreen(TokenAdmin);
    await openFilters(fixture, root);
    typeInto(root, 'Not used for', '90');
    await settle(fixture);
    buttonByText(root, 'Apply filters').click();
    await settle(fixture);
    const before = (get(TokensApi) as FakeTokensApi).lastFilters?.lastUsedBefore;
    expect(before).toBeTruthy();
    const days = (Date.now() - Date.parse(before!)) / 86_400_000;
    expect(days).toBeGreaterThan(89.9);
    expect(days).toBeLessThan(90.1);
  });

  it('R-UI-030 revokes the selected tokens after confirmation', async () => {
    const { fixture, root, get } = await renderScreen(TokenAdmin, {
      setup: (api) => {
        (api(TokensApi) as FakeTokensApi).tokenList = [
          sampleToken({ id: 't1', description: 'one' }),
          sampleToken({ id: 't2', description: 'two' }),
        ];
      },
    });
    const boxes = root.querySelectorAll<HTMLInputElement>('tbody trove-checkbox input');
    boxes[0]!.click();
    boxes[1]!.click();
    await settle(fixture);
    buttonByText(root, 'Revoke selected').click();
    await settle(fixture);
    buttonByText(overlay().querySelector('trove-confirm-dialog') as HTMLElement, 'Revoke').click();
    await sleep(fixture, 300);
    expect((get(TokensApi) as FakeTokensApi).calls).toEqual(['revoke t1', 'revoke t2']);
  });

  it('R-UI-043 revoke by value sends the pasted value once, clears the field and keeps it out of storage', async () => {
    const { fixture, root, get } = await renderScreen(TokenAdmin);
    const field = root.querySelector('input[name="value"]') as HTMLInputElement;
    expect(field.type).toBe('password');
    expect(field.getAttribute('spellcheck')).toBe('false');
    typeInto(root, 'Token value', SECRET_VALUE);
    await settle(fixture);
    buttonByText(root, 'Revoke token').click();
    await settle(fixture);
    expect((get(TokensApi) as FakeTokensApi).calls).toContain(`revokeByValue ${SECRET_VALUE.length}`);
    expect((root.querySelector('input[name="value"]') as HTMLInputElement).value).toBe('');
    expect(storedText()).not.toContain('trv_');
    expect(root.innerHTML).not.toContain(SECRET_VALUE);
  });

  const revokeValue = async (revoked: { id: string; owner: string } | null) => {
    const r = await renderScreen(TokenAdmin, {
      setup: (api) => {
        (api(TokensApi) as FakeTokensApi).revokedByValue = revoked;
      },
    });
    typeInto(r.root, 'Token value', SECRET_VALUE);
    await settle(r.fixture);
    buttonByText(r.root, 'Revoke token').click();
    await settle(r.fixture);
    return r.get(Notifier).notices().map((n) => n.text);
  };

  it('R-UI-043 an administrator who receives the revoked token is told whose token it was', async () => {
    expect(await revokeValue({ id: '00000000-0000-7000-8000-0000000000a1', owner: 'alice' })).toEqual([
      'The token of alice was revoked.',
    ]);
  });

  it('R-UI-043 the empty 202 (unknown value or no admin role, Q-AUTH-6) is not reported as a revocation', async () => {
    const notices = await revokeValue(null);
    expect(notices).toHaveLength(1);
    expect(notices[0]).not.toContain('was revoked');
    expect(notices[0]).toContain('does not say whether');
  });

  it('R-UI-005 without tokens:revoke there are no revoke controls', async () => {
    const { root } = await renderScreen(TokenAdmin, { capabilities: ['tokens:read'] });
    expect(root.textContent).not.toContain('Revoke by value');
    expect(root.querySelector('tbody button')).toBeNull();
  });
});
