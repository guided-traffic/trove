import { Component, inject, signal } from '@angular/core';
import { DIALOG_DATA, DialogClose, DialogRef, DialogTitle, TroveDialog } from '../../shared/ui/dialog';
import { Router, RouterLink } from '@angular/router';
import { firstValueFrom } from 'rxjs';
import { newIdempotencyKey } from '../../api/client';
import { IdentityApi, ServiceAccount } from '../../api/identity-api';
import { ApiError } from '../../api/problem';
import { Notifier } from '../../core/notifier';
import { SessionStore } from '../../core/session-store';
import { TextField } from '../../shared/access/fields';
import { fieldErrorAt } from '../../shared/field-errors';
import { Needs } from '../../shared/access/needs';
import { PagedResource, asApiError } from '../../shared/paged-resource';
import { ErrorNotice } from '../../shared/error-notice';
import { DataTable, TableColumn, TableSort } from '../../shared/ui/data-table';
import { FilterCategory, FilterPanel, FilterToggle } from '../../shared/ui/filter-panel';
import { PageHeader } from '../../shared/ui/page-header';

/** What the dialog is opened with: the project key to create a project service account, named within it (R-AUTHZ-026). */
export interface ServiceAccountCreateData {
  readonly project?: string;
}

/**
 * "New service account" (R-UI-026). The idempotency key lives as long as the dialog (R-UI-042). Opened
 * with a project it creates a project service account: the name is unique within the project and the account
 * belongs to the project for good (R-UI-041).
 */
@Component({
  selector: 'trove-service-account-create-dialog',
  imports: [DialogTitle, DialogClose, ErrorNotice, TextField],
  template: `
    <h2 troveDialogTitle i18n="@@sa-create.title">New service account</h2>
    <form (submit)="submit($event)" novalidate>
      <div class="dialog-body">
        @if (error(); as e) {
          <trove-error-notice [error]="e" />
        }
        <trove-text-field
          i18n-label="@@sa-create.name"
          label="Name"
          name="name"
          [(value)]="name"
          [required]="true"
          [error]="fieldError('/name')"
          [hint]="nameHint"
        />
        <trove-text-field i18n-label="@@sa-create.description" label="Description" name="description" [(value)]="description" [error]="fieldError('/description')" />
      </div>
      <div class="dialog-actions">
        <button class="btn btn-tertiary" type="button" troveDialogClose i18n="@@sa-create.cancel">Cancel</button>
        <button class="btn btn-confirm" type="submit" [disabled]="busy()" i18n="@@sa-create.submit">Create service account</button>
      </div>
    </form>
  `,
  styleUrl: '../../shared/access/access.scss',
})
export class ServiceAccountCreateDialog {
  private readonly api = inject(IdentityApi);
  private readonly ref = inject<DialogRef<ServiceAccount, ServiceAccountCreateDialog>>(DialogRef);
  private readonly notifier = inject(Notifier);
  private readonly data = inject<ServiceAccountCreateData | null>(DIALOG_DATA, { optional: true });
  private readonly key = newIdempotencyKey();

  protected readonly project = this.data?.project;
  protected readonly nameHint = this.project
    ? $localize`:@@sa-create.name-hint-project:Unique within the project ${this.project}:project: and cannot be changed later. Lower-case letters, digits and -.`
    : $localize`:@@sa-create.name-hint:Lower-case letters, digits and -, two to 63 characters`;
  protected readonly name = signal('');
  protected readonly description = signal('');
  protected readonly busy = signal(false);
  protected readonly error = signal<ApiError | null>(null);

  protected fieldError(pointer: string): string | null {
    return fieldErrorAt(this.error(), pointer);
  }

  protected async submit(event: Event): Promise<void> {
    event.preventDefault();
    this.busy.set(true);
    this.error.set(null);
    try {
      const created = await this.api.createServiceAccount(
        this.project ?? null,
        { name: this.name().trim(), description: this.description().trim() },
        this.key,
      );
      this.notifier.status($localize`:@@sa-create.created:Service account created.`);
      this.ref.close(created);
    } catch (e) {
      this.error.set(asApiError(e));
    } finally {
      this.busy.set(false);
    }
  }
}

/**
 * Service accounts (R-UI-026): name, description and status, with a name filter and cursor
 * paging. The hint says when to use one.
 */
@Component({
  selector: 'trove-service-account-list',
  imports: [PageHeader, RouterLink, DataTable, FilterCategory, FilterPanel, FilterToggle, Needs, TextField],
  template: `
    <trove-page-header heading="Service accounts" i18n-heading="@@sas.heading">
      <button actions class="btn btn-confirm" type="button" [disabled]="!session.has('identities:write')" aria-describedby="needs-sas" (click)="add()" i18n="@@sas.add">New service account</button>
    </trove-page-header>
    <trove-needs id="needs-sas" capability="identities:write" />
    <p i18n="@@sas.hint">
      Use a service account for CI systems that cannot sign in through OpenID Connect and for long-running automation. Its tokens act as the account, not as a person.
    </p>

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
          i18n-emptyText="@@sas.empty"
          emptyText="No service account matches."
          [hasMore]="list.hasMore()"
          (more)="list.more()"
          (retry)="list.reload()"
        >
          @for (a of list.items(); track a.id) {
            <tr>
              <th scope="row"><a [routerLink]="saLink(a)" translate="no">{{ a.name }}</a></th>
              <td>{{ a.description }}</td>
              <td>
                @if (a.project) {
                  <a [routerLink]="['/projects', a.project]" translate="no">{{ a.project }}</a>
                } @else {
                  –
                }
              </td>
              <td>
                @if (a.status === 'active') {
                  <span class="ok"><span class="material-icons inline" aria-hidden="true">check_circle</span><ng-container i18n="@@sas.active">Active</ng-container></span>
                } @else {
                  <span class="bad"><span class="material-icons inline" aria-hidden="true">block</span><ng-container i18n="@@sas.disabled">Disabled</ng-container></span>
                }
              </td>
            </tr>
          }
        </trove-data-table>
      </div>

      <trove-filter-panel
        #panel
        class="list-side"
        [(open)]="panelOpen"
        [activeCount]="activeCount()"
        [applyLabel]="applyLabel"
        (apply)="apply()"
        (clearAll)="clear()"
      >
        <trove-filter-category i18n-label="@@sas.q" label="Name" [active]="!!q().trim()" [expanded]="!!q().trim()">
          <trove-text-field i18n-label="@@sas.q" label="Name" name="q" [(value)]="q" />
        </trove-filter-category>
      </trove-filter-panel>
    </div>
  `,
  styleUrl: '../../shared/access/access.scss',
})
export class ServiceAccountList {
  private readonly api = inject(IdentityApi);
  private readonly dialog = inject(TroveDialog);
  private readonly router = inject(Router);
  protected readonly session = inject(SessionStore);

  protected readonly list = new PagedResource<ServiceAccount>();
  protected readonly q = signal('');
  protected readonly panelOpen = signal(false);
  protected readonly activeCount = signal(0);
  protected readonly sort = signal<TableSort>({ column: 'name', direction: 'asc' });
  protected readonly caption = $localize`:@@sas.caption:Service accounts`;
  protected readonly applyLabel = $localize`:@@sas.apply:Apply filter`;
  protected readonly columns: readonly TableColumn[] = [
    { id: 'name', label: $localize`:@@sas.col.name:Name`, sortable: true },
    { id: 'description', label: $localize`:@@sas.col.description:Description` },
    { id: 'project', label: $localize`:@@sas.col.project:Project` },
    { id: 'status', label: $localize`:@@sas.col.status:Status` },
  ];

  constructor() {
    void this.apply();
  }

  /** `/projects/<project>/service-accounts/<name>` for a project's account, `/admin/service-accounts/<name>` otherwise. */
  protected saLink(a: ServiceAccount): string[] {
    return a.project ? ['/projects', a.project, 'service-accounts', a.name] : ['/admin/service-accounts', a.name];
  }

  protected async apply(): Promise<void> {
    const q = this.q().trim() || undefined;
    this.activeCount.set(q ? 1 : 0);
    const sort = this.sort();
    await this.list.start(this.api.serviceAccounts({ q, sort: sort.direction === 'desc' ? '-name' : 'name' }));
  }

  protected sortBy(sort: TableSort): void {
    this.sort.set(sort);
    void this.apply();
  }

  protected clear(): void {
    this.q.set('');
    void this.apply();
  }

  protected async add(): Promise<void> {
    const ref = this.dialog.open<ServiceAccountCreateDialog, undefined, ServiceAccount>(
      ServiceAccountCreateDialog,
      { maxWidth: '36rem' },
    );
    const created = await firstValueFrom(ref.closed);
    if (created) await this.router.navigate(this.saLink(created));
  }
}
