import { Routes } from '@angular/router';

/** R-UI-024. */
export const USERS_ROUTES: Routes = [
  {
    path: '',
    title: $localize`:@@route.users.title:Users`,
    loadComponent: () => import('./user-list').then((m) => m.UserList),
  },
  {
    path: ':username',
    title: $localize`:@@route.users.detail.title:User`,
    loadComponent: () => import('./user-detail').then((m) => m.UserDetail),
  },
];
