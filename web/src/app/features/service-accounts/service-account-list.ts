import { Component, inject, signal } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MatDialog, MatDialogModule, MatDialogRef } from '@angular/material/dialog';
import { Router, RouterLink } from '@angular/router';
import { firstValueFrom } from 'rxjs';
import { newIdempotencyKey } from '../../api/client';
import { IdentityApi, ServiceAccount } from '../../api/identity-api';
import { ApiError } from '../../api/problem';
import { Notifier } from '../../core/notifier';
import { SessionStore } from '../../core/session-store';
import { TextField } from '../../shared/access/fields';
import { fieldErrorAt } from '../../shared/access/field-problem';
import { Needs } from '../../shared/access/needs';
import { PagedSignal, toApiError } from '../../shared/access/paged-signal';
import { ErrorNotice } from '../../shared/error-notice';

/** "New service account" (R-UI-026). The idempotency key lives as long as the dialog (R-UI-042). */
@Component({
  selector: 'trove-service-account-create-dialog',
  imports: [MatButtonModule, MatDialogModule, ErrorNotice, TextField],
  template: `
    <h2 mat-dialog-title i18n="@@sa-create.title">New service account</h2>
    <form (submit)="submit($event)" novalidate>
      <mat-dialog-content>
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
          i18n-hint="@@sa-create.name-hint"
          hint="Lower-case letters, digits and -, two to 63 characters"
        />
        <trove-text-field i18n-label="@@sa-create.description" label="Description" name="description" [(value)]="description" [error]="fieldError('/description')" />
      </mat-dialog-content>
      <mat-dialog-actions align="end">
        <button mat-button type="button" mat-dialog-close i18n="@@sa-create.cancel">Cancel</button>
        <button mat-flat-button type="submit" [disabled]="busy()" i18n="@@sa-create.submit">Create service account</button>
      </mat-dialog-actions>
    </form>
  `,
  styleUrl: '../../shared/access/access.scss',
})
export class ServiceAccountCreateDialog {
  private readonly api = inject(IdentityApi);
  private readonly ref = inject<MatDialogRef<ServiceAccountCreateDialog, ServiceAccount>>(MatDialogRef);
  private readonly notifier = inject(Notifier);
  private readonly key = newIdempotencyKey();

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
        { name: this.name().trim(), description: this.description().trim() },
        this.key,
      );
      this.notifier.status($localize`:@@sa-create.created:Service account created.`);
      this.ref.close(created);
    } catch (e) {
      this.error.set(toApiError(e));
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
  imports: [MatButtonModule, RouterLink, ErrorNotice, Needs, TextField],
  template: `
    <header class="page-header">
      <h1 tabindex="-1" i18n="@@sas.heading">Service accounts</h1>
      <button mat-flat-button type="button" [disabled]="!session.has('identities:write')" aria-describedby="needs-sas" (click)="add()" i18n="@@sas.add">New service account</button>
    </header>
    <trove-needs id="needs-sas" capability="identities:write" />
    <p i18n="@@sas.hint">
      Use a service account for CI systems that cannot sign in through OpenID Connect and for long-running automation. Its tokens act as the account, not as a person.
    </p>

    <form class="toolbar" (submit)="submit($event)" role="search" i18n-aria-label="@@sas.filters" aria-label="Service account filters">
      <trove-text-field i18n-label="@@sas.q" label="Name" name="q" [(value)]="q" />
      <button mat-flat-button type="submit" i18n="@@sas.apply">Apply filter</button>
    </form>

    @if (list.error(); as e) {
      <trove-error-notice [error]="e" (retry)="list.reload()" />
    }
    @if (list.loaded()) {
      @if (list.items().length === 0) {
        <p i18n="@@sas.empty">No service account matches.</p>
      } @else {
        <div class="scroll" tabindex="0" role="region" i18n-aria-label="@@sas.region" aria-label="Service accounts">
          <table class="data">
            <caption class="visually-hidden" i18n="@@sas.caption">Service accounts</caption>
            <thead>
              <tr>
                <th scope="col" i18n="@@sas.col.name">Name</th>
                <th scope="col" i18n="@@sas.col.description">Description</th>
                <th scope="col" i18n="@@sas.col.status">Status</th>
              </tr>
            </thead>
            <tbody>
              @for (a of list.items(); track a.id) {
                <tr>
                  <th scope="row"><a [routerLink]="['/admin/service-accounts', a.name]" translate="no">{{ a.name }}</a></th>
                  <td>{{ a.description }}</td>
                  <td>
                    @if (a.status === 'active') {
                      <span class="ok"><span class="material-icons" aria-hidden="true">check_circle</span> <ng-container i18n="@@sas.active">Active</ng-container></span>
                    } @else {
                      <span class="bad"><span class="material-icons" aria-hidden="true">block</span> <ng-container i18n="@@sas.disabled">Disabled</ng-container></span>
                    }
                  </td>
                </tr>
              }
            </tbody>
          </table>
        </div>
        @if (list.hasMore()) {
          <p><button mat-button type="button" [disabled]="list.loading()" (click)="list.more()" i18n="@@sas.more">Load more</button></p>
        }
      }
    } @else if (list.loading()) {
      <p role="status" i18n="@@sas.loading">Loading service accounts.</p>
    }
  `,
  styleUrl: '../../shared/access/access.scss',
})
export class ServiceAccountList {
  private readonly api = inject(IdentityApi);
  private readonly dialog = inject(MatDialog);
  private readonly router = inject(Router);
  protected readonly session = inject(SessionStore);

  protected readonly list = new PagedSignal<ServiceAccount>();
  protected readonly q = signal('');

  constructor() {
    void this.apply();
  }

  protected async apply(): Promise<void> {
    await this.list.start(this.api.serviceAccounts({ q: this.q().trim() || undefined }));
  }

  protected submit(event: Event): void {
    event.preventDefault();
    void this.apply();
  }

  protected async add(): Promise<void> {
    const ref = this.dialog.open<ServiceAccountCreateDialog, undefined, ServiceAccount>(
      ServiceAccountCreateDialog,
      { maxWidth: '36rem' },
    );
    const created = await firstValueFrom(ref.afterClosed());
    if (created) await this.router.navigate(['/admin/service-accounts', created.name]);
  }
}
