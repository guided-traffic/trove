import { Component, input, model } from '@angular/core';

let nextDisclosureId = 1;

/**
 * The one collapsible section of every screen (Q-UI-322, Q-UI-319): the details of a content page, the groups of a settings page
 * and of an editor, closed at first unless the screen opens it. A small heading at the text's size with a chevron before it, one
 * muted line of hint, and the content that folds with the animation of the style guide.
 */
@Component({
  selector: 'trove-disclosure',
  template: `
    <div class="disclosure">
      <h2 class="heading">
        <button class="toggle" type="button" [attr.aria-expanded]="expanded()" [attr.aria-controls]="bodyId" (click)="expanded.set(!expanded())">
          <span class="material-icons chevron" aria-hidden="true">chevron_right</span>
          <span>{{ heading() }}</span>
        </button>
      </h2>
      @if (hint()) {
        <p class="hint">{{ hint() }}</p>
      }
      <div class="collapse-region" [class.open]="expanded()" [id]="bodyId">
        <div class="body"><ng-content /></div>
      </div>
    </div>
  `,
  styles: `
    :host {
      display: block;
    }
    .disclosure {
      padding-block: var(--trove-space-3);
      border-block-start: 1px solid var(--trove-border);
    }
    .heading {
      margin: 0;
      font-size: var(--trove-text-size);
      font-weight: 600;
    }
    .toggle {
      display: inline-flex;
      align-items: center;
      gap: var(--trove-space-1);
      padding: 0;
      border: 0;
      background: none;
      color: var(--trove-heading);
      font: inherit;
      cursor: pointer;
    }
    .toggle:focus-visible {
      outline: var(--trove-focus-ring);
      outline-offset: 2px;
    }
    .chevron {
      font-size: 1.25rem;
      transition: transform var(--trove-motion-duration) var(--trove-motion-easing);
    }
    .toggle[aria-expanded='true'] .chevron {
      transform: rotate(90deg);
    }
    .hint {
      margin: var(--trove-space-1) 0 0;
      padding-inline-start: calc(1.25rem + var(--trove-space-1));
      color: var(--trove-text-muted);
      font-size: var(--trove-text-muted-size);
    }
    .body {
      padding-block-start: var(--trove-space-3);
      padding-inline-start: calc(1.25rem + var(--trove-space-1));
    }
  `,
})
export class Disclosure {
  readonly heading = input.required<string>();
  /** One muted line under the heading that says what is inside. */
  readonly hint = input('');
  readonly expanded = model(false);

  protected readonly bodyId = `trove-disclosure-${nextDisclosureId++}`;
}
