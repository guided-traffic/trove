import { Routes } from '@angular/router';

/** R-UI-027 (list and editor) of the global policies. `new` precedes `:name`; the explainer (R-UI-028, P1) is not routed yet. */
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

/**
 * The policies of one project, mounted at `/projects/:project/policies` (Q-UI-129): a policy's name is unique within the
 * project only (R-AUTHZ-026); the editor takes the project from the path.
 */
export const PROJECT_PERMISSIONS_ROUTES: Routes = [
  {
    path: '',
    title: $localize`:@@route.permissions.title:Permission policies`,
    loadComponent: () => import('../projects/project-list-pages').then((m) => m.ProjectPoliciesPage),
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
