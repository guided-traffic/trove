import { Component, inject } from '@angular/core';
import { ActivatedRoute } from '@angular/router';

/**
 * Stand-in for a screen that a later work package builds. It has the final route and title, so
 * navigation, deep links and the accessibility run already cover the whole route map (R-UI-003);
 * the owning work package replaces this component in the area's `*.routes.ts`.
 */
@Component({
  selector: 'trove-screen-placeholder',
  template: `
    <h1 tabindex="-1">{{ title }}</h1>
    <p i18n="@@placeholder.text">This screen is not available yet.</p>
  `,
})
export class ScreenPlaceholder {
  protected readonly title = inject(ActivatedRoute).snapshot.title ?? '';
}
