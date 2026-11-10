import { Component, input, model } from '@angular/core';

let nextSectionId = 1;

/**
 * A collapsible group of a settings page (docs/developer/ui-style-guide.md#settings-pages, Q-UI-215): a chevron, the title, one
 * sentence under it, a hairline to the next section, and the content that folds with the animation of the style guide.
 */
@Component({
  selector: 'trove-settings-section',
  template: `
    <section class="section">
      <h2 class="heading">
        <button class="toggle" type="button" [attr.aria-expanded]="expanded()" [attr.aria-controls]="bodyId" (click)="expanded.set(!expanded())">
          <span class="material-icons chevron" aria-hidden="true">chevron_right</span>
          <span>{{ heading() }}</span>
        </button>
      </h2>
      <p class="muted description">{{ description() }}</p>
      <div class="collapse-region" [class.open]="expanded()" [id]="bodyId">
        <div class="body"><ng-content /></div>
      </div>
    </section>
  `,
  styles: `
    .section {
      padding-block: var(--trove-space-5);
      border-block-start: 1px solid var(--trove-border);
    }
    .heading {
      margin: 0;
      font-size: var(--trove-heading-2);
    }
    .toggle {
      display: flex;
      align-items: center;
      gap: var(--trove-space-3);
      padding: 0;
      border: 0;
      background: none;
      color: var(--trove-heading);
      font: inherit;
      letter-spacing: inherit;
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
    [aria-expanded='true'] > .chevron {
      transform: rotate(90deg);
    }
    .description {
      margin: var(--trove-space-1) 0 0;
      padding-inline-start: calc(1.25rem + var(--trove-space-3));
    }
    .body {
      padding-block-start: var(--trove-space-4);
      padding-inline-start: calc(1.25rem + var(--trove-space-3));
    }
  `,
})
export class SettingsSection {
  readonly heading = input.required<string>();
  readonly description = input.required<string>();
  readonly expanded = model(false);

  protected readonly bodyId = `trove-settings-section-${nextSectionId++}`;
}
