import { Component, computed, effect, inject, input, untracked } from '@angular/core';
import { RouterLink } from '@angular/router';
import { RepositoriesApi, RepositorySummary } from '../../api/repositories-api';
import { SessionStore } from '../../core/session-store';
import { PagedResource } from '../../shared/paged-resource';
import { DataTable, TableColumn } from '../../shared/ui/data-table';
import { classLabel } from '../repositories/repository-wizard';
import { ProjectNeeds, createState } from './project-access';

/**
 * The repositories of a project (R-UI-041): the key links to the repository's own screen, which a
 * project administrator with `manageRepositories` may also edit. Creating one opens the wizard with the
 * project and the key prefix `<key>-` filled in (R-AUTHZ-026); assigning existing repositories stays
 * with the system administrators, in the project dialog.
 */
@Component({
  selector: 'trove-project-repositories',
  imports: [RouterLink, DataTable, ProjectNeeds],
  template: `
    <div class="toolbar">
      @if (state() === 'enabled') {
        <a class="btn btn-confirm"
          routerLink="/admin/repositories/new"
          [queryParams]="{ project: project() }"
          i18n="@@project-repos.create"
          >Create repository</a
        >
      } @else {
        <button class="btn btn-confirm" type="button" disabled aria-describedby="needs-project-repos" i18n="@@project-repos.create-disabled">
          Create repository
        </button>
      }
      <a class="btn btn-tertiary" routerLink="/admin/repositories" [queryParams]="{ project: project() }" i18n="@@project-repos.open-list">
        Open in the repository list
      </a>
    </div>
    <trove-project-needs id="needs-project-repos" [state]="state()" capability="repositories:write-config" right="manageRepositories" />

    <trove-data-table
      [caption]="caption"
      [columns]="columns"
      [loaded]="list.loaded()"
      [loading]="list.loading()"
      [error]="list.error()"
      [empty]="list.items().length === 0"
      i18n-emptyText="@@project-repos.empty"
      emptyText="No repository is assigned to this project."
      [hasMore]="list.hasMore()"
      (more)="list.more()"
      (retry)="list.reload()"
    >
      @for (r of list.items(); track r.key) {
        <tr>
          <th scope="row"><a [routerLink]="['/admin/repositories', r.key]" translate="no">{{ r.key }}</a></th>
          <td translate="no">{{ r.format }}</td>
          <td>{{ classText(r.class) }}</td>
          <td>{{ r.description }}</td>
          <td>
            @if (r.lifecycle === 'deleting') {
              <span class="badge" i18n="@@project-repos.deleting">being deleted</span>
            } @else if (r.disabled) {
              <span class="badge" i18n="@@project-repos.disabled">disabled</span>
            }
          </td>
        </tr>
      }
    </trove-data-table>
  `,
  styleUrl: '../../shared/access/access.scss',
})
export class ProjectRepositories {
  readonly project = input.required<string>();

  private readonly api = inject(RepositoriesApi);
  private readonly session = inject(SessionStore);

  protected readonly list = new PagedResource<RepositorySummary>();
  protected readonly state = computed(() =>
    createState(this.session, this.project(), 'manageRepositories', 'repositories:write-config'),
  );
  protected readonly classText = classLabel;
  protected readonly caption = $localize`:@@project-repos.caption:Repositories of the project`;
  protected readonly columns: readonly TableColumn[] = [
    { id: 'key', label: $localize`:@@project-repos.col.key:Key` },
    { id: 'format', label: $localize`:@@project-repos.col.format:Format` },
    { id: 'class', label: $localize`:@@project-repos.col.class:Class` },
    { id: 'description', label: $localize`:@@project-repos.col.description:Description` },
    { id: 'state', label: $localize`:@@project-repos.col.state:State` },
  ];

  constructor() {
    effect(() => {
      const project = this.project();
      untracked(() => void this.list.start(this.api.list({ project })));
    });
  }
}
