import { Routes, UrlMatcher, UrlSegment } from '@angular/router';

/**
 * `/projects/{project}/browse/{key}/{path…}` (R-UI-003, Q-UI-129): the first segment is the repository key, the rest is the
 * item path with its slashes. The project comes from the componentless parent. The inputs `key` and `path` reach the
 * screen through component input binding.
 */
export const browseMatcher: UrlMatcher = (segments) => {
  const [key, ...rest] = segments;
  if (!key) return null;
  return {
    consumed: segments,
    posParams: {
      key: new UrlSegment(key.path, {}),
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
