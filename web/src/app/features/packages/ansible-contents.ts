import { Component, computed, input, linkedSignal, output } from '@angular/core';
import { RouterLink } from '@angular/router';
import { PackageDocEntryHead, PackageDocTree } from '../../api/packages-api';
import { ApiError } from '../../api/problem';
import { DataTable, TableColumn } from '../../shared/ui/data-table';
import { entryDoc, unavailableText } from './docs-model';

/** The rows shown at first and added by each "Load more" (R-UI-062). */
export const CONTENTS_PAGE = 100;

/**
 * The Contents tab (R-UI-062, Q-UI-247): everything the version contains as a flat data table, sorted by type and then name, with a
 * type filter. It reads the answer of the tree that the Documentation tab reads, so the two never disagree, and pages client-side in
 * hundreds. A row whose entry has documentation links to its page in the Documentation tab; one without is plain text.
 */
@Component({
  selector: 'trove-ansible-contents',
  imports: [RouterLink, DataTable],
  template: `
    @if (state() === 'pending') {
      <div class="state" data-testid="contents-pending">
        <p i18n="@@docs.pending">The documentation of this version is being fetched.</p>
        <button type="button" class="btn btn-default" (click)="refresh.emit()" i18n="@@docs.refresh">Refresh</button>
      </div>
    } @else if (state() === 'unavailable') {
      <p data-testid="contents-unavailable">{{ reason() }}</p>
    } @else {
      <div class="filter">
        <label class="picker">
          <span i18n="@@docs.contents.type">Type</span>
          <select class="select-input" data-testid="contents-type" (change)="pick($any($event.target).value)">
            <option value="" [selected]="type() === ''">{{ allLabel() }}</option>
            @for (t of types(); track t.type) {
              <option [value]="t.type" [selected]="type() === t.type" translate="no">{{ t.type }} ({{ t.count }})</option>
            }
          </select>
        </label>
      </div>
      <trove-data-table
        [caption]="caption"
        [columns]="columns"
        [loaded]="tree() !== null"
        [loading]="loading()"
        [error]="failure()"
        [empty]="rows().length === 0"
        [emptyText]="emptyText"
        [hasMore]="shown().length < rows().length"
        (more)="more()"
        (retry)="refresh.emit()"
        data-testid="contents-table"
      >
        @for (e of shown(); track e.type + '/' + e.name) {
          <tr>
            <th scope="row" translate="no">
              @if (e.hasDocs) {
                <a
                  [routerLink]="[]"
                  [queryParams]="{ tab: 'documentation', doc: doc(e), q: null }"
                  queryParamsHandling="merge"
                  >{{ e.name }}</a
                >
              } @else {
                {{ e.name }}
              }
            </th>
            <td translate="no">{{ e.type }}</td>
            <td translate="no">{{ e.shortDescription }}</td>
          </tr>
        }
      </trove-data-table>
    }
  `,
  styles: `
    .state {
      display: flex;
      flex-direction: column;
      align-items: flex-start;
      gap: var(--trove-space-3);
    }
    .state p {
      margin: 0;
    }
    .filter {
      margin-block-end: var(--trove-space-3);
    }
    .picker {
      display: inline-flex;
      flex-direction: column;
      gap: var(--trove-space-1);
    }
  `,
})
export class AnsibleContents {
  /** The whole tree of the version; null while it loads. */
  readonly tree = input.required<PackageDocTree | null>();
  readonly loading = input(false);
  readonly failure = input<ApiError | null>(null);
  readonly refresh = output<void>();

  protected readonly state = computed(() => this.tree()?.state ?? null);
  protected readonly reason = computed(() => unavailableText(this.tree()?.reason));

  protected readonly type = linkedSignal<PackageDocTree | null, string>({
    source: this.tree,
    computation: () => '',
  });
  private readonly limit = linkedSignal<string, number>({ source: this.type, computation: () => CONTENTS_PAGE });

  /** The types present with their counts, in alphabetical order. */
  protected readonly types = computed(() =>
    (this.tree()?.groups ?? [])
      .flatMap((g) => g.types)
      .map((t) => ({ type: t.type, count: t.count }))
      .sort((a, b) => a.type.localeCompare(b.type)),
  );
  protected readonly total = computed(() => this.types().reduce((n, t) => n + t.count, 0));
  protected readonly all = computed<readonly PackageDocEntryHead[]>(() =>
    (this.tree()?.groups ?? [])
      .flatMap((g) => g.types.flatMap((t) => t.entries))
      .sort((a, b) => a.type.localeCompare(b.type) || a.name.localeCompare(b.name)),
  );
  protected readonly rows = computed(() => {
    const t = this.type();
    return t === '' ? this.all() : this.all().filter((e) => e.type === t);
  });
  protected readonly shown = computed(() => this.rows().slice(0, this.limit()));

  protected readonly caption = $localize`:@@docs.contents.caption:Contents of this version`;
  protected readonly emptyText = $localize`:@@docs.contents.empty:This version contains no modules, plugins or roles.`;
  protected readonly columns: readonly TableColumn[] = [
    { id: 'name', label: $localize`:@@docs.col.name:Name` },
    { id: 'type', label: $localize`:@@docs.col.type:Type` },
    { id: 'short', label: $localize`:@@docs.col.short:Short description` },
  ];

  protected allLabel(): string {
    const n = this.total();
    return $localize`:@@docs.contents.all-types:All types (${n}:count:)`;
  }

  protected doc(e: PackageDocEntryHead): string {
    return entryDoc(e.type, e.name);
  }

  protected pick(value: string): void {
    this.type.set(value);
  }

  protected more(): void {
    this.limit.update((n) => n + CONTENTS_PAGE);
  }
}
