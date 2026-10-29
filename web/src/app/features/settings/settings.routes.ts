import { Routes } from '@angular/router';
import { placeholderRoute } from '../../shared/placeholder-route';

/** Settings, R-UI-031 (authentication) and R-UI-032 (general). The P1 pages are not routed yet. */
export const SETTINGS_ROUTES: Routes = [
  { path: '', pathMatch: 'full', redirectTo: 'auth' },
  placeholderRoute('auth', $localize`:@@route.settings.auth.title:Authentication settings`),
  placeholderRoute('general', $localize`:@@route.settings.general.title:General settings`),
];
