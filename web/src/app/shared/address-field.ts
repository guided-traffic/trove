import { Component, ViewEncapsulation, computed, input } from '@angular/core';
import { CopyButton } from './copy-button';

/**
 * One client address in a read-only, bordered input with the copy button joined to its right edge, as the reference
 * shows its clone address (R-UI-065). The address is the input's value: shown in full, never cut with an ellipsis, and
 * scrollable inside the field where the room is smaller than the address. An empty address renders nothing.
 */
@Component({
  selector: 'trove-address-field',
  imports: [CopyButton],
  encapsulation: ViewEncapsulation.None,
  template: `
    @if (value()) {
      <input
        type="text"
        class="address-field-input"
        readonly
        spellcheck="false"
        autocomplete="off"
        translate="no"
        [value]="value()"
        [attr.aria-label]="label()"
        [style.inline-size]="width()"
        (focus)="selectAll($event)"
        (click)="selectAll($event)"
      />
      <trove-copy-button [value]="value()" [label]="copyLabel()" />
    }
  `,
  styles: `
    trove-address-field {
      display: inline-flex;
      align-items: stretch;
      max-inline-size: 100%;
      vertical-align: middle;
    }

    /* The control tokens of .text-input, restated: the read-only grey of .text-input[readonly] is not wanted here. */
    trove-address-field .address-field-input {
      box-sizing: border-box;
      flex: 0 1 auto;
      min-inline-size: 8rem;
      min-block-size: var(--trove-control-height);
      padding: var(--trove-space-1) var(--trove-space-3);
      border: 1px solid var(--trove-control-border);
      border-start-start-radius: var(--trove-control-radius);
      border-end-start-radius: var(--trove-control-radius);
      border-start-end-radius: 0;
      border-end-end-radius: 0;
      background: var(--trove-control-bg);
      color: var(--trove-control-fg);
      font-family: var(--trove-font-mono);
      font-size: 0.875rem;
      text-overflow: clip;
    }

    trove-address-field .address-field-input:hover:not(:focus-visible) {
      border-color: var(--trove-control-border-hover);
    }

    trove-address-field .address-field-input:focus-visible {
      position: relative;
      z-index: 1;
      outline: var(--trove-focus-ring);
      outline-offset: 1px;
    }

    trove-address-field trove-copy-button {
      display: flex;
      flex: none;
      margin-inline-start: -1px;
    }

    trove-address-field trove-copy-button .btn {
      border: 1px solid var(--trove-control-border);
      border-start-start-radius: 0;
      border-end-start-radius: 0;
    }

    trove-address-field trove-copy-button .btn:hover {
      border-color: var(--trove-control-border-hover);
    }

    trove-address-field trove-copy-button .btn:focus-visible {
      position: relative;
      z-index: 1;
    }
  `,
})
export class AddressField {
  /** The address, one line. */
  readonly value = input.required<string>();
  /** The accessible name of the field; in a list it names the row. */
  readonly label = input($localize`:@@address.label:Client address`);
  /** The accessible name and title of the copy button. */
  readonly copyLabel = input($localize`:@@address.copy:Copy client address`);

  /** As wide as the address needs: its length in characters of the monospace face plus the padding and the border. */
  protected readonly width = computed(
    () => `calc(${this.value().length}ch + 2 * var(--trove-space-3) + 2px)`,
  );

  protected selectAll(event: Event): void {
    (event.target as HTMLInputElement).select();
  }
}
