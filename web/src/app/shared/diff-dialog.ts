import { Component, Injectable, inject } from '@angular/core';
import { DIALOG_DATA, DialogClose, DialogTitle, TroveDialog } from './ui/dialog';
import { firstValueFrom } from 'rxjs';
import { ConflictData, diffFields } from './conflict-dialog';

export interface DiffData extends ConflictData {
  readonly title: string;
  readonly confirmLabel: string;
}

/** "What will change" before a save (R-EP-017): the stored values against the draft, field by field. */
@Component({
  selector: 'trove-diff-dialog',
  imports: [DialogTitle, DialogClose],
  template: `
    <h2 troveDialogTitle>{{ data.title }}</h2>
    <div class="dialog-body">
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
    </div>
    <div class="dialog-actions">
      <button class="btn btn-tertiary" type="button" [troveDialogClose]="false" i18n="@@diff.cancel">
        Cancel
      </button>
      <button class="btn btn-confirm"
        type="button"
        [disabled]="rows.length === 0"
        [troveDialogClose]="true"
      >
        {{ data.confirmLabel }}
      </button>
    </div>
  `,
})
export class DiffDialog {
  protected readonly data = inject<DiffData>(DIALOG_DATA);
  protected readonly rows = diffFields(this.data);
}

@Injectable({ providedIn: 'root' })
export class DiffReview {
  private readonly dialog = inject(TroveDialog);

  /** True when the user confirms the save. */
  async confirm(data: DiffData): Promise<boolean> {
    const ref = this.dialog.open<DiffDialog, DiffData, boolean>(DiffDialog, {
      data,
      maxWidth: '48rem',
    });
    return (await firstValueFrom(ref.closed)) === true;
  }
}
