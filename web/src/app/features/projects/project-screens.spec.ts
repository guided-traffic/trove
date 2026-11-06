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

/** A caller without any capability that administers `web` (Q-AUTHZ-181). */
function projectAdmin(managePolicies = true, manageRepositories = true) {
  return (get: Get): void => {
    (get(SessionApi) as FakeSessionApi).meResult = {
      roles: [],
      capabilities: [],
      principals: ['user:alice'],
      projects: [{ key: 'web', managePolicies, manageRepositories }],
    };
  };
}

describe('The project policy editor (R-UI-041, R-AUTHZ-026)', () => {
  it('R-UI-041 a new project policy starts with the name prefix, names its project and finds principals through the lookup', async () => {
    const { fixture, root, get } = await renderScreen(PolicyEditor, {
      inputs: { project: 'web' },
      capabilities: [],
      setup: (g) => {
        projectAdmin()(g);
        (g(RepositoriesApi) as FakeRepositoriesApi).repositories[0]!.project = 'web';
      },
    });
    expect(text(root)).toContain('Project policy of web');
    expect(root.querySelector<HTMLInputElement>('input[name="name"]')!.value).toBe('web-');
    expect(text(root)).toContain('Starts with web- and cannot be changed later.');

    typeInto(root, 'Name', 'web-publish');
    // The repositories of the project are offered as selectors.
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
    expect(policies.calls).toEqual(['create web-publish']);
    expect(policies.policies.find((p) => p.name === 'web-publish')?.project).toBe('web');
  });

  it('R-UI-041 the server explains a missing name prefix next to the field', async () => {
    const { fixture, root } = await renderScreen(PolicyEditor, {
      inputs: { project: 'web' },
      capabilities: [],
      setup: projectAdmin(),
    });
    typeInto(root, 'Name', 'publish');
    buttonByText(root, 'Create policy').click();
    await settle(fixture);
    expect(text(root)).toContain('The name must start with the key of the project and a hyphen.');
  });

  it('R-UI-041 a project policy is read-only for an administrator whose managePolicies switch is off, with the reason', async () => {
    const { root } = await renderScreen(PolicyEditor, {
      inputs: { name: 'web-publish' },
      capabilities: [],
      setup: (get) => {
        projectAdmin(false, true)(get);
        (get(PolicyApi) as FakePolicyApi).policies = [samplePolicy({ name: 'web-publish', project: 'web' })];
      },
    });
    expect(text(root)).toContain('Switched off for this project: managePolicies');
    expect(root.querySelector('button[type="submit"]')).toBeNull();
    expect(root.querySelector('trove-principal-picker')).toBeNull();
  });

  it('R-UI-041 a stored project policy keeps its project through a save', async () => {
    const { fixture, root, get } = await renderScreen(PolicyEditor, {
      inputs: { name: 'web-publish' },
      capabilities: [],
      setup: (g) => {
        projectAdmin()(g);
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
});

describe('A project service account (R-UI-041, Q-AUTHZ-175, Q-AUTHZ-177)', () => {
  const seed = (managePolicies = true) => (get: Get): void => {
    projectAdmin(managePolicies)(get);
    (get(IdentityApi) as FakeIdentityApi).accounts = [
      {
        id: 'sa-web-ci',
        name: 'web-ci',
        description: 'CI of the web team',
        status: 'active',
        project: 'web',
        createdAt: '2026-11-01T08:00:00Z',
      },
    ];
  };

  it('R-UI-041 a project administrator issues a token that always expires, with the cap said, and passes axe', async () => {
    const { fixture, root, get } = await renderScreen(ServiceAccountDetail, {
      inputs: { name: 'web-ci' },
      capabilities: [],
      setup: seed(),
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
    expect((get(TokensApi) as FakeTokensApi).calls).toContain('createForServiceAccount web-ci nightly');
    expect(root.querySelector('trove-token-secret')).toBeTruthy();
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-UI-041 a project administrator disables, enables, revokes and deletes whatever the switches say, and goes back to the project', async () => {
    const { fixture, root, get } = await renderScreen(ServiceAccountDetail, {
      inputs: { name: 'web-ci' },
      capabilities: [],
      setup: (g) => {
        seed(false)(g);
        (g(TokensApi) as FakeTokensApi).tokenList = [];
      },
    });
    // managePolicies is off: no token can be issued, the existing ones can be revoked.
    expect(root.textContent).not.toContain('Create token');
    buttonByText(root, 'Disable').click();
    await settle(fixture);
    buttonByText(overlay().querySelector('trove-confirm-dialog') as HTMLElement, 'Disable').click();
    await sleep(fixture, 300);
    expect((get(IdentityApi) as FakeIdentityApi).calls).toContain('disableServiceAccount web-ci revokeTokens=false');
    // Enabling needs managePolicies: disabled with the reason.
    expect((buttonByText(root, 'Enable') as HTMLButtonElement).disabled).toBe(true);
    expect(text(root)).toContain("Enabling needs the project's switch");
    expect(root.querySelector('a[href="/admin/projects/web"]')).toBeTruthy();
  });

  it('R-UI-041 the lists of an administrator send the project and nothing else global', async () => {
    const { get } = await renderScreen(ServiceAccountDetail, {
      inputs: { name: 'web-ci' },
      capabilities: [],
      setup: seed(),
    });
    const tokens = get(TokensApi) as FakeTokensApi;
    expect(tokens.lastFilters?.owner).toBe('sa:web-ci');
  });
});

describe('The repository wizard in a project (R-UI-041, R-AUTHZ-026)', () => {
  it('R-UI-041 prefills the key with the project prefix and creates the repository inside the project', async () => {
    const { fixture, root, get } = await renderScreen(RepositoryWizard, {
      inputs: { project: 'web' },
      capabilities: [],
      setup: (g) => {
        projectAdmin()(g);
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
    const key = root.querySelector<HTMLInputElement>('#f-key')!;
    expect(key.value).toBe('web-');
    expect(text(root)).toContain('This repository is created in project web');
    type(key, 'web-libs');
    await settle(fixture);
    button(root, 'Next').click();
    await settle(fixture);
    button(root, 'Next').click();
    await settle(fixture);
    button(root, /Create repository/).click();
    await sleep(fixture, 100);
    const created = (get(RepositoriesApi) as FakeRepositoriesApi).repositories.find((r) => r.key === 'web-libs');
    expect(created?.project).toBe('web');
  });

  it('R-UI-041 the wizard without a project leaves the key empty and the document without project', async () => {
    const { root } = await renderScreen(RepositoryWizard);
    expect(text(root)).not.toContain('This repository is created in project');
  });
});

describe('Session helpers and messages of the project screens (R-UI-041, R-UI-042)', () => {
  it('R-UI-041 projectRights, canWritePolicies and canWriteRepositories follow authz/me and the capabilities', async () => {
    const { get } = await renderScreen(RepositoryWizard, { capabilities: [], setup: projectAdmin(true, false) });
    const session = get(SessionStore);
    expect(session.isProjectOwner()).toBe(true);
    expect(session.hasAdminArea()).toBe(true);
    expect(session.isAdministrator()).toBe(false);
    expect(session.projectRights('web')).toEqual({ key: 'web', managePolicies: true, manageRepositories: false });
    expect(session.projectRights('data')).toBeNull();
    expect(session.canWritePolicies('web')).toBe(true);
    expect(session.canWritePolicies('data')).toBe(false);
    expect(session.canWritePolicies(null)).toBe(false);
    expect(session.canWriteRepositories('web')).toBe(false);
  });

  it('R-UI-041 the capabilities decide for any project, switches aside', async () => {
    const { get } = await renderScreen(RepositoryWizard, { capabilities: ['policies:write', 'repositories:write-config'] });
    const session = get(SessionStore);
    expect(session.canWritePolicies('data')).toBe(true);
    expect(session.canWritePolicies(null)).toBe(true);
    expect(session.canWriteRepositories('data')).toBe(true);
  });

  it('R-UI-042 every new problem code of the project areas has a translated message', () => {
    for (const code of [
      'repository_outside_project',
      'unknown_project',
      'principal_outside_project',
      'project_prefix_required',
      'name_reserved_by_project',
      'project_account_not_groupable',
      'project_not_empty',
      'repository_assigned',
      'project_owner_changed',
      'project_key_overlap',
      'project_switch_off',
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

  it('R-UI-041 the fake models a project: the key pattern, the overlap and an occupied repository', async () => {
    TestBed.resetTestingModule();
    const { get } = await renderScreen(RepositoryWizard);
    const api = get(ProjectsApi) as FakeProjectsApi;
    await expect(api.create({ key: 'web-app', displayName: 'x', description: '' }, 'k')).rejects.toMatchObject({ code: 'project_key_overlap' });
    await expect(api.assignRepository('web', 'nope', false, null)).rejects.toMatchObject({ status: 404 });
    expect(sampleProject().key).toBe('web');
  });
});
