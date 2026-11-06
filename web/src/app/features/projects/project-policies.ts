import { Component, computed, effect, inject, input, untracked } from '@angular/core';
import { RouterLink } from '@angular/router';
import { PermissionPolicy, PolicyApi } from '../../api/authz-api';
import { SessionStore } from '../../core/session-store';
import { PagedResource } from '../../shared/paged-resource';
import { DataTable, TableColumn } from '../../shared/ui/data-table';
import { actionSummary, principalSummary } from '../permissions/policy-list';
import { ProjectNeeds, createState } from './project-access';

/**
 * The permission policies of a project (R-UI-041, R-AUTHZ-026): the policies that carry the project's
 * key, with the editor of the permissions area opened in the project's context. Creating one needs
 * `policies:write` or `managePolicies` on the project.
 */
@Component({
  selector: 'trove-project-policies',
  imports: [RouterLink, DataTable, ProjectNeeds],
  template: `
    <div class="toolbar">
      @if (state() === 'enabled') {
        <a class="btn btn-confirm"
          routerLink="/admin/permissions/new"
          [queryParams]="{ project: project() }"
          i18n="@@project-policies.create"
          >Create policy</a
        >
      } @else {
        <button class="btn btn-confirm" type="button" disabled aria-describedby="needs-project-policies" i18n="@@project-policies.create-disabled">
          Create policy
        </button>
      }
    </div>
    <trove-project-needs id="needs-project-policies" [state]="state()" capability="policies:write" />

    <trove-data-table
      [caption]="caption"
      [columns]="columns"
      [loaded]="list.loaded()"
      [loading]="list.loading()"
      [error]="list.error()"
      [empty]="list.items().length === 0"
      i18n-emptyText="@@project-policies.empty"
      emptyText="The project has no permission policy yet."
      [hasMore]="list.hasMore()"
      (more)="list.more()"
      (retry)="list.reload()"
    >
      @for (p of list.items(); track p.name) {
        <tr>
          <th scope="row">
            <a [routerLink]="['/admin/permissions', project(), p.name]" translate="no">{{ p.name }}</a>
            @if (p.inert) {
              <span class="badge warn"><span class="material-icons" aria-hidden="true">warning</span><ng-container i18n="@@project-policies.inert">Grants nothing</ng-container></span>
            }
          </th>
          <td>{{ p.description }}</td>
          <td>{{ principalSummary(p) }}</td>
          <td translate="no">{{ actionSummary(p) }}</td>
        </tr>
      }
    </trove-data-table>
  `,
  styleUrl: '../../shared/access/access.scss',
})
export class ProjectPolicies {
  readonly project = input.required<string>();

  private readonly api = inject(PolicyApi);
  private readonly session = inject(SessionStore);

  protected readonly list = new PagedResource<PermissionPolicy>();
  protected readonly state = computed(() => createState(this.session, this.project(), 'policies:write'));
  protected readonly principalSummary = principalSummary;
  protected readonly actionSummary = actionSummary;
  protected readonly caption = $localize`:@@project-policies.caption:Permission policies of the project`;
  protected readonly columns: readonly TableColumn[] = [
    { id: 'name', label: $localize`:@@project-policies.col.name:Name` },
    { id: 'description', label: $localize`:@@project-policies.col.description:Description` },
    { id: 'principals', label: $localize`:@@project-policies.col.principals:Principals` },
    { id: 'actions', label: $localize`:@@project-policies.col.actions:Actions` },
  ];

  constructor() {
    effect(() => {
      const project = this.project();
      untracked(() => void this.list.start(this.api.list({ project, sort: 'name' })));
    });
  }
}
