import { Injectable, Component, inject } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import {
  MAT_DIALOG_DATA,
  MatDialog,
  MatDialogModule,
} from '@angular/material/dialog';
import { firstValueFrom } from 'rxjs';
import { Versioned } from '../api/repositories-api';
import { ApiError } from '../api/problem';

export type ConflictDecision = 'reload' | 'overwrite';

export interface ConflictData {
  /** The user's draft. */
  readonly mine: Readonly<Record<string, unknown>>;
  /** The resource as stored now (re-read after the 412). */
  readonly theirs: Readonly<Record<string, unknown>>;
}

export interface ConflictRow {
  readonly field: string;
  readonly mine: string;
  readonly theirs: string;
}

/** Fields whose values differ, top level, in the order of the draft then the stored object. */
export function diffFields(data: ConflictData): ConflictRow[] {
  const fields = [...new Set([...Object.keys(data.mine), ...Object.keys(data.theirs)])];
  const show = (v: unknown): string => (v === undefined ? '' : JSON.stringify(v));
  return fields
    .map((field) => ({ field, mine: show(data.mine[field]), theirs: show(data.theirs[field]) }))
    .filter((r) => r.mine !== r.theirs);
}

/** "Changed by someone else since you opened it" (R-UI-042): their version against yours. */
@Component({
  selector: 'trove-conflict-dialog',
  imports: [MatButtonModule, MatDialogModule],
  template: `
    <h2 mat-dialog-title i18n="@@conflict.title">Changed by someone else since you opened it</h2>
    <mat-dialog-content>
      @if (rows.length === 0) {
        <p i18n="@@conflict.same">The stored version already equals yours.</p>
      } @else {
        <table class="diff">
          <caption class="visually-hidden" i18n="@@conflict.caption">
            Fields that differ between the stored version and your version
          </caption>
          <thead>
            <tr>
              <th scope="col" i18n="@@conflict.field">Field</th>
              <th scope="col" i18n="@@conflict.theirs">Stored now</th>
              <th scope="col" i18n="@@conflict.mine">Yours</th>
            </tr>
          </thead>
          <tbody>
            @for (row of rows; track row.field) {
              <tr>
                <th scope="row" translate="no">{{ row.field }}</th>
                <td><code translate="no">{{ row.theirs }}</code></td>
                <td><code translate="no">{{ row.mine }}</code></td>
              </tr>
            }
          </tbody>
        </table>
      }
    </mat-dialog-content>
    <mat-dialog-actions align="end">
      <button mat-button type="button" (click)="copyMine()" i18n="@@conflict.copy">
        Copy my changes
      </button>
      <button mat-button type="button" [mat-dialog-close]="'reload'" i18n="@@conflict.reload">
        Reload (discard mine)
      </button>
      <button
        mat-flat-button
        type="button"
        [mat-dialog-close]="'overwrite'"
        i18n="@@conflict.overwrite"
      >
        Overwrite
      </button>
    </mat-dialog-actions>
  `,
  styles: `
    .diff {
      border-collapse: collapse;
      inline-size: 100%;
    }
    th,
    td {
      padding: var(--trove-space-1) var(--trove-space-3);
      text-align: start;
      border-block-end: 1px solid var(--mat-sys-outline-variant);
      overflow-wrap: anywhere;
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
export class ConflictDialog {
  private readonly data = inject<ConflictData>(MAT_DIALOG_DATA);
  protected readonly rows = diffFields(this.data);

  protected async copyMine(): Promise<void> {
    await navigator.clipboard.writeText(JSON.stringify(this.data.mine, null, 2));
  }
}

/** A merge-patch write guarded by the loaded `ETag`, with the way to re-read the resource. */
export interface GuardedPatch<T> {
  /** The `ETag` the draft was loaded with. */
  readonly etag: string;
  /** The members the user changed (the merge patch); only these are compared in the dialog. */
  readonly patch: Readonly<Record<string, unknown>>;
  /** Sends the patch with the given `ETag`. */
  send(etag: string): Promise<Versioned<T>>;
  /** Reads the stored resource again. */
  reload(): Promise<Versioned<T>>;
}

/**
 * `saved`: the write went through (first try, after "Overwrite", or because a retried first
 * attempt had committed); `reloaded`: the user discarded the draft for the stored version;
 * `dismissed`: the dialog was closed without a choice.
 */
export type GuardedOutcome<T> =
  | { readonly outcome: 'saved' | 'reloaded'; readonly current: Versioned<T> }
  | { readonly outcome: 'dismissed' };

/** Opens the conflict dialog; resolves with the choice, or null when it was dismissed. */
@Injectable({ providedIn: 'root' })
export class ConflictResolver {
  private readonly dialog = inject(MatDialog);

  async resolve(data: ConflictData): Promise<ConflictDecision | null> {
    const ref = this.dialog.open<ConflictDialog, ConflictData, ConflictDecision>(ConflictDialog, {
      data,
      maxWidth: '48rem',
    });
    return (await firstValueFrom(ref.afterClosed())) ?? null;
  }

  /**
   * Sends a merge patch with the loaded `ETag` (R-API-012). A 412 reads the stored resource and
   * opens the dialog (R-UI-042): "Overwrite" sends the same patch with the tag read now. Every
   * other failure is thrown for the caller's error notice.
   */
  async save<T>(write: GuardedPatch<T>): Promise<GuardedOutcome<T>> {
    try {
      return { outcome: 'saved', current: await write.send(write.etag) };
    } catch (e) {
      if (!(e instanceof ApiError) || e.status !== 412) throw e;
      const theirs = await write.reload();
      const stored = theirs.value as Readonly<Record<string, unknown>>;
      const mine = write.patch;
      const theirPart = Object.fromEntries(Object.keys(mine).map((k) => [k, stored[k] ?? null]));
      if (e.afterRetry && JSON.stringify(theirPart) === JSON.stringify(mine)) {
        // The first attempt of a retried write did commit: that is a success, not a conflict.
        return { outcome: 'saved', current: theirs };
      }
      const decision = await this.resolve({ mine, theirs: theirPart });
      if (decision === 'overwrite') return { outcome: 'saved', current: await write.send(theirs.etag) };
      if (decision === 'reload') return { outcome: 'reloaded', current: theirs };
      return { outcome: 'dismissed' };
    }
  }
}
