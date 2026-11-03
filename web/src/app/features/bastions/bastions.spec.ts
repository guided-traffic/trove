import { TestBed } from '@angular/core/testing';
import { BastionsApi } from '../../api/bastions-api';
import { ApiError } from '../../api/problem';
import {
  BASTION_SECRET,
  FakeBastionsApi,
  inDays,
  sampleBastion,
  sampleBastionToken,
} from '../../api/testing/fakes-bastions';
import { helpFor } from '../../core/help-topics';
import { Navigation } from '../../core/navigation';
import { SessionStore } from '../../core/session-store';
import { seriousViolations } from '../../testing/axe';
import { DraftStore } from '../../shared/drafts';
import { browserStorage, buttonByText, renderScreen, settle, sleep, storedText, text, type, typeInto } from '../../testing/screen';
import { BASTION_TOKEN_WARN_DAYS, tokenExpiry } from './bastion-card';
import { BastionList } from './bastion-list';

const overlay = (): HTMLElement => document.querySelector('.cdk-overlay-container') as HTMLElement;
const card = (root: HTMLElement, name: string): HTMLElement =>
  [...root.querySelectorAll<HTMLElement>('trove-bastion-card')].find((c) => c.querySelector('h2')?.textContent?.trim() === name)!;

describe('Bastions screen (R-BAS-007)', () => {
  it('R-BAS-007 lists the registrations with the last contact of every replica, and passes axe', async () => {
    const { root } = await renderScreen(BastionList);
    expect(root.querySelector('h1')?.textContent).toContain('Bastions');
    const partners = card(root, 'partners');
    expect(text(partners)).toContain('Enabled');
    expect(text(partners)).toContain('The partner network');
    const rows = partners.querySelectorAll('table')[0]!.querySelectorAll('tbody tr');
    expect(rows).toHaveLength(2);
    const first = text(rows[0]!);
    for (const part of ['trove-bastion-7d9c-x2k4q', '198.51.100.20', '0.15.0', 'https://packages.example.com']) {
      expect(first).toContain(part);
    }
    expect(text(partners.querySelectorAll('table')[0]!.querySelector('thead')!)).toContain('Interface version');
    expect(text(rows[0]!.querySelectorAll('td')[3]!)).toBe('1');
    expect(text(card(root, 'standby'))).toContain('Disabled');
    expect(text(card(root, 'standby'))).toContain('No replica has contacted');
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-BAS-007 marks a token that expires in less than 30 days and a token that has expired, and no other', async () => {
    const { root } = await renderScreen(BastionList, {
      setup: (get) => {
        (get(BastionsApi) as FakeBastionsApi).bastions = [
          sampleBastion({
            tokens: [
              sampleBastionToken({ id: 't-soon', description: 'twenty days', expiresAt: inDays(20) }),
              sampleBastionToken({ id: 't-far', description: 'far away', expiresAt: inDays(31) }),
              sampleBastionToken({ id: 't-gone', description: 'gone', expiresAt: inDays(-1) }),
            ],
          }),
        ];
      },
    });
    const rows = [...card(root, 'partners').querySelectorAll('table')[1]!.querySelectorAll('tbody tr')];
    const byName = (n: string) => text(rows.find((r) => text(r).includes(n))!);
    expect(byName('twenty days')).toMatch(/Expires in\s+20 days/);
    expect(byName('far away')).not.toContain('Expires in');
    expect(byName('far away')).not.toContain('Expired');
    expect(byName('gone')).toContain('Expired');
    expect(root.querySelectorAll('.badge.warn')).toHaveLength(1);
  });

  it('R-BAS-007 the warning window is 30 days to the millisecond', () => {
    const now = Date.parse('2026-11-03T00:00:00Z');
    const at = (ms: number) => ({ expiresAt: new Date(now + ms).toISOString() });
    const day = 86_400_000;
    expect(BASTION_TOKEN_WARN_DAYS).toBe(30);
    expect(tokenExpiry(at(30 * day - 1), now)).toBe('soon');
    expect(tokenExpiry(at(30 * day), now)).toBe('ok');
    expect(tokenExpiry(at(0), now)).toBe('expired');
    expect(tokenExpiry(at(-day), now)).toBe('expired');
  });

  it('R-BAS-007 shows the last use of a token, or that it was not used yet', async () => {
    const { root } = await renderScreen(BastionList, {
      setup: (get) => {
        (get(BastionsApi) as FakeBastionsApi).bastions = [
          sampleBastion({ tokens: [sampleBastionToken({ lastUsedAt: null, description: 'the new one' })] }),
        ];
      },
    });
    expect(text(card(root, 'partners'))).toContain('Not used yet');
  });

  it('R-BAS-004 creates a registration with an idempotency key and lists it', async () => {
    const { fixture, root, get } = await renderScreen(BastionList);
    buttonByText(root, 'New Bastion').click();
    await settle(fixture);
    const dialog = overlay().querySelector('trove-bastion-create-dialog') as HTMLElement;
    typeInto(dialog, 'Name', 'Partners');
    await settle(fixture);
    expect(text(dialog)).toContain('must start with a lower-case letter');
    typeInto(dialog, 'Name', 'newcomer');
    typeInto(dialog, 'Description', 'A new network');
    buttonByText(dialog, 'Create Bastion').click();
    await sleep(fixture, 100);
    const api = get(BastionsApi) as FakeBastionsApi;
    expect(api.calls).toContain('create newcomer');
    expect(api.keys).toHaveLength(1);
    expect(card(root, 'newcomer')).toBeTruthy();
  });

  it('R-BAS-004 a refused creation keeps the dialog and shows the error', async () => {
    const { fixture, root, get } = await renderScreen(BastionList);
    buttonByText(root, 'New Bastion').click();
    await settle(fixture);
    const dialog = overlay().querySelector('trove-bastion-create-dialog') as HTMLElement;
    typeInto(dialog, 'Name', 'partners');
    buttonByText(dialog, 'Create Bastion').click();
    await settle(fixture);
    expect((get(BastionsApi) as FakeBastionsApi).calls).toContain('create partners');
    expect(overlay().querySelector('trove-bastion-create-dialog')).toBeTruthy();
    expect(text(overlay())).toContain('conflict');
  });

  it('R-BAS-004 issues a token, shows it once and never stores it', async () => {
    const { fixture, root, get } = await renderScreen(BastionList);
    const partners = card(root, 'partners');
    buttonByText(partners, 'Issue token').click();
    await settle(fixture);
    const dialog = overlay().querySelector('trove-bastion-token-dialog') as HTMLElement;
    typeInto(dialog, 'Description', 'partners 2027');
    typeInto(dialog, 'Lifetime in days', '365');
    buttonByText(dialog, 'Issue token').click();
    await sleep(fixture, 100);
    const api = get(BastionsApi) as FakeBastionsApi;
    expect(api.calls).toContain('createToken partners partners 2027 31536000');
    expect(api.keys).toHaveLength(1);
    const secret = card(root, 'partners').querySelector('trove-token-secret') as HTMLElement;
    expect(secret).toBeTruthy();
    expect((secret.querySelector('input') as HTMLInputElement).value).toBe(BASTION_SECRET);
    expect(text(secret)).toContain('shown only once');
    // R-UI-043: the value is in the component only, and no other card shows it.
    expect(storedText()).not.toContain('trv_');
    expect(card(root, 'standby').querySelector('trove-token-secret')).toBeNull();
    // The list was read again and shows the new token by its hint, not by its value.
    expect(text(card(root, 'partners'))).toContain('trv_bst_Zz09');
    expect(await seriousViolations(root)).toEqual([]);
    buttonByText(secret, 'I have copied it').click();
    await settle(fixture);
    expect(root.querySelector('trove-token-secret')).toBeNull();
    expect(root.innerHTML).not.toContain(BASTION_SECRET);
  });

  it('R-BAS-004 an empty lifetime takes the default, and a lifetime that is no number is refused before the request', async () => {
    const { fixture, root, get } = await renderScreen(BastionList);
    buttonByText(card(root, 'partners'), 'Issue token').click();
    await settle(fixture);
    const dialog = overlay().querySelector('trove-bastion-token-dialog') as HTMLElement;
    typeInto(dialog, 'Lifetime in days', '0');
    buttonByText(dialog, 'Issue token').click();
    await settle(fixture);
    const api = get(BastionsApi) as FakeBastionsApi;
    expect(api.calls.filter((c) => c.startsWith('createToken'))).toEqual([]);
    expect(text(dialog)).toContain('greater than zero');
    typeInto(dialog, 'Lifetime in days', '');
    buttonByText(dialog, 'Issue token').click();
    await sleep(fixture, 100);
    expect(api.lastTokenRequest).toEqual({ description: '' });
  });

  it('R-BAS-004 a registration at its token limit answers token_limit_reached and keeps the dialog', async () => {
    const { fixture, root, get } = await renderScreen(BastionList);
    (get(BastionsApi) as FakeBastionsApi).tokenLimit = 1;
    buttonByText(card(root, 'partners'), 'Issue token').click();
    await settle(fixture);
    const dialog = overlay().querySelector('trove-bastion-token-dialog') as HTMLElement;
    buttonByText(dialog, 'Issue token').click();
    await settle(fixture);
    expect(overlay().querySelector('trove-bastion-token-dialog')).toBeTruthy();
    expect(root.querySelector('trove-token-secret')).toBeNull();
  });

  it('R-UI-042 a repeated issue whose first answer was lost revokes the lost token and issues again with a new key', async () => {
    const { fixture, root, get } = await renderScreen(BastionList);
    const api = get(BastionsApi) as FakeBastionsApi;
    api.failure = new ApiError(409, { code: 'token_already_issued', tokenId: 't-lost' }, 'req-1');
    api.bastions = [sampleBastion({ tokens: [sampleBastionToken({ id: 't-lost', description: 'lost' })] })];
    buttonByText(card(root, 'partners'), 'Issue token').click();
    await settle(fixture);
    buttonByText(overlay().querySelector('trove-bastion-token-dialog') as HTMLElement, 'Issue token').click();
    await sleep(fixture, 100);
    expect(api.calls.filter((c) => c.startsWith('revokeToken'))).toEqual(['revokeToken partners t-lost']);
    expect(api.keys).toHaveLength(1);
    expect(root.querySelector('trove-token-secret')).toBeTruthy();
  });

  it('R-API-007 R-UI-042 issuing a token with a session whose sign-in is not recent keeps the non-secret entries as a draft, never a token', async () => {
    browserStorage.clear('session');
    const { fixture, root, get } = await renderScreen(BastionList);
    const api = get(BastionsApi) as FakeBastionsApi;
    api.failure = new ApiError(401, { code: 'reauthentication_required' }, 'req-1');
    buttonByText(card(root, 'partners'), 'Issue token').click();
    await settle(fixture);
    const dialog = overlay().querySelector('trove-bastion-token-dialog') as HTMLElement;
    typeInto(dialog, 'Description', 'partners 2027');
    typeInto(dialog, 'Lifetime in days', '365');
    buttonByText(dialog, 'Issue token').click();
    await settle(fixture);
    const stored = browserStorage.entries('session').filter(([k]) => k.startsWith('trove.draft:'));
    expect(stored).toHaveLength(1);
    expect(JSON.parse(stored[0]![1])).toEqual({ bastion: 'partners', description: 'partners 2027', days: '365' });
    expect(storedText()).not.toContain('trv_');
    expect(root.querySelector('trove-token-secret')).toBeNull();
    browserStorage.clear('session');
  });

  it('R-API-007 R-UI-042 after the sign-in the draft reopens the token dialog of its registration, once, and the token is issued', async () => {
    browserStorage.clear('session');
    const { fixture, root, get } = await renderScreen(BastionList, {
      setup: (get) => {
        get(DraftStore).keep({ bastion: 'partners', description: 'partners 2027', days: '365' });
      },
    });
    await settle(fixture);
    const dialogs = overlay().querySelectorAll('trove-bastion-token-dialog');
    expect(dialogs).toHaveLength(1);
    const dialog = dialogs[0] as HTMLElement;
    expect(text(dialog)).toContain('partners');
    expect((dialog.querySelector('input[name="description"]') as HTMLInputElement).value).toBe('partners 2027');
    expect((dialog.querySelector('input[name="days"]') as HTMLInputElement).value).toBe('365');
    expect(browserStorage.entries('session').filter(([k]) => k.startsWith('trove.draft:'))).toHaveLength(0);
    buttonByText(dialog, 'Issue token').click();
    await sleep(fixture, 100);
    expect((get(BastionsApi) as FakeBastionsApi).calls).toContain('createToken partners partners 2027 31536000');
    expect(card(root, 'partners').querySelector('trove-token-secret')).toBeTruthy();
    expect(overlay().querySelectorAll('trove-bastion-token-dialog')).toHaveLength(0);
  });

  it('R-BAS-004 disables a registration after a confirmation, with the ETag it read, and enables it again', async () => {
    const { fixture, root, get } = await renderScreen(BastionList);
    const api = get(BastionsApi) as FakeBastionsApi;
    buttonByText(card(root, 'partners'), 'Disable').click();
    await settle(fixture);
    buttonByText(overlay().querySelector('trove-confirm-dialog') as HTMLElement, 'Disable').click();
    await sleep(fixture, 100);
    expect(api.calls).toContain('setEnabled partners false');
    expect(api.ifMatch).toEqual(['"1"']);
    expect(text(card(root, 'partners'))).toContain('Disabled');
    buttonByText(card(root, 'partners'), 'Enable').click();
    await sleep(fixture, 100);
    expect(api.calls).toContain('setEnabled partners true');
    expect(api.ifMatch).toEqual(['"1"', '"2"']);
    expect(text(card(root, 'partners'))).toContain('Enabled');
  });

  it('R-BAS-004 a change made by someone else in between is a 412 and is shown', async () => {
    const { fixture, root, get } = await renderScreen(BastionList);
    const api = get(BastionsApi) as FakeBastionsApi;
    const read = api.get.bind(api);
    api.get = async (name) => {
      const answer = await read(name);
      api.bumpRevision(name);
      return answer;
    };
    buttonByText(card(root, 'standby'), 'Enable').click();
    await sleep(fixture, 100);
    expect(text(card(root, 'standby'))).toContain('Disabled');
    expect(text(card(root, 'standby'))).toContain('Changed by someone else');
  });

  it('R-BAS-004 deleting asks for the name and removes the registration', async () => {
    const { fixture, root, get } = await renderScreen(BastionList);
    buttonByText(card(root, 'partners'), 'Delete').click();
    await settle(fixture);
    const dialog = overlay().querySelector('trove-confirm-dialog') as HTMLElement;
    expect((buttonByText(dialog, 'Delete') as HTMLButtonElement).disabled).toBe(true);
    type(dialog.querySelector<HTMLInputElement>('#confirm-name')!, 'partners');
    await settle(fixture);
    buttonByText(dialog, 'Delete').click();
    await sleep(fixture, 100);
    expect((get(BastionsApi) as FakeBastionsApi).calls).toContain('delete partners');
    expect(card(root, 'partners')).toBeUndefined();
    expect(card(root, 'standby')).toBeTruthy();
  });

  it('R-BAS-006 revokes a token after a confirmation that names the renewal order', async () => {
    const { fixture, root, get } = await renderScreen(BastionList);
    buttonByText(card(root, 'partners'), 'Revoke').click();
    await settle(fixture);
    const dialog = overlay().querySelector('trove-confirm-dialog') as HTMLElement;
    expect(text(dialog)).toContain('Last used');
    buttonByText(dialog, 'Revoke').click();
    await sleep(fixture, 100);
    expect((get(BastionsApi) as FakeBastionsApi).calls).toContain('revokeToken partners 0192f0d1-1c2a-7c3e-8a51-6f7d2b9c4e41');
    expect(text(card(root, 'partners'))).toContain('This registration has no token');
  });

  it('R-BAS-004 endpoints:read alone shows the list and offers no action', async () => {
    const { root } = await renderScreen(BastionList, { capabilities: ['endpoints:read'] });
    expect(card(root, 'partners')).toBeTruthy();
    expect([...root.querySelectorAll('button')].map((b) => text(b)).filter((t) => !/^New Bastion|help_outline/.test(t))).toEqual([]);
    const create = buttonByText(root, 'New Bastion') as HTMLButtonElement;
    expect(create.disabled).toBe(true);
    expect(text(root)).toContain('endpoints:write');
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-BAS-004 tokens:revoke alone may revoke a token and nothing else', async () => {
    const { root } = await renderScreen(BastionList, { capabilities: ['endpoints:read', 'tokens:revoke'] });
    const partners = card(root, 'partners');
    expect(buttonByText(partners, 'Revoke')).toBeTruthy();
    expect([...partners.querySelectorAll('button')].map((b) => text(b)).filter((t) => !/help_outline/.test(t))).toEqual(['Revoke partners 2026']);
  });

  it('R-BAS-007 shows the empty state, the loading failure with a retry, and recovers', async () => {
    const { fixture, root, get } = await renderScreen(BastionList, {
      setup: (get) => {
        (get(BastionsApi) as FakeBastionsApi).bastions = [];
      },
    });
    expect(text(root)).toContain('No Bastion is registered');
    const api = get(BastionsApi) as FakeBastionsApi;
    api.listFailure = new ApiError(503, { code: 'unavailable' }, 'req-1');
    api.bastions = [sampleBastion()];
    const again = await renderScreen(BastionList, {
      setup: (g) => {
        (g(BastionsApi) as FakeBastionsApi).listFailure = new ApiError(503, { code: 'unavailable' }, 'req-1');
      },
    });
    expect(again.root.querySelector('trove-error-notice')).toBeTruthy();
    expect(again.root.querySelector('trove-bastion-card')).toBeNull();
    (again.get(BastionsApi) as FakeBastionsApi).listFailure = null;
    buttonByText(again.root, 'Retry').click();
    await settle(again.fixture);
    expect(again.root.querySelector('trove-bastion-card')).toBeTruthy();
    void fixture;
  });
});

describe('Bastions in the navigation and the help (R-BAS-007, R-UI-050)', () => {
  it('R-BAS-007 the entry needs endpoints:read and the "?" opens the Bastion page of the manual', async () => {
    const { get } = await renderScreen(BastionList, { capabilities: ['endpoints:read'] });
    const routes = (): string[] =>
      get(Navigation)
        .areas()
        .flatMap((a) => a.entries)
        .flatMap((e) => (e.kind === 'item' ? [e.route] : e.items.map((i) => i.route)));
    expect(routes()).toContain('/admin/bastions');
    expect(helpFor('/admin/bastions')).toEqual(['/help', 'admin', 'bastion']);
    TestBed.inject(SessionStore);
    const none = await renderScreen(BastionList, { capabilities: ['repositories:read-config'] });
    expect(
      none
        .get(Navigation)
        .areas()
        .flatMap((a) => a.entries)
        .some((e) => e.kind === 'item' && e.route === '/admin/bastions'),
    ).toBe(false);
  });
});
