import { Routes } from '@angular/router';
import { unsavedChangesGuard } from '../../shared/unsaved-guard';

/** R-UI-020 (list), R-UI-021 (wizard and edit form). `new` precedes `:key`. */
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
    path: ':key',
    title: $localize`:@@route.repositories.detail.title:Repository`,
    loadComponent: () => import('./repository-detail').then((m) => m.RepositoryDetail),
    canDeactivate: [unsavedChangesGuard],
  },
];
