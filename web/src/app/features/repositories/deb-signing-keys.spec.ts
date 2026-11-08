import { DebApi } from '../../api/deb-api';
import { ApiError } from '../../api/problem';
import { DEB_PRIMARY, FakeDebApi, debKey } from '../../api/testing/fakes-deb';
import { buttonByText, expectAccessible, renderScreen, settle, text, type } from '../../testing/screen';
import { DebSigningKeys, groupedFingerprint, keyErrorText } from './deb-signing-keys';

const NEXT = debKey('B7C1000000000000000000000000000000001122', 'next', { createdAt: '2026-11-05T09:00:00Z' });
const WITHHELD = debKey('C9D2000000000000000000000000000000003344', 'withheld', { withheldAt: '2026-11-07T09:00:00Z' });
const RETIRED = debKey('D0E3000000000000000000000000000000005566', 'retired', { retiredAt: '2026-10-01T09:00:00Z' });

async function render(keys = [DEB_PRIMARY], canManage = true, fail?: ApiError) {
  const r = await renderScreen(DebSigningKeys, {
    inputs: { key: 'web/deb-local', canManage },
    setup: (get) => {
      const fake = get(DebApi) as FakeDebApi;
      fake.keys = [...keys];
      fake.failure = fail ?? null;
    },
  });
  return { r, fake: r.get(DebApi) as FakeDebApi, body: r.root.ownerDocument.body };
}

const rows = (root: Element) => [...root.querySelectorAll('tbody tr')];
const states = (root: Element) => rows(root).map((tr) => tr.getAttribute('data-state'));

/** The confirm button of the open dialog. */
function dialogButton(body: HTMLElement, label: string): HTMLButtonElement {
  const found = [...body.querySelectorAll<HTMLButtonElement>('.dialog-actions button')].find((b) => text(b) === label);
  if (!found) throw new Error(`no dialog button ${label}`);
  return found;
}

describe('Debian signing keys panel', () => {
  it('R-DEB-015 lists every key with its fingerprint, state, type, origin and public certificate, primary first, and never a private key', async () => {
    const { r } = await render([RETIRED, NEXT, DEB_PRIMARY, WITHHELD]);
    expect(states(r.root)).toEqual(['primary', 'next', 'withheld', 'retired']);
    const first = rows(r.root)[0]!;
    expect(text(first)).toContain('3F2A 0000');
    expect(text(first)).toContain('Primary');
    expect(text(first)).toContain('EdDSA Ed25519');
    expect(text(first)).toContain('Generated');
    expect(first.querySelector('code')?.getAttribute('title')).toBe(DEB_PRIMARY.fingerprint);
    expect(text(first)).toContain('BEGIN PGP PUBLIC KEY BLOCK');
    expect(r.root.textContent).not.toContain('PRIVATE KEY');
    // a retired key keeps its record but offers no certificate to install
    expect(rows(r.root)[3]!.querySelector('details')).toBeNull();
    await expectAccessible(r.root);
  });

  it('R-DEB-015 the fingerprint is shown in groups of four to compare through a second channel', () => {
    expect(groupedFingerprint('3F2A0000000000000000000000000000000090C0')).toBe(
      '3F2A 0000 0000 0000 0000 0000 0000 0000 0000 90C0',
    );
  });

  it('R-DEB-015 without manage the list is read-only: no action, no creation, no import', async () => {
    const { r } = await render([DEB_PRIMARY, NEXT, WITHHELD], false);
    expect(states(r.root)).toEqual(['primary', 'next', 'withheld']);
    expect([...r.root.querySelectorAll('button')].map((b) => text(b)).filter((t) => /Promote|Remove|Reinstate|Confirm|Generate|Import/.test(t))).toEqual([]);
    expect(r.root.querySelector('textarea')).toBeNull();
  });

  it('R-DEB-017 generating the next key starts the overlap and says since when both keys sign', async () => {
    const { r, fake } = await render();
    expect(r.root.querySelector('[data-testid="deb-overlap"]')).toBeNull();
    buttonByText(r.root, 'Generate a next key').click();
    await settle(r.fixture);
    expect(fake.calls).toContain('create:generate');
    expect(states(r.root)).toEqual(['primary', 'next']);
    expect(text(r.root.querySelector('[data-testid="deb-overlap"]')!)).toContain('Both keys sign since');
    expect(r.root.querySelector('[data-testid="deb-generate"]')).toBeNull();
    expect(text(r.root)).toContain('A next key exists');
  });

  it('R-DEB-015 an imported key is sent once with its passphrase and leaves the form, also when the server refuses it', async () => {
    const { r, fake } = await render();
    buttonByText(r.root, 'Import a key').click();
    await settle(r.fixture);
    const secret = r.root.querySelector<HTMLTextAreaElement>('#deb-import-key')!;
    const pass = r.root.querySelector<HTMLInputElement>('#deb-import-pass')!;
    expect(pass.type).toBe('password');
    type(secret, '-----BEGIN PGP PRIVATE KEY BLOCK-----\nabc\n-----END PGP PRIVATE KEY BLOCK-----');
    type(pass, 'hunter2');
    await settle(r.fixture);

    const sent: unknown[] = [];
    fake.createSigningKey = async (_ref, body) => {
      sent.push(body);
      throw new ApiError(409, { code: 'deb_key_in_use', detail: 'known' }, null);
    };
    buttonByText(r.root, 'Import as next key').click();
    await settle(r.fixture);
    expect(sent).toEqual([
      { privateKey: '-----BEGIN PGP PRIVATE KEY BLOCK-----\nabc\n-----END PGP PRIVATE KEY BLOCK-----', passphrase: 'hunter2' },
    ]);
    expect(text(r.root.querySelector('[data-testid="deb-key-error"]')!)).toContain('already known to trove');
    expect(r.root.querySelector<HTMLTextAreaElement>('#deb-import-key')!.value).toBe('');
    expect(r.root.querySelector<HTMLInputElement>('#deb-import-pass')!.value).toBe('');
    expect(states(r.root)).toEqual(['primary']);
  });

  it('R-DEB-015 an accepted import becomes the next key and closes the form; an empty passphrase is not sent', async () => {
    const { r, fake } = await render();
    buttonByText(r.root, 'Import a key').click();
    await settle(r.fixture);
    expect((buttonByText(r.root, 'Import as next key') as HTMLButtonElement).disabled).toBe(true);
    type(r.root.querySelector<HTMLTextAreaElement>('#deb-import-key')!, 'ARMORED');
    await settle(r.fixture);
    const bodies: unknown[] = [];
    const create = fake.createSigningKey.bind(fake);
    fake.createSigningKey = (ref, body) => {
      bodies.push(body);
      return create(ref, body);
    };
    buttonByText(r.root, 'Import as next key').click();
    await settle(r.fixture);
    expect(bodies).toEqual([{ privateKey: 'ARMORED' }]);
    expect(states(r.root)).toEqual(['primary', 'next']);
    expect(rows(r.root)[1]!.textContent).toContain('Imported');
    expect(r.root.querySelector('[data-testid="deb-import"]')).toBeNull();
  });

  it('R-DEB-017 promoting the next key asks first, names what the promotion does, and retires the old primary', async () => {
    const { r, fake, body } = await render([DEB_PRIMARY, NEXT]);
    buttonByText(r.root, 'Promote').click();
    await settle(r.fixture);
    expect(text(body)).toContain('its private key is erased');
    expect(text(body)).toContain('fails to update');
    expect(fake.calls).not.toContain('promote');
    dialogButton(body, 'Promote').click();
    await settle(r.fixture);
    expect(fake.calls).toContain('promote');
    expect(states(r.root)).toEqual(['primary', 'retired']);
    expect(rows(r.root)[0]!.textContent).toContain('B7C1');
  });

  it('R-DEB-017 cancelling the dialog changes nothing, and removing the next key retires it', async () => {
    const { r, fake, body } = await render([DEB_PRIMARY, NEXT]);
    buttonByText(r.root, 'Remove').click();
    await settle(r.fixture);
    buttonByText(body, 'Cancel').click();
    await settle(r.fixture);
    expect(fake.calls).not.toContain(`retire:${NEXT.fingerprint}`);
    buttonByText(r.root, 'Remove').click();
    await settle(r.fixture);
    dialogButton(body, 'Remove the key').click();
    await settle(r.fixture);
    expect(fake.calls).toContain(`retire:${NEXT.fingerprint}`);
    expect(states(r.root)).toEqual(['primary', 'retired']);
  });

  it('R-DEB-032 a withheld key is explained and can be reinstated; a repository without a primary says its builds publish nothing', async () => {
    const { r, fake } = await render([WITHHELD]);
    expect(r.root.querySelector('[data-testid="deb-withheld-note"]')).not.toBeNull();
    expect(text(r.root.querySelector('[data-testid="deb-no-primary"]')!)).toContain('builds publish nothing');
    buttonByText(r.root, 'Reinstate').click();
    await settle(r.fixture);
    expect(fake.calls).toContain(`reinstate:${WITHHELD.fingerprint}`);
    expect(states(r.root)).toEqual(['primary']);
    expect(r.root.querySelector('[data-testid="deb-no-primary"]')).toBeNull();
    expect(r.root.querySelector('[data-testid="deb-withheld-note"]')).toBeNull();
  });

  it('R-DEB-032 confirming the retirement of a withheld key says it cannot be undone and erases the key only after the answer', async () => {
    const { r, fake, body } = await render([DEB_PRIMARY, WITHHELD]);
    buttonByText(r.root, 'Confirm retirement').click();
    await settle(r.fixture);
    expect(text(body)).toContain('This cannot be undone');
    expect(fake.calls).not.toContain(`retire:${WITHHELD.fingerprint}`);
    dialogButton(body, 'Retire the key').click();
    await settle(r.fixture);
    expect(fake.calls).toContain(`retire:${WITHHELD.fingerprint}`);
    expect(states(r.root)).toEqual(['primary', 'retired']);
  });

  it('R-DEB-032 a reinstate the server refuses is shown in words and the list is read again', async () => {
    const { r, fake } = await render([DEB_PRIMARY, NEXT, WITHHELD]);
    fake.failure = new ApiError(409, { code: 'deb_key_state', detail: 'x' }, null);
    buttonByText(r.root, 'Reinstate').click();
    await settle(r.fixture);
    expect(text(r.root.querySelector('[data-testid="deb-key-error"]')!)).toContain('not in a state that allows this');
    expect(fake.calls.filter((c) => c === 'signingKeys').length).toBe(2);
  });

  it('R-DEB-015 a failed read shows the error with a retry, and an unknown code is the shared notice', async () => {
    const { r } = await render([DEB_PRIMARY], true, new ApiError(503, { code: 'unavailable', detail: 'down' }, null));
    expect(r.root.querySelector('[role="alert"]')).not.toBeNull();
    expect(rows(r.root)).toEqual([]);
    buttonByText(r.root, 'Retry').click();
    await settle(r.fixture);
    expect(states(r.root)).toEqual(['primary']);
  });

  it('R-DEB-015 the refusals of the key operations have a sentence each; others have none', () => {
    const e = (code: string) => new ApiError(409, { code, detail: '' }, null);
    expect(keyErrorText(e('deb_key_in_use'))).toContain('already known');
    expect(keyErrorText(e('deb_key_unsupported'))).toContain('3072');
    expect(keyErrorText(e('deb_key_state'))).toContain('Reload');
    expect(keyErrorText(e('deb_no_next_key'))).toContain('no next key');
    expect(keyErrorText(e('internal'))).toBeNull();
  });
});
