import { TestBed } from '@angular/core/testing';
import { PolicyApi } from '../../api/authz-api';
import { IdentityApi } from '../../api/identity-api';
import { ApiError } from '../../api/problem';
import { ProjectsApi } from '../../api/projects-api';
import { RepositoriesApi } from '../../api/repositories-api';
import { SessionApi } from '../../api/session-api';
import { FakeIdentityApi, FakePolicyApi, FakeTokensApi, samplePolicy } from '../../api/testing/access-fakes';
import { FakeSessionApi } from '../../api/testing/fakes';
import { FakeRepositoriesApi, PACKAGE_FORMAT } from '../../api/testing/fakes-catalog';
import { FakeProjectsApi, sampleProject } from '../../api/testing/fakes-projects';
import { TokensApi } from '../../api/tokens-api';
import { describeProblem } from '../../core/http/problem-messages';
import { helpFor } from '../../core/help-topics';
import { SessionStore } from '../../core/session-store';
import { seriousViolations } from '../../testing/axe';
import { button, buttonByText, renderScreen, settle, sleep, text, type, typeInto } from '../../testing/screen';
import { PREVIEW_DEBOUNCE_MS, PolicyEditor } from '../permissions/policy-editor';
import { RepositoryWizard } from '../repositories/repository-wizard';
import { ServiceAccountDetail } from '../service-accounts/service-account-detail';

type Get = <A>(token: abstract new (...args: never[]) => A) => A;

const overlay = (): HTMLElement => document.querySelector('.cdk-overlay-container') as HTMLElement;

/** A caller without any capability that is Owner of `web` (Q-AUTHZ-207, Q-AUTHZ-208). */
function projectOwner(role: 'owner' | 'maintainer' = 'owner') {
  return (get: Get): void => {
    (get(SessionApi) as FakeSessionApi).meResult = {
      roles: [],
      capabilities: [],
      principals: ['user:alice'],
      projects: [{ key: 'web', role }],
    };
  };
}

describe('The project policy editor (R-UI-041, R-AUTHZ-026)', () => {
  it('R-UI-041 a new project policy is named within the project, names its project and finds principals through the lookup', async () => {
    const { fixture, root, get } = await renderScreen(PolicyEditor, {
      inputs: { project: 'web' },
      capabilities: [],
      setup: projectOwner(),
    });
    expect(text(root)).toContain('Project policy of web');
    expect(root.querySelector<HTMLInputElement>('input[name="name"]')!.value).toBe('');
    expect(text(root)).toContain('Unique within the project web and cannot be changed later.');

    typeInto(root, 'Name', 'publish');
    // The repositories of the project are offered as selectors, by bare key.
    buttonByText(root, 'generic-local').click();
    await settle(fixture);
    expect(root.querySelector<HTMLTextAreaElement>('textarea[name="repositories"]')!.value).toBe('generic-local');
    // The lookup (Q-AUTHZ-182): two characters, then the match becomes an entry.
    typeInto(root, 'Find a user or group', 'web');
    await sleep(fixture, 400);
    buttonByText(root, 'group:web-leads').click();
    await settle(fixture);
    expect(root.querySelector<HTMLInputElement>('input[name="principal-0"]')!.value).toBe('group:web-leads');
    expect((get(ProjectsApi) as FakeProjectsApi).lookups).toEqual([{ key: 'web', q: 'web' }]);

    const policies = get(PolicyApi) as FakePolicyApi;
    await sleep(fixture, PREVIEW_DEBOUNCE_MS + 100);
    expect(policies.validated.length).toBeGreaterThan(0);
    expect(policies.validated.every((v) => v.project === 'web')).toBe(true);
    expect(await seriousViolations(root)).toEqual([]);
    buttonByText(root, 'Create policy').click();
    await sleep(fixture, 100);
    expect(policies.calls).toEqual(['create publish']);
    expect(policies.policies.find((p) => p.name === 'publish')?.project).toBe('web');
  });

  it('R-UI-041 a project policy is read-only for a member that is no Owner, with the reason', async () => {
    const { root } = await renderScreen(PolicyEditor, {
      inputs: { name: 'web-publish', project: 'web' },
      capabilities: [],
      setup: (get) => {
        projectOwner('maintainer')(get);
        (get(PolicyApi) as FakePolicyApi).policies = [samplePolicy({ name: 'web-publish', project: 'web' })];
      },
    });
    expect(text(root)).toContain('Read only: changing this needs the permission policies:write');
    expect(root.querySelector('button[type="submit"]')).toBeNull();
    expect(root.querySelector('trove-principal-picker')).toBeNull();
  });

  it('R-UI-041 a stored project policy keeps its project through a save', async () => {
    const { fixture, root, get } = await renderScreen(PolicyEditor, {
      inputs: { name: 'web-publish', project: 'web' },
      capabilities: [],
      setup: (g) => {
        projectOwner()(g);
        (g(PolicyApi) as FakePolicyApi).policies = [samplePolicy({ name: 'web-publish', project: 'web' })];
      },
    });
    typeInto(root, 'Description', 'Changed');
    await settle(fixture);
    buttonByText(root, 'Review and save').click();
    await settle(fixture);
    buttonByText(overlay().querySelector('trove-policy-diff-dialog') as HTMLElement, 'Save policy').click();
    await sleep(fixture, 300);
    const api = get(PolicyApi) as FakePolicyApi;
    expect(api.calls).toEqual(['replace web-publish "3"']);
    expect(api.validated.length).toBeGreaterThan(0);
    expect(api.validated.every((v) => v.project === 'web')).toBe(true);
  });

  it('R-UI-027 a global policy is unchanged: no project line, the capability decides', async () => {
    const { root, get } = await renderScreen(PolicyEditor);
    expect(text(root)).not.toContain('Project policy of');
    expect(root.querySelector('trove-principal-picker')).toBeNull();
    expect((get(PolicyApi) as FakePolicyApi).validated.every((v) => v.project === undefined)).toBe(true);
  });

  it('R-AUTHZ-004 a global policy asks for project/key selectors, a project policy for bare keys', async () => {
    const global = await renderScreen(PolicyEditor);
    expect(text(global.root)).toContain('One per line: project/key (web/npm-internal)');
    TestBed.resetTestingModule();
    const project = await renderScreen(PolicyEditor, { inputs: { project: 'web' }, capabilities: [], setup: projectOwner() });
    expect(text(project.root)).toContain('One per line: a key (npm-internal)');
  });
});

describe('A project service account (R-UI-041, Q-AUTHZ-175, Q-AUTHZ-177)', () => {
  const seed = (get: Get): void => {
    projectOwner()(get);
    (get(IdentityApi) as FakeIdentityApi).accounts = [
      {
        id: 'sa-web-ci',
        name: 'ci',
        principal: 'sa:web/ci',
        description: 'CI of the web team',
        status: 'active',
        project: 'web',
        createdAt: '2026-11-01T08:00:00Z',
      },
    ];
  };

  it('R-UI-041 an Owner issues a token that always expires, with the cap said, and passes axe', async () => {
    const { fixture, root, get } = await renderScreen(ServiceAccountDetail, {
      inputs: { name: 'ci', project: 'web' },
      capabilities: [],
      setup: seed,
    });
    expect(text(root)).toContain('Service account of project web');
    // No role section for a project account, and nothing global is read.
    expect(root.querySelector('trove-principal-roles')).toBeNull();
    buttonByText(root, 'Create token').click();
    await settle(fixture);
    const form = root.querySelector('trove-token-form') as HTMLElement;
    expect(text(form)).not.toContain('Never expires');
    expect(text(form)).toContain('caps the lifetime of service-account tokens');
    typeInto(form, 'Description', 'nightly');
    buttonByText(form, 'Create token').click();
    await settle(fixture);
    expect((get(TokensApi) as FakeTokensApi).calls).toContain('createForServiceAccount ci nightly');
    expect(root.querySelector('trove-token-secret')).toBeTruthy();
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-UI-041 an Owner disables, enables, revokes and deletes, and goes back to the project', async () => {
    const { fixture, root, get } = await renderScreen(ServiceAccountDetail, {
      inputs: { name: 'ci', project: 'web' },
      capabilities: [],
      setup: (g) => {
        seed(g);
        (g(TokensApi) as FakeTokensApi).tokenList = [];
      },
    });
    buttonByText(root, 'Disable').click();
    await settle(fixture);
    buttonByText(overlay().querySelector('trove-confirm-dialog') as HTMLElement, 'Disable').click();
    await sleep(fixture, 300);
    expect((get(IdentityApi) as FakeIdentityApi).calls).toContain('disableServiceAccount ci revokeTokens=false');
    expect((buttonByText(root, 'Enable') as HTMLButtonElement).disabled).toBe(false);
    expect(root.querySelector('a[href="/admin/projects/web"]')).toBeTruthy();
  });

  it('R-UI-041 the token list of a project account asks for its qualified principal', async () => {
    const { get } = await renderScreen(ServiceAccountDetail, {
      inputs: { name: 'ci', project: 'web' },
      capabilities: [],
      setup: seed,
    });
    const tokens = get(TokensApi) as FakeTokensApi;
    expect(tokens.lastFilters?.owner).toBe('sa:web/ci');
  });

  it('R-AUTHZ-026 the same name may exist in two projects: the account is read inside the project of the route', async () => {
    const { root } = await renderScreen(ServiceAccountDetail, {
      inputs: { name: 'ci', project: 'data' },
      capabilities: ['identities:write', 'tokens:read'],
      setup: (g) => {
        seed(g);
        const api = g(IdentityApi) as FakeIdentityApi;
        api.accounts = [...api.accounts, { ...api.accounts[0]!, id: 'sa-data-ci', principal: 'sa:data/ci', description: 'CI of data', project: 'data' }];
      },
    });
    expect(text(root)).toContain('Service account of project data');
  });
});

describe('The repository wizard in a project (R-REPO-001, R-AUTHZ-026)', () => {
  it('R-REPO-001 starts on the project of the link, and creates the repository inside it', async () => {
    const { fixture, root, get } = await renderScreen(RepositoryWizard, {
      inputs: { project: 'web' },
      capabilities: [],
      setup: (g) => {
        projectOwner()(g);
        (g(RepositoriesApi) as FakeRepositoriesApi).formatsResult = [PACKAGE_FORMAT];
      },
    });
    const radio = (kind: string, value: string): void => {
      const el = root.querySelector<HTMLInputElement>(`input[name="${kind}"][value="${value}"]`)!;
      el.checked = true;
      el.dispatchEvent(new Event('change', { bubbles: true }));
    };
    root.querySelector<HTMLElement>('[role="option"]')!.click();
    await settle(fixture);
    button(root, 'Next').click();
    await settle(fixture);
    radio('class', 'local');
    await settle(fixture);
    button(root, 'Next').click();
    await settle(fixture);
    expect(root.querySelector<HTMLSelectElement>('select[name="project"]')!.value).toBe('web');
    type(root.querySelector<HTMLInputElement>('#f-key')!, 'libs');
    await settle(fixture);
    button(root, 'Next').click();
    await settle(fixture);
    button(root, 'Next').click();
    await settle(fixture);
    button(root, /Create repository/).click();
    await sleep(fixture, 100);
    const api = get(RepositoriesApi) as FakeRepositoriesApi;
    expect(api.createProjects).toEqual(['web']);
    expect(api.repositories.find((r) => r.key === 'libs')?.project).toBe('web');
  });

  it('R-REPO-001 the wizard without a project asks for one and does not go on without', async () => {
    const { fixture, root } = await renderScreen(RepositoryWizard, { setup: (g) => ((g(RepositoriesApi) as FakeRepositoriesApi).formatsResult = [PACKAGE_FORMAT]) });
    root.querySelector<HTMLElement>('[role="option"]')!.click();
    await settle(fixture);
    button(root, 'Next').click();
    await settle(fixture);
    const el = root.querySelector<HTMLInputElement>('input[name="class"][value="local"]')!;
    el.checked = true;
    el.dispatchEvent(new Event('change', { bubbles: true }));
    await settle(fixture);
    button(root, 'Next').click();
    await settle(fixture);
    expect(root.querySelector<HTMLSelectElement>('select[name="project"]')!.value).toBe('');
    type(root.querySelector<HTMLInputElement>('#f-key')!, 'libs');
    await settle(fixture);
    expect((button(root, 'Next') as HTMLButtonElement).disabled).toBe(true);
  });
});

describe('Session helpers and messages of the project screens (R-UI-041, R-UI-042)', () => {
  it('R-UI-041 projectRole, canWritePolicies and canWriteRepositories follow authz/me and the capabilities', async () => {
    const { get } = await renderScreen(RepositoryWizard, { capabilities: [], setup: projectOwner() });
    const session = get(SessionStore);
    expect(session.isProjectOwner()).toBe(true);
    expect(session.hasAdminArea()).toBe(true);
    expect(session.isAdministrator()).toBe(false);
    expect(session.projectRole('web')).toBe('owner');
    expect(session.projectRole('data')).toBeNull();
    expect(session.canWritePolicies('web')).toBe(true);
    expect(session.canWritePolicies('data')).toBe(false);
    expect(session.canWritePolicies(null)).toBe(false);
    expect(session.canWriteRepositories('web')).toBe(true);
    expect(session.canWriteRepositories('data')).toBe(false);
  });

  it('R-UI-041 a Maintainer reads and does not administer', async () => {
    const { get } = await renderScreen(RepositoryWizard, { capabilities: [], setup: projectOwner('maintainer') });
    const session = get(SessionStore);
    expect(session.isProjectOwner()).toBe(false);
    expect(session.hasAdminArea()).toBe(false);
    expect(session.canWriteRepositories('web')).toBe(false);
  });

  it('R-UI-041 the capabilities decide for any project', async () => {
    const { get } = await renderScreen(RepositoryWizard, { capabilities: ['policies:write', 'repositories:write-config'] });
    const session = get(SessionStore);
    expect(session.canWritePolicies('data')).toBe(true);
    expect(session.canWritePolicies(null)).toBe(true);
    expect(session.canWriteRepositories('data')).toBe(true);
  });

  it('R-UI-042 every problem code of the project areas has a translated message', () => {
    for (const code of [
      'repository_outside_project',
      'unknown_project',
      'principal_outside_project',
      'principal_not_allowed',
      'project_account_not_groupable',
      'project_not_empty',
      'secret_value_required',
      'outbound_path_restricted',
    ]) {
      const p = describeProblem(new ApiError(409, { code, detail: 'English detail' }, 'rid-1'));
      expect(p.known, code).toBe(true);
      expect(p.text, code).not.toBe('English detail');
    }
  });

  it('R-UI-050 the project screens open the manual page about projects', () => {
    expect(helpFor('/admin/projects')).toEqual(['/help', 'admin', 'projects']);
    expect(helpFor('/admin/projects/web')).toEqual(['/help', 'admin', 'projects']);
  });

  it('Q-AUTHZ-217 the fake models a project: the key pattern, an existing key and the last Owner', async () => {
    TestBed.resetTestingModule();
    const { get } = await renderScreen(RepositoryWizard);
    const api = get(ProjectsApi) as FakeProjectsApi;
    await expect(api.create({ key: 'web', displayName: 'x', description: '' }, 'k')).rejects.toMatchObject({ code: 'already_exists' });
    await expect(api.removeMember('web', 'group:web-leads')).rejects.toMatchObject({ code: 'last_owner' });
    expect(sampleProject().key).toBe('web');
  });
});
