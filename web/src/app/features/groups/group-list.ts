import { Component, inject, signal } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MatDialog } from '@angular/material/dialog';
import { Router, RouterLink } from '@angular/router';
import { firstValueFrom } from 'rxjs';
import { Group, GroupFilters, IdentityApi } from '../../api/identity-api';
import { SessionStore } from '../../core/session-store';
import { SelectField, SelectOption, TextField } from '../../shared/access/fields';
import { Needs } from '../../shared/access/needs';
import { PagedResource } from '../../shared/paged-resource';
import { DataTable, TableColumn, TableSort } from '../../shared/ui/data-table';
import { FilterCategory, FilterPanel, FilterToggle } from '../../shared/ui/filter-panel';
import { GroupCreateDialog } from './group-create-dialog';

export function groupOriginLabel(origin: Group['origin']): string {
  switch (origin) {
    case 'admin':
      return $localize`:@@group.origin.admin:Created by an administrator`;
    case 'oidc':
      return $localize`:@@group.origin.oidc:Identity provider`;
    default:
      return $localize`:@@group.origin.scim:SCIM`;
  }
}

/** Groups (R-UI-025): name, description, origin and member count, with the filter panel, a sortable name and paging. */
@Component({
  selector: 'trove-group-list',
  imports: [
    MatButtonModule,
    RouterLink,
    DataTable,
    FilterCategory,
    FilterPanel,
    FilterToggle,
    Needs,
    SelectField,
    TextField,
  ],
  template: `
    <header class="page-header">
      <h1 tabindex="-1" i18n="@@groups.heading">Groups</h1>
      <button mat-flat-button type="button" [disabled]="!session.has('identities:write')" aria-describedby="needs-groups" (click)="add()" i18n="@@groups.add">New group</button>
    </header>
    <trove-needs id="needs-groups" capability="identities:write" />

    <div class="list-layout" [class.panel-open]="panelOpen()">
      <div class="list-main">
        <div class="list-toolbar">
          <trove-filter-toggle [(open)]="panelOpen" [activeCount]="activeCount()" [controls]="panel.panelId" />
        </div>

        <trove-data-table
          [caption]="caption"
          [columns]="columns"
          [sort]="sort()"
          (sortChange)="sortBy($event)"
          [loaded]="list.loaded()"
          [loading]="list.loading()"
          [error]="list.error()"
          [empty]="list.items().length === 0"
          i18n-emptyText="@@groups.empty"
          emptyText="No group matches."
          [hasMore]="list.hasMore()"
          (more)="list.more()"
          (retry)="list.reload()"
        >
          @for (g of list.items(); track g.id) {
            <tr>
              <th scope="row"><a [routerLink]="['/admin/groups', g.name]" translate="no">{{ g.name }}</a></th>
              <td>{{ g.description }}</td>
              <td>{{ originLabel(g.origin) }}</td>
              <td class="num">{{ g.memberCount }}</td>
            </tr>
          }
        </trove-data-table>
      </div>

      <trove-filter-panel
        #panel
        class="list-side"
        [(open)]="panelOpen"
        [activeCount]="activeCount()"
        (apply)="apply()"
        (clearAll)="clear()"
      >
        <trove-filter-category i18n-label="@@groups.q" label="Name" [active]="!!q().trim()" [expanded]="!!q().trim()">
          <trove-text-field i18n-label="@@groups.q" label="Name" name="q" [(value)]="q" />
        </trove-filter-category>
        <trove-filter-category i18n-label="@@groups.origin" label="Origin" [active]="!!origin()" [expanded]="!!origin()">
          <trove-select-field i18n-label="@@groups.origin" label="Origin" name="origin" [options]="originOptions" [(value)]="origin" />
        </trove-filter-category>
      </trove-filter-panel>
    </div>
  `,
  styleUrl: '../../shared/access/access.scss',
})
export class GroupList {
  private readonly api = inject(IdentityApi);
  private readonly dialog = inject(MatDialog);
  private readonly router = inject(Router);
  protected readonly session = inject(SessionStore);

  protected readonly list = new PagedResource<Group>();
  protected readonly q = signal('');
  protected readonly origin = signal('');
  protected readonly panelOpen = signal(false);
  protected readonly activeCount = signal(0);
  protected readonly sort = signal<TableSort>({ column: 'name', direction: 'asc' });
  protected readonly caption = $localize`:@@groups.caption:Groups`;
  protected readonly columns: readonly TableColumn[] = [
    { id: 'name', label: $localize`:@@groups.col.name:Name`, sortable: true },
    { id: 'description', label: $localize`:@@groups.col.description:Description` },
    {
      id: 'origin',
      label: $localize`:@@groups.col.origin:Origin`,
      help: $localize`:@@groups.help.origin:Where the group comes from: created by an administrator, mapped from the identity provider, or provisioned by SCIM.`,
    },
    { id: 'members', label: $localize`:@@groups.col.members:Members`, numeric: true },
  ];
  protected readonly originLabel = groupOriginLabel;
  protected readonly originOptions: readonly SelectOption[] = [
    { value: '', label: $localize`:@@groups.any:Any` },
    { value: 'admin', label: groupOriginLabel('admin') },
    { value: 'oidc', label: groupOriginLabel('oidc') },
    { value: 'scim', label: groupOriginLabel('scim') },
  ];

  constructor() {
    void this.apply();
  }

  protected async apply(): Promise<void> {
    const filters: GroupFilters = {
      q: this.q().trim() || undefined,
      origin: (this.origin() || undefined) as GroupFilters['origin'],
    };
    this.activeCount.set(Object.values(filters).filter((v) => v !== undefined).length);
    const sort = this.sort();
    await this.list.start(this.api.groups({ ...filters, sort: sort.direction === 'desc' ? '-name' : 'name' }));
  }

  protected sortBy(sort: TableSort): void {
    this.sort.set(sort);
    void this.apply();
  }

  protected clear(): void {
    this.q.set('');
    this.origin.set('');
    void this.apply();
  }

  protected async add(): Promise<void> {
    const ref = this.dialog.open<GroupCreateDialog, undefined, Group>(GroupCreateDialog, { maxWidth: '36rem' });
    const created = await firstValueFrom(ref.afterClosed());
    if (created) await this.router.navigate(['/admin/groups', created.name]);
  }
}
