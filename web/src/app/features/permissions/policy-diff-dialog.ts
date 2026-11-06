import { Component, inject } from '@angular/core';
import { DIALOG_DATA, DialogClose, DialogTitle } from '../../shared/ui/dialog';
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
  imports: [DialogTitle, DialogClose],
  template: `
    <h2 troveDialogTitle i18n="@@policy-diff.title">Save these changes?</h2>
    <div class="dialog-body">
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
    </div>
    <div class="dialog-actions">
      <button class="btn btn-tertiary" type="button" [troveDialogClose]="false" i18n="@@policy-diff.cancel">Keep editing</button>
      <button class="btn btn-confirm" type="button" [troveDialogClose]="true" i18n="@@policy-diff.confirm">Save policy</button>
    </div>
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
  protected readonly data = inject<PolicyDiffData>(DIALOG_DATA);
}
