import { DatePipe } from '@angular/common';
import { Component, computed, effect, inject, input, signal, untracked } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { Router, RouterLink } from '@angular/router';
import { IdentityApi, ServiceAccount } from '../../api/identity-api';
import { ApiError } from '../../api/problem';
import { Notifier } from '../../core/notifier';
import { SessionStore } from '../../core/session-store';
import { Confirmer } from '../../shared/access/confirm-dialog';
import { TextField } from '../../shared/access/fields';
import { fieldErrorAt } from '../../shared/access/field-problem';
import { toApiError } from '../../shared/access/paged-signal';
import { Needs } from '../../shared/access/needs';
import { PrincipalRoles } from '../../shared/access/principal-roles';
import { ErrorNotice } from '../../shared/error-notice';
import { TokenManager, TokenOwnerScope } from '../tokens/token-manager';

/**
 * One service account (R-UI-026): description, status with disable and enable, and the Tokens
 * section that issues (scope and expiry as for personal tokens, one-time display), lists and
 * revokes its tokens. Issuing needs recent authentication; the dialog of R-UI-042 handles it.
 */
@Component({
  selector: 'trove-service-account-detail',
  imports: [DatePipe, MatButtonModule, RouterLink, ErrorNotice, Needs, PrincipalRoles, TextField, TokenManager],
  template: `
    <header class="page-header">
      <h1 tabindex="-1" i18n="@@sa.heading">Service account</h1>
      <a mat-button routerLink="/admin/service-accounts" i18n="@@sa.back">All service accounts</a>
    </header>
    <p class="subject" translate="no">{{ name() }}</p>
    <trove-needs capability="identities:write" />

    @if (loadError(); as e) {
      <trove-error-notice [error]="e" (retry)="load()" />
    }
    @if (actionError(); as e) {
      <trove-error-notice [error]="e" />
    }

    @if (account(); as a) {
      <section class="card" aria-labelledby="sa-general">
        <h2 id="sa-general" i18n="@@sa.general">General</h2>
        <dl class="facts">
          <dt i18n="@@sa.id">ID</dt>
          <dd><code translate="no">{{ a.id }}</code></dd>
          <dt i18n="@@sa.status">Status</dt>
          <dd>
            @if (a.status === 'active') {
              <span class="ok"><span class="material-icons" aria-hidden="true">check_circle</span> <ng-container i18n="@@sa.active">Active</ng-container></span>
            } @else {
              <span class="bad"><span class="material-icons" aria-hidden="true">block</span> <ng-container i18n="@@sa.disabled">Disabled</ng-container></span>
            }
          </dd>
          <dt i18n="@@sa.created">Created</dt>
          <dd>{{ a.createdAt | date: 'medium' }}</dd>
        </dl>
        <form class="form-grid" (submit)="save($event)">
          <trove-text-field i18n-label="@@sa.description" label="Description" name="description" [(value)]="description" [readonly]="!canWrite()" [error]="fieldError('/description')" />
          @if (canWrite()) {
            <div class="row-actions">
              <button mat-flat-button type="submit" [disabled]="busy()" i18n="@@sa.save">Save description</button>
              @if (a.status === 'active') {
                <button mat-button type="button" [disabled]="busy()" (click)="disable()" i18n="@@sa.disable">Disable</button>
              } @else {
                <button mat-button type="button" [disabled]="busy()" (click)="enable()" i18n="@@sa.enable">Enable</button>
              }
              <button mat-button type="button" [disabled]="busy()" (click)="remove()" i18n="@@sa.delete">Delete</button>
            </div>
          }
        </form>
      </section>

      @if (session.has('roles:read')) {
        <trove-principal-roles [principal]="'sa:' + a.name" />
      }

      <section class="card" aria-labelledby="sa-tokens">
        <h2 id="sa-tokens" i18n="@@sa.tokens">Tokens</h2>
        @if (session.has('tokens:read')) {
          <trove-token-manager [scope]="scope()" />
        } @else {
          <p i18n="@@sa.tokens-denied">You need the permission to read tokens to manage the tokens of this account.</p>
        }
      </section>
    }
  `,
  styleUrl: '../../shared/access/access.scss',
})
export class ServiceAccountDetail {
  /** Route parameter `:name`. */
  readonly name = input.required<string>();

  private readonly api = inject(IdentityApi);
  private readonly confirmer = inject(Confirmer);
  private readonly notifier = inject(Notifier);
  private readonly router = inject(Router);
  protected readonly session = inject(SessionStore);

  protected readonly account = signal<ServiceAccount | null>(null);
  protected readonly loadError = signal<ApiError | null>(null);
  protected readonly actionError = signal<ApiError | null>(null);
  protected readonly description = signal('');
  protected readonly busy = signal(false);

  protected readonly canWrite = computed(() => this.session.has('identities:write'));
  protected readonly scope = computed<TokenOwnerScope>(() => ({ kind: 'service-account', name: this.name() }));

  constructor() {
    effect(() => {
      this.name();
      untracked(() => void this.load());
    });
  }

  protected async load(): Promise<void> {
    this.loadError.set(null);
    try {
      const a = await this.api.serviceAccount(this.name());
      this.account.set(a);
      this.description.set(a.description);
    } catch (e) {
      this.account.set(null);
      this.loadError.set(toApiError(e));
    }
  }

  protected fieldError(pointer: string): string | null {
    return fieldErrorAt(this.actionError(), pointer);
  }

  protected async save(event: Event): Promise<void> {
    event.preventDefault();
    const a = this.account();
    if (!a || this.description().trim() === a.description) return;
    await this.act(async () => {
      this.account.set(await this.api.updateServiceAccount(a.name, { description: this.description().trim() }));
    }, $localize`:@@sa.saved:Service account saved.`);
  }

  protected async disable(): Promise<void> {
    const answer = await this.confirmer.ask({
      title: $localize`:@@sa.disable-title:Disable this service account?`,
      text: $localize`:@@sa.disable-text:${this.name()}:account: can no longer authenticate.`,
      confirm: $localize`:@@sa.disable-confirm:Disable`,
      option: { label: $localize`:@@sa.disable-revoke:Also revoke all tokens of this account`, checked: false },
    });
    if (!answer.confirmed) return;
    await this.act(async () => {
      await this.api.disableServiceAccount(this.name(), { revokeTokens: answer.option });
      await this.load();
    }, $localize`:@@sa.disabled-done:Service account disabled.`);
  }

  protected async enable(): Promise<void> {
    await this.act(async () => {
      await this.api.enableServiceAccount(this.name());
      await this.load();
    }, $localize`:@@sa.enabled-done:Service account enabled.`);
  }

  protected async remove(): Promise<void> {
    const answer = await this.confirmer.ask({
      title: $localize`:@@sa.delete-title:Delete this service account?`,
      text: $localize`:@@sa.delete-text:${this.name()}:account: and its tokens are removed. This cannot be undone.`,
      confirm: $localize`:@@sa.delete-confirm:Delete`,
    });
    if (!answer.confirmed) return;
    this.actionError.set(null);
    try {
      await this.api.deleteServiceAccount(this.name());
      this.notifier.status($localize`:@@sa.deleted:Service account deleted.`);
      await this.router.navigate(['/admin/service-accounts']);
    } catch (e) {
      this.actionError.set(toApiError(e));
    }
  }

  private async act(run: () => Promise<void>, done: string): Promise<void> {
    this.busy.set(true);
    this.actionError.set(null);
    try {
      await run();
      this.notifier.status(done);
    } catch (e) {
      this.actionError.set(toApiError(e));
    } finally {
      this.busy.set(false);
    }
  }
}
