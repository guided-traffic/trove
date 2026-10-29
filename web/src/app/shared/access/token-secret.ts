import { Component, ElementRef, afterNextRender, inject, input, output, signal } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { Notifier } from '../../core/notifier';

/**
 * Shows a created or rotated token value once (R-UI-029, R-UI-043). The value arrives as an
 * input from the owning component, which keeps it in a signal of its own; nothing here stores
 * it. It is a password input with a reveal toggle, `autocomplete="off"` and `spellcheck="false"`;
 * copying happens on an explicit click only. The owner clears the value when `closed` fires.
 */
@Component({
  selector: 'trove-token-secret',
  imports: [MatButtonModule],
  template: `
    <section class="secret" aria-labelledby="secret-heading" tabindex="-1">
      <h2 id="secret-heading" i18n="@@token-secret.heading">Your new token</h2>
      <p class="warning">
        <span class="material-icons" aria-hidden="true">warning</span>
        <span i18n="@@token-secret.warning">
          Copy the token now. It is shown only once and cannot be retrieved again.
        </span>
      </p>
      <p class="description">{{ description() }}</p>
      <div class="value">
        <label class="visually-hidden" for="secret-value" i18n="@@token-secret.label">Token value</label>
        <input
          id="secret-value"
          [type]="revealed() ? 'text' : 'password'"
          [value]="value()"
          readonly
          autocomplete="off"
          spellcheck="false"
          translate="no"
        />
        <button
          mat-button
          type="button"
          [attr.aria-pressed]="revealed()"
          (click)="revealed.set(!revealed())"
        >
          @if (revealed()) {
            <ng-container i18n="@@token-secret.hide">Hide</ng-container>
          } @else {
            <ng-container i18n="@@token-secret.show">Show</ng-container>
          }
        </button>
        <button mat-flat-button type="button" (click)="copy()" i18n="@@token-secret.copy">Copy</button>
      </div>
      <button mat-button type="button" (click)="closed.emit()" i18n="@@token-secret.done">
        I have copied it
      </button>
    </section>
  `,
  styles: `
    .secret {
      margin-block: var(--trove-space-4);
      padding: var(--trove-space-4);
      border: 2px solid var(--trove-status-warn);
      border-radius: var(--mat-sys-corner-medium);
    }
    h2 {
      margin-block: 0 var(--trove-space-3);
      font: var(--mat-sys-title-medium);
    }
    .warning {
      display: flex;
      align-items: flex-start;
      gap: var(--trove-space-2);
      margin-block: 0 var(--trove-space-3);
      font-weight: 600;
    }
    .description {
      margin-block: 0 var(--trove-space-3);
    }
    .value {
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      gap: var(--trove-space-2);
      margin-block-end: var(--trove-space-3);
    }
    input {
      flex: 1 1 18rem;
      min-inline-size: 0;
      padding: var(--trove-space-2);
      font-family: ui-monospace, monospace;
      color: var(--mat-sys-on-surface);
      background: var(--mat-sys-surface-container);
      border: 1px solid var(--mat-sys-outline);
      border-radius: var(--mat-sys-corner-extra-small);
    }
    .visually-hidden {
      position: absolute;
      inline-size: 1px;
      block-size: 1px;
      overflow: hidden;
      clip-path: inset(50%);
    }
  `,
})
export class TokenSecret {
  readonly value = input.required<string>();
  readonly description = input('');
  readonly closed = output<void>();

  protected readonly revealed = signal(false);
  private readonly notifier = inject(Notifier);

  constructor() {
    const host = inject<ElementRef<HTMLElement>>(ElementRef);
    afterNextRender(() => host.nativeElement.querySelector<HTMLElement>('section')?.focus());
  }

  protected async copy(): Promise<void> {
    try {
      await navigator.clipboard.writeText(this.value());
      this.notifier.status($localize`:@@token-secret.copied:Token copied to the clipboard.`);
    } catch {
      this.notifier.alert(
        $localize`:@@token-secret.copy-failed:The token could not be copied. Show it and copy it by hand.`,
      );
    }
  }
}
