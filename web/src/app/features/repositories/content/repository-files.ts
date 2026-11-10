import { DatePipe } from '@angular/common';
import { Component, computed, effect, inject, input, output, resource, signal, untracked } from '@angular/core';
import { RouterLink } from '@angular/router';
import { Child, FilesApi } from '../../../api/files-api';
import { FormatDescriptor } from '../../../api/repositories-api';
import { browseCommands } from '../../../core/project-links';
import { ServerAddress } from '../../../core/server-address';
import { CopyButton } from '../../../shared/copy-button';
import { PagedResource } from '../../../shared/paged-resource';
import { DataTable, TableColumn } from '../../../shared/ui/data-table';
import { formatSize } from '../../packages/format-size';
import { clientAddress } from '../repository-model';
import { contentShapeOf } from './content-shape';

/** How many children a folder lists at a time. */
const FILES_PAGE = 100;

/**
 * The files of a repository of the generic format (R-UI-052): for this format files are the logical level. A folder lists its
 * children (R-API-025), a file links to its detail in the file browser and offers a copy of its download address. The path is the
 * `path` query parameter (R-UI-003), owned by the content page; a filter narrows what is loaded, since the server has no name filter
 * on children.
 */
@Component({
  selector: 'trove-repository-files',
  imports: [DatePipe, RouterLink, CopyButton, DataTable],
  template: `
    @if (crumbs().length > 0) {
      <nav class="path-trail" i18n-aria-label="@@repo.content.folder-path" aria-label="Folder path">
        <button type="button" class="btn btn-tertiary" (click)="open('')" i18n="@@repo.content.root">All files</button>
        @for (c of crumbs(); track c.path) {
          <span aria-hidden="true">/</span>
          <button type="button" class="btn btn-tertiary" translate="no" (click)="open(c.path)">{{ c.name }}</button>
        }
      </nav>
    }
    <div class="toolbar">
      <div class="filter">
        <input
          class="text-input"
          type="search"
          i18n-aria-label="@@repo.content.filter-files"
          aria-label="Filter files"
          i18n-placeholder="@@repo.content.filter-files"
          placeholder="Filter files"
          [value]="filter()"
          (input)="filter.set($any($event.target).value)"
        />
        <span class="material-icons" aria-hidden="true">search</span>
      </div>
    </div>
    <trove-data-table
      [caption]="caption"
      [columns]="columns"
      [loaded]="list.loaded()"
      [loading]="list.loading()"
      [error]="list.error()"
      [empty]="shown().length === 0"
      [emptyText]="filter() ? emptyFiltered : emptyText"
      [hasMore]="list.hasMore()"
      (more)="list.more()"
      (retry)="list.more()"
    >
      @for (c of shown(); track c.path) {
        <tr>
          <th scope="row">
            @if (c.kind === 'folder') {
              <button type="button" class="file-link" translate="no" (click)="open(fullPath(c))">
                <span class="material-icons" aria-hidden="true">folder</span>{{ c.path }}
              </button>
            } @else {
              <a class="file-link" [routerLink]="browseLink(c)" translate="no">
                <span class="material-icons" aria-hidden="true">description</span>{{ c.path }}
              </a>
            }
          </th>
          <td class="num">
            @if (c.kind === 'file') {
              {{ size(c.size ?? 0) }}
            } @else {
              –
            }
          </td>
          <td>
            @if (c.modifiedAt; as at) {
              <time [attr.datetime]="at">{{ at | date: 'medium' }}</time>
            } @else {
              –
            }
          </td>
          <td class="actions">
            @if (c.kind === 'file' && fileUrl(c); as url) {
              <trove-copy-button [value]="url" [label]="copyUrlLabel" />
            }
          </td>
        </tr>
      }
    </trove-data-table>
  `,
  styleUrl: './repository-content.scss',
})
export class RepositoryFiles {
  private readonly files = inject(FilesApi);
  private readonly address = inject(ServerAddress);

  /** The qualified name of the repository. */
  readonly repository = input.required<string>();
  readonly format = input.required<FormatDescriptor | undefined>();
  /** The folder shown; '' is the root. */
  readonly path = input<string>('');
  readonly pathChange = output<string>();

  protected readonly list = new PagedResource<Child>(FILES_PAGE);
  protected readonly filter = signal('');
  private readonly base = resource({ loader: () => this.address.baseUrl() });

  protected readonly shown = computed(() => {
    const f = this.filter().trim().toLowerCase();
    return f ? this.list.items().filter((c) => c.path.toLowerCase().includes(f)) : this.list.items();
  });
  protected readonly crumbs = computed(() => {
    const parts = this.path().split('/').filter((s) => s !== '');
    return parts.map((name, i) => ({ name, path: parts.slice(0, i + 1).join('/') }));
  });

  protected readonly caption = contentShapeOf('generic').itemsLabel;
  protected readonly emptyText = $localize`:@@repo.content.files-empty:This repository holds no files yet.`;
  protected readonly emptyFiltered = $localize`:@@repo.content.files-empty-filtered:No file matches the filter.`;
  protected readonly copyUrlLabel = $localize`:@@repo.content.copy-url:Copy the download address`;
  protected readonly columns: readonly TableColumn[] = [
    { id: 'name', label: $localize`:@@repo.content.col.name:Name` },
    { id: 'size', label: $localize`:@@repo.content.col.size:Size`, numeric: true },
    { id: 'modified', label: $localize`:@@repo.content.col.modified:Last change` },
    { id: 'actions', label: $localize`:@@repo.col.actions:Actions`, hideLabel: true },
  ];

  constructor() {
    effect(() => {
      const repository = this.repository();
      const path = this.path();
      untracked(() => {
        this.filter.set('');
        void this.list.start(this.files.children(repository, path, FILES_PAGE), FILES_PAGE);
      });
    });
  }

  protected fullPath(c: Child): string {
    const dir = this.path();
    return dir ? `${dir}/${c.path}` : c.path;
  }

  protected open(path: string): void {
    this.pathChange.emit(path);
  }

  protected browseLink(c: Child): string[] {
    return browseCommands(this.repository(), this.fullPath(c).split('/'));
  }

  protected fileUrl(c: Child): string {
    const base = this.base.hasValue() ? this.base.value() : null;
    const root = clientAddress(base, this.format(), this.repository());
    return root ? `${root}${this.fullPath(c).split('/').map(encodeURIComponent).join('/')}` : '';
  }

  protected size(bytes: number): string {
    return formatSize(bytes);
  }
}
