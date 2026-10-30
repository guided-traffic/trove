import { DatePipe } from '@angular/common';
import { Component, inject, signal } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MatDialog } from '@angular/material/dialog';
import { Router, RouterLink } from '@angular/router';
import { firstValueFrom } from 'rxjs';
import { IdentityApi, User, UserFilters } from '../../api/identity-api';
import { SessionStore } from '../../core/session-store';
import { SelectField, SelectOption, TextField } from '../../shared/access/fields';
import { Needs } from '../../shared/access/needs';
import { PagedResource } from '../../shared/paged-resource';
import { DataTable, TableColumn, TableSort } from '../../shared/ui/data-table';
import { FilterCategory, FilterPanel, FilterToggle } from '../../shared/ui/filter-panel';
import { UserCreateDialog } from './user-create-dialog';
import { originLabel } from './user-labels';

/**
 * Users (R-UI-024): username, display name, email, origin, status and last sign-in; filters text,
 * status, origin, group and administrator in the filter panel; the user name column sorts through
 * the API's `sort` parameter; cursor paging with "Load more". "Add user"
 * pre-provisions a user linked to an identity-provider identity; there are no password fields.
 */
@Component({
  selector: 'trove-user-list',
  imports: [
    DatePipe,
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
  templateUrl: './user-list.html',
  styleUrl: '../../shared/access/access.scss',
})
export class UserList {
  private readonly api = inject(IdentityApi);
  private readonly dialog = inject(MatDialog);
  private readonly router = inject(Router);
  protected readonly session = inject(SessionStore);

  protected readonly list = new PagedResource<User>();
  protected readonly q = signal('');
  protected readonly status = signal('');
  protected readonly origin = signal('');
  protected readonly group = signal('');
  protected readonly admin = signal('');
  protected readonly panelOpen = signal(false);
  /** How many filters the list currently applies (not the staged values in the panel). */
  protected readonly activeCount = signal(0);
  protected readonly sort = signal<TableSort>({ column: 'username', direction: 'asc' });

  protected readonly caption = $localize`:@@users.caption:Users`;
  protected readonly columns: readonly TableColumn[] = [
    { id: 'username', label: $localize`:@@users.col.username:User name`, sortable: true },
    { id: 'displayName', label: $localize`:@@users.col.display-name:Display name` },
    { id: 'email', label: $localize`:@@users.col.email:Email` },
    {
      id: 'origin',
      label: $localize`:@@users.col.origin:Origin`,
      help: $localize`:@@users.help.origin:How the account came to exist: at first sign-in through the identity provider, created by an administrator, provisioned by SCIM, or the built-in break-glass account.`,
    },
    { id: 'status', label: $localize`:@@users.col.status:Status` },
    { id: 'lastLogin', label: $localize`:@@users.col.last-login:Last sign-in` },
  ];

  protected readonly statusOptions: readonly SelectOption[] = [
    { value: '', label: $localize`:@@users.any:Any` },
    { value: 'active', label: $localize`:@@users.status.active:Active` },
    { value: 'disabled', label: $localize`:@@users.status.disabled:Disabled` },
    { value: 'locked', label: $localize`:@@users.status.locked:Locked` },
  ];
  protected readonly originOptions: readonly SelectOption[] = [
    { value: '', label: $localize`:@@users.any:Any` },
    { value: 'jit', label: originLabel('jit') },
    { value: 'admin', label: originLabel('admin') },
    { value: 'scim', label: originLabel('scim') },
    { value: 'system', label: originLabel('system') },
  ];
  protected readonly adminOptions: readonly SelectOption[] = [
    { value: '', label: $localize`:@@users.any:Any` },
    { value: 'true', label: $localize`:@@users.admin.yes:Administrators` },
    { value: 'false', label: $localize`:@@users.admin.no:Not administrators` },
  ];
  protected readonly originLabel = originLabel;

  constructor() {
    void this.apply();
  }

  protected async apply(): Promise<void> {
    const filters: UserFilters = {
      q: this.q().trim() || undefined,
      status: (this.status() || undefined) as UserFilters['status'],
      origin: (this.origin() || undefined) as UserFilters['origin'],
      group: this.group().trim() || undefined,
      admin: this.admin() === '' ? undefined : this.admin() === 'true',
    };
    this.activeCount.set(Object.values(filters).filter((v) => v !== undefined).length);
    const sort = this.sort();
    await this.list.start(
      this.api.users({ ...filters, sort: sort.direction === 'desc' ? '-username' : 'username' }),
    );
  }

  protected sortBy(sort: TableSort): void {
    this.sort.set(sort);
    void this.apply();
  }

  protected clear(): void {
    this.q.set('');
    this.status.set('');
    this.origin.set('');
    this.group.set('');
    this.admin.set('');
    void this.apply();
  }

  protected async add(): Promise<void> {
    const ref = this.dialog.open<UserCreateDialog, undefined, User>(UserCreateDialog, { maxWidth: '36rem' });
    const created = await firstValueFrom(ref.afterClosed());
    if (created) await this.router.navigate(['/admin/users', created.username]);
  }
}
