import { AnsibleApi } from '../../api/ansible-api';
import { ANSIBLE_REPOSITORY, FakeAnsibleApi, ansibleProblem, tombstone } from '../../api/testing/fakes-ansible';
import { buttonByText, expectAccessible, renderScreen, settle, text } from '../../testing/screen';
import { AnsibleTombstones } from './ansible-tombstones';

async function render(canManage = true, stones = [tombstone(), tombstone({ version: '1.1.0' })]) {
  const r = await renderScreen(AnsibleTombstones, {
    inputs: { key: ANSIBLE_REPOSITORY, canManage },
    setup: (get) => {
      (get(AnsibleApi) as FakeAnsibleApi).stones = [...stones];
    },
  });
  return { r, fake: r.get(AnsibleApi) as FakeAnsibleApi, body: r.root.ownerDocument.body };
}

const rows = (root: Element) => [...root.querySelectorAll('tbody tr')];

function dialogButton(body: HTMLElement, label: string): HTMLButtonElement {
  const found = [...body.querySelectorAll<HTMLButtonElement>('.dialog-actions button')].find((b) => text(b) === label);
  if (!found) throw new Error(`no dialog button ${label}`);
  return found;
}

describe('Ansible tombstones', () => {
  it('R-ANS-012 lists the blocked version numbers with the digest, the time and who deleted them', async () => {
    const { r } = await render();
    expect(rows(r.root)).toHaveLength(2);
    const first = rows(r.root)[0]!;
    expect(text(first)).toContain('acme.tools');
    expect(text(first)).toContain('1.0.0');
    expect(text(first)).toContain('d3499c000000');
    expect(first.querySelector('code')?.getAttribute('title')).toBe(tombstone().sha256);
    expect(text(first)).toContain('user:alice');
    await expectAccessible(r.root);
  });

  it('R-ANS-012 says that no number is blocked when there is no tombstone', async () => {
    const { r } = await render(true, []);
    expect(text(r.root)).toContain('no number is blocked');
  });

  it('R-ANS-012 clearing asks first, then clears the one tombstone and reloads the list', async () => {
    const { r, fake, body } = await render();
    buttonByText(r.root, 'Clear').click();
    await settle(r.fixture);
    expect(text(body)).toContain('can then be published again');
    dialogButton(body, 'Clear tombstone').click();
    await settle(r.fixture);
    expect(fake.calls).toContain('clear:acme.tools:1.0.0');
    expect(rows(r.root).map((tr) => text(tr))).toEqual([expect.stringContaining('1.1.0')]);
  });

  it('R-ANS-012 declining the question clears nothing', async () => {
    const { r, fake, body } = await render();
    buttonByText(r.root, 'Clear').click();
    await settle(r.fixture);
    [...body.querySelectorAll<HTMLButtonElement>('.dialog-actions button')].find((b) => text(b) !== 'Clear tombstone')!.click();
    await settle(r.fixture);
    expect(fake.calls.filter((c) => c.startsWith('clear'))).toEqual([]);
    expect(rows(r.root)).toHaveLength(2);
  });

  it('R-ANS-012 without manage the list is read-only', async () => {
    const { r } = await render(false);
    expect([...r.root.querySelectorAll('button')].map((b) => text(b))).not.toContain('Clear');
  });

  it('R-ANS-012 a refusal of the clearing is shown and the list stays', async () => {
    const { r, fake } = await render();
    buttonByText(r.root, 'Clear').click();
    await settle(r.fixture);
    dialogButton(r.root.ownerDocument.body, 'Clear tombstone').click();
    fake.failure = ansibleProblem(403, 'permission_denied', 'You may not manage this repository.');
    await settle(r.fixture);
    expect(text(r.root)).toContain('You may not manage this repository.');
    expect(rows(r.root)).toHaveLength(2);
  });
});
