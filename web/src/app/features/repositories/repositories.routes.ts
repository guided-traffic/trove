import { Routes } from '@angular/router';
import { unsavedChangesGuard } from '../../shared/unsaved-guard';
import { newRepositoryTitle } from './repository-create';

/**
 * The format chooser and the creation form of a format below `new` (R-UI-080): the trail reads "New repository" and then the
 * form's own title, "New OCI (Docker) repository".
 */
const NEW_REPOSITORY_ROUTES: Routes = [
  {
    path: '',
    loadComponent: () => import('./repository-format-chooser').then((m) => m.RepositoryFormatChooser),
  },
  {
    path: ':format',
    title: newRepositoryTitle,
    loadComponent: () => import('./repository-create').then((m) => m.RepositoryCreate),
    canDeactivate: [unsavedChangesGuard],
  },
];

/**
 * R-UI-020 (list), R-UI-080 (a new repository). The admin area's list of every repository, each row naming its project, the format
 * chooser and the creation form of a format, with a project to choose. A repository's own screen lives in its project
 * (`PROJECT_REPOSITORIES_ROUTES`, R-REPO-002, Q-UI-129).
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
    children: NEW_REPOSITORY_ROUTES,
  },
];

/**
 * The repositories of one project, mounted at `/projects/:project/repositories` (Q-UI-129): the list, the format chooser and the
 * creation form with the project given, a repository's content (R-UI-052) and its settings (R-UI-087). `new` precedes `:key`.
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
    children: NEW_REPOSITORY_ROUTES,
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
        title: $localize`:@@route.repositories.settings.title:Settings`,
        loadComponent: () => import('./repository-detail').then((m) => m.RepositoryDetail),
        canDeactivate: [unsavedChangesGuard],
      },
    ],
  },
];
