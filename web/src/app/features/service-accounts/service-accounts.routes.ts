import { Routes } from '@angular/router';

/** R-UI-026: the instance-wide service accounts. */
export const SERVICE_ACCOUNTS_ROUTES: Routes = [
  {
    path: '',
    title: $localize`:@@route.service-accounts.title:Service accounts`,
    loadComponent: () => import('./service-account-list').then((m) => m.ServiceAccountList),
  },
  {
    path: ':name',
    title: $localize`:@@route.service-accounts.detail.title:Service account`,
    loadComponent: () => import('./service-account-detail').then((m) => m.ServiceAccountDetail),
  },
];

/**
 * The service accounts of one project, mounted at `/projects/:project/service-accounts` (Q-UI-129): a name is unique
 * within the project only (R-AUTHZ-026); the screen takes the project from the path.
 */
export const PROJECT_SERVICE_ACCOUNTS_ROUTES: Routes = [
  {
    path: '',
    title: $localize`:@@route.service-accounts.title:Service accounts`,
    loadComponent: () => import('../projects/project-list-pages').then((m) => m.ProjectServiceAccountsPage),
  },
  {
    path: ':name',
    title: $localize`:@@route.service-accounts.detail.title:Service account`,
    loadComponent: () => import('./service-account-detail').then((m) => m.ServiceAccountDetail),
  },
];
