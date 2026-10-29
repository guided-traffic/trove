import { Routes } from '@angular/router';

/** System role bindings, owned by R-UI-024. */
export const ROLES_ROUTES: Routes = [
  {
    path: '',
    title: $localize`:@@route.roles.title:System roles`,
    loadComponent: () => import('./role-bindings').then((m) => m.RoleBindings),
  },
];
