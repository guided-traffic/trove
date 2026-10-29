import { Routes } from '@angular/router';

/** R-UI-025. */
export const GROUPS_ROUTES: Routes = [
  {
    path: '',
    title: $localize`:@@route.groups.title:Groups`,
    loadComponent: () => import('./group-list').then((m) => m.GroupList),
  },
  {
    path: ':name',
    title: $localize`:@@route.groups.detail.title:Group`,
    loadComponent: () => import('./group-detail').then((m) => m.GroupDetail),
  },
];
