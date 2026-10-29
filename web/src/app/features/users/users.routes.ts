import { Routes } from '@angular/router';
import { placeholderRoute } from '../../shared/placeholder-route';

/** R-UI-024. */
export const USERS_ROUTES: Routes = [
  placeholderRoute('', $localize`:@@route.users.title:Users`),
  placeholderRoute(':username', $localize`:@@route.users.detail.title:User`),
];
