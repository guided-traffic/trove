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
 * answers, in the shared list layout with the project list's columns plus the project (Q-UI-330): the key a link to the
 * repository's page in its project, with its description, then its project, its format, its class, and its client address as a
 * field of its share; and it offers the start page's package search for the same text.
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
      [stackWide]="true"
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
          <td class="project-cell">
            <a [routerLink]="['/projects', row.project]" translate="no">{{ row.project }}</a>
          </td>
          <!-- Format and class, each a column, as in a project's repository list (Q-UI-330). -->
          <td class="format-cell">
            <span class="format-name"><trove-format-icon [format]="row.format" /><span translate="no">{{ formatLabel(row.format) }}</span></span>
          </td>
          <td class="class-cell">
            <span class="class-name"><span class="material-icons" aria-hidden="true">{{ classIcon(row.class) }}</span>{{ classText(row.class) }}</span>
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
  /**
   * The project list's columns plus the project (Q-UI-330), each width its column's least (Q-UI-308): the address field has its
   * share of 46 characters at most (Q-UI-300), the repository takes the rest.
   */
  protected readonly columns: readonly TableColumn[] = [
    { id: 'key', label: $localize`:@@repo.col.repository:Repository`, width: '12rem' },
    { id: 'project', label: $localize`:@@projects.col.project:Project`, width: '6.5rem' },
    { id: 'format', label: $localize`:@@repo.col.format:Format`, width: '8rem' },
    {
      id: 'class',
      label: $localize`:@@repo.col.class:Class`,
      help: $localize`:@@repo.help.class:Local repositories hold what you upload, remote repositories fetch and cache from another registry, virtual repositories combine other repositories behind one address.`,
      width: '5rem',
    },
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
