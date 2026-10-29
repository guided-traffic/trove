import { Route } from '@angular/router';

/** Lazy loader of the placeholder screen. */
export const loadPlaceholder = (): Promise<typeof import('./screen-placeholder').ScreenPlaceholder> =>
  import('./screen-placeholder').then((m) => m.ScreenPlaceholder);

/** A route that shows `ScreenPlaceholder` under its final path and title. */
export function placeholderRoute(path: string, title: string): Route {
  return { path, title, loadComponent: loadPlaceholder };
}
