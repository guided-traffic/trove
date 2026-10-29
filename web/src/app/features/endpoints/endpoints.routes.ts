import { Routes } from '@angular/router';
import { placeholderRoute } from '../../shared/placeholder-route';

/** Endpoints and exposure rules, R-EP-017. `rules/new` precedes `rules/:name`. */
export const ENDPOINTS_ROUTES: Routes = [
  placeholderRoute('', $localize`:@@route.endpoints.title:Endpoints`),
  placeholderRoute(':endpoint', $localize`:@@route.endpoints.detail.title:Endpoint`),
  placeholderRoute(':endpoint/rules/new', $localize`:@@route.endpoints.rules.new.title:New exposure rule`),
  placeholderRoute(':endpoint/rules/:name', $localize`:@@route.endpoints.rules.detail.title:Exposure rule`),
];
