import { Routes } from '@angular/router';

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
];
