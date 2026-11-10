import { Routes } from '@angular/router';
import { unsavedChangesGuard } from '../../shared/unsaved-guard';

/**
 * R-UI-020 (list), R-UI-021 (wizard). The admin area's list of every repository, each row naming its project, and the
 * wizard with a project to choose. A repository's own screen lives in its project (`PROJECT_REPOSITORIES_ROUTES`,
 * R-REPO-002, Q-UI-129).
 */
export const REPOSITORIES_ROUTES: Routes = [
  {
    path: '',
    title: $localize`:@@route.repositories.title:Repositories`,
    loadComponent: () => import('./repository-list').then((m) => m.RepositoryList),
  },
  {
    path: 'new',
    title: $localize`:@@route.repositories.new.title:New repository`,
    loadComponent: () => import('./repository-wizard').then((m) => m.RepositoryWizard),
    canDeactivate: [unsavedChangesGuard],
  },
];

/**
 * The repositories of one project, mounted at `/projects/:project/repositories` (Q-UI-129): the list, the wizard with the
 * project given, a repository's content (R-UI-052) and its settings. `new` precedes `:key`.
 */
export const PROJECT_REPOSITORIES_ROUTES: Routes = [
  {
    path: '',
    title: $localize`:@@route.repositories.title:Repositories`,
    loadComponent: () => import('./repository-list').then((m) => m.RepositoryList),
  },
  {
    path: 'new',
    title: $localize`:@@route.repositories.new.title:New repository`,
    loadComponent: () => import('./repository-wizard').then((m) => m.RepositoryWizard),
    canDeactivate: [unsavedChangesGuard],
  },
  {
    path: ':key',
    title: $localize`:@@route.repositories.detail.title:Repository`,
    children: [
      {
        path: '',
        pathMatch: 'full',
        // The page is named after the repository, as its heading is; the breadcrumb step comes from the parent route.
        title: (route) => route.paramMap.get('key') ?? '',
        loadComponent: () => import('./content/repository-content').then((m) => m.RepositoryContent),
      },
      {
        path: 'settings',
        title: $localize`:@@route.repositories.settings.title:Repository settings`,
        loadComponent: () => import('./repository-detail').then((m) => m.RepositoryDetail),
        canDeactivate: [unsavedChangesGuard],
      },
    ],
  },
];
