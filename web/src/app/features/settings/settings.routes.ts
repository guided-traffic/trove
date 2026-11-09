import { Routes } from '@angular/router';

/** Settings, R-UI-031 (authentication) and R-UI-033 (general). The P1 pages are not routed yet. */
export const SETTINGS_ROUTES: Routes = [
  { path: '', pathMatch: 'full', redirectTo: 'auth' },
  {
    path: 'auth',
    title: $localize`:@@route.settings.auth.title:Authentication settings`,
    loadComponent: () => import('./auth-settings').then((m) => m.AuthSettings),
  },
  {
    path: 'general',
    title: $localize`:@@route.settings.general.title:General settings`,
    loadComponent: () => import('./general-settings').then((m) => m.GeneralSettings),
  },
];
