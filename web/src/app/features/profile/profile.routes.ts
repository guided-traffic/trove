import { Routes } from '@angular/router';
import { placeholderRoute } from '../../shared/placeholder-route';

/** R-UI-029. */
export const PROFILE_ROUTES: Routes = [
  placeholderRoute('', $localize`:@@route.profile.title:Profile`),
  placeholderRoute('tokens', $localize`:@@route.profile.tokens.title:Personal tokens`),
  placeholderRoute('sessions', $localize`:@@route.profile.sessions.title:Sessions`),
];
