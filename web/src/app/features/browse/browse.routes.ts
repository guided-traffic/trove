import { Routes, UrlMatcher, UrlSegment } from '@angular/router';

/**
 * `/browse/{project}/{key}/{path…}` (R-UI-003): the first two segments are the project and the repository key,
 * the rest is the item path with its slashes. The input `repo` is the qualified name `<project>/<key>` of the
 * repository (R-REPO-002), the input `path` the item path; both reach the screen through component input binding.
 */
export const browseMatcher: UrlMatcher = (segments) => {
  const [project, key, ...rest] = segments;
  if (!project || !key) return null;
  return {
    consumed: segments,
    posParams: {
      repo: new UrlSegment(`${project.path}/${key.path}`, {}),
      path: new UrlSegment(rest.map((s) => s.path).join('/'), {}),
    },
  };
};

/** R-UI-010. */
const loadBrowse = (): Promise<typeof import('./browse').Browse> =>
  import('./browse').then((m) => m.Browse);

export const BROWSE_ROUTES: Routes = [
  { path: '', title: $localize`:@@route.browse.title:Browse`, loadComponent: loadBrowse },
  {
    matcher: browseMatcher,
    title: $localize`:@@route.browse.tree.title:File tree`,
    loadComponent: loadBrowse,
  },
];
