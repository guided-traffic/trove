import { Routes } from '@angular/router';
import { placeholderRoute } from '../../shared/placeholder-route';

/** R-UI-026. */
export const SERVICE_ACCOUNTS_ROUTES: Routes = [
  placeholderRoute('', $localize`:@@route.service-accounts.title:Service accounts`),
  placeholderRoute(':name', $localize`:@@route.service-accounts.detail.title:Service account`),
];
