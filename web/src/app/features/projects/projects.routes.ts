import { Routes } from '@angular/router';

/**
 * The admin area's list of every project (R-UI-041, Q-UI-129). A project's own screens, with its settings, are under
 * `/projects/:project` ([project-context.routes.ts](project-context.routes.ts)).
 */
export const PROJECTS_ROUTES: Routes = [
  {
    path: '',
    title: $localize`:@@route.projects.title:Projects`,
    loadComponent: () => import('./project-list').then((m) => m.ProjectList),
  },
];
