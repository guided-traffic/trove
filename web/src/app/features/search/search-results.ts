import { Component, computed, effect, inject, input, resource, untracked } from '@angular/core';
import { RouterLink } from '@angular/router';
import { RepositoriesApi, RepositorySummary, refOf } from '../../api/repositories-api';
import { AddressField } from '../../shared/address-field';
import { FormatIconView } from '../../shared/format-icon';
import { PagedResource, valueOf } from '../../shared/paged-resource';
import { DataTable, TableColumn } from '../../shared/ui/data-table';
import { PageHeader } from '../../shared/ui/page-header';
import { classLabel } from '../repositories/repository-wizard';

/**
 * The results of the top bar's search (R-UI-002, Q-UI-390): a page of the main area, never of the admin area. The stopgap until the
 * owner's task T-22 brings one search with suggestions for every reader. It lists the repositories `GET /api/v1/repositories?q=`
 * answers, in the shared list layout: the key a link to the repository's page in its project, with its description, then its
 * project, its format and its class, and its client address; and it offers the start page's package search for the same text.
 * Every link of the page leads into a project or to the start page.
 */
@Component({
  selector: 'trove-search-results',
  imports: [RouterLink, AddressField, DataTable, FormatIconView, PageHeader],
  template: `
    <trove-page-header [heading]="heading" [description]="description()" />

    @if (term()) {
      <p class="search-packages">
        <a routerLink="/projects" [queryParams]="{ find: term() }" i18n="@@search.packages">Find packages matching “{{ term() }}”</a>
      </p>
    }

    <trove-data-table
      [caption]="caption"
      [columns]="columns"
      [loaded]="!term() || list.loaded()"
      [loading]="list.loading()"
      [error]="list.error()"
      [empty]="!term() || list.items().length === 0"
      [emptyText]="term() ? noMatch() : noTerm"
      [hasMore]="list.hasMore()"
      [style.--trove-address-chars]="addressChars()"
      (more)="list.more()"
      (retry)="list.reload()"
    >
      @for (row of list.items(); track refOf(row)) {
        <tr>
          <th scope="row" class="wrap-name">
            <a [routerLink]="['/projects', row.project, 'repositories', row.key]" translate="no">{{ row.key }}</a>
            @if (row.description) {
              <span class="secondary">{{ row.description }}</span>
            }
          </th>
          <td>
            <a [routerLink]="['/projects', row.project]" translate="no">{{ row.project }}</a>
          </td>
          <!-- Format and class in one cell, the class under the format (Q-UI-305), as in a project's repository list. -->
          <td class="kind-cell">
            <span class="format-name">
              <trove-format-icon [format]="row.format" />
              <span translate="no">{{ formatLabel(row.format) }}</span>
            </span>
            <span class="type-cell secondary">
              <span class="material-icons" aria-hidden="true">{{ classIcon(row.class) }}</span>
              {{ classText(row.class) }}
            </span>
          </td>
          <td class="address-cell">
            <trove-address-field [value]="address(row)" [label]="addressLabel(row)" [copyLabel]="copyLabel(row)" />
          </td>
        </tr>
      }
    </trove-data-table>
  `,
  styles: `
    .search-packages {
      margin-block: 0 var(--trove-space-3);
    }
    .kind-cell > .format-name {
      display: inline-flex;
      align-items: flex-start;
      gap: var(--trove-space-2);
    }
    .kind-cell > .format-name trove-format-icon {
      font-size: 1.25rem;
      color: var(--trove-text-muted);
    }
    .kind-cell > .type-cell {
      display: flex;
      align-items: center;
      gap: var(--trove-space-1);
      margin-block-start: var(--trove-space-1);
    }
    .kind-cell .material-icons {
      font-size: 1rem;
    }
  `,
})
export class SearchResults {
  private readonly api = inject(RepositoriesApi);

  /** The text of the search, the query parameter `q`. */
  readonly q = input<string | undefined>();

  protected readonly term = computed(() => (this.q() ?? '').trim());
  protected readonly list = new PagedResource<RepositorySummary>();
  private readonly formats = resource({ loader: () => this.api.formats() });

  protected readonly heading = $localize`:@@search.heading:Search results`;
  protected readonly caption = $localize`:@@search.caption:Repositories`;
  protected readonly noTerm = $localize`:@@search.no-term:Type a word into the search field to find a repository.`;
  protected readonly description = computed(() =>
    this.term() ? $localize`:@@search.description:Repositories whose key or description matches “${this.term()}:term:”.` : '',
  );
  protected readonly noMatch = computed(() => $localize`:@@search.none:No repository matches “${this.term()}:term:”.`);
  /** The list's budget (Q-UI-308): the key is the name column, the address takes what the others leave (Q-UI-300). */
  protected readonly columns: readonly TableColumn[] = [
    { id: 'key', label: $localize`:@@repo.col.repository:Repository`, width: '16rem' },
    { id: 'project', label: $localize`:@@projects.col.project:Project`, width: '8rem' },
    { id: 'format', label: $localize`:@@repo.col.format:Format`, width: '7rem', stackLabel: false },
    { id: 'address', label: $localize`:@@repo.col.address:Client address`, fill: true, stackLabel: false },
  ];
  protected readonly refOf = refOf;
  protected readonly classText = classLabel;

  constructor() {
    effect(() => {
      const q = this.term();
      untracked(() => {
        if (q) void this.list.start(this.api.list({ q }));
      });
    });
  }

  protected formatLabel(name: string): string {
    return (valueOf(this.formats) ?? []).find((f) => f.name === name)?.displayName ?? name;
  }

  protected classIcon(c: RepositorySummary['class']): string {
    return c === 'local' ? 'storage' : c === 'remote' ? 'cloud_download' : 'layers';
  }

  protected address(row: RepositorySummary): string {
    return row.addresses[0] ?? '';
  }

  /** The length of the longest address: the column is sized to it, so every field of the list has one width (Q-UI-300). */
  protected readonly addressChars = computed(() => Math.max(0, ...this.list.items().map((r) => this.address(r).length)));

  protected addressLabel(row: RepositorySummary): string {
    return $localize`:@@repo.address-of:Client address of ${row.key}:key:`;
  }

  protected copyLabel(row: RepositorySummary): string {
    return $localize`:@@repo.copy-address-of:Copy client address of ${row.key}:key:`;
  }
}
