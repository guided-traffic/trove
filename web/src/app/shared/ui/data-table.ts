import { Component, computed, input, output } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MatTooltipModule } from '@angular/material/tooltip';
import { ApiError } from '../../api/problem';
import { ErrorNotice } from '../error-notice';

/** One column of a `<trove-data-table>`. The label is the translated header text. */
export interface TableColumn {
  readonly id: string;
  readonly label: string;
  /** Explains the column; shown as a "?" button whose tooltip is also its accessible description. */
  readonly help?: string;
  /** Only where the API (or a complete client-side list) can really sort by this column. */
  readonly sortable?: boolean;
  /** Right-aligned figures. */
  readonly numeric?: boolean;
  /** The header text stays for assistive technology only (checkbox and action columns). */
  readonly hideLabel?: boolean;
}

export type SortDirection = 'asc' | 'desc';

/** The active sort: one column and a direction. */
export interface TableSort {
  readonly column: string;
  readonly direction: SortDirection;
}

let nextTableId = 1;

/**
 * The data table of every list screen (R-UI-020, R-UI-024 to R-UI-027, R-UI-030, R-UI-037, R-UI-006).
 * It owns the frame: a bordered, inner-scrolling region with a sticky header, sortable headers
 * (`aria-sort`, a real button per sortable column), a "?" help tooltip per column that needs one,
 * and the four states: loading, error (with retry), empty and filled, plus the "Load more" button of
 * cursor lists (R-UI-006). The rows are the screen's own `<tr>` elements, projected into the body, so
 * links, badges and actions stay in the screen's template.
 *
 * Sorting is a request, not a reordering: the table emits `sortChange` and the screen restarts its
 * list with the sort the API understands. A column is `sortable` only where that is true.
 */
@Component({
  selector: 'trove-data-table',
  imports: [MatButtonModule, MatTooltipModule, ErrorNotice],
  template: `
    @if (error(); as e) {
      <trove-error-notice [error]="e" (retry)="retry.emit()" />
    }
    @if (!loaded()) {
      @if (loading()) {
        <p class="table-status" role="status" i18n="@@table.loading">Loading.</p>
      }
    } @else if (empty()) {
      <p class="table-empty">{{ emptyText() }}</p>
    } @else {
      <div class="table-frame">
        <div class="scroll table-scroll" tabindex="0" role="region" [attr.aria-labelledby]="captionId">
          <table class="data">
            <caption [id]="captionId" class="visually-hidden">
              {{ caption() }}
            </caption>
            <thead>
              <tr>
                @for (c of columns(); track c.id) {
                  <th scope="col" [class.num]="c.numeric" [attr.aria-sort]="ariaSort(c)">
                    <span class="th-inner">
                      @if (c.sortable) {
                        <button type="button" class="th-sort" (click)="toggle(c)">
                          <span [class.visually-hidden]="c.hideLabel">{{ c.label }}</span>
                          <span class="material-icons th-arrow" aria-hidden="true" [class.sorted]="isSorted(c)">{{ arrow(c) }}</span>
                        </button>
                      } @else {
                        <span [class.visually-hidden]="c.hideLabel">{{ c.label }}</span>
                      }
                      @if (c.help) {
                        <button
                          type="button"
                          class="th-help"
                          [matTooltip]="c.help"
                          matTooltipPosition="below"
                          [attr.aria-label]="helpLabel(c)"
                        >
                          <span class="material-icons" aria-hidden="true">help_outline</span>
                        </button>
                      }
                    </span>
                  </th>
                }
              </tr>
            </thead>
            <tbody>
              <ng-content />
            </tbody>
          </table>
        </div>
      </div>
      @if (hasMore()) {
        <p class="table-more">
          <button
            mat-stroked-button
            type="button"
            [disabled]="loading()"
            (click)="more.emit()"
            i18n="@@list.load-more"
          >
            Load more
          </button>
        </p>
      }
    }
  `,
  host: { class: 'trove-data-table' },
})
export class DataTable {
  /** Accessible name of the table (rendered as a visually hidden caption). */
  readonly caption = input.required<string>();
  readonly columns = input.required<readonly TableColumn[]>();
  readonly sort = input<TableSort | null>(null);

  /** True once the first answer has arrived. */
  readonly loaded = input(true);
  /** True while a request is running; before `loaded` it shows the loading status. */
  readonly loading = input(false);
  readonly error = input<ApiError | null>(null);
  readonly empty = input(false);
  readonly emptyText = input($localize`:@@table.empty:Nothing to show.`);
  readonly hasMore = input(false);

  readonly sortChange = output<TableSort>();
  readonly more = output<void>();
  readonly retry = output<void>();

  protected readonly captionId = `trove-table-${nextTableId++}`;

  protected readonly sortedId = computed(() => this.sort()?.column ?? null);

  protected isSorted(c: TableColumn): boolean {
    return this.sortedId() === c.id;
  }

  protected ariaSort(c: TableColumn): 'ascending' | 'descending' | 'none' | null {
    if (!c.sortable) return null;
    const s = this.sort();
    if (s?.column !== c.id) return 'none';
    return s.direction === 'asc' ? 'ascending' : 'descending';
  }

  protected arrow(c: TableColumn): string {
    const s = this.sort();
    if (s?.column !== c.id) return 'unfold_more';
    return s.direction === 'asc' ? 'arrow_upward' : 'arrow_downward';
  }

  protected helpLabel(c: TableColumn): string {
    return $localize`:@@table.help-label:About the column ${c.label}:column:`;
  }

  protected toggle(c: TableColumn): void {
    const s = this.sort();
    const direction: SortDirection = s?.column === c.id && s.direction === 'asc' ? 'desc' : 'asc';
    this.sortChange.emit({ column: c.id, direction });
  }
}
