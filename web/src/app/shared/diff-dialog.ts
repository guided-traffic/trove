import { Component, Injectable, inject } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MAT_DIALOG_DATA, MatDialog, MatDialogModule } from '@angular/material/dialog';
import { firstValueFrom } from 'rxjs';
import { ConflictData, diffFields } from './conflict-dialog';

export interface DiffData extends ConflictData {
  readonly title: string;
  readonly confirmLabel: string;
}

/** "What will change" before a save (R-EP-017): the stored values against the draft, field by field. */
@Component({
  selector: 'trove-diff-dialog',
  imports: [MatButtonModule, MatDialogModule],
  template: `
    <h2 mat-dialog-title>{{ data.title }}</h2>
    <mat-dialog-content>
      @if (rows.length === 0) {
        <p i18n="@@diff.none">Nothing differs from the stored version.</p>
      } @else {
        <table class="data">
          <caption class="visually-hidden" i18n="@@diff.caption">
            Fields that change
          </caption>
          <thead>
            <tr>
              <th scope="col" i18n="@@conflict.field">Field</th>
              <th scope="col" i18n="@@diff.stored">Stored now</th>
              <th scope="col" i18n="@@diff.new">After saving</th>
            </tr>
          </thead>
          <tbody>
            @for (row of rows; track row.field) {
              <tr>
                <th scope="row" translate="no">{{ row.field }}</th>
                <td>
                  <code translate="no">{{ row.theirs || '–' }}</code>
                </td>
                <td>
                  <code translate="no">{{ row.mine || '–' }}</code>
                </td>
              </tr>
            }
          </tbody>
        </table>
      }
    </mat-dialog-content>
    <mat-dialog-actions align="end">
      <button mat-button type="button" [mat-dialog-close]="false" i18n="@@diff.cancel">
        Cancel
      </button>
      <button
        mat-flat-button
        type="button"
        [disabled]="rows.length === 0"
        [mat-dialog-close]="true"
      >
        {{ data.confirmLabel }}
      </button>
    </mat-dialog-actions>
  `,
})
export class DiffDialog {
  protected readonly data = inject<DiffData>(MAT_DIALOG_DATA);
  protected readonly rows = diffFields(this.data);
}

@Injectable({ providedIn: 'root' })
export class DiffReview {
  private readonly dialog = inject(MatDialog);

  /** True when the user confirms the save. */
  async confirm(data: DiffData): Promise<boolean> {
    const ref = this.dialog.open<DiffDialog, DiffData, boolean>(DiffDialog, {
      data,
      maxWidth: '48rem',
    });
    return (await firstValueFrom(ref.afterClosed())) === true;
  }
}
