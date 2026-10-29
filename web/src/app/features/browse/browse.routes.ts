import { Routes, UrlMatcher, UrlSegment } from '@angular/router';

/**
 * `/browse/{repo}/{path…}` (R-UI-003): the first segment is the repository, the rest is the item
 * path with its slashes. Inputs `repo` and `path` reach the screen through component input binding.
 */
export const browseMatcher: UrlMatcher = (segments) => {
  const [repo, ...rest] = segments;
  if (!repo) return null;
  return {
    consumed: segments,
    posParams: {
      repo,
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
