import { Routes } from '@angular/router';
import { placeholderRoute } from '../../shared/placeholder-route';

/** R-UI-038 (status screen and jobs), backups (R-UI-048) as a placeholder. */
export const SYSTEM_ROUTES: Routes = [
  {
    path: '',
    title: $localize`:@@route.system.title:System status`,
    loadComponent: () => import('./system-status').then((m) => m.SystemStatus),
  },
  {
    path: 'jobs',
    title: $localize`:@@route.system.jobs.title:Jobs`,
    loadComponent: () => import('./jobs-page').then((m) => m.JobsPage),
  },
  placeholderRoute('backups', $localize`:@@route.system.backups.title:Backups`),
];
