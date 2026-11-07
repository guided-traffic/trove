import { provideHttpClient } from '@angular/common/http';
import { TestBed } from '@angular/core/testing';
import { Router, provideRouter } from '@angular/router';
import { provideFakeApis } from '../api/testing/fakes';
import { routes } from '../app.routes';
import { Breadcrumbs } from './breadcrumbs';

/** The trail of a URL of the real route map (R-UI-003), as the shell would render it. */
async function trailAt(url: string) {
  TestBed.resetTestingModule();
  TestBed.configureTestingModule({
    providers: [provideRouter(routes), provideHttpClient(), ...provideFakeApis()],
  });
  const router = TestBed.inject(Router);
  const crumbs = TestBed.inject(Breadcrumbs);
  await router.navigateByUrl(url);
  return crumbs.trail().map((c) => ({ label: c.label, link: c.commands?.join('/') ?? null, literal: c.literal }));
}

describe('Breadcrumbs', () => {
  it('R-UI-002 a list screen reads area, then screen; the area is a name without a link', async () => {
    expect(await trailAt('/admin/repositories')).toEqual([
      { label: 'Admin area', link: null, literal: false },
      { label: 'Repositories', link: '//admin/repositories', literal: false },
    ]);
  });

  it('R-UI-002 Q-UI-129 a detail screen of a project reads projects, the project, the section, then the parameter value', async () => {
    expect(await trailAt('/projects/web/repositories/my-repo')).toEqual([
      { label: 'Projects', link: '//projects', literal: false },
      { label: 'web', link: '//projects/web', literal: true },
      { label: 'Repositories', link: '//projects/web/repositories', literal: false },
      { label: 'my-repo', link: '//projects/web/repositories/my-repo', literal: true },
    ]);
  });

  it('Q-UI-129 no step of a project screen links to a route that does not exist', async () => {
    TestBed.resetTestingModule();
    TestBed.configureTestingModule({ providers: [provideRouter(routes), provideHttpClient(), ...provideFakeApis()] });
    const router = TestBed.inject(Router);
    const crumbs = TestBed.inject(Breadcrumbs);
    // Not the screens that ask before they are left (the repository and the mapping editors): without an outlet they have no component to ask.
    for (const url of [
      '/projects/web/browse/npm-local/docs/guide',
      '/projects/web/packages/npm/@team-a/ui',
      '/projects/web/policies/team-a',
      '/projects/web/service-accounts/ci-bot',
      '/projects/web/vulnerabilities/findings/abc',
      '/projects/web/vulnerabilities/advisories/npm/GHSA-aaaa-aaaa-0001',
      '/projects/web/ci/test',
    ]) {
      await router.navigateByUrl(url);
      // The last step is the page itself, shown without a link.
      for (const crumb of crumbs.trail().slice(0, -1)) {
        if (!crumb.commands) continue;
        const target = crumb.commands.join('/').replace(/^\/\//, '/');
        expect(router.parseUrl(target)).toBeTruthy();
        expect(await router.navigateByUrl(target), `${url}: ${crumb.label} -> ${target}`).toBe(true);
        expect(router.url).not.toContain('**');
        await router.navigateByUrl(url);
      }
    }
  });

  it('R-UI-002 a literal child route such as "new" uses its title', async () => {
    const trail = await trailAt('/admin/repositories/new');
    expect(trail.map((c) => c.label)).toEqual(['Admin area', 'Repositories', 'New repository']);
  });

  it('R-UI-002 a multi-segment route skips the fixed word and keeps the parameters', async () => {
    expect((await trailAt('/admin/endpoints/customers/rules/docs')).map((c) => c.label)).toEqual([
      'Admin area',
      'Endpoints',
      'customers',
      'docs',
    ]);
    expect((await trailAt('/admin/endpoints/customers/rules/new')).map((c) => c.label)).toEqual([
      'Admin area',
      'Endpoints',
      'customers',
      'New exposure rule',
    ]);
  });

  it('R-UI-002 the file tree adds one step per path segment, each linking to its folder', async () => {
    const trail = await trailAt('/projects/web/browse/generic-local/docs/guide');
    expect(trail.map((c) => c.label)).toEqual(['Projects', 'web', 'Browse', 'generic-local', 'docs', 'guide']);
    expect(trail.at(-1)?.link).toBe('//projects/web/browse/generic-local/docs/guide');
    expect(trail.slice(3).every((c) => c.literal)).toBe(true);
    expect(trail[0]?.link).toBe('//projects');
    expect(trail[2]?.link).toBe('//projects/web/browse');
  });

  it('R-UI-002 R-UI-003 a package adds one step with its whole name, scope slash included, linking the package', async () => {
    const trail = await trailAt('/projects/web/packages/npm/@team-a/ui');
    expect(trail.map((c) => c.label)).toEqual(['Projects', 'web', 'Packages', '@team-a/ui']);
    expect(trail[2]?.link).toBe('//projects/web/packages');
    expect(trail[3]).toEqual({ label: '@team-a/ui', link: '//projects/web/packages/npm/@team-a/ui', literal: true });
  });

  it('R-UI-002 a nested system screen reads area, screen group, screen', async () => {
    expect((await trailAt('/admin/system/jobs')).map((c) => c.label)).toEqual(['Admin area', 'System status', 'Jobs']);
  });

  it('R-UI-002 a trail of one step is left out because it repeats the page title', async () => {
    expect(await trailAt('/profile')).toEqual([]);
  });
});
