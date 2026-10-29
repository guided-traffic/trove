import { Component, Injectable, inject, signal } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MatCheckboxModule } from '@angular/material/checkbox';
import { MAT_DIALOG_DATA, MatDialog, MatDialogModule, MatDialogRef } from '@angular/material/dialog';
import { firstValueFrom } from 'rxjs';

export interface ConfirmData {
  readonly title: string;
  readonly text: string;
  readonly confirm: string;
  /** An optional checkbox such as "Revoke the user's tokens"; its answer is returned. */
  readonly option?: { readonly label: string; readonly checked: boolean };
}

export interface ConfirmResult {
  readonly confirmed: boolean;
  readonly option: boolean;
}

/**
 * Confirmation of a destructive or far-reaching action (R-UI-042). Focus lands in the dialog on
 * open and returns to the trigger on close (Material's default, WCAG 2.4.3).
 */
@Component({
  selector: 'trove-confirm-dialog',
  imports: [MatButtonModule, MatCheckboxModule, MatDialogModule],
  template: `
    <h2 mat-dialog-title>{{ data.title }}</h2>
    <mat-dialog-content>
      <p>{{ data.text }}</p>
      @if (data.option; as option) {
        <mat-checkbox [checked]="checked()" (change)="checked.set($event.checked)">
          {{ option.label }}
        </mat-checkbox>
      }
    </mat-dialog-content>
    <mat-dialog-actions align="end">
      <button mat-button type="button" (click)="close(false)" i18n="@@confirm.cancel">Cancel</button>
      <button mat-flat-button type="button" (click)="close(true)">{{ data.confirm }}</button>
    </mat-dialog-actions>
  `,
})
export class ConfirmDialog {
  protected readonly data = inject<ConfirmData>(MAT_DIALOG_DATA);
  private readonly ref = inject<MatDialogRef<ConfirmDialog, ConfirmResult>>(MatDialogRef);
  protected readonly checked = signal(this.data.option?.checked ?? false);

  protected close(confirmed: boolean): void {
    this.ref.close({ confirmed, option: this.checked() });
  }
}

@Injectable({ providedIn: 'root' })
export class Confirmer {
  private readonly dialog = inject(MatDialog);

  async ask(data: ConfirmData): Promise<ConfirmResult> {
    const ref = this.dialog.open<ConfirmDialog, ConfirmData, ConfirmResult>(ConfirmDialog, {
      data,
      maxWidth: '32rem',
    });
    return (await firstValueFrom(ref.afterClosed())) ?? { confirmed: false, option: false };
  }
}
