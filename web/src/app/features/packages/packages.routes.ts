import { Routes, UrlMatcher, UrlSegment } from '@angular/router';

/**
 * `/packages/{format}/{name…}` (R-UI-003): the first segment is the format, the rest is the package
 * name with its slashes (`@team-a/ui`). Inputs `format` and `name` reach the screen through component
 * input binding.
 */
export const packageMatcher: UrlMatcher = (segments) => {
  const [format, ...rest] = segments;
  if (!format || rest.length === 0) return null;
  return {
    consumed: segments,
    posParams: {
      format,
      name: new UrlSegment(rest.map((s) => s.path).join('/'), {}),
    },
  };
};

/** R-UI-006 (list), R-UI-007 (details). */
export const PACKAGES_ROUTES: Routes = [
  {
    path: '',
    title: $localize`:@@route.packages.title:Packages`,
    loadComponent: () => import('./package-list').then((m) => m.PackageList),
  },
  {
    matcher: packageMatcher,
    title: $localize`:@@route.packages.detail.title:Package`,
    data: { crumbParam: 'name' },
    loadComponent: () => import('./package-detail').then((m) => m.PackageDetail),
  },
];
