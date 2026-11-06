import { provideHttpClient } from '@angular/common/http';
import { signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { Router, TitleStrategy, provideRouter, withComponentInputBinding } from '@angular/router';
import { App } from './app';
import { routes } from './app.routes';
import { PreferencesApi } from './api/preferences-api';
import { ApiError } from './api/problem';
import { ProjectsApi } from './api/projects-api';
import { SessionApi } from './api/session-api';
import { FakePreferencesApi, FakeSessionApi, provideFakeApis } from './api/testing/fakes';
import { FakeProjectsApi, sampleProject } from './api/testing/fakes-projects';
import { Layout } from './core/layout';
import { BrowserStorage } from './core/storage';
import { TroveTitleStrategy } from './core/title-strategy';
import { seriousViolations } from './testing/axe';

type Me = Exclude<FakeSessionApi['meResult'], ApiError>;

async function render(url: string, me?: Me, setup?: (projects: FakeProjectsApi, prefs: FakePreferencesApi) => void) {
  new BrowserStorage().clear('local');
  TestBed.resetTestingModule();
  await TestBed.configureTestingModule({
    imports: [App],
    providers: [
      provideRouter(routes, withComponentInputBinding()),
      { provide: TitleStrategy, useExisting: TroveTitleStrategy },
      provideHttpClient(),
      ...provideFakeApis(),
      // jsdom has no viewport: the sidebar is pinned.
      { provide: Layout, useValue: { pinned: signal(true) } },
    ],
  }).compileComponents();
  if (me) (TestBed.inject(SessionApi) as FakeSessionApi).meResult = me;
  setup?.(TestBed.inject(ProjectsApi) as FakeProjectsApi, TestBed.inject(PreferencesApi) as FakePreferencesApi);
  const fixture = TestBed.createComponent(App);
  await TestBed.inject(Router).navigateByUrl(url);
  for (let i = 0; i < 6; i++) {
    await fixture.whenStable();
    fixture.detectChanges();
  }
  return { fixture, root: fixture.nativeElement as HTMLElement };
}

const navLabels = (root: HTMLElement): string[] =>
  [...root.querySelectorAll('nav[aria-label="Main navigation"] .nav-label')].map((l) => l.textContent?.trim() ?? '');

const PROJECT_ADMIN: Me = {
  roles: [],
  capabilities: [],
  principals: ['user:alice'],
  projects: [{ key: 'web', managePolicies: true, manageRepositories: false }],
};

describe('The administration area for project administrators (R-UI-041, Q-AUTHZ-181)', () => {
  it('R-UI-041 a caller without any capability that administers a project sees the project screens only', async () => {
    const { root } = await render('/admin', PROJECT_ADMIN);
    expect(root.querySelector('#nav-admin')?.textContent).toContain('Admin area');
    expect(navLabels(root)).toEqual(['Projects']);
    // /admin starts at the projects, because the repository list is not for this caller.
    expect(root.querySelector('main h1')?.textContent?.trim()).toBe('Projects');
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-UI-041 a caller that administers no project and holds no capability sees no administration area', async () => {
    const { root } = await render('/browse', { roles: [], capabilities: [], principals: [], projects: [] });
    expect(root.querySelector('header a.admin-button')).toBeNull();
    expect(root.querySelector('#nav-admin')).toBeNull();
  });

  it('R-UI-041 the administrator sees the projects next to the other entries; the auditor too', async () => {
    const admin = await render('/admin/projects');
    expect(navLabels(admin.root)).toContain('Projects');
    expect(navLabels(admin.root)).toContain('Repositories');
    const auditor = await render('/admin/audit', {
      roles: ['auditor'],
      capabilities: ['projects:read', 'repositories:read-config', 'audit:read'],
      principals: [],
      projects: [],
    });
    expect(navLabels(auditor.root)).toContain('Projects');
  });

  it('R-UI-041 a caller with capabilities but no project and no projects:read has no Projects entry', async () => {
    const { root } = await render('/admin/audit', { roles: [], capabilities: ['audit:read'], principals: [], projects: [] });
    expect(navLabels(root)).not.toContain('Projects');
  });
});

describe('The project filter of the top bar (R-UI-041, R-API-021)', () => {
  const projects = (api: FakeProjectsApi): void => {
    api.projects = [sampleProject(), sampleProject({ key: 'data', displayName: 'Data' })];
  };

  it('R-UI-041 is offered when projects exist and the caller may list them, and stores the choice as projectFilter', async () => {
    const { fixture, root } = await render('/browse', undefined, projects);
    const button = root.querySelector<HTMLButtonElement>('button.project-button')!;
    expect(button.textContent).toContain('All projects');
    button.click();
    await fixture.whenStable();
    const items = [...document.querySelectorAll<HTMLElement>('.cdk-overlay-container [role="menuitemradio"]')];
    expect(items.map((i) => i.textContent?.replace(/\s+/g, ' ').trim())).toEqual([
      expect.stringContaining('All projects'),
      expect.stringContaining('Data (data)'),
      expect.stringContaining('Web platform (web)'),
    ]);
    expect(items[0]!.getAttribute('aria-checked')).toBe('true');
    items[2]!.click();
    for (let i = 0; i < 4; i++) {
      await fixture.whenStable();
      fixture.detectChanges();
    }
    expect((TestBed.inject(PreferencesApi) as FakePreferencesApi).stored.projectFilter).toBe('web');
    expect(root.querySelector('button.project-button')?.textContent).toContain('web');
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-UI-041 is not offered while no project exists', async () => {
    const { root } = await render('/browse', undefined, (api) => {
      api.projects = [];
    });
    expect(root.querySelector('button.project-button')).toBeNull();
  });

  it('R-UI-041 is offered to a project administrator without capabilities', async () => {
    const { root } = await render('/browse', PROJECT_ADMIN);
    expect(root.querySelector('button.project-button')).toBeTruthy();
  });

  it('R-UI-041 a stored key that names no visible project filters nothing', async () => {
    const { root } = await render('/browse', undefined, (api, prefs) => {
      api.projects = [sampleProject()];
      prefs.stored = { ...prefs.stored, projectFilter: 'gone' };
    });
    expect(root.querySelector('button.project-button')?.textContent).toContain('All projects');
  });

  it('R-UI-041 filters the repository list through project=', async () => {
    const { root } = await render('/admin/repositories', undefined, (api, prefs) => {
      api.projects = [sampleProject()];
      prefs.stored = { ...prefs.stored, projectFilter: 'web' };
    });
    expect(root.querySelector('.project-note')?.textContent).toContain('Showing the repositories of project');
    expect(root.querySelector('.project-note strong')?.textContent).toBe('web');
    // The fake repositories belong to no project: the filtered list is empty.
    expect(root.textContent).toContain('No repository matches the filters');
  });

  it('R-UI-041 filters the policy list through project=', async () => {
    const { root } = await render('/admin/permissions', undefined, (api, prefs) => {
      api.projects = [sampleProject()];
      prefs.stored = { ...prefs.stored, projectFilter: 'web' };
    });
    expect(root.textContent).toContain('Showing the policies of project');
    expect(root.textContent).toContain('No policy matches');
  });
});
