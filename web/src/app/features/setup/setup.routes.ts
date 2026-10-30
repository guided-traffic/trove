import { Routes } from '@angular/router';

/** R-UI-016. */
export const SETUP_ROUTES: Routes = [
  {
    path: '',
    title: $localize`:@@route.setup.title:Client setup`,
    loadComponent: () => import('./client-setup').then((m) => m.ClientSetupPage),
  },
];
