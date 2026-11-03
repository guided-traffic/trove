import { Routes } from '@angular/router';

/** R-BAS-007. */
export const BASTIONS_ROUTES: Routes = [
  {
    path: '',
    title: $localize`:@@route.bastions.title:Bastions`,
    loadComponent: () => import('./bastion-list').then((m) => m.BastionList),
  },
];
