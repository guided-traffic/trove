import { Component, computed, input, output } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { ApiError } from '../api/problem';
import { describeProblem } from '../core/http/problem-messages';

/**
 * Renders a failed API call (R-UI-042): the translated message of the problem `code`, or the
 * server's English `detail` plus the request ID with a copy button for an unknown code
 * ("give this ID to your administrator"). A 503 and a lost connection offer "Retry".
 */
@Component({
  selector: 'trove-error-notice',
  imports: [MatButtonModule],
  template: `
    <div class="notice" role="alert">
      <span class="material-icons" aria-hidden="true">error</span>
      <div class="body">
        <p class="text">{{ message().text }}</p>
        @if (!message().known && error().requestId) {
          <p class="request">
            <span i18n="@@error.request-id">Request ID</span>:
            <code translate="no">{{ error().requestId }}</code>
            <button mat-button type="button" (click)="copyRequestId()" i18n="@@error.copy">
              Copy
            </button>
          </p>
          <p class="hint" i18n="@@error.hint">Give this ID to your administrator.</p>
        }
      </div>
      @if (retryable()) {
        <button mat-button type="button" (click)="retry.emit()" i18n="@@error.retry">Retry</button>
      }
    </div>
  `,
  styles: `
    .notice {
      display: flex;
      align-items: flex-start;
      gap: var(--trove-space-3);
      padding: var(--trove-space-3) var(--trove-space-4);
      border: 1px solid var(--mat-sys-error);
      border-radius: var(--mat-sys-corner-small);
      background: var(--mat-sys-error-container);
      color: var(--mat-sys-on-error-container);
    }
    .body {
      flex: 1 1 auto;
    }
    p {
      margin: 0;
    }
    .hint {
      margin-block-start: var(--trove-space-1);
    }
  `,
})
export class ErrorNotice {
  readonly error = input.required<ApiError>();
  readonly retry = output<void>();

  protected readonly message = computed(() => describeProblem(this.error()));
  protected readonly retryable = computed(() => {
    const s = this.error().status;
    return s === 503 || s === 0;
  });

  protected async copyRequestId(): Promise<void> {
    const id = this.error().requestId;
    if (id) await navigator.clipboard.writeText(id);
  }
}
