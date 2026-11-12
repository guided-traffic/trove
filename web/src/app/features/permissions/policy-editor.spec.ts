import { PolicyApi } from '../../api/authz-api';
import { FakePolicyApi, problem } from '../../api/testing/access-fakes';
import { Notifier } from '../../core/notifier';
import { buttonByText, renderScreen, settle, text, typeAndLeave, typeInto, sleep } from '../../testing/screen';
import { seriousViolations } from '../../testing/axe';
import { PREVIEW_DEBOUNCE_MS, PolicyEditor } from './policy-editor';
import { PolicyList } from './policy-list';

const overlay = (): HTMLElement => document.querySelector('.cdk-overlay-container') as HTMLElement;
const afterDebounce = (): Promise<void> => new Promise((r) => setTimeout(r, PREVIEW_DEBOUNCE_MS + 80));

describe('Policy list (R-UI-027)', () => {
  it('R-UI-027 lists name, description, principals, repositories and actions, and passes axe', async () => {
    const { root } = await renderScreen(PolicyList);
    const row = root.querySelector('tbody tr') as HTMLElement;
    expect(text(row)).toContain('team-web-publish');
    expect(text(row)).toContain('Groups: 1');
    expect(text(row)).toContain('npm-internal');
    expect(text(row)).toContain('read, annotate, deploy');
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-UI-027 the principal query parameter filters the list', async () => {
    const { root } = await renderScreen(PolicyList, { inputs: { principal: 'user:nobody' } });
    expect(root.textContent).toContain('No policy matches');
    expect(root.querySelector<HTMLInputElement>('input[name="principal"]')!.value).toBe('user:nobody');
  });
});

describe('Policy editor (R-UI-027)', () => {
  it('Q-UI-224 the groups are sections without a card, and Delete policy is last under Advanced', async () => {
    const { root } = await renderScreen(PolicyEditor, { inputs: { name: 'team-web-publish' } });
    expect(root.querySelector('section.card')).toBeNull();
    const toggles = [...root.querySelectorAll('.toggle')].map((e) => text(e));
    expect(toggles.map((t) => t.replace('chevron_right', ''))).toEqual([
      'Principals and actions',
      'Repositories and patterns',
      'Limit to package names or paths',
      'Advanced',
    ]);
    // Package names and paths are details on demand: open here, where the policy has patterns, closed on a new draft.
    const expanded = (r: HTMLElement) => r.querySelector('trove-disclosure.limits button[aria-expanded]')?.getAttribute('aria-expanded');
    expect(expanded(root)).toBe('true');
    const draft = await renderScreen(PolicyEditor);
    expect(expanded(draft.root)).toBe('false');
    const advanced = [...root.querySelectorAll('trove-disclosure')].pop()!;
    expect(advanced.querySelector('button.btn-danger')).toBeTruthy();
    expect(root.querySelector('.save-bar')!.textContent).not.toContain('Delete policy');
  });

  it('R-UI-027 validates the draft after 500 ms and shows the covered repositories', async () => {
    const { fixture, root, get } = await renderScreen(PolicyEditor);
    const api = get(PolicyApi) as FakePolicyApi;
    api.validated.length = 0;
    typeInto(root, 'Repository selectors', 'npm-internal');
    await settle(fixture);
    expect(api.validated).toHaveLength(0);
    await afterDebounce();
    await settle(fixture);
    expect(api.validated).toHaveLength(1);
    expect(api.validated[0]?.resources?.repositories).toEqual(['npm-internal']);
    expect(text(root)).toContain('Repositories covered now: 1');
    expect(text(root.querySelector('.covered')!)).toBe('npm-internal');
  });

  it('R-UI-027 R-UI-044 the name shows no error before it is touched, then its rule in words, never the pattern', async () => {
    const { fixture, root } = await renderScreen(PolicyEditor, {
      setup: (api) => {
        (api(PolicyApi) as FakePolicyApi).validation = () => ({
          valid: false,
          errors: [{ pointer: '/name', code: 'invalid_name', detail: 'The name matches ^[a-z0-9][a-z0-9._-]{0,127}$.' }],
          warnings: [],
          covers: [],
        });
      },
    });
    await afterDebounce();
    await settle(fixture);
    const nameField = () => [...root.querySelectorAll('trove-text-field')].find((f) => text(f).startsWith('Name'))!;
    // An empty field the user has not touched is never red (UI-A-42), and the preview lists no problem of it either.
    expect(nameField().querySelector('.field-error')).toBeNull();
    expect(nameField().querySelector('input')?.getAttribute('aria-invalid')).toBeNull();
    expect(root.querySelector('.preview .problems')).toBeNull();
    typeAndLeave(root, 'Name', 'Team X');
    await afterDebounce();
    await settle(fixture);
    const error = text(nameField().querySelector('.field-error')!);
    expect(error).toContain('lower-case letters, digits');
    expect(text(root)).not.toContain('^[a-z0-9]');
    // The preview names the field by its label, not by its JSON pointer.
    const problem = text(root.querySelector('.preview .problems li')!);
    expect(problem).toMatch(/^Name: Use lower-case letters/);
    expect(problem).not.toContain('/name');
  });

  it('R-UI-027 Explain is a normal button that says what is missing when pressed before a principal and a repository are named', async () => {
    const { fixture, root } = await renderScreen(PolicyEditor);
    const explain = buttonByText(root, 'Explain') as HTMLButtonElement;
    expect(explain.disabled).toBe(false);
    explain.click();
    await settle(fixture);
    expect(text(root.querySelector('.preview [role="alert"]')!)).toBe('Name a principal and a repository first.');
  });

  it('R-UI-027 shows validation problems next to the field and refuses to save before the API does', async () => {
    const { fixture, root, get } = await renderScreen(PolicyEditor, {
      setup: (api) => {
        (api(PolicyApi) as FakePolicyApi).validation = () => ({
          valid: false,
          errors: [{ pointer: '/entries/0/principal', code: 'unknown_principal', detail: 'no such group' }],
          warnings: ['selector_matches_nothing'],
          covers: [],
        });
      },
    });
    buttonByText(root, 'Add principal').click();
    await settle(fixture);
    typeInto(root, 'Name', 'team-x');
    typeAndLeave(root, 'Principal', 'group:nope');
    await afterDebounce();
    await settle(fixture);
    expect(text(root.querySelector('fieldset.entry .field-error')!)).toContain('A principal does not exist');
    expect(text(root)).toContain('A repository selector matches no repository yet');
    expect(await seriousViolations(root)).toEqual([]);

    buttonByText(root, 'Create policy').click();
    await settle(fixture);
    expect((get(PolicyApi) as FakePolicyApi).calls).toEqual([]);
    expect(text(root)).toContain('Fix the problems in the preview first');
  });

  it('R-UI-027 creating through the editor sends the same document the API takes, with an idempotency key', async () => {
    const { fixture, root, get } = await renderScreen(PolicyEditor);
    typeInto(root, 'Name', 'team-x');
    typeInto(root, 'Repository selectors', 'npm-internal\nnpm-team-*');
    buttonByText(root, 'Add principal').click();
    await settle(fixture);
    typeInto(root, 'Principal', 'group:team-web');
    await settle(fixture);
    buttonByText(root.querySelector('fieldset.entry')!, 'write').click();
    await settle(fixture);
    buttonByText(root, 'Create policy').click();
    await settle(fixture);
    const api = get(PolicyApi) as FakePolicyApi;
    expect(api.calls).toEqual(['create team-x']);
    expect(api.keys[0]).toMatch(/^[0-9a-f-]{36}$/);
    const stored = api.policies.find((p) => p.name === 'team-x')!;
    expect(stored.resources.repositories).toEqual(['npm-internal', 'npm-team-*']);
    expect(stored.entries).toEqual([{ principal: 'group:team-web', actions: ['read', 'annotate', 'deploy'] }]);
  });

  it('R-UI-027 saving an existing policy shows the diff, then PUTs with the loaded ETag', async () => {
    const { fixture, root, get } = await renderScreen(PolicyEditor, { inputs: { name: 'team-web-publish' } });
    buttonByText(root, 'Add principal').click();
    await settle(fixture);
    const rows = root.querySelectorAll('fieldset.entry');
    typeInto(rows[1]!, 'Principal', 'user:bob');
    await settle(fixture);
    buttonByText(root, 'Review and save').click();
    await settle(fixture);
    const dialog = overlay().querySelector('trove-policy-diff-dialog') as HTMLElement;
    const items = dialog.querySelectorAll('li');
    expect(items).toHaveLength(1);
    expect(text(items[0]!)).toContain('Principal added: user:bob');
    expect(await seriousViolations(overlay())).toEqual([]);
    buttonByText(dialog, 'Save policy').click();
    await sleep(fixture, 300);
    expect((get(PolicyApi) as FakePolicyApi).calls).toEqual(['replace team-web-publish "3"']);
  });

  it('R-UI-042 a 412 opens the conflict dialog and Overwrite sends mine with the ETag read now', async () => {
    const { fixture, root, get } = await renderScreen(PolicyEditor, { inputs: { name: 'team-web-publish' } });
    const api = get(PolicyApi) as FakePolicyApi;
    // Someone else saves first: version 3 becomes 4 and the description differs.
    const current = await api.load('team-web-publish', null);
    await api.replace(
      'team-web-publish',
      null,
      { resourceType: 'repository', description: 'Changed by someone else', resources: current.policy.resources, entries: current.policy.entries },
      current.etag,
    );
    api.calls.length = 0;
    typeInto(root, 'Description', 'Mine');
    await settle(fixture);
    buttonByText(root, 'Review and save').click();
    await settle(fixture);
    buttonByText(overlay().querySelector('trove-policy-diff-dialog') as HTMLElement, 'Save policy').click();
    await sleep(fixture, 300);
    const conflict = overlay().querySelector('trove-conflict-dialog') as HTMLElement;
    expect(conflict).toBeTruthy();
    expect(text(conflict)).toContain('Changed by someone else');
    buttonByText(conflict, 'Overwrite').click();
    await sleep(fixture, 300);
    expect(api.calls).toEqual(['replace team-web-publish "3"', 'replace team-web-publish "4"']);
    expect(api.policies[0]?.description).toBe('Mine');
  });

  it('R-UI-042 a 412 authz_epoch_unknown re-reads the policy and says the change is stored when it matches', async () => {
    const { fixture, root, get } = await renderScreen(PolicyEditor, { inputs: { name: 'team-web-publish' } });
    const api = get(PolicyApi) as FakePolicyApi;
    typeInto(root, 'Description', 'Same text');
    await settle(fixture);
    // The write committed, but its answer was refused on a replica that did not know the epoch.
    const realReplace = api.replace.bind(api);
    api.replace = async (name, project, input, etag) => {
      await realReplace(name, project, input, etag);
      throw problem(412, 'authz_epoch_unknown');
    };
    buttonByText(root, 'Review and save').click();
    await settle(fixture);
    buttonByText(overlay().querySelector('trove-policy-diff-dialog') as HTMLElement, 'Save policy').click();
    await sleep(fixture, 300);
    expect(api.policies[0]?.description).toBe('Same text');
    expect(get(Notifier).notices().map((n) => n.text)).toContain('The change is stored.');
    expect(document.querySelector('trove-conflict-dialog')).toBeNull();
  });

  it('R-UI-027 the pattern tester says whether a value matches and why', async () => {
    const { fixture, root } = await renderScreen(PolicyEditor);
    typeInto(root, 'Include', '@team-a/*');
    await settle(fixture);
    typeInto(root, 'Does this match?', '@team-a/ui');
    await settle(fixture);
    expect(text(root.querySelector('.tester + p')!)).toContain('Matches: included by @team-a/*');
    typeInto(root, 'Does this match?', '@team-b/ui');
    await settle(fixture);
    expect(text(root.querySelector('.tester + p')!)).toContain('no include pattern matches');
  });

  it('R-UI-027 "Try it" explains an allowed and a denied question from the draft, in order', async () => {
    const { fixture, root } = await renderScreen(PolicyEditor, { inputs: { name: 'team-web-publish' } });
    await afterDebounce();
    await settle(fixture);
    const ask = async (action: string): Promise<string> => {
      const form = root.querySelector('.try') as HTMLElement;
      typeInto(form, 'Principal', 'user:alice');
      typeInto(form, 'Repository', 'npm-internal');
      typeInto(form, 'Action', action);
      await settle(fixture);
      buttonByText(root, 'Explain').click();
      await settle(fixture);
      return text(root.querySelector('.preview-body > div[role="status"]') ?? root);
    };
    // The fake user alice is in team-web, which the stored entry names.
    const allowed = await ask('deploy');
    expect(allowed).toContain('Allowed by this draft');
    expect(allowed).toContain('applies to user:alice through its group or class');
    const denied = await ask('delete');
    expect(denied).toContain('Not granted by this draft');
    expect(denied).toContain('do not include the action delete');
    expect(root.querySelectorAll('.steps')[0]?.tagName).toBe('OL');
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-UI-027 without policies:write the editor is read only', async () => {
    const { root } = await renderScreen(PolicyEditor, {
      inputs: { name: 'team-web-publish' },
      capabilities: ['policies:read'],
    });
    expect(root.textContent).not.toContain('Review and save');
    expect(root.textContent).not.toContain('Add principal');
    expect(root.querySelector<HTMLInputElement>('input[name="description"]')!.readOnly).toBe(true);
  });

  it('R-UI-005 an unknown policy is an error notice and the page still passes axe', async () => {
    const { root } = await renderScreen(PolicyEditor, { inputs: { name: 'nope' } });
    expect(root.querySelector('[role="alert"]')).toBeTruthy();
    expect(await seriousViolations(root)).toEqual([]);
  });
});
