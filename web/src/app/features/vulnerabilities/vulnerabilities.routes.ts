import { Routes, UrlMatcher, UrlSegment } from '@angular/router';

/**
 * `/vulnerabilities/advisories/{ecosystem}/{id}` (R-VULN-022): the identifier is one segment (an advisory id has no
 * slash) and is the step the breadcrumbs show. Inputs `ecosystem` and `id` reach the screen through component input
 * binding. A matcher, not a path with two parameters, so that the trail has one step for the advisory and the
 * ecosystem is not a link to a page that does not exist.
 */
export const advisoryMatcher: UrlMatcher = (segments) => {
  const [head, ecosystem, id, ...rest] = segments;
  if (head?.path !== 'advisories' || !ecosystem || !id || rest.length > 0) return null;
  return { consumed: segments, posParams: { ecosystem: new UrlSegment(ecosystem.path, {}), id: new UrlSegment(id.path, {}) } };
};

/** R-VULN-022: the report, a finding, an advisory and the sources. */
export const VULNERABILITIES_ROUTES: Routes = [
  {
    path: '',
    pathMatch: 'full',
    title: $localize`:@@route.vulnerabilities.title:Vulnerabilities`,
    loadComponent: () => import('./vulnerability-report').then((m) => m.VulnerabilityReport),
  },
  {
    path: 'findings/:id',
    title: $localize`:@@route.vulnerabilities.finding.title:Finding`,
    loadComponent: () => import('./vulnerability-finding').then((m) => m.FindingPage),
  },
  {
    matcher: advisoryMatcher,
    title: $localize`:@@route.vulnerabilities.advisory.title:Advisory`,
    data: { crumbParam: 'id' },
    loadComponent: () => import('./vulnerability-advisory').then((m) => m.AdvisoryPage),
  },
];
