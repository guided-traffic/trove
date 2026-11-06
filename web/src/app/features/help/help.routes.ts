import { inject } from '@angular/core';
import { ResolveFn, Routes } from '@angular/router';
import { ManualApi } from '../../api/manual-api';

const HELP = $localize`:@@route.help.title:Documentation`;

/** The page's own title, or "Documentation" while it cannot be read (the screen then shows the failure). */
const pageTitle: ResolveFn<string> = async (route) => {
  try {
    return (await inject(ManualApi).page(route.paramMap.get('section') ?? '', route.paramMap.get('page') ?? '')).title;
  } catch {
    return HELP;
  }
};

/** R-UI-050. */
export const HELP_ROUTES: Routes = [
  {
    path: '',
    title: HELP,
    loadComponent: () => import('./help-overview').then((m) => m.HelpOverview),
  },
  {
    path: ':section/:page',
    title: pageTitle,
    loadComponent: () => import('./help-page').then((m) => m.HelpPage),
  },
];
