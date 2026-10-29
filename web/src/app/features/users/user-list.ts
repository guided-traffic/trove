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
import { ErrorNotice } from '../../shared/error-notice';
import { UserCreateDialog } from './user-create-dialog';
import { originLabel } from './user-labels';

/**
 * Users (R-UI-024): username, display name, email, origin, status and last sign-in; filters text,
 * status, origin, group and administrator; cursor paging with "Load more". "Add user"
 * pre-provisions a user linked to an identity-provider identity; there are no password fields.
 */
@Component({
  selector: 'trove-user-list',
  imports: [DatePipe, MatButtonModule, RouterLink, ErrorNotice, Needs, SelectField, TextField],
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
    await this.list.start(this.api.users(filters));
  }

  protected submit(event: Event): void {
    event.preventDefault();
    void this.apply();
  }

  protected async add(): Promise<void> {
    const ref = this.dialog.open<UserCreateDialog, undefined, User>(UserCreateDialog, { maxWidth: '36rem' });
    const created = await firstValueFrom(ref.afterClosed());
    if (created) await this.router.navigate(['/admin/users', created.username]);
  }
}
