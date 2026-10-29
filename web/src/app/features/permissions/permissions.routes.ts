import { Routes } from '@angular/router';
import { placeholderRoute } from '../../shared/placeholder-route';

/** R-UI-027 (list and editor). `new` precedes `:name`; the explainer (R-UI-028, P1) is not routed yet. */
export const PERMISSIONS_ROUTES: Routes = [
  placeholderRoute('', $localize`:@@route.permissions.title:Permission policies`),
  placeholderRoute('new', $localize`:@@route.permissions.new.title:New permission policy`),
  placeholderRoute(':name', $localize`:@@route.permissions.detail.title:Permission policy`),
];
