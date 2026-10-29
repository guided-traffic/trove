import { DatePipe } from '@angular/common';
import { Component, computed, input, model, output } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MatCheckboxModule } from '@angular/material/checkbox';
import { Token, TokenScope } from '../../api/tokens-api';

/** Tokens that expire within this many days are marked (R-UI-029). */
export const EXPIRING_SOON_DAYS = 7;

/** One-line summary of what a token may do (R-AUTH-024); the full scope is in the API. */
export function scopeSummary(scope: TokenScope): string {
  const parts: string[] = [];
  if (scope.inherit) {
    parts.push($localize`:@@token-table.scope.inherit:All permissions of the owner`);
  } else {
    const grants = scope.repositories ?? [];
    parts.push(
      grants.length === 0
        ? $localize`:@@token-table.scope.none:No repository access`
        : $localize`:@@token-table.scope.grants:Repository grants: ${grants.length}:count:`,
    );
  }
  if (scope.admin) parts.push($localize`:@@token-table.scope.admin:administrator roles`);
  const system = scope.system ?? [];
  if (system.length > 0) parts.push($localize`:@@token-table.scope.system:system: ${system.join(', ')}:list:`);
  return parts.join('; ');
}

/** True for an active token that expires within `EXPIRING_SOON_DAYS` days of `now`. */
export function expiresSoon(token: Pick<Token, 'state' | 'expiresAt'>, now: number): boolean {
  if (token.state !== 'active' || token.expiresAt === null) return false;
  const left = Date.parse(token.expiresAt) - now;
  return left >= 0 && left <= EXPIRING_SOON_DAYS * 86_400_000;
}

/**
 * A table of tokens (R-UI-029, R-UI-030, R-UI-026). Status, expiry warnings and type carry text and
 * icon, never colour alone. Neither the value nor its hash is part of a listed token, only the
 * `hint` that helps to recognise one.
 */
@Component({
  selector: 'trove-token-table',
  imports: [DatePipe, MatButtonModule, MatCheckboxModule],
  template: `
    @if (tokens().length === 0) {
      <p i18n="@@token-table.empty">No tokens match.</p>
    } @else {
      <div class="scroll" tabindex="0" role="region" i18n-aria-label="@@token-table.region" aria-label="Tokens">
        <table class="data">
          <caption class="visually-hidden" i18n="@@token-table.caption">Tokens</caption>
          <thead>
            <tr>
              @if (selectable()) {
                <th scope="col"><span class="visually-hidden" i18n="@@token-table.select">Select</span></th>
              }
              <th scope="col" i18n="@@token-table.description">Description</th>
              <th scope="col" i18n="@@token-table.type">Type</th>
              @if (showOwner()) {
                <th scope="col" i18n="@@token-table.owner">Owner</th>
              }
              <th scope="col" i18n="@@token-table.scope">Scope</th>
              <th scope="col" i18n="@@token-table.created">Created</th>
              <th scope="col" i18n="@@token-table.expires">Expires</th>
              <th scope="col" i18n="@@token-table.last-used">Last used</th>
              <th scope="col" i18n="@@token-table.via">Created via</th>
              <th scope="col" i18n="@@token-table.state">State</th>
              <th scope="col"><span class="visually-hidden" i18n="@@token-table.actions">Actions</span></th>
            </tr>
          </thead>
          <tbody>
            @for (t of tokens(); track t.id) {
              <tr>
                @if (selectable()) {
                  <td>
                    @if (t.state === 'active') {
                      <mat-checkbox
                        [checked]="selected().includes(t.id)"
                        (change)="toggle(t.id, $event.checked)"
                        [aria-label]="selectLabel(t)"
                      />
                    }
                  </td>
                }
                <th scope="row">
                  {{ t.description }}
                  <br /><code translate="no" class="muted">{{ t.hint }}</code>
                </th>
                <td translate="no">{{ t.type }}</td>
                @if (showOwner()) {
                  <td translate="no">{{ ownerName(t) }}</td>
                }
                <td>
                  {{ summary(t) }}
                  @if (t.unknownEndpoints.length > 0) {
                    <br /><span class="badge warn"
                      ><span class="material-icons" aria-hidden="true">warning</span
                      ><ng-container i18n="@@token-table.unknown-endpoints"
                        >Bound to an endpoint that is not configured:</ng-container
                      >
                      <code translate="no">{{ t.unknownEndpoints.join(', ') }}</code></span
                    >
                  }
                </td>
                <td>{{ t.createdAt | date: 'medium' }}</td>
                <td>
                  @if (t.expiresAt) {
                    {{ t.expiresAt | date: 'medium' }}
                    @if (soon(t)) {
                      <br /><span class="badge warn"
                        ><span class="material-icons" aria-hidden="true">schedule</span
                        ><ng-container i18n="@@token-table.soon">Expires soon</ng-container></span
                      >
                    }
                  } @else {
                    <ng-container i18n="@@token-table.never">Never</ng-container>
                  }
                </td>
                <td>
                  @if (t.lastUsedAt) {
                    {{ t.lastUsedAt | date: 'medium' }}
                  } @else {
                    <span class="muted" i18n="@@token-table.not-recorded">Not recorded</span>
                  }
                </td>
                <td translate="no">{{ t.createdVia }}</td>
                <td>
                  @switch (t.state) {
                    @case ('active') {
                      <span class="ok"><span class="material-icons" aria-hidden="true">check_circle</span> <ng-container i18n="@@token-table.state.active">Active</ng-container></span>
                    }
                    @case ('expired') {
                      <span class="warn"><span class="material-icons" aria-hidden="true">history</span> <ng-container i18n="@@token-table.state.expired">Expired</ng-container></span>
                    }
                    @default {
                      <span class="bad"><span class="material-icons" aria-hidden="true">block</span> <ng-container i18n="@@token-table.state.revoked">Revoked</ng-container></span>
                    }
                  }
                </td>
                <td class="actions">
                  @if (t.state === 'active') {
                    @if (canRotate()) {
                      <button mat-button type="button" (click)="rotate.emit(t)">
                        <ng-container i18n="@@token-table.rotate">Rotate</ng-container>
                        <span class="visually-hidden"> {{ t.description }}</span>
                      </button>
                    }
                    @if (canRevoke()) {
                      <button mat-button type="button" (click)="revoke.emit(t)">
                        <ng-container i18n="@@token-table.revoke">Revoke</ng-container>
                        <span class="visually-hidden"> {{ t.description }}</span>
                      </button>
                    }
                  }
                </td>
              </tr>
            }
          </tbody>
        </table>
      </div>
    }
  `,
  styleUrl: './access.scss',
})
export class TokenTable {
  readonly tokens = input.required<readonly Token[]>();
  readonly showOwner = input(false);
  readonly selectable = input(false);
  readonly canRotate = input(false);
  readonly canRevoke = input(true);
  /** IDs of the selected (active) tokens. */
  readonly selected = model<readonly string[]>([]);
  readonly revoke = output<Token>();
  readonly rotate = output<Token>();

  private readonly now = computed(() => {
    this.tokens();
    return Date.now();
  });

  protected summary(t: Token): string {
    return scopeSummary(t.scope);
  }

  protected soon(t: Token): boolean {
    return expiresSoon(t, this.now());
  }

  protected ownerName(t: Token): string {
    return t.owner.kind === 'service_account' ? `sa:${t.owner.name}` : t.owner.name;
  }

  protected selectLabel(t: Token): string {
    return $localize`:@@token-table.select-one:Select ${t.description}:description:`;
  }

  protected toggle(id: string, on: boolean): void {
    this.selected.update((list) => (on ? [...new Set([...list, id])] : list.filter((x) => x !== id)));
  }
}
