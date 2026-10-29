import { TokensApi } from '../../api/tokens-api';
import { FakeTokensApi, SECRET_VALUE, sampleToken } from '../../api/testing/access-fakes';
import { buttonByText, renderScreen, settle, storedText, textOf, typeInto } from '../../shared/access/testing';
import { seriousViolations } from '../../testing/axe';
import { TokenAdmin } from './token-admin';

const overlay = (): HTMLElement => document.querySelector('.cdk-overlay-container') as HTMLElement;

describe('Token administration (R-UI-030)', () => {
  it('R-UI-030 lists all active tokens with owner, type, scope and state, and passes axe', async () => {
    const { root } = await renderScreen(TokenAdmin);
    const rows = root.querySelectorAll('tbody tr');
    expect(rows).toHaveLength(1);
    expect(textOf(rows[0]!)).toContain('laptop npm');
    expect(textOf(rows[0]!)).toContain('alice');
    expect(textOf(rows[0]!)).toContain('Active');
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-UI-030 the state filter "All" also lists revoked tokens', async () => {
    const { fixture, root } = await renderScreen(TokenAdmin);
    typeInto(root, 'State', 'all');
    buttonByText(root, 'Apply filters').click();
    await settle(fixture);
    expect(root.querySelectorAll('tbody tr')).toHaveLength(2);
    expect(textOf(root)).toContain('Revoked');
  });

  it('R-UI-030 "unused for N days" becomes lastUsedBefore N days ago', async () => {
    const { fixture, root, get } = await renderScreen(TokenAdmin);
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
    const boxes = root.querySelectorAll<HTMLInputElement>('tbody mat-checkbox input');
    boxes[0]!.click();
    boxes[1]!.click();
    await settle(fixture);
    buttonByText(root, 'Revoke selected').click();
    await settle(fixture);
    buttonByText(overlay().querySelector('trove-confirm-dialog') as HTMLElement, 'Revoke').click();
    await settle(fixture, 300);
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

  it('R-UI-005 without tokens:revoke there are no revoke controls', async () => {
    const { root } = await renderScreen(TokenAdmin, { capabilities: ['tokens:read'] });
    expect(root.textContent).not.toContain('Revoke by value');
    expect(root.querySelector('tbody button')).toBeNull();
  });
});
