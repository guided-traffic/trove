import { Routes } from '@angular/router';

/** Token administration, R-UI-030. */
export const TOKENS_ROUTES: Routes = [
  {
    path: '',
    title: $localize`:@@route.tokens.title:Token administration`,
    loadComponent: () => import('./token-admin').then((m) => m.TokenAdmin),
  },
];
