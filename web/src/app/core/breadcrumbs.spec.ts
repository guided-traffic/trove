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
      { label: 'Administration', link: null, literal: false },
      { label: 'Repositories', link: '//admin/repositories', literal: false },
    ]);
  });

  it('R-UI-002 a detail screen shows the parameter value, not the generic route title, and links the list', async () => {
    expect(await trailAt('/admin/repositories/my-repo')).toEqual([
      { label: 'Administration', link: null, literal: false },
      { label: 'Repositories', link: '//admin/repositories', literal: false },
      { label: 'my-repo', link: '//admin/repositories/my-repo', literal: true },
    ]);
  });

  it('R-UI-002 a literal child route such as "new" uses its title', async () => {
    const trail = await trailAt('/admin/repositories/new');
    expect(trail.map((c) => c.label)).toEqual(['Administration', 'Repositories', 'New repository']);
  });

  it('R-UI-002 a multi-segment route skips the fixed word and keeps the parameters', async () => {
    expect((await trailAt('/admin/endpoints/customers/rules/docs')).map((c) => c.label)).toEqual([
      'Administration',
      'Endpoints',
      'customers',
      'docs',
    ]);
    expect((await trailAt('/admin/endpoints/customers/rules/new')).map((c) => c.label)).toEqual([
      'Administration',
      'Endpoints',
      'customers',
      'New exposure rule',
    ]);
  });

  it('R-UI-002 the file tree adds one step per path segment, each linking to its folder', async () => {
    const trail = await trailAt('/browse/generic-local/docs/guide');
    expect(trail.map((c) => c.label)).toEqual(['Packages and files', 'Browse', 'generic-local', 'docs', 'guide']);
    expect(trail.at(-1)?.link).toBe('//browse/generic-local/docs/guide');
    expect(trail.slice(2).every((c) => c.literal)).toBe(true);
    expect(trail[0]?.link).toBeNull();
    expect(trail[1]?.link).toBe('//browse');
  });

  it('R-UI-002 a nested system screen reads area, screen group, screen', async () => {
    expect((await trailAt('/admin/system/jobs')).map((c) => c.label)).toEqual(['Administration', 'System status', 'Jobs']);
  });

  it('R-UI-002 a trail of one step is left out because it repeats the page title', async () => {
    expect(await trailAt('/profile')).toEqual([]);
  });
});
