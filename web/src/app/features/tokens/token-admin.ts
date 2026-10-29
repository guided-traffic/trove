import { Component, inject, signal } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MatCheckboxModule } from '@angular/material/checkbox';
import { ApiError } from '../../api/problem';
import { Token, TokenFilters, TokensApi } from '../../api/tokens-api';
import { Notifier } from '../../core/notifier';
import { SessionStore } from '../../core/session-store';
import { Confirmer } from '../../shared/access/confirm-dialog';
import { SelectField, SelectOption, TextField } from '../../shared/access/fields';
import { Needs } from '../../shared/access/needs';
import { PagedSignal, toApiError } from '../../shared/access/paged-signal';
import { TokenTable } from '../../shared/access/token-table';
import { ErrorNotice } from '../../shared/error-notice';

/**
 * Token administration (R-UI-030): every token with filters (type, owner, state, expiring before,
 * unused for N days), revoke one or the selected ones with confirmation, and "Revoke by value" for
 * a leaked token. The pasted value is sent once and cleared; it lives in this component's signal
 * and in no store (R-UI-043). Issuances of CI pipelines (`/api/v1/ci/issuances`) belong to the CI
 * work package and are not shown here.
 */
@Component({
  selector: 'trove-token-admin',
  imports: [MatButtonModule, MatCheckboxModule, ErrorNotice, Needs, SelectField, TextField, TokenTable],
  templateUrl: './token-admin.html',
  styleUrl: '../../shared/access/access.scss',
})
export class TokenAdmin {
  private readonly api = inject(TokensApi);
  private readonly confirmer = inject(Confirmer);
  private readonly notifier = inject(Notifier);
  protected readonly session = inject(SessionStore);

  protected readonly list = new PagedSignal<Token>();
  protected readonly owner = signal('');
  protected readonly type = signal('');
  protected readonly state = signal('active');
  protected readonly expiresBefore = signal('');
  protected readonly unusedDays = signal('');
  protected readonly selected = signal<readonly string[]>([]);
  protected readonly actionError = signal<ApiError | null>(null);

  protected readonly pasted = signal('');
  protected readonly reveal = signal(false);
  protected readonly valueError = signal<ApiError | null>(null);
  protected readonly busy = signal(false);

  protected readonly typeOptions: readonly SelectOption[] = [
    { value: '', label: $localize`:@@tokens-admin.any:Any` },
    { value: 'pat', label: $localize`:@@tokens-admin.type.pat:Personal (pat)` },
    { value: 'sat', label: $localize`:@@tokens-admin.type.sat:Service account (sat)` },
    { value: 'cit', label: $localize`:@@tokens-admin.type.cit:Pipeline (cit)` },
  ];
  protected readonly stateOptions: readonly SelectOption[] = [
    { value: 'active', label: $localize`:@@tokens-admin.state.active:Active` },
    { value: 'expired', label: $localize`:@@tokens-admin.state.expired:Expired` },
    { value: 'revoked', label: $localize`:@@tokens-admin.state.revoked:Revoked` },
    { value: 'all', label: $localize`:@@tokens-admin.state.all:All` },
  ];

  constructor() {
    void this.apply();
  }

  /** Starts a new traversal with the current filters (a cursor is bound to its filters). */
  protected async apply(): Promise<void> {
    this.selected.set([]);
    const days = Number.parseInt(this.unusedDays(), 10);
    const filters: TokenFilters = {
      owner: this.owner().trim() || undefined,
      type: (this.type() || undefined) as TokenFilters['type'],
      state: this.state() as TokenFilters['state'],
      expiresBefore: this.expiresBefore() ? `${this.expiresBefore()}T23:59:59Z` : undefined,
      lastUsedBefore:
        Number.isFinite(days) && days > 0 ? new Date(Date.now() - days * 86_400_000).toISOString() : undefined,
    };
    await this.list.start(this.api.list(filters));
  }

  protected submit(event: Event): void {
    event.preventDefault();
    void this.apply();
  }

  protected async revokeOne(token: Token): Promise<void> {
    const answer = await this.confirmer.ask({
      title: $localize`:@@tokens-admin.revoke-title:Revoke this token?`,
      text: $localize`:@@tokens-admin.revoke-text:“${token.description}:description:” of ${token.owner.name}:owner: stops working at once on every replica.`,
      confirm: $localize`:@@tokens-admin.revoke-confirm:Revoke`,
    });
    if (answer.confirmed) await this.revoke([token.id]);
  }

  protected async revokeSelected(): Promise<void> {
    const ids = this.selected();
    if (ids.length === 0) return;
    const answer = await this.confirmer.ask({
      title: $localize`:@@tokens-admin.revoke-selected-title:Revoke the selected tokens?`,
      text: $localize`:@@tokens-admin.revoke-selected-text:The selected tokens (${ids.length}:count:) stop working at once on every replica. This cannot be undone.`,
      confirm: $localize`:@@tokens-admin.revoke-confirm:Revoke`,
    });
    if (answer.confirmed) await this.revoke(ids);
  }

  private async revoke(ids: readonly string[]): Promise<void> {
    this.actionError.set(null);
    const failed: string[] = [];
    for (const id of ids) {
      try {
        await this.api.revoke(id);
      } catch (e) {
        failed.push(id);
        this.actionError.set(toApiError(e));
      }
    }
    if (failed.length < ids.length) this.notifier.status($localize`:@@tokens-admin.revoked:Tokens revoked.`);
    await this.apply();
  }

  protected async revokeByValue(event: Event): Promise<void> {
    event.preventDefault();
    const value = this.pasted().trim();
    if (!value) return;
    this.busy.set(true);
    this.valueError.set(null);
    try {
      const revoked = await this.api.revokeByValue(value);
      this.notifier.status($localize`:@@tokens-admin.value-revoked:The token of ${revoked.owner}:owner: was revoked.`);
      await this.apply();
    } catch (e) {
      this.valueError.set(toApiError(e));
    } finally {
      // The value was sent once; it is gone from the form whatever the answer was (R-UI-043).
      this.pasted.set('');
      this.reveal.set(false);
      this.busy.set(false);
    }
  }
}
