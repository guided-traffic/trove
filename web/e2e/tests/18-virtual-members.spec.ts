import { e2eProject, expect, expectAccessible, repositories, repositoryApi, signInLocal, test, unique } from '../support';

// R-UI-047 journey "virtual repository members" (R-UI-022): an administrator opens a virtual npm repository, adds two members
// with the keyboard only, moves one to the top, chooses the default deployment target, saves, and the API shows that order. The
// effective resolution order and the explanation of a path follow on the same screen. Data is prepared through the API (INV-17).

const localKey = unique('ui-vm-local');
const teamKey = unique('ui-vm-team');
const virtualKey = unique('ui-vm-virtual');

test.beforeAll(async ({ admin }) => {
  for (const key of [localKey, teamKey]) {
    const created = await admin.post(repositories(), { data: { key, class: 'local', format: 'npm' } });
    expect(created.status(), await created.text()).toBe(201);
  }
  const virtual = await admin.post(repositories(), {
    data: { key: virtualKey, class: 'virtual', format: 'npm', virtual: { members: [] } },
  });
  expect(virtual.status(), await virtual.text()).toBe(201);
});

test.afterAll(async ({ admin }) => {
  await admin.delete(repositoryApi(virtualKey));
  await admin.delete(repositoryApi(localKey));
  await admin.delete(repositoryApi(teamKey));
});

test('R-UI-022 with the keyboard only an administrator adds two members, reorders them, sets the deployment target and saves; the API shows that order', async ({
  page,
  admin,
  replicas,
}) => {
  await signInLocal(page);
  await page.goto(`/ui/en/projects/${e2eProject}/repositories/${virtualKey}`);
  await page.getByRole('tab', { name: 'Members' }).click();

  const available = page.getByRole('listbox', { name: 'Available repositories' });
  const members = page.getByRole('listbox', { name: 'Members in resolution order' });
  const live = page.getByRole('status').filter({ hasText: /added|moved|removed/ });

  /**
   * Picks the option with the keyboard: focus the list, Home to its first option (a listbox remembers its last active option),
   * move the active option with the arrow key, and select it with Space unless it is selected already (Space toggles).
   */
  async function pick(list: typeof available, key: string): Promise<void> {
    await list.focus();
    const options = list.getByRole('option');
    const names = await options.evaluateAll((els) => els.map((e) => e.querySelector('[translate="no"]')?.textContent?.trim() ?? ''));
    const index = names.indexOf(key);
    expect(index, `${key} among ${names}`).toBeGreaterThanOrEqual(0);
    await page.keyboard.press('Home');
    for (let i = 0; i < index; i++) await page.keyboard.press('ArrowDown');
    if ((await options.nth(index).getAttribute('aria-selected')) !== 'true') await page.keyboard.press('Space');
    await expect(options.nth(index)).toHaveAttribute('aria-selected', 'true');
  }
  /** Presses a button with the keyboard. */
  async function press(name: string): Promise<void> {
    const b = page.getByRole('button', { name, exact: true });
    await b.focus();
    await page.keyboard.press('Enter');
  }

  await test.step('two members are added', async () => {
    await pick(available, localKey);
    await press('Add');
    await expect(live).toContainText(`${localKey} added at position 1 of 1`);
    await pick(available, teamKey);
    await press('Add');
    await expect(live).toContainText(`${teamKey} added at position 2 of 2`);
    await expect(members.getByRole('option')).toHaveCount(2);
  });

  await test.step('the second member moves to the top and the announcement says where it went', async () => {
    await press('Move to top');
    await expect(live).toContainText(`${teamKey} moved to position 1 of 2`);
    await expect(members.getByRole('option').first()).toContainText(teamKey);
  });

  await test.step('the default deployment target is one of the local members', async () => {
    await page.getByLabel('Default deployment target').selectOption(localKey);
  });

  await test.step('Save members stores the order', async () => {
    await press('Save members');
    await expect(page.getByText('Members saved.')).toBeVisible();
    const stored = await admin.get(repositoryApi(virtualKey));
    expect(stored.status()).toBe(200);
    const doc = (await stored.json()) as { virtual: { members: string[]; deployTarget: string | null } };
    expect(doc.virtual.members).toEqual([teamKey, localKey]);
    expect(doc.virtual.deployTarget).toBe(localKey);
  });

  await test.step('the effective resolution order is the saved list and a path is explained member by member', async () => {
    const order = page.getByRole('list').filter({ has: page.locator('li', { hasText: teamKey }) }).last();
    await expect(order.getByRole('listitem')).toHaveText([new RegExp(teamKey), new RegExp(`${localKey}.*Default deployment target`)]);
    await page.getByLabel('Explain a path').fill('left-pad');
    await page.getByRole('button', { name: 'Explain', exact: true }).click();
    const table = page.getByRole('table', { name: 'How each member is asked for the path' });
    await expect(table.getByRole('row', { name: new RegExp(teamKey) })).toBeVisible();
    await expect(table.getByRole('row', { name: new RegExp(localKey) })).toBeVisible();
  });

  await test.step('a member is removed and the list shows it as available again', async () => {
    await pick(members, teamKey);
    await press('Remove');
    await expect(live).toContainText(`${teamKey} removed`);
    await expect(available.getByRole('option', { name: new RegExp(teamKey) })).toBeVisible();
    await press('Discard changes');
    await expect(members.getByRole('option')).toHaveCount(2);
  });

  await expectAccessible(page, 'virtual repository members');
  expect(replicas.size).toBeGreaterThanOrEqual(1);
});
