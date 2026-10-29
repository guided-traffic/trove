import { Routes } from '@angular/router';

/** R-UI-026. */
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
