import { TestBed } from '@angular/core/testing';
import { Router } from '@angular/router';
import { describe, expect, it, vi } from 'vitest';
import { CiApi } from '../../api/ci-api';
import { ApiError } from '../../api/problem';
import { SessionApi } from '../../api/session-api';
import { FakeSessionApi } from '../../api/testing/fakes';
import { FakeCiApi, sampleIssuance, sampleMapping } from '../../api/testing/fakes-ci';
import { RepositoriesApi } from '../../api/repositories-api';
import { FakeRepositoriesApi } from '../../api/testing/fakes-catalog';
import { storedText, buttonByText, expectAccessible, renderScreen, settle, sleep, text, type, typeInto } from '../../testing/screen';
import { ClaimTester } from './claim-tester';
import { MappingEditor } from './mapping-editor';
import { ProjectCi } from './project-ci';
import { ProjectDetail } from '../projects/project-detail';

type Get = <A>(token: abstract new (...args: never[]) => A) => A;
const ci = (get: Get): FakeCiApi => get(CiApi) as FakeCiApi;

const b64url = (v: unknown): string =>
  btoa(JSON.stringify(v)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
const SIGNATURE = 'c2lnbmF0dXJlLW9mLXRoZS10b2tlbg';
const token = (claims: unknown): string => `${b64url({ alg: 'RS256', typ: 'JWT' })}.${b64url(claims)}.${SIGNATURE}`;

const valueArea = (root: HTMLElement): HTMLTextAreaElement => root.querySelector('textarea[id$="-value"]') as HTMLTextAreaElement;
const checkbox = (root: HTMLElement, label: string): HTMLInputElement =>
  [...root.querySelectorAll('mat-checkbox')].find((c) => text(c).startsWith(label))!.querySelector('input') as HTMLInputElement;
const select = (root: HTMLElement, i = 0): HTMLSelectElement => root.querySelectorAll('trove-ci-name-picker select')[i] as HTMLSelectElement;

/** A caller without any capability that administers `web` (Q-AUTHZ-181, Q-CI-26). */
function projectAdmin(managePolicies = true) {
  return (get: Get): void => {
    (get(SessionApi) as FakeSessionApi).meResult = {
      roles: [],
      capabilities: [],
      principals: ['user:alice'],
      projects: [{ key: 'web', managePolicies, manageRepositories: false }],
    };
    (get(RepositoriesApi) as FakeRepositoriesApi).repositories.forEach((r) => (r.project = r.key === 'generic-local' ? 'web' : r.project));
  };
}

async function fillMinimalNewMapping(root: HTMLElement, fixture: Parameters<typeof settle>[0], write: boolean): Promise<void> {
  typeInto(root, 'Name', 'team-b-release');
  type(valueArea(root), '4711');
  type(select(root), 'team-web-publish');
  await settle(fixture);
  if (write) {
    buttonByText(root, 'Add inline grant').click();
    await settle(fixture);
    type(root.querySelector('textarea[name="repositories-0"]') as HTMLTextAreaElement, 'npm-internal');
    type(root.querySelector('textarea[name="include-0"]') as HTMLTextAreaElement, '@team-b/*');
    checkbox(root, 'deploy').click();
    await settle(fixture);
  }
}

describe('The mapping editor (R-UI-035, R-CI-009 to R-CI-013)', () => {
  it('R-UI-035 an existing mapping opens with its condition as a tree, its grants and its options', async () => {
    const { root } = await renderScreen(MappingEditor, { inputs: { issuer: 'gitlab-com', mapping: 'team-a-release' } });
    expect(text(root)).toContain('gitlab-com/team-a-release');
    expect(root.querySelectorAll('trove-ci-condition-node input[list="ci-claims"]')).toHaveLength(2);
    expect([...root.querySelectorAll('trove-ci-condition-node input[list="ci-claims"]')].map((i) => (i as HTMLInputElement).value)).toEqual(['namespace_id', 'ref_protected']);
    expect(text(root)).toContain('ci-read-internal');
    expect(root.querySelector<HTMLTextAreaElement>('textarea[name="repositories-0"]')!.value).toBe('npm-internal');
    expect(root.querySelector<HTMLInputElement>('input[name="maxTtlSeconds"]')!.value).toBe('1800');
    expect(text(root)).toContain('Reads as: namespace_id in [4711] and ref_protected = true');
    // The claim names of GitLab are offered as suggestions.
    const options = [...root.querySelectorAll('datalist#ci-claims option')].map((o) => (o as HTMLOptionElement).value);
    for (const claim of ['project_id', 'namespace_id', 'project_path', 'namespace_path', 'ref', 'ref_type', 'ref_protected', 'environment', 'pipeline_source', 'user_login']) {
      expect(options).toContain(claim);
    }
    await expectAccessible(root);
  });

  it('R-UI-035 a new mapping starts at the next priority, in the tier of the issuer, and saves what the form shows', async () => {
    const { root, fixture, get } = await renderScreen(MappingEditor, { inputs: { issuer: 'gitlab-com' } });
    expect(root.querySelector<HTMLInputElement>('input[name="priority"]')!.value).toBe('110');
    const navigate = vi.spyOn(TestBed.inject(Router), 'navigate').mockResolvedValue(true);
    await fillMinimalNewMapping(root, fixture, false);
    buttonByText(root, 'Create mapping').click();
    await sleep(fixture, 100);
    const stored = ci(get).mappings.find((m) => m.name === 'team-b-release')!;
    expect(stored.match).toEqual({ all: [{ claim: 'project_id', in: ['4711'] }] });
    expect(stored.priority).toBe(110);
    expect(stored.grants.policies).toEqual(['team-web-publish']);
    expect(navigate).toHaveBeenCalledWith(['/admin/ci', 'gitlab-com', 'mappings', 'team-b-release'], expect.objectContaining({ replaceUrl: true }));
  });

  it('R-CI-011 a grant that writes without ref_protected = true shows the rule before the save and one click adds the condition', async () => {
    const { root, fixture, get } = await renderScreen(MappingEditor, { inputs: { issuer: 'gitlab-com' } });
    await fillMinimalNewMapping(root, fixture, true);
    expect(text(root)).toContain('must require ref_protected = true');
    buttonByText(root, 'Require protected refs').click();
    await settle(fixture);
    expect(text(root)).not.toContain('must require ref_protected = true');
    expect(text(root)).toContain('ref_protected = true');
    vi.spyOn(TestBed.inject(Router), 'navigate').mockResolvedValue(true);
    buttonByText(root, 'Create mapping').click();
    await sleep(fixture, 100);
    expect(ci(get).mappings.find((m) => m.name === 'team-b-release')?.match).toEqual({
      all: [{ claim: 'project_id', in: ['4711'] }, { claim: 'ref_protected', equals: true }],
    });
  });

  it('R-CI-011 the server refusal unprotected_write is shown next to the condition in clear words', async () => {
    const { root, fixture } = await renderScreen(MappingEditor, { inputs: { issuer: 'gitlab-com' } });
    await fillMinimalNewMapping(root, fixture, true);
    buttonByText(root, 'Create mapping').click();
    await sleep(fixture, 100);
    const error = root.querySelector('#ci-m-match-error');
    expect(text(error!)).toContain('must require protected refs');
  });

  it('R-CI-011 allowing unprotected refs shows a visible warning and the save shows the warning the server returns', async () => {
    const { root, fixture } = await renderScreen(MappingEditor, { inputs: { issuer: 'gitlab-com' } });
    await fillMinimalNewMapping(root, fixture, true);
    expect(root.querySelector('#ci-m-unprotected-warning')).toBeNull();
    checkbox(root, 'Allow unprotected refs').click();
    await settle(fixture);
    const warning = root.querySelector('#ci-m-unprotected-warning');
    expect(text(warning!)).toContain('Anyone who can push a branch');
    expect(warning!.querySelector('.material-icons')).toBeTruthy();
    // With the option the hint of the rule is gone: the choice is made knowingly.
    expect(text(root)).not.toContain('must require ref_protected = true');
    const navigate = vi.spyOn(TestBed.inject(Router), 'navigate').mockResolvedValue(true);
    buttonByText(root, 'Create mapping').click();
    await sleep(fixture, 100);
    const state = (navigate.mock.calls[0]![1] as { state: { warnings: { code: string }[] } }).state;
    expect(state.warnings.map((w) => w.code)).toEqual(expect.arrayContaining(['unprotected_refs_allowed', 'write_without_pipeline_source']));
  });

  it('R-CI-010 the warnings of a replace are shown with their code and detail', async () => {
    const { root, fixture, get } = await renderScreen(MappingEditor, { inputs: { issuer: 'gitlab-com', mapping: 'team-a-release' } });
    typeInto(root, 'Description', 'Changed text');
    buttonByText(root, 'Save mapping').click();
    await sleep(fixture, 100);
    expect(ci(get).calls).toContain('replaceMapping system gitlab-com/team-a-release');
    expect(ci(get).ifMatch).toEqual(['"1"']);
    const warnings = text(root.querySelector('trove-ci-warnings')!);
    expect(warnings).toContain('Saved, with warnings');
    expect(warnings).toContain('write_without_pipeline_source');
    expect(warnings).toContain('Add a pipeline_source condition');
  });

  it('R-CI-013 explicit ownership none on a writing grant warns before the save (shared_ownership)', async () => {
    const { root, fixture } = await renderScreen(MappingEditor, { inputs: { issuer: 'gitlab-com', mapping: 'team-a-release' } });
    const owner = [...root.querySelectorAll('trove-select-field')].find((f) => text(f).includes('Ownership of package names'))!.querySelector('select') as HTMLSelectElement;
    type(owner, 'none');
    await settle(fixture);
    expect(text(root)).toContain('co-owns them (shared_ownership)');
  });

  it('R-CI-009 a priority that is taken shows next to the priority field', async () => {
    const { root, fixture } = await renderScreen(MappingEditor, { inputs: { issuer: 'gitlab-com' } });
    await fillMinimalNewMapping(root, fixture, false);
    typeInto(root, 'Priority', '100');
    buttonByText(root, 'Create mapping').click();
    await sleep(fixture, 100);
    expect(text(root)).toContain('Another mapping of this tier has this priority');
  });

  it('R-CI-013 the 422 reasons of the grants appear under the grant they name', async () => {
    const { root, fixture, get } = await renderScreen(MappingEditor, { inputs: { issuer: 'gitlab-com' } });
    await fillMinimalNewMapping(root, fixture, false);
    ci(get).failure = new ApiError(
      422,
      { code: 'validation_failed', errors: [{ pointer: '/grants/inline/0/actions', code: 'action_not_allowed' }] },
      'r',
    );
    buttonByText(root, 'Add inline grant').click();
    await settle(fixture);
    buttonByText(root, 'Create mapping').click();
    await sleep(fixture, 100);
    expect(text(root.querySelector('.inline-grant')!)).toContain('This principal cannot hold this action');
  });

  it('R-CI-010 an invalid condition reports its leaf where the server points', async () => {
    const { root, fixture, get } = await renderScreen(MappingEditor, { inputs: { issuer: 'gitlab-com' } });
    await fillMinimalNewMapping(root, fixture, false);
    ci(get).failure = new ApiError(422, { code: 'validation_failed', errors: [{ pointer: '/match/all/0', code: 'condition_invalid' }] }, 'r');
    buttonByText(root, 'Create mapping').click();
    await sleep(fixture, 100);
    const leaf = root.querySelector('[data-pointer="/match/all/0"]')!;
    expect(text(leaf)).toContain('The condition is not valid');
  });

  it('R-UI-035 the builder offers all, any and not, the operators and ignore case, and the JSON view shows and takes the same condition', async () => {
    const { root, fixture } = await renderScreen(MappingEditor, { inputs: { issuer: 'gitlab-com' } });
    buttonByText(root, 'Add negation').click();
    await settle(fixture);
    buttonByText(root, 'Add group').click();
    await settle(fixture);
    expect(root.querySelectorAll('trove-ci-condition-node .group')).toHaveLength(3);
    const operators = [...root.querySelectorAll('trove-ci-condition-node select')]
      .filter((s) => [...(s as HTMLSelectElement).options].some((o) => o.value === 'regex'))
      .map((s) => [...(s as HTMLSelectElement).options].map((o) => o.value));
    expect(operators[0]).toEqual(['equals', 'in', 'glob', 'regex', 'present']);
    buttonByText(root, 'JSON').click();
    await settle(fixture);
    const json = root.querySelector<HTMLTextAreaElement>('#ci-m-json')!;
    expect(JSON.parse(json.value)).toEqual({
      all: [{ claim: 'project_id', in: [] }, { not: { claim: '', equals: '' } }, { all: [{ claim: '', equals: '' }] }],
    });
    type(json, JSON.stringify({ all: [{ claim: 'project_id', equals: '4242', ignoreCase: true }] }));
    await settle(fixture);
    expect(text(root)).toContain('Reads as: project_id = 4242 (ignoring case)');
    buttonByText(root, 'Builder').click();
    await settle(fixture);
    expect((root.querySelector('trove-ci-condition-node input[list="ci-claims"]') as HTMLInputElement).value).toBe('project_id');
    expect(root.querySelector<HTMLInputElement>('.fold input')!.checked).toBe(true);
  });

  it('R-UI-035 JSON that is not an object is flagged and blocks the save until it is valid', async () => {
    const { root, fixture, get } = await renderScreen(MappingEditor, { inputs: { issuer: 'gitlab-com' } });
    await fillMinimalNewMapping(root, fixture, false);
    buttonByText(root, 'JSON').click();
    await settle(fixture);
    type(root.querySelector('#ci-m-json') as HTMLTextAreaElement, '[1, 2');
    await settle(fixture);
    expect(text(root)).toContain('This is not a JSON object');
    expect((buttonByText(root, 'Create mapping') as HTMLButtonElement).disabled).toBe(true);
    expect((buttonByText(root, 'Builder') as HTMLButtonElement).getAttribute('aria-pressed')).toBe('false');
    expect(ci(get).calls).toEqual([]);
  });

  it('R-UI-035 a caller with ci:read only sees the mapping and cannot change it, and is told which permission is missing', async () => {
    const { root } = await renderScreen(MappingEditor, { inputs: { issuer: 'gitlab-com', mapping: 'team-a-release' }, capabilities: ['ci:read'] });
    expect(text(root)).toContain('Read only: changing this needs the permission ci:write');
    expect(root.querySelector('button[type="submit"]')).toBeNull();
    expect(root.querySelector('fieldset.plain')!.hasAttribute('disabled')).toBe(true);
    expect(root.querySelector('trove-ci-condition-node button')).toBeNull();
  });

  it('R-UI-035 "Test with claims" opens the tester for the stored mapping, and says so when the form has unsaved changes', async () => {
    const { root, fixture } = await renderScreen(MappingEditor, { inputs: { issuer: 'gitlab-com', mapping: 'team-a-release' } });
    const link = buttonByText(root, 'Test with claims') as HTMLAnchorElement;
    expect(link.getAttribute('href')).toBe('/admin/ci/gitlab-com/test?mapping=team-a-release');
    expect(text(root)).not.toContain('Save your changes first');
    typeInto(root, 'Description', 'edited');
    await settle(fixture);
    expect(text(root)).toContain('Save your changes first to test them');
  });

  it('R-UI-035 "CI snippet" shows the snippet text of the chosen client with a copy button', async () => {
    const { root, fixture, get } = await renderScreen(MappingEditor, { inputs: { issuer: 'gitlab-com', mapping: 'team-a-release' } });
    ci(get).snippets.set('gitlab-com/team-a-release/npm', 'id_tokens:\n  TROVE_ID_TOKEN:\n    aud: https://trove.example.com\nnpm-line');
    ci(get).snippets.set('gitlab-com/team-a-release/nuget', 'id_tokens:\n  TROVE_ID_TOKEN:\nnuget-line');
    buttonByText(root, 'CI snippet').click();
    await sleep(fixture, 80);
    expect(root.querySelector('pre.snippet')!.textContent).toContain('npm-line');
    expect(root.querySelector('trove-copy-button button')!.getAttribute('aria-label')).toBe('Copy the snippet');
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    (root.querySelector('trove-copy-button button') as HTMLButtonElement).click();
    await settle(fixture);
    expect(writeText).toHaveBeenCalledWith(expect.stringContaining('npm-line'));
    type([...root.querySelectorAll('trove-select-field')].find((f) => text(f).includes('Package client'))!.querySelector('select') as HTMLSelectElement, 'nuget');
    await sleep(fixture, 80);
    expect(root.querySelector('pre.snippet')!.textContent).toContain('nuget-line');
    await expectAccessible(root);
  });

  it('R-UI-035 a stale replace opens the conflict dialog instead of overwriting', async () => {
    const { root, fixture, get } = await renderScreen(MappingEditor, { inputs: { issuer: 'gitlab-com', mapping: 'team-a-release' } });
    ci(get).mappings = ci(get).mappings.map((m) => ({ ...m, version: 2 }));
    typeInto(root, 'Description', 'mine');
    buttonByText(root, 'Save mapping').click();
    await sleep(fixture, 150);
    expect(document.querySelector('trove-conflict-dialog')).toBeTruthy();
  });
});

describe('The mapping editor of a project (Q-CI-26, R-CI-019)', () => {
  const inputs = { key: 'web', issuer: 'gitlab-com' };

  it('Q-CI-26 starts with the name prefix, offers the project repositories and policies, and offers no groups', async () => {
    const { root } = await renderScreen(MappingEditor, { inputs, capabilities: [], setup: projectAdmin() });
    expect(root.querySelector<HTMLInputElement>('input[name="name"]')!.value).toBe('web-');
    expect(text(root)).toContain('Starts with web- and cannot be changed later.');
    expect(text(root)).toContain('Only the permission policies of this project can be named. Groups are global');
    expect(text(root)).not.toContain('Add a group');
    const policies = [...select(root).options].map((o) => o.value);
    expect(policies).toContain('');
  });

  it('Q-CI-26 saves through the project route, never sends groups, and keeps the project repositories as the quick choices', async () => {
    const { root, fixture, get } = await renderScreen(MappingEditor, { inputs, capabilities: [], setup: projectAdmin() });
    ci(get).projectPolicies['web'] = ['web-read'];
    typeInto(root, 'Name', 'web-publish');
    type(valueArea(root), '4242');
    buttonByText(root, 'Add inline grant').click();
    await settle(fixture);
    expect(text(root.querySelector('.quick')!)).toContain('generic-local');
    expect(text(root.querySelector('.quick')!)).toContain('@any');
    type(root.querySelector('textarea[name="include-0"]') as HTMLTextAreaElement, '@web/*');
    vi.spyOn(TestBed.inject(Router), 'navigate').mockResolvedValue(true);
    buttonByText(root, 'Create mapping').click();
    await sleep(fixture, 100);
    expect(ci(get).calls).toEqual(['createMapping web gitlab-com/web-publish']);
    const stored = ci(get).mappings.find((m) => m.name === 'web-publish')!;
    expect(stored.tier).toBe('project:web');
    expect(stored.grants.groups).toBeUndefined();
    expect(stored.grants.inline?.[0]?.repositories).toEqual(['generic-local']);
  });

  it('Q-CI-26 the reasons of the project bounds are explained next to the member', async () => {
    const { root, fixture, get } = await renderScreen(MappingEditor, { inputs, capabilities: [], setup: projectAdmin() });
    typeInto(root, 'Name', 'publish');
    type(valueArea(root), '4242');
    buttonByText(root, 'Add inline grant').click();
    await settle(fixture);
    buttonByText(root, 'Create mapping').click();
    await sleep(fixture, 100);
    expect(text(root)).toContain('The name must start with the key of the project and a hyphen.');
    typeInto(root, 'Name', 'web-publish');
    type(root.querySelector('textarea[name="repositories-0"]') as HTMLTextAreaElement, 'npm-internal');
    buttonByText(root, 'Create mapping').click();
    await sleep(fixture, 100);
    expect(text(root.querySelector('.inline-grant')!)).toContain('does not belong to this project');
    ci(get).failure = new ApiError(422, { code: 'validation_failed', errors: [{ pointer: '/grants/policies/0', code: 'policy_outside_project' }] }, 'r');
    buttonByText(root, 'Create mapping').click();
    await sleep(fixture, 100);
    expect(text(root)).toContain("can carry only the project's own policies");
    ci(get).failure = new ApiError(422, { code: 'validation_failed', errors: [{ pointer: '/grants/groups', code: 'groups_not_allowed_in_project' }] }, 'r');
    buttonByText(root, 'Create mapping').click();
    await sleep(fixture, 100);
    expect(text(root)).toContain('cannot carry groups');
  });

  it('Q-CI-26 an administrator whose managePolicies switch is off reads the mapping and is told why it is read-only', async () => {
    const { root } = await renderScreen(MappingEditor, {
      inputs: { key: 'web', issuer: 'gitlab-com', mapping: 'web-x' },
      capabilities: [],
      setup: (g) => {
        projectAdmin(false)(g);
        ci(g).mappings = [sampleMapping({ name: 'web-x', tier: 'project:web' })];
      },
    });
    expect(text(root)).toContain('Switched off for this project: managePolicies');
    expect(root.querySelector('button[type="submit"]')).toBeNull();
    // The snippet is the system administrators' operation: a project administrator is not offered it.
    expect(root.textContent).not.toContain('CI snippet');
  });
});

describe('The claim tester (R-UI-036, R-CI-019)', () => {
  const claimsArea = (root: HTMLElement): HTMLTextAreaElement => root.querySelector('#ci-t-claims') as HTMLTextAreaElement;
  const future = Math.floor(Date.now() / 1000) + 3600;

  it('R-UI-036 a pasted ID token becomes its pretty-printed claims at once, the token is discarded and only the claims are sent', async () => {
    const { root, fixture, get } = await renderScreen(ClaimTester, { inputs: { issuer: 'gitlab-com' } });
    const claims = { iss: 'https://gitlab.example.com', namespace_id: '4711', ref_protected: 'true', ref: 'main', exp: future };
    const jwt = token(claims);
    type(claimsArea(root), jwt);
    await settle(fixture);
    // The textarea no longer holds the token: not the signature, not the header.
    expect(claimsArea(root).value).toBe(JSON.stringify(claims, null, 2));
    expect(claimsArea(root).value).not.toContain(SIGNATURE);
    expect(text(root)).toContain('decoded in your browser and discarded');
    expect(text(root)).toContain('Only its claims are used');
    buttonByText(root, 'Evaluate').click();
    await sleep(fixture, 100);
    const sent = ci(get).evaluations;
    expect(sent).toHaveLength(1);
    expect(sent[0]!.project).toBeNull();
    expect(Object.keys(sent[0]!.request).sort()).toEqual(['claims', 'subjectKind']);
    expect(sent[0]!.request.claims).toEqual(claims);
    const wire = JSON.stringify(sent[0]);
    expect(wire).not.toContain(SIGNATURE);
    expect(wire).not.toContain(jwt);
  });

  it('R-UI-036 a token whose exp lies in the future says it is still a valid credential, one that has expired does not', async () => {
    const { root, fixture } = await renderScreen(ClaimTester, { inputs: { issuer: 'gitlab-com' } });
    type(claimsArea(root), token({ ref: 'main', exp: future }));
    await settle(fixture);
    expect(text(root.querySelector('#ci-t-token-valid')!)).toContain('still a valid credential');
    type(claimsArea(root), token({ ref: 'main', exp: 1000 }));
    await settle(fixture);
    expect(root.querySelector('#ci-t-token-valid')).toBeNull();
    expect(text(root)).toContain('decoded in your browser');
  });

  it('R-UI-036 typed JSON is kept as typed, and input that is neither JSON nor a token is explained', async () => {
    const { root, fixture, get } = await renderScreen(ClaimTester, { inputs: { issuer: 'gitlab-com' } });
    type(claimsArea(root), '{"ref":"main"}');
    await settle(fixture);
    expect(claimsArea(root).value).toBe('{"ref":"main"}');
    expect(root.querySelector('#ci-t-token-note')).toBeNull();
    type(claimsArea(root), 'hello');
    buttonByText(root, 'Evaluate').click();
    await settle(fixture);
    expect(text(root.querySelector('#ci-t-error')!)).toContain('neither a JSON object nor an ID token');
    type(claimsArea(root), '');
    buttonByText(root, 'Evaluate').click();
    await settle(fixture);
    expect(text(root.querySelector('#ci-t-error')!)).toContain('Enter the claims');
    expect(ci(get).evaluations).toEqual([]);
  });

  it('R-UI-036 "Insert sample GitLab claims" fills a realistic payload with the issuer and audience of the issuer', async () => {
    const { root, fixture } = await renderScreen(ClaimTester, { inputs: { issuer: 'gitlab-com' } });
    buttonByText(root, 'Insert sample GitLab claims').click();
    await settle(fixture);
    const claims = JSON.parse(claimsArea(root).value) as Record<string, unknown>;
    expect(claims['iss']).toBe('https://gitlab.example.com');
    expect(claims['aud']).toBe('https://trove.example.com');
    expect(claims['sub']).toBe('project_path:team-a/app:ref_type:branch:ref:main');
    for (const k of ['namespace_id', 'namespace_path', 'project_id', 'project_path', 'ref', 'ref_type', 'ref_protected', 'pipeline_source', 'environment', 'user_login', 'job_id', 'iat', 'nbf', 'exp']) {
      expect(claims, k).toHaveProperty(k);
    }
  });

  it('R-UI-036 Evaluate shows the matched mapping, each condition with expected and actual value and a mark of icon and text, and the resolved grants', async () => {
    const { root, fixture } = await renderScreen(ClaimTester, {
      inputs: { issuer: 'gitlab-com' },
      setup: (g) => {
        ci(g).mappings = [
          sampleMapping({
            grants: { inline: [{ repositories: ['npm-internal'], packages: { include: ['@{{ namespace_path | slug }}/*'] }, actions: ['read', 'deploy'], ownership: 'bind_project' }] },
          }),
        ];
      },
    });
    buttonByText(root, 'Insert sample GitLab claims').click();
    await settle(fixture);
    buttonByText(root, 'Evaluate').click();
    await sleep(fixture, 100);
    const result = text(root.querySelector('#ci-t-result')!.parentElement!);
    expect(result).toContain('Matched: team-a-release');
    expect(result).toContain('namespace_id in');
    expect(result).toContain('Passed');
    // Templates are resolved with the claims.
    expect(result).toContain('read, deploy: npm-internal · @team-a/*');
    expect(result).toContain('trv_cit_');
    expect(result).toContain('15 minutes');
    expect(root.querySelector('table.data caption')!.textContent).toContain('team-a-release');
    await expectAccessible(root);
  });

  it('R-UI-036 changing ref_protected to false switches the result to no match and marks that condition', async () => {
    const { root, fixture } = await renderScreen(ClaimTester, { inputs: { issuer: 'gitlab-com' } });
    buttonByText(root, 'Insert sample GitLab claims').click();
    await settle(fixture);
    const claims = JSON.parse(claimsArea(root).value) as Record<string, unknown>;
    type(claimsArea(root), JSON.stringify({ ...claims, ref_protected: 'false' }, null, 2));
    buttonByText(root, 'Evaluate').click();
    await sleep(fixture, 100);
    expect(text(root)).toContain('No mapping matches; the exchange would fail.');
    expect(text(root)).toContain('no_matching_mapping');
    const failed = root.querySelector('tr.failed')!;
    expect(text(failed)).toContain('ref_protected');
    expect(text(failed)).toContain('true');
    expect(text(failed)).toContain('false');
    // The mark is an icon and text, not colour alone.
    expect(text(failed)).toContain('Failed');
    expect(failed.querySelector('.material-icons')!.textContent).toBe('cancel');
    expect(text(root)).toContain('Did not match');
    await expectAccessible(root);
  });

  it('R-UI-036 an absent claim is shown as absent', async () => {
    const { root, fixture } = await renderScreen(ClaimTester, { inputs: { issuer: 'gitlab-com' } });
    type(claimsArea(root), '{"iss":"x"}');
    buttonByText(root, 'Evaluate').click();
    await sleep(fixture, 100);
    expect(text(root.querySelector('tr.failed')!)).toContain('(absent)');
  });

  it('R-UI-036 the claims are never put into browser storage and are gone when the route is left', async () => {
    const { root, fixture } = await renderScreen(ClaimTester, { inputs: { issuer: 'gitlab-com' } });
    const secretClaim = 'sentinel-project-path-1f3a9';
    type(claimsArea(root), JSON.stringify({ project_path: secretClaim }));
    buttonByText(root, 'Evaluate').click();
    await sleep(fixture, 100);
    expect(storedText()).not.toContain(secretClaim);
    expect(document.cookie).not.toContain(secretClaim);
    fixture.destroy();
    expect(storedText()).not.toContain(secretClaim);
    expect(fixture.nativeElement.textContent).not.toContain(secretClaim);
  });

  it('R-UI-036 opened from a mapping the claims start out matching it and its candidate is marked', async () => {
    const { root, fixture } = await renderScreen(ClaimTester, { inputs: { issuer: 'gitlab-com', mapping: 'team-a-release' } });
    expect((JSON.parse(claimsArea(root).value) as Record<string, string>)['namespace_id']).toBe('4711');
    buttonByText(root, 'Evaluate').click();
    await sleep(fixture, 100);
    expect(text(root)).toContain('The mapping you came from');
    expect(text(root)).toContain('Matched: team-a-release');
  });

  it('R-UI-036 a project\'s tester asks for an issuer and evaluates over the project tier only', async () => {
    const { root, fixture, get } = await renderScreen(ClaimTester, {
      inputs: { key: 'web' },
      capabilities: [],
      setup: (g) => {
        projectAdmin()(g);
        ci(g).mappings = [sampleMapping({ name: 'web-x', tier: 'project:web' }), sampleMapping({ name: 'system-only', tier: 'system', priority: 5 })];
      },
    });
    type(root.querySelector('trove-select-field select') as HTMLSelectElement, 'gitlab-com');
    await settle(fixture);
    type(claimsArea(root), JSON.stringify({ namespace_id: '4711', ref_protected: 'true' }));
    buttonByText(root, 'Evaluate').click();
    await sleep(fixture, 100);
    expect(ci(get).evaluations[0]!.project).toBe('web');
    const names = [...root.querySelectorAll('.candidate-head strong')].map((n) => text(n));
    expect(names).toEqual(['web-x']);
  });

  it('R-UI-036 a server without the federation shows "not available yet" when the tester evaluates', async () => {
    const { root, fixture, get } = await renderScreen(ClaimTester, { inputs: { issuer: 'gitlab-com' } });
    ci(get).notImplemented = true;
    type(claimsArea(root), '{"ref":"main"}');
    buttonByText(root, 'Evaluate').click();
    await sleep(fixture, 100);
    expect(text(root)).toContain('Not available yet');
  });
});

describe('The CI identity of a project (R-UI-035, Q-CI-26)', () => {
  it('Q-CI-26 shows the issuers read-only, the project mappings with the project editor, the dry run and the project issuances', async () => {
    const { root, fixture } = await renderScreen(ProjectCi, {
      inputs: { key: 'web' },
      capabilities: [],
      setup: (g) => {
        projectAdmin()(g);
        ci(g).mappings = [sampleMapping({ name: 'web-x', tier: 'project:web' }), sampleMapping({ name: 'system-only', tier: 'system', priority: 5 })];
        ci(g).issuances = [sampleIssuance({ mappingNames: ['web-x'] }), sampleIssuance({ tokenId: '55555555-5555-4555-8555-555555555555', mappingNames: ['system-only'] })];
      },
    });
    await settle(fixture);
    const sections = [...root.querySelectorAll('section.card')];
    expect(sections).toHaveLength(3);
    // The issuers are a read-only choice: no create, no edit.
    expect(text(sections[0]!)).toContain('gitlab-com');
    expect(text(sections[0]!)).toContain('https://gitlab.example.com');
    expect(sections[0]!.querySelector('a, button:not(.th-help)')).toBeNull();
    // Only the project's mappings, with the project's routes.
    expect(text(sections[1]!)).toContain('web-x');
    expect(text(sections[1]!)).not.toContain('system-only');
    expect(sections[1]!.querySelector('a[href="/admin/projects/web/ci/mappings/web-x?issuer=gitlab-com"]')).toBeTruthy();
    expect(sections[1]!.querySelector('a[href="/admin/projects/web/ci/mappings/new"]')).toBeTruthy();
    // Only the tokens of the project's mappings, and no bulk revoke.
    expect([...sections[2]!.querySelectorAll('tbody tr')]).toHaveLength(1);
    expect(sections[2]!.textContent).not.toContain('Revoke matching tokens');
    expect(root.querySelector('a[href="/admin/projects/web/ci/test"]')).toBeTruthy();
    await expectAccessible(root);
  });

  it('Q-CI-26 403 project_switch_off is shown as the other project screens show it, and nothing else is rendered', async () => {
    const { root } = await renderScreen(ProjectCi, {
      inputs: { key: 'web' },
      capabilities: [],
      setup: (g) => {
        projectAdmin(false)(g);
        ci(g).failure = new ApiError(403, { code: 'project_switch_off' }, 'r');
      },
    });
    expect(text(root)).toContain('This is switched off for the project. An administrator of trove can switch it on.');
    expect(root.querySelectorAll('section.card')).toHaveLength(0);
  });

  it('R-UI-035 a server whose federation is not built shows one "not available yet" card, not three errors', async () => {
    const { root } = await renderScreen(ProjectCi, { inputs: { key: 'web' }, capabilities: [], setup: (g) => { projectAdmin()(g); ci(g).notImplemented = true; } });
    expect(root.querySelectorAll('.unavailable')).toHaveLength(1);
    expect(root.querySelector('[role="alert"]')).toBeNull();
  });
});

describe('The project page links to the CI identity (Q-CI-26)', () => {
  it('Q-CI-26 an administrator of the project and a holder of ci:read are offered it, others are not', async () => {
    const admin = await renderScreen(ProjectDetail, { inputs: { key: 'web' }, capabilities: [], setup: projectAdmin() });
    expect(admin.root.querySelector('a[href="/admin/projects/web/ci"]')).toBeTruthy();
    const auditor = await renderScreen(ProjectDetail, { inputs: { key: 'web' }, capabilities: ['projects:read', 'ci:read'] });
    expect(auditor.root.querySelector('a[href="/admin/projects/web/ci"]')).toBeTruthy();
    const other = await renderScreen(ProjectDetail, { inputs: { key: 'web' }, capabilities: ['projects:read'] });
    expect(other.root.querySelector('a[href="/admin/projects/web/ci"]')).toBeNull();
  });
});
