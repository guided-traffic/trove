import { Routes, UrlMatcher, UrlSegment } from '@angular/router';
import { loadPlaceholder, placeholderRoute } from '../../shared/placeholder-route';

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
export const BROWSE_ROUTES: Routes = [
  placeholderRoute('', $localize`:@@route.browse.title:Browse`),
  {
    matcher: browseMatcher,
    title: $localize`:@@route.browse.tree.title:File tree`,
    loadComponent: loadPlaceholder,
  },
];
