import { DatePipe, DecimalPipe } from '@angular/common';
import { Component, computed, effect, inject, input, output, signal, untracked } from '@angular/core';
import { RouterLink } from '@angular/router';
import { PackageSort, PackageSummary, PackagesApi } from '../../../api/packages-api';
import { splitRef } from '../../../api/repositories-api';
import { PagedResource } from '../../../shared/paged-resource';
import { DataTable, TableColumn, TableSort } from '../../../shared/ui/data-table';
import { GradeLabel } from '../../../shared/vulnerability/grade';
import { gradeRank } from '../../../shared/vulnerability/grade-words';
import { packageCommands } from '../../packages/package-links';
import { ContentShape } from './content-shape';
import { RepositoryItemVersions } from './repository-item-versions';

/** How long a change of the filter waits before it asks the server. */
export const FILTER_DEBOUNCE_MS = 250;

/** The sorts of the list: the server's, and the grade, which the browser sorts (Q-UI-373). */
export type ItemSort = PackageSort | 'grade' | '-grade';

/** Sorting by grade loads the list in pages of this size, up to GRADE_SORT_MAX entries, and sorts them in the browser (Q-UI-373, H-980). */
const GRADE_SORT_PAGE = 500;
export const GRADE_SORT_MAX = 1000;

/**
 * The items of a repository of a package format (R-UI-052): its images or packages, with a filter and paging on the server
 * (R-API-029; `repository` expands a virtual repository to its members, and a remote repository answers with what it has cached),
 * the latest version, the number of versions, the grade of each item's latest version (R-UI-094), and a row that opens the item's
 * versions with their install command. The filter and the sort live in the URL (R-UI-003), owned by the content page. The column of
 * grades appears only when the answer carries them (Q-UI-377), and sorting by it loads the list up to 1,000 entries and sorts in the
 * browser, worst last ascending and first descending (Q-UI-373).
 */
@Component({
  selector: 'trove-repository-items',
  imports: [DatePipe, DecimalPipe, RouterLink, DataTable, GradeLabel, RepositoryItemVersions],
  template: `
    <div class="toolbar">
      <div class="filter">
        <input
          class="text-input"
          type="search"
          [attr.aria-label]="shape().filterLabel"
          [attr.placeholder]="shape().filterLabel"
          [value]="draft()"
          (input)="onFilter($any($event.target).value)"
        />
        <span class="material-icons" aria-hidden="true">search</span>
      </div>
    </div>

    <trove-data-table
      [caption]="caption()"
      [columns]="columns()"
      [sort]="sort()"
      (sortChange)="changeSort($event)"
      [loaded]="list.loaded()"
      [loading]="list.loading()"
      [error]="list.error()"
      [empty]="list.items().length === 0"
      [emptyText]="q() ? shape().emptyFilteredText : shape().emptyText"
      [hasMore]="list.hasMore()"
      (more)="list.more()"
      (retry)="list.more()"
    >
      @if (offerSetup() && !q()) {
        <!-- The action that fills an empty repository (Q-UI-309): the setup shows how to publish or upload with a client. -->
        <a emptyAction class="btn btn-default" routerLink="/setup" [queryParams]="{ repository: repository() }" i18n="@@repo.content.tab.setup"
          >Set up client</a
        >
      }
      @for (p of rows(); track p.id) {
        <tr>
          <th scope="row" class="wrap-name">
            <div class="name-cell">
              <button
                type="button"
                class="btn btn-tertiary btn-icon"
                [attr.aria-expanded]="isOpen(p.id)"
                [attr.aria-controls]="'versions-' + p.id"
                [attr.aria-label]="openLabel(p)"
                (click)="toggle(p.id)"
              >
                <span class="material-icons expand-chevron" aria-hidden="true">expand_more</span>
              </button>
              <span>
                <a class="item-name" [routerLink]="detailLink(p)" translate="no">{{ p.name }}</a>
                @if (p.description; as d) {
                  <span class="item-description">{{ d }}</span>
                }
              </span>
            </div>
          </th>
          <td translate="no">{{ p.latestVersion }}</td>
          <td class="num">{{ p.versionCount | number }}</td>
          @if (graded()) {
            <td>
              @if (p.vulnerabilityGrade; as g) {
                <trove-grade [grade]="g" />
              }
            </td>
          }
          <td>
            <time [attr.datetime]="p.updatedAt" [attr.title]="p.updatedAt | date: 'long'">{{ p.updatedAt | date: 'mediumDate' }}</time>
          </td>
          <td class="num">{{ p.downloadCount | number }}</td>
        </tr>
        <tr class="expansion">
          <td [attr.colspan]="columns().length">
            <div [id]="'versions-' + p.id" class="collapse-region" [class.open]="isOpen(p.id)">
              <div>
                @if (opened().has(p.id)) {
                  <trove-repository-item-versions [pkg]="p" [repository]="repository()" [shape]="shape()" [scanned]="graded()" />
                }
              </div>
            </div>
          </td>
        </tr>
      }
    </trove-data-table>
    @if (gradeSorted() && list.hasMore()) {
      <p class="content-note" i18n="@@repo.content.grade-sort-partial">
        Sorted by grade among the first {{ list.items().length }} entries; the list holds more.
      </p>
    }
  `,
  styleUrl: './repository-content.scss',
})
export class RepositoryItems {
  private readonly api = inject(PackagesApi);

  /** The qualified name of the repository. */
  readonly repository = input.required<string>();
  readonly format = input.required<string>();
  readonly shape = input.required<ContentShape>();
  /** The format is matched against advisories: the column of findings is shown. */
  readonly scanned = input(false);
  /** The filter and the sort, from the URL of the page. */
  readonly q = input<string>('');
  readonly sortParam = input<ItemSort>('name');
  /** Whether an empty repository offers Set up client: not for a disabled one, which no client can use. */
  readonly offerSetup = input(true);
  readonly filterChange = output<string>();
  readonly sortParamChange = output<ItemSort>();

  protected readonly list = new PagedResource<PackageSummary>();
  /** The filter as typed; the URL follows after a short pause. */
  protected readonly draft = signal('');
  /** The items whose versions are open now. */
  private readonly open = signal<ReadonlySet<string>>(new Set());
  /** The items whose versions were opened at least once: the first request is lazy, and a closed item keeps its list. */
  protected readonly opened = signal<ReadonlySet<string>>(new Set());
  private debounce: ReturnType<typeof setTimeout> | undefined;

  protected readonly sort = computed<TableSort>(() => {
    const raw = this.sortParam();
    return { column: raw.replace('-', ''), direction: raw.startsWith('-') ? 'desc' : 'asc' };
  });
  protected readonly caption = computed(() => this.shape().itemsLabel);
  /** The answer carries grades: a signed-in caller of an installation that scans, for a format it scans (Q-VULN-158, Q-UI-377). */
  protected readonly graded = computed(() => this.scanned() && this.list.items().some((p) => p.vulnerabilityGrade !== undefined));
  protected readonly gradeSorted = computed(() => this.sortParam().replace('-', '') === 'grade');
  /** The rows in the order asked: the server's, or by grade (R-VULN-038), then by name. */
  protected readonly rows = computed<readonly PackageSummary[]>(() => {
    const items = this.list.items();
    if (!this.gradeSorted()) return items;
    const sign = this.sortParam().startsWith('-') ? -1 : 1;
    return [...items].sort(
      (a, b) => sign * (gradeRank(a.vulnerabilityGrade) - gradeRank(b.vulnerabilityGrade)) || a.name.localeCompare(b.name),
    );
  });
  protected readonly columns = computed<readonly TableColumn[]>(() => {
    const s = this.shape();
    const columns: TableColumn[] = [
      // The name takes 16 rem and wraps after a hyphen beyond (Q-UI-303, Q-UI-308); the date is the day, its time the title.
      { id: 'name', label: s.itemLabel, sortable: true, width: '16rem' },
      { id: 'latest', label: $localize`:@@repo.content.col.latest:Latest` },
      { id: 'versions', label: s.versionsLabel, numeric: true },
    ];
    if (this.graded()) {
      columns.push({
        id: 'grade',
        label: $localize`:@@repo.content.col.vulnerabilities:Vulnerabilities`,
        help: $localize`:@@repo.content.help.grade:The grade of the latest version in this repository, from S (no known vulnerabilities) to E (more than 5 Critical); N/A when it is not scanned.`,
        sortable: true,
      });
    }
    columns.push(
      { id: 'updatedAt', label: $localize`:@@repo.content.col.updated:Updated`, sortable: true },
      { id: 'downloads', label: $localize`:@@repo.content.col.downloads:Downloads`, numeric: true },
    );
    return columns;
  });

  constructor() {
    effect(() => {
      const repository = this.repository();
      const q = this.q();
      const sort = this.sortParam();
      const format = this.format();
      // The format's catalog entry can arrive after the first request: a list asked before it knew that the format is scanned
      // is asked again with the grades (R-UI-094).
      const scanned = this.scanned();
      untracked(() => {
        this.draft.set(q);
        this.open.set(new Set());
        this.opened.set(new Set());
        const byGrade = sort.replace('-', '') === 'grade';
        const fetcher = this.api.list({
          repository: [repository],
          format: [format],
          q: q || undefined,
          sort: byGrade ? 'name' : (sort as PackageSort),
          include: scanned ? ['vulnerabilities'] : [],
        });
        void (byGrade ? this.loadForGrade(fetcher) : this.list.start(fetcher));
      });
    });
  }

  /** The whole list up to GRADE_SORT_MAX entries, so that the browser sorts it by grade (Q-UI-373). */
  private async loadForGrade(fetcher: ReturnType<PackagesApi['list']>): Promise<void> {
    await this.list.start(fetcher, GRADE_SORT_PAGE);
    while (this.list.hasMore() && !this.list.error() && this.list.items().length < GRADE_SORT_MAX) await this.list.more();
  }

  protected isOpen(id: string): boolean {
    return this.open().has(id);
  }

  protected toggle(id: string): void {
    const next = new Set(this.open());
    if (next.has(id)) {
      next.delete(id);
    } else {
      next.add(id);
      this.opened.update((o) => new Set([...o, id]));
    }
    this.open.set(next);
  }

  protected openLabel(p: PackageSummary): string {
    return $localize`:@@repo.content.open-versions:Show the versions of ${p.name}:name:`;
  }

  protected detailLink(p: PackageSummary): string[] {
    return packageCommands(splitRef(this.repository()).project, p.format, p.name);
  }

  protected onFilter(value: string): void {
    this.draft.set(value);
    clearTimeout(this.debounce);
    this.debounce = setTimeout(() => this.filterChange.emit(value.trim()), FILTER_DEBOUNCE_MS);
  }

  protected changeSort(next: TableSort): void {
    this.sortParamChange.emit(`${next.direction === 'desc' ? '-' : ''}${next.column}` as ItemSort);
  }
}
