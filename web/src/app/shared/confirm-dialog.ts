import { Component, Injectable, inject, signal } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import {
  MAT_DIALOG_DATA,
  MatDialog,
  MatDialogModule,
  MatDialogRef,
} from '@angular/material/dialog';
import { firstValueFrom } from 'rxjs';

export interface ConfirmData {
  readonly title: string;
  /** Paragraphs of explanation. */
  readonly lines: readonly string[];
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
  imports: [MatButtonModule, MatDialogModule],
  template: `
    <h2 mat-dialog-title>{{ data.title }}</h2>
    <mat-dialog-content>
      @for (line of data.lines; track $index) {
        <p>{{ line }}</p>
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
    </mat-dialog-content>
    <mat-dialog-actions align="end">
      <button mat-button type="button" mat-dialog-close i18n="@@confirm.cancel">Cancel</button>
      <button mat-flat-button type="button" [disabled]="!ready()" (click)="confirm()">
        {{ data.confirmLabel }}
      </button>
    </mat-dialog-actions>
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
  protected readonly data = inject<ConfirmData>(MAT_DIALOG_DATA);
  private readonly ref = inject<MatDialogRef<ConfirmDialog, ConfirmResult>>(MatDialogRef);

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
  private readonly dialog = inject(MatDialog);

  /** Resolves with the answer when confirmed, null when cancelled. */
  async ask(data: ConfirmData): Promise<ConfirmResult | null> {
    const ref = this.dialog.open<ConfirmDialog, ConfirmData, ConfirmResult>(ConfirmDialog, {
      data,
      maxWidth: '40rem',
    });
    return (await firstValueFrom(ref.afterClosed())) ?? null;
  }
}
