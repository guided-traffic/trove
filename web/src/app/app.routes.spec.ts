import { provideHttpClient } from '@angular/common/http';
import { TestBed } from '@angular/core/testing';
import { Router, TitleStrategy, provideRouter, withComponentInputBinding } from '@angular/router';
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
  ['/packages', 'Packages'],
  ['/setup', 'Client setup'],
  ['/guides', 'Guides'],
  ['/help', 'Documentation'],
  ['/help/user/getting-started', 'Getting started'],
  ['/browse', 'Browse'],
  ['/browse/npm-local/@team-a/ui/-/ui-1.0.0.tgz', 'File tree'],
  ['/profile', 'Profile'],
  ['/profile/tokens', 'Personal tokens'],
  ['/profile/sessions', 'Sessions'],
  ['/admin/projects', 'Projects'],
  ['/admin/projects/web', 'Project'],
  ['/admin/repositories', 'Repositories'],
  ['/admin/repositories/new', 'New repository'],
  ['/admin/repositories/npm-local', 'Repository'],
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
  ['/vulnerabilities', 'Vulnerabilities'],
  ['/vulnerabilities/findings/0193a000-0000-7000-8000-000000000001', 'Finding'],
  ['/vulnerabilities/advisories/npm/GHSA-aaaa-aaaa-0001', 'Advisory'],
  ['/vulnerabilities/sources', 'Advisory sources'],
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

  it('R-UI-003 the file-tree route captures the repository and the slash-containing path', async () => {
    await open('/browse/npm-local/@team-a/ui/-/ui-1.0.0.tgz');
    const route = TestBed.inject(Router).routerState.snapshot.root.firstChild?.firstChild?.firstChild;
    expect(route?.params['repo']).toBe('npm-local');
    expect(route?.params['path']).toBe('@team-a/ui/-/ui-1.0.0.tgz');
  });

  it('R-UI-003 R-UI-007 /packages/{format}/{name…} captures the format and the scoped name with its slash, shows the name as heading and passes the accessibility check', async () => {
    const root = await open('/packages/npm/@team-a/ui?tab=metadata');
    const route = TestBed.inject(Router).routerState.snapshot.root.firstChild?.firstChild?.firstChild;
    expect(route?.params['format']).toBe('npm');
    expect(route?.params['name']).toBe('@team-a/ui');
    expect(root.querySelector('main h1')?.textContent?.trim()).toBe('@team-a/ui');
    expect(document.title).toBe('Package – trove');
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-VULN-022 /vulnerabilities/advisories/{ecosystem}/{id} binds both parameters and the trail names the advisory once, with a link that exists', async () => {
    const root = await open('/vulnerabilities/advisories/npm/GHSA-aaaa-aaaa-0001');
    expect(root.querySelector('main')?.textContent).toContain('Prototype pollution in trove-test-vuln');
    const trail = TestBed.inject(Breadcrumbs).trail();
    expect(trail.map((c) => c.label)).toEqual(['Vulnerabilities', 'GHSA-aaaa-aaaa-0001']);
    expect(trail[1]?.commands).toEqual(['/', 'vulnerabilities', 'advisories', 'npm', 'GHSA-aaaa-aaaa-0001']);
  });

  it('R-VULN-022 /vulnerabilities/findings/{id} binds the finding and the trail links the report', async () => {
    const root = await open('/vulnerabilities/findings/0193a000-0000-7000-8000-000000000001');
    expect(root.querySelector('main')?.textContent).toContain('trove-test-vuln@1.0.0');
    expect(TestBed.inject(Breadcrumbs).trail().map((c) => c.label)).toEqual(['Vulnerabilities', '0193a000-0000-7000-8000-000000000001']);
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
