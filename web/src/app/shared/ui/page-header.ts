import { Component, ViewEncapsulation, input } from '@angular/core';
import { AddressField } from '../address-field';

/**
 * The header of a page (Q-UI-307, the style guide's anatomy of a page): the object's name as the `<h1>`, one muted sentence,
 * the facts that name it (projected with `meta`, the format and class badges for example), its client address field where the
 * object has one, and the page's own actions as buttons at the right end (projected with `actions`: at most one
 * `btn btn-confirm`, `btn btn-default` beside it). Views of the same object are the page's tabs, under the header; a way up to a
 * list is the breadcrumb.
 */
@Component({
  selector: 'trove-page-header',
  imports: [AddressField],
  encapsulation: ViewEncapsulation.None,
  template: `
    <header class="page-header-row">
      <div class="page-header-main">
        <h1 tabindex="-1" [attr.translate]="name() ? 'no' : null">{{ heading() }}</h1>
        @if (description()) {
          <p class="page-header-description">{{ description() }}</p>
        }
        <ng-content select="[meta]" />
        @for (a of addresses(); track a) {
          <trove-address-field [value]="a" [label]="addressLabel()" [copyLabel]="copyLabel()" />
        }
      </div>
      <div class="page-header-actions">
        <ng-content select="[actions]" />
      </div>
    </header>
  `,
  styles: `
    trove-page-header {
      display: block;
      margin-block-end: var(--trove-space-4);
    }

    trove-page-header .page-header-row {
      display: flex;
      flex-wrap: wrap;
      align-items: flex-start;
      gap: var(--trove-space-3) var(--trove-space-4);
    }

    trove-page-header .page-header-main {
      display: flex;
      flex: 1 1 24rem;
      flex-direction: column;
      gap: var(--trove-space-2);
      min-inline-size: 0;
    }

    trove-page-header h1 {
      margin: 0;
      overflow-wrap: anywhere;
    }

    trove-page-header .page-header-description {
      margin: 0;
      color: var(--trove-text-muted);
    }

    trove-page-header .page-header-actions {
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      gap: var(--trove-space-2);
    }

    trove-page-header .page-header-actions:empty {
      display: none;
    }
  `,
})
export class PageHeader {
  /** The `<h1>`: the object's name on the page of an object, the list's noun on a list. */
  readonly heading = input.required<string>();
  /** The heading is a name (a key, a package name): it is not translated. */
  readonly name = input(false);
  /** One muted sentence under the heading; nothing when empty. */
  readonly description = input<string | null | undefined>('');
  /** The object's client addresses, each one field (R-UI-065, Q-UI-301); none when empty. */
  readonly addresses = input<readonly string[]>([]);
  readonly addressLabel = input($localize`:@@address.label:Client address`);
  readonly copyLabel = input($localize`:@@address.copy:Copy client address`);
}
