import { Routes } from '@angular/router';
import { placeholderRoute } from '../../shared/placeholder-route';

/** R-UI-038 (status screen, built), jobs (R-UI-038) and backups (R-UI-048) as placeholders. */
export const SYSTEM_ROUTES: Routes = [
  {
    path: '',
    title: $localize`:@@route.system.title:System status`,
    loadComponent: () => import('./system-status').then((m) => m.SystemStatus),
  },
  placeholderRoute('jobs', $localize`:@@route.system.jobs.title:Jobs`),
  placeholderRoute('backups', $localize`:@@route.system.backups.title:Backups`),
];
