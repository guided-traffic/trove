import { Component, ViewEncapsulation, input } from '@angular/core';

/**
 * The one empty state of a list or a region (the style guide's Lists): a sentence that says what is not there yet, and the
 * action that fills it, projected as content (a `btn btn-confirm`, or a link to the setup). No frame, no illustration. A filter
 * that finds nothing is not an empty state: it says so in a row of the table.
 */
@Component({
  selector: 'trove-empty-state',
  encapsulation: ViewEncapsulation.None,
  template: `
    <p class="empty-state-text">{{ text() }}</p>
    <ng-content />
  `,
  styles: `
    trove-empty-state {
      display: flex;
      flex-direction: column;
      align-items: flex-start;
      gap: var(--trove-space-3);
      margin-block: var(--trove-space-4);
      padding-block: var(--trove-space-4);
      border-block-start: 1px solid var(--trove-border);
    }

    trove-empty-state .empty-state-text {
      margin: 0;
      color: var(--trove-text-muted);
    }
  `,
})
export class EmptyState {
  /** What is not there yet, in one sentence. */
  readonly text = input.required<string>();
}
