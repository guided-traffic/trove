import { Routes } from '@angular/router';

/** R-UI-038 (status screen and jobs), R-UI-048 (backups). */
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
  {
    path: 'backups',
    title: $localize`:@@route.system.backups.title:Backups`,
    loadComponent: () => import('./backups/backups-page').then((m) => m.BackupsPage),
  },
];
