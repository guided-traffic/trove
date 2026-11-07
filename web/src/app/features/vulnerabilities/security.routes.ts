import { Routes } from '@angular/router';
import { advisoryMatcher } from './vulnerabilities.routes';
import { SECURITY_CONTEXT, VULN_CONTEXT } from './vuln-context';

/**
 * The admin area's security dashboard (Q-UI-130, R-VULN-022): the report over every project, and the finding and advisory
 * screens it links to, mounted below `/admin/security`. The screens are the content area's, so a change to
 * one is a change to both; `VULN_CONTEXT` keeps the links between them inside this area and switches on the project
 * column and the search.
 */
export const SECURITY_ROUTES: Routes = [
  {
    path: '',
    providers: [{ provide: VULN_CONTEXT, useValue: SECURITY_CONTEXT }],
    children: [
      {
        path: '',
        pathMatch: 'full',
        title: $localize`:@@route.security.title:Security dashboard`,
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
    ],
  },
];
