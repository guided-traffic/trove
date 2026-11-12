import { provideHttpClient } from '@angular/common/http';
import { signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { Router, TitleStrategy, provideRouter, withComponentInputBinding } from '@angular/router';
import { App } from './app';
import { routes } from './app.routes';
import { ApiError } from './api/problem';
import { ProjectsApi } from './api/projects-api';
import { SessionApi } from './api/session-api';
import { FakeSessionApi, provideFakeApis } from './api/testing/fakes';
import { FakeProjectsApi, sampleProject } from './api/testing/fakes-projects';
import { Layout } from './core/layout';
import { BrowserStorage } from './core/storage';
import { TroveTitleStrategy } from './core/title-strategy';
import { seriousViolations } from './testing/axe';

type Me = Exclude<FakeSessionApi['meResult'], ApiError>;

async function render(url: string, me?: Me, setup?: (projects: FakeProjectsApi) => void, keepStorage = false) {
  if (!keepStorage) new BrowserStorage().clear('local');
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
  setup?.(TestBed.inject(ProjectsApi) as FakeProjectsApi);
  const fixture = TestBed.createComponent(App);
  await TestBed.inject(Router).navigateByUrl(url);
  await settleApp(fixture);
  return { fixture, root: fixture.nativeElement as HTMLElement };
}

async function settleApp(fixture: { whenStable(): Promise<unknown>; detectChanges(): void }): Promise<void> {
  for (let i = 0; i < 6; i++) {
    await fixture.whenStable();
    fixture.detectChanges();
  }
}

const navLabels = (root: HTMLElement): string[] =>
  [...root.querySelectorAll('nav[aria-label] .nav-label')].map((l) => l.textContent?.trim() ?? '');

const TWO = (api: FakeProjectsApi): void => {
  api.projects = [sampleProject(), sampleProject({ key: 'data', displayName: 'Data' })];
};

const OWNER: Me = {
  roles: [],
  capabilities: [],
  principals: ['user:alice'],
  projects: [{ key: 'web', role: 'owner' }],
};
const READER: Me = { ...OWNER, projects: [{ key: 'web', role: 'reader' }] };

describe('The project is the context of the content (Q-UI-129)', () => {
  it('Q-UI-129 a caller with exactly one project starts in it, with the project sidebar', async () => {
    const { root } = await render('/', OWNER);
    expect(TestBed.inject(Router).url).toBe('/projects/web/repositories');
    expect(root.querySelector('.project-switcher .switcher-name')?.textContent).toContain('Web platform');
    expect(navLabels(root)).toEqual([
      'Repositories',
      'Packages',
      'Browse',
      'Vulnerabilities',
      'Access',
      'Members',
      'Permission policies',
      'Service accounts',
      'CI identity',
      'Settings',
    ]);
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('Q-UI-129 a caller with several projects starts on the list of them, and an entry opens the project', async () => {
    const { fixture, root } = await render('/', undefined, TWO);
    expect(TestBed.inject(Router).url).toBe('/projects');
    expect(root.querySelector('main h1')?.textContent?.trim()).toBe('Projects');
    // A project is named by its display name, with its key under it (Q-UI-311).
    expect([...root.querySelectorAll('main tbody tr th .secondary')].map((t) => t.textContent?.trim())).toEqual(['data', 'web']);
    // Outside a project the sidebar is the start sidebar, without a switcher.
    expect(root.querySelector('.project-switcher')).toBeNull();
    expect(navLabels(root)).toEqual(['Projects', 'Guides']);
    root.querySelector<HTMLAnchorElement>('main tbody a[href="/projects/data"]')!.click();
    await settleApp(fixture);
    expect(TestBed.inject(Router).url).toBe('/projects/data/repositories');
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('Q-UI-129 a caller without a project is told how to get one', async () => {
    const { root } = await render('/', { roles: [], capabilities: [], principals: ['user:alice'], projects: [] }, (api) => {
      api.projects = [];
    });
    expect(TestBed.inject(Router).url).toBe('/projects');
    expect(root.querySelector('main')?.textContent).toContain('You are not a member of a project yet');
  });

  it('Q-UI-129 R-UI-041 the top bar has no project filter any more', async () => {
    const { root } = await render('/', undefined, TWO);
    expect(root.querySelector('button.project-button')).toBeNull();
    expect(root.querySelector('header')?.textContent).not.toContain('All projects');
  });

  it('Q-UI-129 a reader sees the project without the screens that need the Owner role or a capability', async () => {
    const { root } = await render('/projects/web/packages', READER);
    // A plain user gets the content alone; members and settings are the About section of the list (Q-UI-317).
    expect(navLabels(root)).toEqual(['Repositories', 'Packages', 'Browse', 'Vulnerabilities']);
  });

  it('Q-UI-129 the switcher is a disclosure: it lists the recent projects and the matches, narrows by typing and closes with Escape', async () => {
    const { fixture, root } = await render('/projects/web/packages', undefined, TWO);
    // Visiting a project remembers it.
    TestBed.inject(Router).navigateByUrl('/projects/data/packages');
    await settleApp(fixture);
    await TestBed.inject(Router).navigateByUrl('/projects/web/packages');
    await settleApp(fixture);
    const trigger = root.querySelector<HTMLButtonElement>('.switcher-button')!;
    expect(trigger.getAttribute('aria-expanded')).toBe('false');
    trigger.click();
    await settleApp(fixture);
    expect(trigger.getAttribute('aria-expanded')).toBe('true');
    const panel = root.querySelector('#project-switcher-panel')!;
    expect(panel.textContent).toContain('Recently opened');
    expect([...panel.querySelectorAll('a.switcher-item')].map((a) => a.getAttribute('href'))).toEqual([
      '/projects/data',
      '/projects/data',
      '/projects/web',
    ]);
    expect(panel.querySelector('a[aria-current="true"]')?.getAttribute('href')).toBe('/projects/web');
    expect(await seriousViolations(root)).toEqual([]);

    const search = panel.querySelector<HTMLInputElement>('input[type="search"]')!;
    search.value = 'dat';
    search.dispatchEvent(new Event('input', { bubbles: true }));
    await settleApp(fixture);
    expect([...panel.querySelectorAll('a.switcher-item')].map((a) => a.getAttribute('href'))).toEqual(['/projects/data']);
    search.value = 'zzz';
    search.dispatchEvent(new Event('input', { bubbles: true }));
    await settleApp(fixture);
    expect(panel.textContent).toContain('No project matches.');

    panel.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    await settleApp(fixture);
    expect(root.querySelector('#project-switcher-panel')).toBeNull();
    expect(trigger.getAttribute('aria-expanded')).toBe('false');
  });

  it('Q-UI-129 choosing a project in the switcher opens it', async () => {
    const { fixture, root } = await render('/projects/web/members', undefined, TWO);
    root.querySelector<HTMLButtonElement>('.switcher-button')!.click();
    await settleApp(fixture);
    root.querySelector<HTMLAnchorElement>('#project-switcher-panel a[href="/projects/data"]')!.click();
    await settleApp(fixture);
    expect(TestBed.inject(Router).url).toBe('/projects/data/repositories');
    expect(root.querySelector('#project-switcher-panel')).toBeNull();
    expect(root.querySelector('.switcher-name')?.textContent).toContain('Data');
  });

  it('Q-UI-129 the recent projects are kept per signed-in principal in browser storage', async () => {
    await render('/projects/web/packages', undefined, TWO);
    expect(new BrowserStorage().get('local', 'trove.recent-projects.alice')).toBe('["web"]');
  });

  it('Q-UI-129 the breadcrumbs of a project screen read projects, the project, the section', async () => {
    const { root } = await render('/projects/web/repositories', undefined, TWO);
    const crumbs = [...root.querySelectorAll('nav.breadcrumbs li')].map((li) => li.textContent?.trim());
    expect(crumbs).toEqual(['Projects', 'web', 'Repositories']);
    expect([...root.querySelectorAll('nav.breadcrumbs a')].map((a) => a.getAttribute('href'))).toEqual(['/projects', '/projects/web']);
  });

  it('R-UI-020 inside a project the repository list names format and class under the key, without the project and without a column of them', async () => {
    const { root } = await render('/projects/web/repositories', undefined, TWO);
    const kind = root.querySelector('main tbody th[scope="row"] .repo-kind')!;
    expect(kind.querySelector('.format-name trove-format-icon')).toBeTruthy();
    expect(kind.querySelector('.class-name')).toBeTruthy();
    expect(kind.querySelector('a.project-link')).toBeNull();
    expect(root.querySelector('main tbody td.kind-cell')).toBeNull();
    expect([...root.querySelectorAll('main thead th')].some((th) => th.textContent?.trim().startsWith('Format'))).toBe(false);
  });
});

describe('The administration area (R-UI-041, Q-UI-129)', () => {
  it('Q-UI-129 an Owner without any capability manages the project inside it and has no admin area', async () => {
    const { root } = await render('/projects/web/policies', OWNER);
    expect(root.querySelector('header a.admin-button')).toBeNull();
    expect(root.querySelector('#nav-admin')).toBeNull();
  });

  it('R-UI-041 a caller that administers no project and holds no capability sees no administration area', async () => {
    const { root } = await render('/projects', { roles: [], capabilities: [], principals: [], projects: [] });
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

  it('Q-UI-129 the admin lists name the project and link to it; the repository list in the line under the key (UI-A-30)', async () => {
    const repositories = await render('/admin/repositories');
    expect([...repositories.root.querySelectorAll('th[scope="col"]')].map((h) => h.textContent?.trim())).not.toContain('Project');
    expect(repositories.root.querySelector('tbody th .secondary a.project-link[href="/projects/web"]')).toBeTruthy();
    expect(repositories.root.querySelector('tbody a[href^="/projects/web/repositories/"]')).toBeTruthy();
    expect(repositories.root.querySelector('tbody a[href="/projects/web"]')).toBeTruthy();
    const accounts = await render('/admin/service-accounts');
    expect([...accounts.root.querySelectorAll('th[scope="col"]')].map((h) => h.textContent?.trim())).toContain('Project');
    const policies = await render('/admin/permissions');
    expect([...policies.root.querySelectorAll('th[scope="col"]')].map((h) => h.textContent?.trim() ?? '')).toContainEqual(expect.stringMatching(/^Project/));
    const audit = await render('/admin/audit');
    expect([...audit.root.querySelectorAll('th[scope="col"]')].map((h) => h.textContent?.trim() ?? '')).toContainEqual(expect.stringMatching(/^Project/));
  });
});
