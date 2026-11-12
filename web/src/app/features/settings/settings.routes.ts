import { Routes } from '@angular/router';

/**
 * The group Settings of the admin area (Q-UI-326): General (R-UI-033) first, where `/admin/settings` leads, then Authentication
 * (R-UI-031). The P1 pages join the group when they are built.
 */
export const SETTINGS_ROUTES: Routes = [
  { path: '', pathMatch: 'full', redirectTo: 'general' },
  {
    path: 'general',
    title: $localize`:@@route.settings.general.title:General settings`,
    loadComponent: () => import('./general-settings').then((m) => m.GeneralSettings),
  },
  {
    path: 'auth',
    title: $localize`:@@route.settings.auth.title:Authentication settings`,
    loadComponent: () => import('./auth-settings').then((m) => m.AuthSettings),
  },
];
