import { Routes } from '@angular/router';

/** One lazily loaded route per area (R-ARCH-004). Add a feature area here, never eagerly. */
export const routes: Routes = [
  {
    path: '',
    pathMatch: 'full',
    title: $localize`:@@route.home.title:Status`,
    loadComponent: () => import('./features/home/home').then((m) => m.Home),
  },
  {
    path: '**',
    title: $localize`:@@route.notfound.title:Page not found`,
    loadComponent: () => import('./features/not-found/not-found').then((m) => m.NotFound),
  },
];
