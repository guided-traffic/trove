import { Routes } from '@angular/router';

/** R-UI-027 (list and editor). `new` precedes `:name`; the explainer (R-UI-028, P1) is not routed yet. */
export const PERMISSIONS_ROUTES: Routes = [
  {
    path: '',
    title: $localize`:@@route.permissions.title:Permission policies`,
    loadComponent: () => import('./policy-list').then((m) => m.PolicyList),
  },
  {
    path: 'new',
    title: $localize`:@@route.permissions.new.title:New permission policy`,
    loadComponent: () => import('./policy-editor').then((m) => m.PolicyEditor),
  },
  {
    path: ':name',
    title: $localize`:@@route.permissions.detail.title:Permission policy`,
    loadComponent: () => import('./policy-editor').then((m) => m.PolicyEditor),
  },
];
