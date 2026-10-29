import { Routes } from '@angular/router';
import { unsavedChangesGuard } from '../../shared/unsaved-guard';

/** Endpoints and exposure rules, R-EP-017. `rules/new` precedes `rules/:name`. */
export const ENDPOINTS_ROUTES: Routes = [
  {
    path: '',
    title: $localize`:@@route.endpoints.title:Endpoints`,
    loadComponent: () => import('./endpoint-list').then((m) => m.EndpointList),
  },
  {
    path: ':endpoint',
    title: $localize`:@@route.endpoints.detail.title:Endpoint`,
    loadComponent: () => import('./endpoint-detail').then((m) => m.EndpointDetail),
  },
  {
    path: ':endpoint/rules/new',
    title: $localize`:@@route.endpoints.rules.new.title:New exposure rule`,
    loadComponent: () => import('./exposure-rule-editor').then((m) => m.ExposureRuleEditor),
    canDeactivate: [unsavedChangesGuard],
  },
  {
    path: ':endpoint/rules/:name',
    title: $localize`:@@route.endpoints.rules.detail.title:Exposure rule`,
    loadComponent: () => import('./exposure-rule-editor').then((m) => m.ExposureRuleEditor),
    canDeactivate: [unsavedChangesGuard],
  },
];
