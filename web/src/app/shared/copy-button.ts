import { Component, inject, input } from '@angular/core';
import { Notifier } from '../core/notifier';

/**
 * A button that copies `value` to the clipboard and confirms through the polite live region.
 * `label` names what is copied, so that a screen reader hears "Copy SHA-256" and not just "Copy".
 */
@Component({
  selector: 'trove-copy-button',
  imports: [],
  template: `
    <button class="btn btn-icon"
      type="button"
      [attr.aria-label]="label()"
      [title]="label()"
      (click)="copy()"
    >
      <span class="material-icons" aria-hidden="true">{{ icon() }}</span>
    </button>
  `,
})
export class CopyButton {
  private readonly notifier = inject(Notifier);

  readonly value = input.required<string>();
  readonly label = input($localize`:@@copy.label:Copy`);
  /** The Material icon; the default is the copy glyph, a different one tells apart two copy buttons side by side. */
  readonly icon = input('content_copy');

  protected async copy(): Promise<void> {
    try {
      await navigator.clipboard.writeText(this.value());
      this.notifier.status($localize`:@@copy.done:Copied to the clipboard.`);
    } catch {
      this.notifier.alert(
        $localize`:@@copy.failed:The clipboard is not available. Select the text and copy it.`,
      );
    }
  }
}
