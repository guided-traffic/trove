import { Routes } from '@angular/router';
import { placeholderRoute } from '../../shared/placeholder-route';

/** R-UI-025. */
export const GROUPS_ROUTES: Routes = [
  placeholderRoute('', $localize`:@@route.groups.title:Groups`),
  placeholderRoute(':name', $localize`:@@route.groups.detail.title:Group`),
];
