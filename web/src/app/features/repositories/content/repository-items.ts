import { DatePipe, DecimalPipe } from '@angular/common';
import { Component, computed, effect, inject, input, output, signal, untracked } from '@angular/core';
import { RouterLink } from '@angular/router';
import { PackageSort, PackageSummary, PackagesApi } from '../../../api/packages-api';
import { splitRef } from '../../../api/repositories-api';
import { PagedResource } from '../../../shared/paged-resource';
import { DataTable, TableColumn, TableSort } from '../../../shared/ui/data-table';
import { packageCommands } from '../../packages/package-links';
import { ContentShape } from './content-shape';
import { RepositoryItemVersions } from './repository-item-versions';
import { VulnCounts } from './vuln-counts';

/** How long a change of the filter waits before it asks the server. */
export const FILTER_DEBOUNCE_MS = 250;

/**
 * The items of a repository of a package format (R-UI-052): its images or packages, with a filter and paging on the server
 * (R-API-029; `repository` expands a virtual repository to its members, and a remote repository answers with what it has cached),
 * the latest version, the number of versions, the open findings of the items, and a row that opens the item's versions with
 * their install command. The filter and the sort live in the URL (R-UI-003), owned by the content page.
 */
@Component({
  selector: 'trove-repository-items',
  imports: [DatePipe, DecimalPipe, RouterLink, DataTable, RepositoryItemVersions, VulnCounts],
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
      @for (p of list.items(); track p.id) {
        <tr>
          <th scope="row">
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
          @if (scanned()) {
            <td><trove-vuln-counts [counts]="p.openVulnerabilities" /></td>
          }
          <td>
            <time [attr.datetime]="p.updatedAt" [attr.title]="p.updatedAt | date: 'long'">{{ p.updatedAt | date: 'medium' }}</time>
          </td>
          <td class="num">{{ p.downloadCount | number }}</td>
        </tr>
        <tr class="expansion">
          <td [attr.colspan]="columns().length">
            <div [id]="'versions-' + p.id" class="collapse-region" [class.open]="isOpen(p.id)">
              <div>
                @if (opened().has(p.id)) {
                  <trove-repository-item-versions [pkg]="p" [repository]="repository()" [shape]="shape()" [scanned]="scanned()" />
                }
              </div>
            </div>
          </td>
        </tr>
      }
    </trove-data-table>
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
  readonly sortParam = input<PackageSort>('name');
  readonly filterChange = output<string>();
  readonly sortParamChange = output<PackageSort>();

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
  protected readonly columns = computed<readonly TableColumn[]>(() => {
    const s = this.shape();
    const columns: TableColumn[] = [
      { id: 'name', label: s.itemLabel, sortable: true },
      { id: 'latest', label: $localize`:@@repo.content.col.latest:Latest` },
      { id: 'versions', label: s.versionsLabel, numeric: true },
    ];
    if (this.scanned()) {
      columns.push({
        id: 'vulnerabilities',
        label: $localize`:@@repo.content.col.vulnerabilities:Vulnerabilities`,
        help: $localize`:@@repo.content.help.vulnerabilities:The open findings of all versions in this repository, with the highest severity.`,
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
      untracked(() => {
        this.draft.set(q);
        this.open.set(new Set());
        this.opened.set(new Set());
        void this.list.start(
          this.api.list({
            repository: [repository],
            format: [format],
            q: q || undefined,
            sort,
            include: this.scanned() ? ['vulnerabilities'] : [],
          }),
        );
      });
    });
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
    this.sortParamChange.emit(`${next.direction === 'desc' ? '-' : ''}${next.column}` as PackageSort);
  }
}
