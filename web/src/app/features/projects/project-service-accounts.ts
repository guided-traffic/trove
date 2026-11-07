import { Component, computed, effect, inject, input, untracked } from '@angular/core';
import { TroveDialog } from '../../shared/ui/dialog';
import { Router, RouterLink } from '@angular/router';
import { firstValueFrom } from 'rxjs';
import { IdentityApi, ServiceAccount } from '../../api/identity-api';
import { SessionStore } from '../../core/session-store';
import { PagedResource } from '../../shared/paged-resource';
import { DataTable, TableColumn } from '../../shared/ui/data-table';
import { ServiceAccountCreateData, ServiceAccountCreateDialog } from '../service-accounts/service-account-list';
import { ProjectNeeds, createState } from './project-access';

/**
 * The service accounts of a project (R-UI-041, R-AUTHZ-026, Q-AUTHZ-175): create one named within the project,
 * and open it for its tokens, disabling and deleting. The account's own screen is the one of the
 * identity area; it recognises a project account and offers the project Owner its rights.
 */
@Component({
  selector: 'trove-project-service-accounts',
  imports: [RouterLink, DataTable, ProjectNeeds],
  template: `
    <div class="toolbar">
      <button class="btn btn-confirm"
        type="button"
        [disabled]="state() !== 'enabled'"
        aria-describedby="needs-project-sas"
        (click)="add()"
        i18n="@@project-sas.create"
      >
        Create service account
      </button>
    </div>
    <trove-project-needs id="needs-project-sas" [state]="state()" capability="identities:write" />

    <trove-data-table
      [caption]="caption"
      [columns]="columns"
      [loaded]="list.loaded()"
      [loading]="list.loading()"
      [error]="list.error()"
      [empty]="list.items().length === 0"
      i18n-emptyText="@@project-sas.empty"
      emptyText="The project has no service account yet."
      [hasMore]="list.hasMore()"
      (more)="list.more()"
      (retry)="list.reload()"
    >
      @for (a of list.items(); track a.id) {
        <tr>
          <th scope="row"><a [routerLink]="['/projects', project(), 'service-accounts', a.name]" translate="no">{{ a.name }}</a></th>
          <td>{{ a.description }}</td>
          <td>
            @if (a.status === 'active') {
              <span class="ok"><span class="material-icons inline" aria-hidden="true">check_circle</span><ng-container i18n="@@project-sas.active">Active</ng-container></span>
            } @else {
              <span class="bad"><span class="material-icons inline" aria-hidden="true">block</span><ng-container i18n="@@project-sas.disabled">Disabled</ng-container></span>
            }
          </td>
        </tr>
      }
    </trove-data-table>
  `,
  styleUrl: '../../shared/access/access.scss',
})
export class ProjectServiceAccounts {
  readonly project = input.required<string>();

  private readonly api = inject(IdentityApi);
  private readonly dialog = inject(TroveDialog);
  private readonly router = inject(Router);
  private readonly session = inject(SessionStore);

  protected readonly list = new PagedResource<ServiceAccount>();
  protected readonly state = computed(() => createState(this.session, this.project(), 'identities:write'));
  protected readonly caption = $localize`:@@project-sas.caption:Service accounts of the project`;
  protected readonly columns: readonly TableColumn[] = [
    { id: 'name', label: $localize`:@@project-sas.col.name:Name` },
    { id: 'description', label: $localize`:@@project-sas.col.description:Description` },
    { id: 'status', label: $localize`:@@project-sas.col.status:Status` },
  ];

  constructor() {
    effect(() => {
      const project = this.project();
      untracked(() => void this.list.start(this.api.serviceAccounts({ project, sort: 'name' })));
    });
  }

  protected async add(): Promise<void> {
    const ref = this.dialog.open<ServiceAccountCreateDialog, ServiceAccountCreateData, ServiceAccount>(
      ServiceAccountCreateDialog,
      { data: { project: this.project() }, maxWidth: '36rem' },
    );
    const created = await firstValueFrom(ref.closed);
    if (created) await this.router.navigate(['/projects', this.project(), 'service-accounts', created.name]);
  }
}
