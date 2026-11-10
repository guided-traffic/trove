import { Routes } from '@angular/router';
import { CrumbSpec } from '../../core/breadcrumbs';
// >>> M4 ci
import { PROJECT_CI_ROUTES } from '../ci/ci.routes';
// <<< M4 ci

/** The step of a section whose routes are loaded lazily and have no title of their own. */
const section = (label: string): { crumbs: CrumbSpec[] } => ({ crumbs: [{ label, link: true }] });

/**
 * The screens of one project, `/projects/<project>/…` (Q-UI-129): the start page lists the caller's projects; below a
 * project every content screen of that project, with the project's sidebar. The parameter is named `project`, as the
 * inputs of the screens are, and reaches them through the componentless parent. `/projects/<project>` itself answers with
 * the repositories (Q-UI-216), so that the breadcrumb step of the project links to a route that exists.
 */
export const PROJECT_CONTEXT_ROUTES: Routes = [
  {
    path: '',
    pathMatch: 'full',
    title: $localize`:@@route.projects.title:Projects`,
    loadComponent: () => import('./projects-home').then((m) => m.ProjectsHome),
  },
  {
    path: ':project',
    data: { crumbParam: 'project' },
    children: [
      { path: '', pathMatch: 'full', redirectTo: 'repositories' },
      {
        path: 'packages',
        data: section($localize`:@@nav.packages:Packages`),
        loadChildren: () => import('../packages/packages.routes').then((m) => m.PACKAGES_ROUTES),
      },
      {
        path: 'browse',
        data: section($localize`:@@nav.browse:Browse`),
        loadChildren: () => import('../browse/browse.routes').then((m) => m.BROWSE_ROUTES),
      },
      {
        path: 'repositories',
        data: section($localize`:@@route.repositories.title:Repositories`),
        loadChildren: () => import('../repositories/repositories.routes').then((m) => m.PROJECT_REPOSITORIES_ROUTES),
      },
      // >>> M4b vulnerabilities
      {
        path: 'vulnerabilities',
        data: section($localize`:@@route.vulnerabilities.title:Vulnerabilities`),
        loadChildren: () =>
          import('../vulnerabilities/vulnerabilities.routes').then((m) => m.VULNERABILITIES_ROUTES),
      },
      // <<< M4b vulnerabilities
      {
        path: 'members',
        title: $localize`:@@route.project.members.title:Members`,
        loadComponent: () => import('./project-members-page').then((m) => m.ProjectMembersPage),
      },
      {
        path: 'policies',
        data: section($localize`:@@route.permissions.title:Permission policies`),
        loadChildren: () => import('../permissions/permissions.routes').then((m) => m.PROJECT_PERMISSIONS_ROUTES),
      },
      {
        path: 'service-accounts',
        data: section($localize`:@@route.service-accounts.title:Service accounts`),
        loadChildren: () =>
          import('../service-accounts/service-accounts.routes').then((m) => m.PROJECT_SERVICE_ACCOUNTS_ROUTES),
      },
      // >>> M4 ci
      {
        path: 'ci',
        data: section($localize`:@@route.ci.title:CI identity`),
        children: PROJECT_CI_ROUTES,
      },
      // <<< M4 ci
      {
        path: 'settings',
        title: $localize`:@@route.project.settings.title:Settings`,
        loadComponent: () => import('./project-settings').then((m) => m.ProjectSettings),
      },
    ],
  },
];
