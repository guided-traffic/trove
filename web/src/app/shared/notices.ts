import { Component, computed, inject } from '@angular/core';
import { Notifier } from '../core/notifier';

/**
 * The live regions of R-UI-042: confirmations in a polite `role="status"` region, errors in a
 * `role="alert"` region. Both containers exist from the start, because a region that appears
 * together with its text is not announced reliably.
 */
@Component({
  selector: 'trove-notices',
  imports: [],
  template: `
    <div class="notices">
      <div role="status">
        @for (n of statuses(); track n.id) {
          <div class="notice">
            <span>{{ n.text }}</span>
            @if (n.action; as action) {
              <button class="btn btn-tertiary" type="button" (click)="action.run(); notifier.dismiss(n.id)">
                {{ action.label }}
              </button>
            }
            <button class="btn btn-tertiary" type="button" (click)="notifier.dismiss(n.id)" i18n="@@notice.dismiss">
              Dismiss
            </button>
          </div>
        }
      </div>
      <div role="alert">
        @for (n of alerts(); track n.id) {
          <div class="notice alert">
            <span>{{ n.text }}</span>
            @if (n.action; as action) {
              <button class="btn btn-tertiary" type="button" (click)="action.run(); notifier.dismiss(n.id)">
                {{ action.label }}
              </button>
            }
            <button class="btn btn-tertiary" type="button" (click)="notifier.dismiss(n.id)" i18n="@@notice.dismiss-alert">
              Dismiss
            </button>
          </div>
        }
      </div>
    </div>
  `,
  styles: `
    .notices {
      position: fixed;
      inset-block-end: var(--trove-space-4);
      inset-inline-start: var(--trove-space-4);
      z-index: 900;
      display: flex;
      flex-direction: column;
      gap: var(--trove-space-2);
      max-inline-size: min(40rem, calc(100vw - 2 * var(--trove-space-4)));
    }
    .notice {
      display: flex;
      align-items: center;
      gap: var(--trove-space-2);
      padding: var(--trove-space-2) var(--trove-space-4);
      border-radius: var(--trove-radius-m);
      background: var(--trove-inverse-surface);
      color: var(--trove-on-inverse-surface);
    }
    .notice.alert {
      background: var(--trove-error-container);
      color: var(--trove-on-error-container);
    }
    .notice button {
      color: inherit;
    }
  `,
})
export class Notices {
  protected readonly notifier = inject(Notifier);
  protected readonly statuses = computed(() => this.notifier.notices().filter((n) => n.kind === 'status'));
  protected readonly alerts = computed(() => this.notifier.notices().filter((n) => n.kind === 'alert'));
}
