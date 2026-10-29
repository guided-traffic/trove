import { Component, DestroyRef, computed, effect, inject, input, signal, untracked } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MatCheckboxModule } from '@angular/material/checkbox';
import { MatDialog, MatDialogModule, MatDialogRef } from '@angular/material/dialog';
import { firstValueFrom } from 'rxjs';
import { newIdempotencyKey } from '../../api/client';
import { ApiError } from '../../api/problem';
import { CreateTokenRequest, Token, TokenCreated, TokensApi } from '../../api/tokens-api';
import { Notifier } from '../../core/notifier';
import { SessionStore } from '../../core/session-store';
import { Confirmer } from '../../shared/access/confirm-dialog';
import { DraftStore } from '../../shared/access/drafts';
import { SelectField } from '../../shared/access/fields';
import { PagedSignal, toApiError } from '../../shared/access/paged-signal';
import { TokenDraft, TokenForm } from '../../shared/access/token-form';
import { TokenSecret } from '../../shared/access/token-secret';
import { TokenTable } from '../../shared/access/token-table';
import { ErrorNotice } from '../../shared/error-notice';

/** Whose tokens the manager shows and what it may do with them. */
export type TokenOwnerScope =
  | { readonly kind: 'own' }
  | { readonly kind: 'user'; readonly name: string }
  | { readonly kind: 'service-account'; readonly name: string };

interface RotateChoice {
  readonly overlapSeconds: number;
}

/** Asks how long the old value of a rotated token stays valid (R-AUTH-030). */
@Component({
  selector: 'trove-rotate-dialog',
  imports: [MatButtonModule, MatDialogModule, SelectField],
  template: `
    <h2 mat-dialog-title i18n="@@rotate.title">Rotate this token?</h2>
    <mat-dialog-content>
      <p i18n="@@rotate.text">
        A new token with the same scope is created and shown once. Everything that uses the old token must switch to it.
      </p>
      <trove-select-field
        i18n-label="@@rotate.overlap"
        label="The old token stays valid for"
        [options]="options"
        [(value)]="overlap"
      />
    </mat-dialog-content>
    <mat-dialog-actions align="end">
      <button mat-button type="button" mat-dialog-close i18n="@@rotate.cancel">Cancel</button>
      <button mat-flat-button type="button" (click)="confirm()" i18n="@@rotate.confirm">Rotate</button>
    </mat-dialog-actions>
  `,
})
export class RotateDialog {
  private readonly ref = inject<MatDialogRef<RotateDialog, RotateChoice>>(MatDialogRef);
  protected readonly overlap = signal('0');
  protected readonly options = [
    { value: '0', label: $localize`:@@rotate.overlap.none:Not at all (revoked now)` },
    { value: '3600', label: $localize`:@@rotate.overlap.hour:One hour` },
    { value: '86400', label: $localize`:@@rotate.overlap.day:One day` },
  ];

  protected confirm(): void {
    this.ref.close({ overlapSeconds: Number.parseInt(this.overlap(), 10) });
  }
}

/**
 * The tokens of one owner: list with state filter and paging, create where the caller may
 * (R-UI-029, R-UI-026), revoke one or all with confirmation, rotate. A created or rotated value is
 * shown once through `<trove-token-secret>` and lives in this component's signal only (R-UI-043);
 * it is dropped when the panel is dismissed or the component is destroyed. Writes use the
 * idempotency key of the open form (R-UI-042); a retried creation that answers
 * `token_already_issued` revokes the token whose value was lost and creates again with a new key.
 */
@Component({
  selector: 'trove-token-manager',
  imports: [MatButtonModule, MatCheckboxModule, ErrorNotice, TokenForm, TokenSecret, TokenTable],
  template: `
    <div class="toolbar">
      @if (canCreate() && !formOpen()) {
        <button mat-flat-button type="button" (click)="openForm()" i18n="@@tokens.create">Create token</button>
      }
      <mat-checkbox [checked]="showAll()" (change)="setShowAll($event.checked)">
        <ng-container i18n="@@tokens.show-all">Show revoked and expired tokens</ng-container>
      </mat-checkbox>
      @if (canRevokeAll() && list.items().length > 0) {
        <button mat-button type="button" (click)="revokeAllTokens()" i18n="@@tokens.revoke-all">
          Revoke all tokens
        </button>
      }
    </div>

    @if (secret(); as s) {
      <trove-token-secret [value]="s.token" [description]="s.description" (closed)="secret.set(null)" />
    }

    @if (formOpen()) {
      <section class="card" aria-labelledby="token-form-heading">
        <h2 id="token-form-heading" i18n="@@tokens.form-heading">New token</h2>
        @if (formError(); as e) {
          <trove-error-notice [error]="e" />
        }
        <trove-token-form
          [submitLabel]="createLabel"
          [allowNonExpiring]="scope().kind === 'service-account'"
          [busy]="busy()"
          [failure]="formError()"
          [restore]="restored()"
          (submitted)="create($event)"
          (draft)="lastDraft = $event"
          (cancelled)="closeForm()"
        />
      </section>
    }

    @if (actionError(); as e) {
      <trove-error-notice [error]="e" />
    }
    @if (list.error(); as e) {
      <trove-error-notice [error]="e" (retry)="list.reload()" />
    }
    @if (list.loaded()) {
      <trove-token-table
        [tokens]="list.items()"
        [canRotate]="canRotate()"
        [canRevoke]="canRevoke()"
        (revoke)="revoke($event)"
        (rotate)="rotate($event)"
      />
      @if (list.hasMore()) {
        <button mat-button type="button" [disabled]="list.loading()" (click)="list.more()" i18n="@@tokens.more">
          Load more
        </button>
      }
    } @else if (list.loading()) {
      <p role="status" i18n="@@tokens.loading">Loading tokens.</p>
    }
  `,
  styleUrl: '../../shared/access/access.scss',
})
export class TokenManager {
  readonly scope = input.required<TokenOwnerScope>();

  private readonly api = inject(TokensApi);
  private readonly session = inject(SessionStore);
  private readonly confirmer = inject(Confirmer);
  private readonly notifier = inject(Notifier);
  private readonly drafts = inject(DraftStore);
  private readonly dialog = inject(MatDialog);

  protected readonly list = new PagedSignal<Token>();
  protected readonly showAll = signal(false);
  protected readonly formOpen = signal(false);
  protected readonly busy = signal(false);
  protected readonly formError = signal<ApiError | null>(null);
  protected readonly actionError = signal<ApiError | null>(null);
  protected readonly restored = signal<TokenDraft | null>(null);
  /** The value of a token that was just created; component-local, never stored (R-UI-043). */
  protected readonly secret = signal<TokenCreated | null>(null);
  protected readonly createLabel = $localize`:@@tokens.create-submit:Create token`;
  protected lastDraft: TokenDraft | null = null;

  private idempotencyKey = newIdempotencyKey();

  protected readonly owner = computed(() => {
    const s = this.scope();
    return s.kind === 'own' ? 'me' : s.kind === 'user' ? s.name : `sa:${s.name}`;
  });
  protected readonly canCreate = computed(() => {
    const s = this.scope();
    return s.kind === 'own' || (s.kind === 'service-account' && this.session.has('identities:write'));
  });
  protected readonly canRotate = computed(() => {
    const s = this.scope();
    return s.kind === 'own' || (s.kind === 'service-account' && this.session.has('identities:write'));
  });
  protected readonly canRevoke = computed(() => this.scope().kind === 'own' || this.session.has('tokens:revoke'));
  protected readonly canRevokeAll = computed(() => this.canRevoke());

  constructor() {
    inject(DestroyRef).onDestroy(() => this.secret.set(null));
    effect(() => {
      this.owner();
      this.showAll();
      untracked(() => void this.reload());
    });
    // A draft kept across re-authentication (R-UI-042) reopens the form.
    effect(() => {
      if (!this.canCreate()) return;
      untracked(() => {
        const draft = this.drafts.take<TokenDraft>();
        if (draft) {
          this.restored.set(draft);
          this.openForm();
          this.notifier.status($localize`:@@tokens.draft-restored:Your entries were restored. Create the token again.`);
        }
      });
    });
  }

  private reload(): Promise<void> {
    const state = this.showAll() ? 'all' : 'active';
    return this.list.start(this.api.list({ owner: this.owner(), state }));
  }

  protected setShowAll(on: boolean): void {
    this.showAll.set(on);
  }

  protected openForm(): void {
    this.idempotencyKey = newIdempotencyKey();
    this.formError.set(null);
    this.formOpen.set(true);
  }

  protected closeForm(): void {
    this.formOpen.set(false);
    this.restored.set(null);
  }

  protected async create(request: CreateTokenRequest): Promise<void> {
    this.busy.set(true);
    this.formError.set(null);
    try {
      const created = await this.createOnce(request);
      this.secret.set(created);
      this.closeForm();
      await this.reload();
    } catch (e) {
      const error = toApiError(e);
      this.formError.set(error);
      if (error.code === 'reauthentication_required' && this.lastDraft) this.drafts.keep(this.lastDraft);
    } finally {
      this.busy.set(false);
    }
  }

  private async createOnce(request: CreateTokenRequest): Promise<TokenCreated> {
    const s = this.scope();
    const send = (key: string): Promise<TokenCreated> =>
      s.kind === 'service-account'
        ? this.api.createForServiceAccount(s.name, request, key)
        : this.api.create(request, key);
    try {
      return await send(this.idempotencyKey);
    } catch (e) {
      // The first attempt created a token whose value was lost with its answer: revoke it,
      // create again with a new key, and show only the new one (R-UI-042).
      const lost = e instanceof ApiError && e.code === 'token_already_issued' ? e.problem?.['tokenId'] : null;
      if (typeof lost !== 'string') throw e;
      await this.api.revoke(lost);
      this.idempotencyKey = newIdempotencyKey();
      return send(this.idempotencyKey);
    }
  }

  protected async revoke(token: Token): Promise<void> {
    const answer = await this.confirmer.ask({
      title: $localize`:@@tokens.revoke-title:Revoke this token?`,
      text: $localize`:@@tokens.revoke-text:“${token.description}:description:” stops working at once on every replica. This cannot be undone.`,
      confirm: $localize`:@@tokens.revoke-confirm:Revoke`,
    });
    if (!answer.confirmed) return;
    await this.act(async () => {
      await this.api.revoke(token.id);
      this.notifier.status($localize`:@@tokens.revoked:Token revoked.`);
    });
  }

  protected async revokeAllTokens(): Promise<void> {
    const answer = await this.confirmer.ask({
      title: $localize`:@@tokens.revoke-all-title:Revoke all tokens?`,
      text: $localize`:@@tokens.revoke-all-text:Every token of this owner stops working at once. This cannot be undone.`,
      confirm: $localize`:@@tokens.revoke-all-confirm:Revoke all`,
    });
    if (!answer.confirmed) return;
    const s = this.scope();
    await this.act(async () => {
      if (s.kind === 'own') await this.api.revokeAllOwn();
      else if (s.kind === 'user') await this.api.revokeAllOfUser(s.name);
      else await this.api.revokeAllOfServiceAccount(s.name);
      this.notifier.status($localize`:@@tokens.revoked-all:All tokens revoked.`);
    });
  }

  protected async rotate(token: Token): Promise<void> {
    const ref = this.dialog.open<RotateDialog, undefined, RotateChoice>(RotateDialog, { maxWidth: '32rem' });
    const choice = await firstValueFrom(ref.afterClosed());
    if (!choice) return;
    const key = newIdempotencyKey();
    await this.act(async () => {
      this.secret.set(await this.api.rotate(token.id, choice.overlapSeconds, key));
    });
  }

  private async act(run: () => Promise<void>): Promise<void> {
    this.actionError.set(null);
    try {
      await run();
      await this.reload();
    } catch (e) {
      this.actionError.set(toApiError(e));
    }
  }
}
