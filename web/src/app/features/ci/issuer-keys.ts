import { DatePipe } from '@angular/common';
import { Component, effect, inject, input, signal, untracked } from '@angular/core';
import { CiApi, CiKeyStatus } from '../../api/ci-api';
import { ApiError } from '../../api/problem';
import { Notifier } from '../../core/notifier';
import { SessionStore } from '../../core/session-store';
import { Needs } from '../../shared/access/needs';
import { asApiError } from '../../shared/paged-resource';
import { DataTable, TableColumn } from '../../shared/ui/data-table';
import { ErrorNotice } from '../../shared/error-notice';

/**
 * The signing keys of an issuer (R-UI-035, R-CI-002, R-CI-003): the cached key set with its fetch time,
 * the next refresh and the last error (the failure class, the status and the host; never bytes of an
 * answer), and "Refresh keys", which fetches the discovery document and the keys now. A key that does
 * not meet the key rules is ignored and says why.
 */
@Component({
  selector: 'trove-ci-issuer-keys',
  imports: [DatePipe, ErrorNotice, DataTable, Needs],
  template: `
    <div class="toolbar">
      <button class="btn btn-confirm" type="button" [disabled]="busy() || !session.has('ci:write')" aria-describedby="needs-ci-keys" (click)="refresh()" i18n="@@ci.keys.refresh">
        Refresh keys
      </button>
    </div>
    <trove-needs id="needs-ci-keys" capability="ci:write" />
    @if (error(); as e) {
      <trove-error-notice [error]="e" (retry)="load()" />
    }
    @if (status(); as s) {
      <dl class="facts">
        <dt i18n="@@ci.keys.jwks">Key set</dt>
        <dd translate="no">{{ s.jwksUri ?? '–' }}</dd>
        <dt i18n="@@ci.keys.fetched-at">Last fetch</dt>
        <dd>{{ s.fetchedAt ? (s.fetchedAt | date: 'medium') : '–' }}</dd>
        <dt i18n="@@ci.keys.next">Next refresh</dt>
        <dd>{{ s.nextRefreshAt ? (s.nextRefreshAt | date: 'medium') : '–' }}</dd>
        <dt i18n="@@ci.keys.last-error">Last error</dt>
        <dd>
          @if (s.lastError) {
            <span class="bad"><span class="material-icons" aria-hidden="true">error</span> {{ s.lastError }}</span>
            @if (s.lastErrorAt) {
              <span class="muted">({{ s.lastErrorAt | date: 'medium' }})</span>
            }
          } @else {
            <span class="ok"><span class="material-icons" aria-hidden="true">check_circle</span> <ng-container i18n="@@ci.keys.no-error">None</ng-container></span>
          }
        </dd>
      </dl>
      <trove-data-table
        [caption]="caption"
        [columns]="columns"
        [loaded]="true"
        [empty]="s.keys.length === 0"
        i18n-emptyText="@@ci.keys.empty"
        emptyText="No key has been fetched yet."
      >
        @for (k of s.keys; track k.kid) {
          <tr>
            <th scope="row" translate="no">{{ k.kid }}</th>
            <td translate="no">{{ k.kty }}</td>
            <td translate="no">{{ k.alg ?? '–' }}</td>
            <td>
              @if (k.ignored) {
                <span class="warn"><span class="material-icons" aria-hidden="true">warning</span> <ng-container i18n="@@ci.keys.ignored">Ignored</ng-container></span>
                <span class="muted" translate="no">{{ k.ignoredReason }}</span>
              } @else {
                <span class="ok"><span class="material-icons" aria-hidden="true">check_circle</span> <ng-container i18n="@@ci.keys.in-use">In use</ng-container></span>
              }
            </td>
          </tr>
        }
      </trove-data-table>
    }
  `,
  styleUrl: '../../shared/access/access.scss',
})
export class IssuerKeys {
  readonly issuer = input.required<string>();

  private readonly api = inject(CiApi);
  private readonly notifier = inject(Notifier);
  protected readonly session = inject(SessionStore);

  protected readonly status = signal<CiKeyStatus | null>(null);
  protected readonly error = signal<ApiError | null>(null);
  protected readonly busy = signal(false);

  protected readonly caption = $localize`:@@ci.keys.caption:Signing keys of the issuer`;
  protected readonly columns: readonly TableColumn[] = [
    { id: 'kid', label: $localize`:@@ci.keys.col.kid:Key ID` },
    { id: 'kty', label: $localize`:@@ci.keys.col.kty:Type` },
    { id: 'alg', label: $localize`:@@ci.keys.col.alg:Algorithm` },
    { id: 'state', label: $localize`:@@ci.keys.col.state:State` },
  ];

  constructor() {
    effect(() => {
      this.issuer();
      untracked(() => void this.load());
    });
  }

  protected async load(): Promise<void> {
    this.error.set(null);
    try {
      this.status.set(await this.api.keys(this.issuer()));
    } catch (e) {
      this.error.set(asApiError(e));
    }
  }

  protected async refresh(): Promise<void> {
    this.busy.set(true);
    this.error.set(null);
    try {
      this.status.set(await this.api.refreshKeys(this.issuer()));
      this.notifier.status($localize`:@@ci.keys.refreshed:The keys were fetched again.`);
    } catch (e) {
      this.error.set(asApiError(e));
    } finally {
      this.busy.set(false);
    }
  }
}
