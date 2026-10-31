import { Routes } from '@angular/router';

/** R-UI-049. */
export const GUIDES_ROUTES: Routes = [
  {
    path: '',
    title: $localize`:@@route.guides.title:Guides`,
    loadComponent: () => import('./guide-page').then((m) => m.GuidePage),
  },
];
