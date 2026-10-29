import { Component, inject } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MAT_DIALOG_DATA, MatDialogModule } from '@angular/material/dialog';
import { DiffLine } from './policy-model';

export interface PolicyDiffData {
  readonly name: string;
  readonly lines: readonly DiffLine[];
}

/**
 * What a save changes against the loaded revision (R-UI-027). Each line carries an icon and a
 * word as well as its sign, never colour alone (R-UI-044). Confirming sends the `PUT`.
 */
@Component({
  selector: 'trove-policy-diff-dialog',
  imports: [MatButtonModule, MatDialogModule],
  template: `
    <h2 mat-dialog-title i18n="@@policy-diff.title">Save these changes?</h2>
    <mat-dialog-content>
      @if (data.lines.length === 0) {
        <p i18n="@@policy-diff.none">Nothing differs from the stored policy.</p>
      } @else {
        <p i18n="@@policy-diff.intro">Changes to the policy {{ data.name }} since you opened it:</p>
        <ul class="diff">
          @for (line of data.lines; track $index) {
            <li [class]="line.change">
              @switch (line.change) {
                @case ('added') {
                  <span class="material-icons" aria-hidden="true">add_circle</span>
                  <span class="visually-hidden" i18n="@@policy-diff.added">Added:</span>
                }
                @case ('removed') {
                  <span class="material-icons" aria-hidden="true">remove_circle</span>
                  <span class="visually-hidden" i18n="@@policy-diff.removed">Removed:</span>
                }
                @default {
                  <span class="material-icons" aria-hidden="true">edit</span>
                  <span class="visually-hidden" i18n="@@policy-diff.changed">Changed:</span>
                }
              }
              {{ line.text }}
            </li>
          }
        </ul>
      }
    </mat-dialog-content>
    <mat-dialog-actions align="end">
      <button mat-button type="button" [mat-dialog-close]="false" i18n="@@policy-diff.cancel">Keep editing</button>
      <button mat-flat-button type="button" [mat-dialog-close]="true" i18n="@@policy-diff.confirm">Save policy</button>
    </mat-dialog-actions>
  `,
  styles: `
    .diff {
      list-style: none;
      margin: 0;
      padding: 0;
    }
    li {
      display: flex;
      align-items: center;
      gap: var(--trove-space-2);
      padding-block: var(--trove-space-1);
      overflow-wrap: anywhere;
    }
    .added {
      color: var(--trove-status-ok);
    }
    .removed {
      color: var(--trove-status-bad);
    }
    .material-icons {
      font-size: 1.125rem;
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
export class PolicyDiffDialog {
  protected readonly data = inject<PolicyDiffData>(MAT_DIALOG_DATA);
}
