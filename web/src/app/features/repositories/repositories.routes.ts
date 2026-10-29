import { Routes } from '@angular/router';
import { placeholderRoute } from '../../shared/placeholder-route';

/** R-UI-020 (list), R-UI-021 (wizard and edit form). `new` precedes `:key`. */
export const REPOSITORIES_ROUTES: Routes = [
  placeholderRoute('', $localize`:@@route.repositories.title:Repositories`),
  placeholderRoute('new', $localize`:@@route.repositories.new.title:New repository`),
  placeholderRoute(':key', $localize`:@@route.repositories.detail.title:Repository`),
];
