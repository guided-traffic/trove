import { DatePipe } from '@angular/common';
import { Component, computed, effect, inject, input, signal, untracked } from '@angular/core';
import { Router } from '@angular/router';
import { IdentityApi, ServiceAccount } from '../../api/identity-api';
import { ApiError } from '../../api/problem';
import { Versioned } from '../../api/repositories-api';
import { Notifier } from '../../core/notifier';
import { SessionStore } from '../../core/session-store';
import { Confirm } from '../../shared/confirm-dialog';
import { ConflictResolver } from '../../shared/conflict-dialog';
import { TextField } from '../../shared/access/fields';
import { fieldErrorAt } from '../../shared/field-errors';
import { asApiError } from '../../shared/paged-resource';
import { Needs } from '../../shared/access/needs';
import { PrincipalRoles } from '../../shared/access/principal-roles';
import { ErrorNotice } from '../../shared/error-notice';
import { Disclosure } from '../../shared/ui/disclosure';
import { TokenManager, TokenOwnerScope } from '../tokens/token-manager';
import { PageHeader } from '../../shared/ui/page-header';

/**
 * One service account (R-UI-026): description, status with disable and enable, and the Tokens
 * section that issues (scope and expiry as for personal tokens, one-time display), lists and
 * revokes its tokens. Issuing needs recent authentication; the dialog of R-UI-042 handles it.
 */
@Component({
  selector: 'trove-service-account-detail',
  imports: [PageHeader, DatePipe, ErrorNotice, Needs, PrincipalRoles, Disclosure, TextField, TokenManager],
  template: `
    <trove-page-header [heading]="name()" [name]="true">
      <span meta class="badge" i18n="@@sa.heading">Service account</span>
    </trove-page-header>
    @if (projectKey(); as key) {
      <p class="subject">
        <ng-container i18n="@@sa.project">Service account of project</ng-container>&ngsp;
        <span translate="no">{{ key }}</span>
      </p>
    }
    @if (!canWrite()) {
      <trove-needs capability="identities:write" />
    }

    @if (loadError(); as e) {
      <trove-error-notice [error]="e" (retry)="load()" />
    }
    @if (actionError(); as e) {
      <trove-error-notice [error]="e" />
    }

    @if (account(); as a) {
      <div class="settings">
        <form (submit)="save($event)" novalidate>
          <div class="row">
            <trove-text-field i18n-label="@@sa.description" label="Description" name="description" [(value)]="description" [readonly]="!canWrite()" [error]="fieldError('/description')" />
            <trove-text-field i18n-label="@@sa.id" label="ID" name="id" [value]="a.id" [readonly]="true" />
          </div>
          @if (canWrite()) {
            <button class="btn btn-confirm" type="submit" [disabled]="busy() || !dirty()" i18n="@@sa.save-changes">Save changes</button>
          }
        </form>

        <trove-disclosure
          i18n-heading="@@sa.details"
          heading="Details"
          i18n-hint="@@sa.details-hint"
          hint="Status, administrator role and when the account was created."
        >
          <dl class="facts">
            <dt i18n="@@sa.status">Status</dt>
            <dd>
              @if (a.status === 'active') {
                <span class="ok"><span class="material-icons" aria-hidden="true">check_circle</span> <ng-container i18n="@@sa.active">Active</ng-container></span>
              } @else {
                <span class="bad"><span class="material-icons" aria-hidden="true">block</span> <ng-container i18n="@@sa.disabled">Disabled</ng-container></span>
              }
            </dd>
            @if (!a.project) {
              <dt i18n="@@sa.admin">Administrator role</dt>
              <dd data-testid="sa-admin">
                @if (a.isAdmin) {
                  <ng-container i18n="@@sa.admin.yes">Held by this account</ng-container>
                } @else {
                  <ng-container i18n="@@sa.admin.no">Not held</ng-container>
                }
              </dd>
            }
            <dt i18n="@@sa.created">Created</dt>
            <dd>{{ a.createdAt | date: 'medium' }}</dd>
          </dl>
        </trove-disclosure>

        @if (session.has('roles:read') && !a.project) {
          <trove-principal-roles [principal]="a.principal" />
        }

        <section class="tokens" aria-labelledby="sa-tokens">
          <h2 id="sa-tokens" i18n="@@sa.tokens">Tokens</h2>
          @if (canReadTokens()) {
            <trove-token-manager [scope]="scope()" [accountIsAdmin]="a.isAdmin" />
          } @else {
            <p i18n="@@sa.tokens-denied">You need the permission to read tokens to manage the tokens of this account.</p>
          }
        </section>

        @if (canWrite()) {
          <trove-disclosure
            i18n-heading="@@sa.advanced"
            heading="Advanced"
            i18n-hint="@@sa.advanced-hint"
            hint="Disable or delete this account."
          >
            <p class="muted">
              @if (a.status === 'active') {
                <ng-container i18n="@@sa.disable-note">A disabled account can no longer authenticate; you can also revoke its tokens.</ng-container>
              } @else {
                <ng-container i18n="@@sa.enable-note">The account is disabled and cannot authenticate. Enabling it lets its tokens work again.</ng-container>
              }
            </p>
            <p>
              @if (a.status === 'active') {
                <button class="btn btn-tertiary" type="button" [disabled]="busy()" (click)="disable()" i18n="@@sa.disable">Disable</button>
              } @else {
                <button class="btn btn-tertiary" type="button" [disabled]="busy()" (click)="enable()" i18n="@@sa.enable">Enable</button>
              }
            </p>
            <p class="muted" i18n="@@sa.delete-note">Deleting the account removes it and its tokens. This cannot be undone.</p>
            <button class="btn btn-danger" type="button" [disabled]="busy()" (click)="remove()" i18n="@@sa.delete">Delete</button>
          </trove-disclosure>
        }
      </div>
    }
  `,
  styleUrl: '../../shared/access/access.scss',
  styles: `
    .settings {
      max-inline-size: var(--trove-settings-measure);
    }
    form {
      padding-block-end: var(--trove-space-6);
    }
    .row {
      display: grid;
      grid-template-columns: minmax(0, 5fr) minmax(0, 4fr);
      gap: var(--trove-space-6);
    }
    .tokens {
      padding-block: var(--trove-space-5);
      border-block-start: 1px solid var(--trove-border);
    }
    @media (max-width: 40rem) {
      .row {
        grid-template-columns: minmax(0, 1fr);
      }
    }
  `,
})
export class ServiceAccountDetail {
  /** Route parameter `:name`: bare within the project. */
  readonly name = input.required<string>();
  /** Route parameter `:project`; absent for an instance-wide account (R-AUTHZ-026). */
  readonly project = input<string>();

  private readonly api = inject(IdentityApi);
  private readonly confirmer = inject(Confirm);
  private readonly conflicts = inject(ConflictResolver);
  private readonly notifier = inject(Notifier);
  private readonly router = inject(Router);
  protected readonly session = inject(SessionStore);

  protected readonly account = signal<ServiceAccount | null>(null);
  /** The `ETag` the record was read with; every write sends it as `If-Match` (Q-API-42). */
  private etag = '';
  protected readonly loadError = signal<ApiError | null>(null);
  protected readonly actionError = signal<ApiError | null>(null);
  protected readonly description = signal('');
  protected readonly busy = signal(false);
  protected readonly dirty = computed(() => {
    const a = this.account();
    return a !== null && this.description().trim() !== a.description;
  });

  /** The project of the account, or null for an instance-wide account (R-AUTHZ-026). */
  protected readonly projectKey = computed(() => this.project() ?? null);
  /** `identities:write`, or the Owner role of the account's project. */
  protected readonly canWrite = computed(
    () => this.session.has('identities:write') || this.session.isOwnerOf(this.projectKey()),
  );
  protected readonly canReadTokens = computed(
    () => this.session.has('tokens:read') || this.session.isOwnerOf(this.projectKey()),
  );
  protected readonly scope = computed<TokenOwnerScope>(() => ({
    kind: 'service-account',
    name: this.name(),
    project: this.projectKey(),
  }));

  constructor() {
    effect(() => {
      this.name();
      untracked(() => void this.load());
    });
  }

  protected async load(): Promise<void> {
    this.loadError.set(null);
    try {
      this.adopt(await this.api.serviceAccount(this.name(), this.projectKey()));
    } catch (e) {
      this.account.set(null);
      this.loadError.set(asApiError(e));
    }
  }

  private adopt(loaded: Versioned<ServiceAccount>): void {
    this.etag = loaded.etag;
    this.account.set(loaded.value);
    this.description.set(loaded.value.description);
  }

  protected fieldError(pointer: string): string | null {
    return fieldErrorAt(this.actionError(), pointer);
  }

  protected async save(event: Event): Promise<void> {
    event.preventDefault();
    const a = this.account();
    if (!a || this.description().trim() === a.description) return;
    const patch = { description: this.description().trim() };
    this.busy.set(true);
    this.actionError.set(null);
    try {
      const result = await this.conflicts.save<ServiceAccount>({
        etag: this.etag,
        patch,
        send: (etag) => this.api.updateServiceAccount(a.name, this.projectKey(), patch, etag),
        reload: () => this.api.serviceAccount(a.name, this.projectKey()),
      });
      if (result.outcome !== 'dismissed') this.adopt(result.current);
      if (result.outcome === 'saved') this.notifier.status($localize`:@@sa.saved:Service account saved.`);
    } catch (e) {
      this.actionError.set(asApiError(e));
    } finally {
      this.busy.set(false);
    }
  }

  protected async disable(): Promise<void> {
    const answer = await this.confirmer.ask({
      title: $localize`:@@sa.disable-title:Disable this service account?`,
      lines: [$localize`:@@sa.disable-text:${this.name()}:account: can no longer authenticate.`],
      confirmLabel: $localize`:@@sa.disable-confirm:Disable`,
      choice: $localize`:@@sa.disable-revoke:Also revoke all tokens of this account`,
      choiceChecked: false,
    });
    if (answer === null) return;
    await this.act(async () => {
      await this.api.disableServiceAccount(this.name(), this.projectKey(), { revokeTokens: answer.checked });
      await this.load();
    }, $localize`:@@sa.disabled-done:Service account disabled.`);
  }

  protected async enable(): Promise<void> {
    await this.act(async () => {
      await this.api.enableServiceAccount(this.name(), this.projectKey());
      await this.load();
    }, $localize`:@@sa.enabled-done:Service account enabled.`);
  }

  protected async remove(): Promise<void> {
    const answer = await this.confirmer.ask({
      title: $localize`:@@sa.delete-title:Delete this service account?`,
      lines: [$localize`:@@sa.delete-text:${this.name()}:account: and its tokens are removed. This cannot be undone.`],
      confirmLabel: $localize`:@@sa.delete-confirm:Delete`,
    });
    if (answer === null) return;
    this.actionError.set(null);
    try {
      await this.api.deleteServiceAccount(this.name(), this.projectKey());
      this.notifier.status($localize`:@@sa.deleted:Service account deleted.`);
      const projectKey = this.projectKey();
      await this.router.navigate(projectKey ? ['/projects', projectKey, 'service-accounts'] : ['/admin/service-accounts']);
    } catch (e) {
      this.actionError.set(asApiError(e));
    }
  }

  private async act(run: () => Promise<void>, done: string): Promise<void> {
    this.busy.set(true);
    this.actionError.set(null);
    try {
      await run();
      this.notifier.status(done);
    } catch (e) {
      this.actionError.set(asApiError(e));
    } finally {
      this.busy.set(false);
    }
  }
}
