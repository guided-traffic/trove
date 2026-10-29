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
import { ErrorNotice } from '../../shared/error-notice';
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

/** Groups (R-UI-025): name, description, origin and member count, with filters and paging. */
@Component({
  selector: 'trove-group-list',
  imports: [MatButtonModule, RouterLink, ErrorNotice, Needs, SelectField, TextField],
  template: `
    <header class="page-header">
      <h1 tabindex="-1" i18n="@@groups.heading">Groups</h1>
      <button mat-flat-button type="button" [disabled]="!session.has('identities:write')" aria-describedby="needs-groups" (click)="add()" i18n="@@groups.add">New group</button>
    </header>
    <trove-needs id="needs-groups" capability="identities:write" />

    <form class="toolbar" (submit)="submit($event)" role="search" i18n-aria-label="@@groups.filters" aria-label="Group filters">
      <trove-text-field i18n-label="@@groups.q" label="Name" name="q" [(value)]="q" />
      <trove-select-field i18n-label="@@groups.origin" label="Origin" name="origin" [options]="originOptions" [(value)]="origin" />
      <button mat-flat-button type="submit" i18n="@@groups.apply">Apply filters</button>
    </form>

    @if (list.error(); as e) {
      <trove-error-notice [error]="e" (retry)="list.reload()" />
    }
    @if (list.loaded()) {
      @if (list.items().length === 0) {
        <p i18n="@@groups.empty">No group matches.</p>
      } @else {
        <div class="scroll" tabindex="0" role="region" i18n-aria-label="@@groups.region" aria-label="Groups">
          <table class="data">
            <caption class="visually-hidden" i18n="@@groups.caption">Groups</caption>
            <thead>
              <tr>
                <th scope="col" i18n="@@groups.col.name">Name</th>
                <th scope="col" i18n="@@groups.col.description">Description</th>
                <th scope="col" i18n="@@groups.col.origin">Origin</th>
                <th scope="col" i18n="@@groups.col.members">Members</th>
              </tr>
            </thead>
            <tbody>
              @for (g of list.items(); track g.id) {
                <tr>
                  <th scope="row"><a [routerLink]="['/admin/groups', g.name]" translate="no">{{ g.name }}</a></th>
                  <td>{{ g.description }}</td>
                  <td>{{ originLabel(g.origin) }}</td>
                  <td>{{ g.memberCount }}</td>
                </tr>
              }
            </tbody>
          </table>
        </div>
        @if (list.hasMore()) {
          <p><button mat-button type="button" [disabled]="list.loading()" (click)="list.more()" i18n="@@groups.more">Load more</button></p>
        }
      }
    } @else if (list.loading()) {
      <p role="status" i18n="@@groups.loading">Loading groups.</p>
    }
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
    await this.list.start(this.api.groups(filters));
  }

  protected submit(event: Event): void {
    event.preventDefault();
    void this.apply();
  }

  protected async add(): Promise<void> {
    const ref = this.dialog.open<GroupCreateDialog, undefined, Group>(GroupCreateDialog, { maxWidth: '36rem' });
    const created = await firstValueFrom(ref.afterClosed());
    if (created) await this.router.navigate(['/admin/groups', created.name]);
  }
}
