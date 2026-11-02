import { Routes } from '@angular/router';
// >>> M4 ci
import { PROJECT_CI_ROUTES } from '../ci/ci.routes';
// <<< M4 ci

/** R-UI-041: the overview and the detail of one project. */
export const PROJECTS_ROUTES: Routes = [
  {
    path: '',
    title: $localize`:@@route.projects.title:Projects`,
    loadComponent: () => import('./project-list').then((m) => m.ProjectList),
  },
  {
    path: ':key',
    title: $localize`:@@route.projects.detail.title:Project`,
    loadComponent: () => import('./project-detail').then((m) => m.ProjectDetail),
  },
  // >>> M4 ci
  // The project's CI identity (Q-CI-26): the screens of the CI area, bounded to the project.
  ...PROJECT_CI_ROUTES,
  // <<< M4 ci
];
