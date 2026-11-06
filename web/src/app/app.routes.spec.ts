import { provideHttpClient } from '@angular/common/http';
import { TestBed } from '@angular/core/testing';
import { ActivatedRouteSnapshot, Router, TitleStrategy, provideRouter, withComponentInputBinding } from '@angular/router';
import { App } from './app';
import { routes } from './app.routes';
import { provideFakeApis } from './api/testing/fakes';
import { Breadcrumbs } from './core/breadcrumbs';
import { TroveTitleStrategy } from './core/title-strategy';
import { seriousViolations } from './testing/axe';

/** Every screen of the M1 route map (R-UI-003) with a concrete URL and its expected heading. */
const SCREENS: readonly (readonly [url: string, heading: string])[] = [
  ['/login', 'Sign in'],
  ['/logged-out', 'Signed out'],
  ['/projects', 'Projects'],
  ['/projects/web/packages', 'Packages'],
  ['/projects/web/browse', 'Browse'],
  ['/projects/web/browse/npm-local/@team-a/ui/-/ui-1.0.0.tgz', 'File tree'],
  ['/projects/web/repositories', 'Repositories'],
  ['/projects/web/repositories/new', 'New repository'],
  ['/projects/web/repositories/npm-local', 'Repository'],
  ['/projects/web/members', 'Members'],
  ['/projects/web/settings', 'Settings'],
  ['/projects/web/policies', 'Permission policies'],
  ['/projects/web/policies/new', 'New permission policy'],
  ['/projects/web/policies/team-a', 'Permission policy'],
  ['/projects/web/service-accounts', 'Service accounts'],
  ['/projects/web/service-accounts/ci-bot', 'Service account'],
  ['/projects/web/ci', 'CI identity'],
  // >>> M4b vulnerabilities
  ['/projects/web/vulnerabilities', 'Vulnerabilities'],
  ['/projects/web/vulnerabilities/findings/0193a000-0000-7000-8000-000000000001', 'Finding'],
  ['/projects/web/vulnerabilities/advisories/npm/GHSA-aaaa-aaaa-0001', 'Advisory'],
  // <<< M4b vulnerabilities
  ['/setup', 'Client setup'],
  ['/guides', 'Guides'],
  ['/help', 'Documentation'],
  ['/help/user/getting-started', 'Getting started'],
  ['/profile', 'Profile'],
  ['/profile/tokens', 'Personal tokens'],
  ['/profile/sessions', 'Sessions'],
  ['/admin/projects', 'Projects'],
  ['/admin/repositories', 'Repositories'],
  ['/admin/repositories/new', 'New repository'],
  ['/admin/users', 'Users'],
  ['/admin/users/alice', 'User'],
  ['/admin/groups', 'Groups'],
  ['/admin/groups/platform', 'Group'],
  ['/admin/service-accounts', 'Service accounts'],
  ['/admin/service-accounts/ci-bot', 'Service account'],
  ['/admin/permissions', 'Permission policies'],
  ['/admin/permissions/new', 'New permission policy'],
  ['/admin/permissions/team-a', 'Permission policy'],
  ['/admin/roles', 'System roles'],
  ['/admin/tokens', 'Token administration'],
  ['/admin/endpoints', 'Endpoints'],
  ['/admin/bastions', 'Bastions'],
  // >>> M4b vulnerabilities
  ['/admin/advisory-sources', 'Advisory sources'],
  // <<< M4b vulnerabilities
  ['/admin/endpoints/main', 'Endpoint'],
  ['/admin/endpoints/main/rules/new', 'New exposure rule'],
  ['/admin/endpoints/main/rules/r1', 'Exposure rule'],
  ['/admin/audit', 'Audit log'],
  ['/admin/system', 'System status'],
  ['/admin/system/jobs', 'Jobs'],
  ['/admin/system/backups', 'Backups'],
  ['/admin/settings/auth', 'Authentication settings'],
  ['/admin/settings/general', 'General settings'],
  ['/no/such/page', 'Page not found'],
];

async function open(url: string) {
  TestBed.resetTestingModule();
  await TestBed.configureTestingModule({
    imports: [App],
    providers: [
      provideRouter(routes, withComponentInputBinding()),
      { provide: TitleStrategy, useExisting: TroveTitleStrategy },
      provideHttpClient(),
      ...provideFakeApis(),
    ],
  }).compileComponents();
  const fixture = TestBed.createComponent(App);
  await TestBed.inject(Router).navigateByUrl(url);
  await fixture.whenStable();
  return fixture.nativeElement as HTMLElement;
}

describe('route map', () => {
  for (const [url, heading] of SCREENS) {
    it(`R-UI-003 ${url} shows "${heading}", sets the page title and has no serious accessibility violation (R-UI-044)`, async () => {
      const root = await open(url);
      expect(root.querySelector('main h1')?.textContent?.trim()).toBe(heading);
      expect(document.title).toBe(`${heading} – trove`);
      expect(await seriousViolations(root)).toEqual([]);
    });
  }

  /** The route of the screen: the deepest primary child of the router state. */
  const leaf = (): ActivatedRouteSnapshot => {
    let route = TestBed.inject(Router).routerState.snapshot.root;
    while (route.firstChild) route = route.firstChild;
    return route;
  };

  it('R-UI-003 Q-UI-129 the file-tree route captures the project, the repository key and the slash-containing path', async () => {
    await open('/projects/web/browse/npm-local/@team-a/ui/-/ui-1.0.0.tgz');
    expect(leaf().params['project']).toBe('web');
    expect(leaf().params['key']).toBe('npm-local');
    expect(leaf().params['path']).toBe('@team-a/ui/-/ui-1.0.0.tgz');
  });

  it('Q-UI-129 /projects/{project} answers with the packages of the project, so that its breadcrumb step links to a route that exists', async () => {
    const root = await open('/projects/web');
    expect(TestBed.inject(Router).url).toBe('/projects/web/packages');
    expect(root.querySelector('main h1')?.textContent?.trim()).toBe('Packages');
  });

  it('Q-UI-129 the old content routes are gone: /packages, /browse, /vulnerabilities and /admin/repositories/{project}/{key} are not found', async () => {
    for (const url of ['/packages', '/browse', '/vulnerabilities', '/admin/repositories/web/npm-local', '/admin/projects/web']) {
      const root = await open(url);
      expect(root.querySelector('main h1')?.textContent?.trim(), url).toBe('Page not found');
    }
  });

  it('R-UI-003 R-UI-007 /projects/{project}/packages/{format}/{name…} captures the format and the scoped name with its slash, shows the name as heading and passes the accessibility check', async () => {
    const root = await open('/projects/web/packages/npm/@team-a/ui?tab=metadata');
    expect(leaf().params['project']).toBe('web');
    expect(leaf().params['format']).toBe('npm');
    expect(leaf().params['name']).toBe('@team-a/ui');
    expect(root.querySelector('main h1')?.textContent?.trim()).toBe('@team-a/ui');
    expect(document.title).toBe('Package – trove');
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-VULN-022 /projects/{project}/vulnerabilities/advisories/{ecosystem}/{id} binds both parameters and the trail names the advisory once, with a link that exists', async () => {
    const root = await open('/projects/web/vulnerabilities/advisories/npm/GHSA-aaaa-aaaa-0001');
    expect(root.querySelector('main')?.textContent).toContain('Prototype pollution in trove-test-vuln');
    const trail = TestBed.inject(Breadcrumbs).trail();
    expect(trail.map((c) => c.label)).toEqual(['Projects', 'web', 'Vulnerabilities', 'GHSA-aaaa-aaaa-0001']);
    expect(trail[3]?.commands).toEqual(['/', 'projects', 'web', 'vulnerabilities', 'advisories', 'npm', 'GHSA-aaaa-aaaa-0001']);
  });

  it('R-VULN-022 /projects/{project}/vulnerabilities/findings/{id} binds the finding and the trail links the report', async () => {
    const root = await open('/projects/web/vulnerabilities/findings/0193a000-0000-7000-8000-000000000001');
    expect(root.querySelector('main')?.textContent).toContain('trove-test-vuln@1.0.0');
    expect(TestBed.inject(Breadcrumbs).trail().map((c) => c.label)).toEqual(['Projects', 'web', 'Vulnerabilities', '0193a000-0000-7000-8000-000000000001']);
  });

  it('R-UI-003 /admin/settings opens the authentication settings', async () => {
    await open('/admin/settings');
    expect(TestBed.inject(Router).url).toBe('/admin/settings/auth');
  });

  it('R-UI-044 the accessibility check fails on a real violation (the tooling is live)', async () => {
    const host = document.createElement('div');
    host.innerHTML = '<img src="x.png"><button></button>';
    document.body.appendChild(host);
    const found = await seriousViolations(host);
    host.remove();
    expect(found.join('\n')).toContain('image-alt');
    expect(found.join('\n')).toContain('button-name');
  });
});
