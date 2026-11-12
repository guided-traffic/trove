import { Component, Injectable, inject, signal } from '@angular/core';
import { DIALOG_DATA, DialogClose, DialogRef, DialogTitle, TroveDialog } from './ui/dialog';
import { firstValueFrom } from 'rxjs';

export interface ConfirmData {
  readonly title: string;
  /** Paragraphs of explanation. */
  readonly lines: readonly string[];
  /** A list under the paragraphs, for what the action affects. */
  readonly items?: readonly string[];
  readonly confirmLabel: string;
  /** The user must type this exactly before the action is enabled (R-UI-011, R-UI-020). */
  readonly typeToConfirm?: string;
  /** An optional choice, e.g. "detach"; its answer comes back as `checked`. */
  readonly choice?: string;
  /** Whether the choice starts ticked; default false. */
  readonly choiceChecked?: boolean;
}

export interface ConfirmResult {
  readonly checked: boolean;
}

/** A confirmation that names what is affected and, for large deletes, asks for the name (R-UI-011). */
@Component({
  selector: 'trove-confirm-dialog',
  imports: [DialogTitle, DialogClose],
  template: `
    <h2 troveDialogTitle>{{ data.title }}</h2>
    <div class="dialog-body">
      @for (line of data.lines; track $index) {
        <p>{{ line }}</p>
      }
      @if (data.items; as items) {
        <ul class="items">
          @for (item of items; track item) {
            <li translate="no">{{ item }}</li>
          }
        </ul>
      }
      @if (data.typeToConfirm; as expected) {
        <div class="field">
          <label class="field-label" for="confirm-name" i18n="@@confirm.type-name">
            Type the name to confirm
          </label>
          <code translate="no">{{ expected }}</code>
          <input
            id="confirm-name"
            class="text-input"
            autocomplete="off"
            spellcheck="false"
            [value]="typed()"
            (input)="typed.set($any($event.target).value)"
          />
        </div>
      }
      @if (data.choice; as choice) {
        <label class="check-row">
          <input
            type="checkbox"
            [checked]="checked()"
            (change)="checked.set($any($event.target).checked)"
          />
          <span>{{ choice }}</span>
        </label>
      }
    </div>
    <div class="dialog-actions">
      <button class="btn btn-tertiary" type="button" troveDialogClose i18n="@@confirm.cancel">Cancel</button>
      <button class="btn btn-confirm" type="button" [disabled]="!ready()" (click)="confirm()">
        {{ data.confirmLabel }}
      </button>
    </div>
  `,
  styles: `
    .field {
      display: flex;
      flex-direction: column;
      gap: var(--trove-space-1);
      margin-block: var(--trove-space-3);
    }
    .check-row {
      display: flex;
      gap: var(--trove-space-2);
      margin-block-start: var(--trove-space-3);
    }
  `,
})
export class ConfirmDialog {
  protected readonly data = inject<ConfirmData>(DIALOG_DATA);
  private readonly ref = inject<DialogRef<ConfirmResult, ConfirmDialog>>(DialogRef);

  protected readonly typed = signal('');
  protected readonly checked = signal(this.data.choiceChecked ?? false);

  protected ready(): boolean {
    return this.data.typeToConfirm === undefined || this.typed() === this.data.typeToConfirm;
  }

  protected confirm(): void {
    if (this.ready()) this.ref.close({ checked: this.checked() });
  }
}

@Injectable({ providedIn: 'root' })
export class Confirm {
  private readonly dialog = inject(TroveDialog);

  /** Resolves with the answer when confirmed, null when cancelled. */
  async ask(data: ConfirmData): Promise<ConfirmResult | null> {
    const ref = this.dialog.open<ConfirmDialog, ConfirmData, ConfirmResult>(ConfirmDialog, {
      data,
      maxWidth: '40rem',
    });
    return (await firstValueFrom(ref.closed)) ?? null;
  }
}
