import { DatePipe } from '@angular/common';
import { Component, DestroyRef, LOCALE_ID, computed, inject, input, output, signal } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MatDialog } from '@angular/material/dialog';
import { firstValueFrom } from 'rxjs';
import { Bastion, BastionsApi, BastionTokenCreated, BastionTokenSummary } from '../../api/bastions-api';
import { ApiError } from '../../api/problem';
import { Notifier } from '../../core/notifier';
import { SessionStore } from '../../core/session-store';
import { formatDuration } from '../../shared/access/duration';
import { TokenSecret } from '../../shared/access/token-secret';
import { Confirm } from '../../shared/confirm-dialog';
import { ErrorNotice } from '../../shared/error-notice';
import { asApiError } from '../../shared/paged-resource';
import { DataTable, TableColumn } from '../../shared/ui/data-table';
import { BastionTokenDialog } from './bastion-dialogs';

/** A token that expires within this many days carries the warning mark (R-BAS-007). */
export const BASTION_TOKEN_WARN_DAYS = 30;

const DAY_MS = 86_400_000;

/** The state of a token against `now`: expired, expiring within the warning window, or fine. */
export function tokenExpiry(token: Pick<BastionTokenSummary, 'expiresAt'>, now: number): 'expired' | 'soon' | 'ok' {
  const left = Date.parse(token.expiresAt) - now;
  if (left <= 0) return 'expired';
  return left < BASTION_TOKEN_WARN_DAYS * DAY_MS ? 'soon' : 'ok';
}

/**
 * One Bastion registration (R-BAS-007): its facts and actions, the last contact of each replica and its
 * tokens. Disable and enable read the registration for its `ETag` first, because the list carries none
 * (R-API-012). A token's value is shown once through `<trove-token-secret>` and lives in this card's
 * signal only (R-UI-043). Which actions show follows the capabilities of R-BAS-004: `endpoints:write`
 * for the registration and for issuing a token, `endpoints:write` or `tokens:revoke` for revoking one.
 */
@Component({
  selector: 'trove-bastion-card',
  imports: [DatePipe, MatButtonModule, DataTable, ErrorNotice, TokenSecret],
  template: `
    <section class="card" [attr.aria-labelledby]="headingId()">
      <h2 [id]="headingId()" translate="no">{{ bastion().name }}</h2>
      <dl class="facts">
        <dt i18n="@@bastion.status">Status</dt>
        <dd>
          @if (bastion().enabled) {
            <span class="ok"><span class="material-icons" aria-hidden="true">check_circle</span> <ng-container i18n="@@bastion.enabled">Enabled</ng-container></span>
          } @else {
            <span class="bad"><span class="material-icons" aria-hidden="true">block</span> <ng-container i18n="@@bastion.disabled">Disabled</ng-container></span>
          }
        </dd>
        @if (bastion().description) {
          <dt i18n="@@bastion.description">Description</dt>
          <dd>{{ bastion().description }}</dd>
        }
        <dt i18n="@@bastion.created">Created</dt>
        <dd>{{ bastion().createdAt | date: 'medium' }}</dd>
      </dl>

      @if (canWrite()) {
        <div class="row-actions">
          <button mat-flat-button type="button" [disabled]="busy()" (click)="issue()">
            <ng-container i18n="@@bastion.issue">Issue token</ng-container>
            <span class="visually-hidden"> {{ bastion().name }}</span>
          </button>
          @if (bastion().enabled) {
            <button mat-button type="button" [disabled]="busy()" (click)="disable()">
              <ng-container i18n="@@bastion.disable">Disable</ng-container>
              <span class="visually-hidden"> {{ bastion().name }}</span>
            </button>
          } @else {
            <button mat-button type="button" [disabled]="busy()" (click)="enable()">
              <ng-container i18n="@@bastion.enable">Enable</ng-container>
              <span class="visually-hidden"> {{ bastion().name }}</span>
            </button>
          }
          <button mat-button type="button" [disabled]="busy()" (click)="remove()">
            <ng-container i18n="@@bastion.delete">Delete</ng-container>
            <span class="visually-hidden"> {{ bastion().name }}</span>
          </button>
        </div>
      }

      @if (actionError(); as e) {
        <trove-error-notice [error]="e" />
      }
      @if (secret(); as s) {
        <trove-token-secret [value]="s.token" [description]="secretNote(s)" (closed)="secret.set(null)" />
      }

      <h3 i18n="@@bastion.contacts">Last contact</h3>
      <trove-data-table
        [caption]="contactsCaption()"
        [columns]="contactColumns"
        [empty]="bastion().contacts.length === 0"
        i18n-emptyText="@@bastion.contacts-empty"
        emptyText="No replica has contacted the main installation with a token of this registration yet. Its replicas appear here after their first handshake."
      >
        @for (c of bastion().contacts; track c.replica) {
          <tr>
            <th scope="row" translate="no">{{ c.replica }}</th>
            <td>{{ c.contactAt | date: 'medium' }}</td>
            <td translate="no">{{ c.clientAddress || '–' }}</td>
            <td translate="no">{{ c.release }}</td>
            <td translate="no">{{ c.interfaceVersion }}</td>
            <td translate="no">{{ c.baseUrl || '–' }}</td>
          </tr>
        }
      </trove-data-table>

      <h3 i18n="@@bastion.tokens">Tokens</h3>
      <trove-data-table
        [caption]="tokensCaption()"
        [columns]="tokenColumns"
        [empty]="bastion().tokens.length === 0"
        i18n-emptyText="@@bastion.tokens-empty"
        emptyText="This registration has no token. Issue one to connect a Bastion."
      >
        @for (t of bastion().tokens; track t.id) {
          <tr>
            <th scope="row">
              {{ t.description }}
              <code translate="no" class="secondary">{{ t.hint }}</code>
            </th>
            <td>{{ t.createdAt | date: 'medium' }}</td>
            <td>
              {{ t.expiresAt | date: 'medium' }}
              @switch (expiry(t)) {
                @case ('expired') {
                  <br /><span class="badge bad"><span class="material-icons" aria-hidden="true">error</span><ng-container i18n="@@bastion.token.expired">Expired</ng-container></span>
                }
                @case ('soon') {
                  <br /><span class="badge warn"><span class="material-icons" aria-hidden="true">schedule</span><ng-container i18n="@@bastion.token.soon">Expires in</ng-container>&ngsp;{{ remaining(t) }}</span>
                }
              }
            </td>
            <td>
              @if (t.lastUsedAt) {
                {{ t.lastUsedAt | date: 'medium' }}
              } @else {
                <span class="muted" i18n="@@bastion.token.unused">Not used yet</span>
              }
            </td>
            <td class="actions">
              @if (canRevoke()) {
                <button mat-button type="button" [disabled]="busy()" (click)="revoke(t)">
                  <ng-container i18n="@@bastion.token.revoke">Revoke</ng-container>
                  <span class="visually-hidden"> {{ t.description }}</span>
                </button>
              }
            </td>
          </tr>
        }
      </trove-data-table>
    </section>
  `,
  styleUrl: '../../shared/access/access.scss',
})
export class BastionCard {
  readonly bastion = input.required<Bastion>();
  /** The registration changed or went away: the page reads the list again. */
  readonly changed = output<void>();

  private readonly api = inject(BastionsApi);
  private readonly confirmer = inject(Confirm);
  private readonly dialog = inject(MatDialog);
  private readonly notifier = inject(Notifier);
  private readonly session = inject(SessionStore);
  private readonly locale = inject(LOCALE_ID);

  protected readonly busy = signal(false);
  protected readonly actionError = signal<ApiError | null>(null);
  /** The value of a token that was just issued; card-local, never stored (R-UI-043). */
  protected readonly secret = signal<BastionTokenCreated | null>(null);

  protected readonly canWrite = computed(() => this.session.has('endpoints:write'));
  protected readonly canRevoke = computed(() => this.session.has('endpoints:write') || this.session.has('tokens:revoke'));
  protected readonly headingId = computed(() => `bastion-${this.bastion().name}`);
  protected readonly contactsCaption = computed(() => $localize`:@@bastion.contacts-caption:Last contact of the replicas of ${this.bastion().name}:name:`);
  protected readonly tokensCaption = computed(() => $localize`:@@bastion.tokens-caption:Tokens of ${this.bastion().name}:name:`);

  protected readonly contactColumns: readonly TableColumn[] = [
    { id: 'replica', label: $localize`:@@bastion.col.replica:Replica` },
    { id: 'time', label: $localize`:@@bastion.col.time:Time` },
    {
      id: 'address',
      label: $localize`:@@bastion.col.address:Client address`,
      help: $localize`:@@bastion.help.address:The address the main installation saw the replica connect from.`,
    },
    { id: 'release', label: $localize`:@@bastion.col.release:Release` },
    {
      id: 'interface',
      label: $localize`:@@bastion.col.interface:Interface version`,
      help: $localize`:@@bastion.help.interface:The version of the interface between the Bastion and this installation. Both sides must agree.`,
    },
    {
      id: 'base',
      label: $localize`:@@bastion.col.base:Reported base URL`,
      help: $localize`:@@bastion.help.base:The address the Bastion says clients reach it on. Client setup uses it when it is listed in server.additional_base_urls.`,
    },
  ];
  protected readonly tokenColumns: readonly TableColumn[] = [
    { id: 'description', label: $localize`:@@bastion.col.token:Token` },
    { id: 'created', label: $localize`:@@bastion.col.token-created:Created` },
    { id: 'expires', label: $localize`:@@bastion.col.expires:Expires` },
    {
      id: 'lastUsed',
      label: $localize`:@@bastion.col.last-used:Last used`,
      help: $localize`:@@bastion.help.last-used:The last time the token authenticated a request. When you renew, revoke the old token once the new one shows a recent time.`,
    },
    { id: 'actions', label: $localize`:@@bastion.col.actions:Actions`, hideLabel: true },
  ];

  private readonly now = computed(() => {
    this.bastion();
    return Date.now();
  });

  constructor() {
    inject(DestroyRef).onDestroy(() => this.secret.set(null));
  }

  protected expiry(t: BastionTokenSummary): 'expired' | 'soon' | 'ok' {
    return tokenExpiry(t, this.now());
  }

  protected remaining(t: BastionTokenSummary): string {
    const seconds = Math.max(60, Math.ceil((Date.parse(t.expiresAt) - this.now()) / 1000));
    // Whole days, rounded up, while a day or more is left; else whole minutes.
    return formatDuration(seconds >= 86_400 ? Math.ceil(seconds / 86_400) * 86_400 : Math.ceil(seconds / 60) * 60, this.locale);
  }

  protected secretNote(s: BastionTokenCreated): string {
    return $localize`:@@bastion.secret-note:For ${this.bastion().name}:name:: ${s.description}:description:`;
  }

  protected async issue(): Promise<void> {
    const ref = this.dialog.open<BastionTokenDialog, { name: string }, BastionTokenCreated>(BastionTokenDialog, {
      data: { name: this.bastion().name },
      maxWidth: '36rem',
    });
    const created = await firstValueFrom(ref.afterClosed());
    if (!created) return;
    this.secret.set(created);
    this.changed.emit();
  }

  protected async disable(): Promise<void> {
    const answer = await this.confirmer.ask({
      title: $localize`:@@bastion.disable-title:Disable this Bastion?`,
      lines: [
        $localize`:@@bastion.disable-text:The tokens of ${this.bastion().name}:name: are refused until you enable it again. Its replicas treat the main installation as unreachable and serve from their cache within the grace period.`,
      ],
      confirmLabel: $localize`:@@bastion.disable-confirm:Disable`,
    });
    if (answer === null) return;
    await this.setEnabled(false, $localize`:@@bastion.disabled-done:Bastion disabled.`);
  }

  protected async enable(): Promise<void> {
    await this.setEnabled(true, $localize`:@@bastion.enabled-done:Bastion enabled.`);
  }

  private async setEnabled(enabled: boolean, done: string): Promise<void> {
    await this.act(async () => {
      const { etag } = await this.api.get(this.bastion().name);
      await this.api.setEnabled(this.bastion().name, enabled, etag);
    }, done);
  }

  protected async remove(): Promise<void> {
    const name = this.bastion().name;
    const answer = await this.confirmer.ask({
      title: $localize`:@@bastion.delete-title:Delete this Bastion?`,
      lines: [$localize`:@@bastion.delete-text:${name}:name: and its tokens are removed; the tokens stop working at once. This cannot be undone.`],
      confirmLabel: $localize`:@@bastion.delete-confirm:Delete`,
      typeToConfirm: name,
    });
    if (answer === null) return;
    await this.act(() => this.api.delete(name), $localize`:@@bastion.deleted:Bastion deleted.`);
  }

  protected async revoke(t: BastionTokenSummary): Promise<void> {
    const answer = await this.confirmer.ask({
      title: $localize`:@@bastion.revoke-title:Revoke this token?`,
      lines: [
        $localize`:@@bastion.revoke-text:“${t.description}:description:” stops working at once on every replica. A replica that still uses it treats the main installation as unreachable. When you renew, revoke the old token only after the new one shows a recent time in Last used. This cannot be undone.`,
      ],
      confirmLabel: $localize`:@@bastion.revoke-confirm:Revoke`,
    });
    if (answer === null) return;
    await this.act(() => this.api.revokeToken(this.bastion().name, t.id), $localize`:@@bastion.revoked:Token revoked.`);
  }

  private async act(run: () => Promise<void>, done: string): Promise<void> {
    this.busy.set(true);
    this.actionError.set(null);
    try {
      await run();
      this.notifier.status(done);
      this.changed.emit();
    } catch (e) {
      this.actionError.set(asApiError(e));
    } finally {
      this.busy.set(false);
    }
  }
}
