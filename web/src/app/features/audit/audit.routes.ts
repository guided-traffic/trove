import { Routes } from '@angular/router';

/** Audit log, R-UI-037. */
export const AUDIT_ROUTES: Routes = [
  {
    path: '',
    title: $localize`:@@route.audit.title:Audit log`,
    loadComponent: () => import('./audit-log').then((m) => m.AuditLog),
  },
];
