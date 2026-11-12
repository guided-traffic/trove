import { Component, DestroyRef, ElementRef, afterRenderEffect, computed, inject, input, output } from '@angular/core';
import { Tooltip } from './tooltip';
import { ApiError } from '../../api/problem';
import { ErrorNotice } from '../error-notice';
import { EmptyState } from './empty-state';

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
  /**
   * The column's least width (a CSS length), from the list's budget (Q-UI-308): the header cell carries it, and a list with a
   * `fill` column gives that column what the others leave. It must hold the header's label.
   */
  readonly width?: string;
  /** The column that takes the room the others leave, up to its own content: the client address (Q-UI-300). */
  readonly fill?: boolean;
  /**
   * The width the column takes before the `fill` column gets the rest (the repository's 20 rem, Q-UI-330). It gives way down to
   * `width` where the list is narrow, and it takes what is left over where the `fill` column's content is shorter; in a list
   * without a `fill` column it takes everything the others leave (the search results). Every other column of such a list keeps
   * its `width`.
   */
  readonly prefer?: string;
  /** When the rows stack (below 48 rem), the cell shows its column's name before its value; false where the value says it. */
  readonly stackLabel?: boolean;
  /**
   * The column gives its room to the address below 64 rem of the list's width (Q-UI-308, UI-A-30): its header and the list's
   * cells marked `narrow-drop` are hidden there, and the address column takes their width.
   */
  readonly narrow?: boolean;
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
  imports: [Tooltip, ErrorNotice, EmptyState],
  template: `
    @if (error(); as e) {
      <trove-error-notice [error]="e" (retry)="retry.emit()" />
    }
    @if (!loaded()) {
      @if (loading()) {
        <p class="table-status" role="status" i18n="@@table.loading">Loading.</p>
      }
    } @else if (empty()) {
      <trove-empty-state [text]="emptyText()"><ng-content select="[emptyAction]" /></trove-empty-state>
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
                  <th scope="col" [class.num]="c.numeric" [class.narrow-drop]="c.narrow" [attr.aria-sort]="ariaSort(c)" [style.min-inline-size]="c.width" [style.inline-size]="kept(c)">
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
                          [troveTooltip]="c.help"
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
          <button class="btn btn-default"
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
  host: {
    class: 'trove-data-table',
    '[class.stack]': 'stack()',
    '[class.stack-wide]': 'stackWide()',
    '[style.--trove-list-others]': 'others()',
    '[style.--trove-list-narrow]': 'narrowRoom()',
  },
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

  /** Below 48 rem of its width the list shows each row as a block (Q-UI-306); a list whose rows cannot be read so turns it off. */
  readonly stack = input(true);
  /**
   * A list whose least widths need more than 48 rem stacks below 64 rem of columns instead (the administrator's list of
   * everyone's tokens, Q-UI-332).
   */
  readonly stackWide = input(false);

  protected readonly captionId = `trove-table-${nextTableId++}`;

  /**
   * The room of every column but the `fill` one, with their padding: `td.address-cell` leaves it to them (Q-UI-308). A column
   * with a `prefer`red width counts with it, so the address gets the rest after it (Q-UI-330).
   */
  protected readonly others = computed(() => {
    const cols = this.columns();
    if (!cols.some((c) => c.fill)) return null;
    const rest = cols.filter((c) => !c.fill);
    const widths = rest.map((c) => c.prefer ?? c.width ?? '0px').join(' + ');
    return `calc(${widths} + ${rest.length + 1} * 2 * var(--trove-space-4))`;
  });
  /**
   * In a list with a column that prefers a width, every other column keeps its width (the header cell gets it as its size, not
   * only as its least) and that column asks for the whole table, so the room left over goes to it alone (Q-UI-330); the `fill`
   * column keeps the width its cells compute.
   */
  protected kept(c: TableColumn): string | null {
    if (c.fill || !this.columns().some((x) => x.prefer)) return null;
    return c.prefer ? '100%' : (c.width ?? null);
  }
  /** The room the columns marked `narrow` give back below 64 rem: their widths and padding. */
  protected readonly narrowRoom = computed(() => {
    const narrow = this.columns().filter((c) => c.narrow && !c.fill);
    if (narrow.length === 0) return null;
    return `calc(${narrow.map((c) => c.width ?? '0px').join(' + ')} + ${narrow.length} * 2 * var(--trove-space-4))`;
  });

  constructor() {
    // Each cell carries its column's name for the stacked rows, and an empty value ("–") is marked, so a stacked row leaves its
    // line out. The rows are the screen's, projected: they are labelled after every change of the body.
    const host = inject(ElementRef<HTMLElement>).nativeElement as HTMLElement;
    let observer: MutationObserver | undefined;
    let body: Element | null = null;
    afterRenderEffect(() => {
      const columns = this.columns();
      // The body appears with the first answer and goes with an empty list or an error: look for it again then.
      this.loaded();
      this.empty();
      this.error();
      const current = host.querySelector('tbody');
      const label = (): void => {
        for (const row of Array.from(current?.rows ?? [])) {
          Array.from(row.cells).forEach((cell, i) => {
            const c = columns[i];
            const text = cell.textContent?.trim() ?? '';
            const named = c && cell.colSpan === 1 && cell.tagName === 'TD' && !c.hideLabel && c.stackLabel !== false;
            if (named) cell.setAttribute('data-label', c.label);
            else cell.removeAttribute('data-label');
            cell.toggleAttribute('data-empty', cell.tagName === 'TD' && (text === '' || text === '–') && cell.children.length === 0);
          });
        }
      };
      label();
      if (current !== body && typeof MutationObserver !== 'undefined') {
        observer?.disconnect();
        body = current;
        if (current) {
          observer = new MutationObserver(label);
          observer.observe(current, { childList: true, subtree: true, characterData: true });
        }
      }
    });
    inject(DestroyRef).onDestroy(() => observer?.disconnect());
  }

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
