import { Routes } from '@angular/router';
import { unsavedChangesGuard } from '../../shared/unsaved-guard';

/** R-UI-020 (list), R-UI-021 (wizard and edit form). `new` precedes `:project/:key`; a repository is addressed inside its project (R-REPO-002). */
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
  {
    path: ':project/:key',
    title: $localize`:@@route.repositories.detail.title:Repository`,
    loadComponent: () => import('./repository-detail').then((m) => m.RepositoryDetail),
    canDeactivate: [unsavedChangesGuard],
  },
];
