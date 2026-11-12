import { Component, DestroyRef, computed, effect, inject, input, signal, untracked } from '@angular/core';
import { Checkbox } from '../../shared/ui/checkbox';
import { DialogClose, DialogRef, DialogTitle, TroveDialog } from '../../shared/ui/dialog';
import { firstValueFrom } from 'rxjs';
import { newIdempotencyKey } from '../../api/client';
import { ApiError } from '../../api/problem';
import { CreateTokenRequest, Token, TokenCreated, TokensApi } from '../../api/tokens-api';
import { Notifier } from '../../core/notifier';
import { SessionStore } from '../../core/session-store';
import { Confirm } from '../../shared/confirm-dialog';
import { DraftStore } from '../../shared/drafts';
import { SelectField } from '../../shared/access/fields';
import { PagedResource, asApiError } from '../../shared/paged-resource';
import { TokenDraft, TokenForm } from '../../shared/access/token-form';
import { TokenSecret } from '../../shared/access/token-secret';
import { TokenTable } from '../../shared/access/token-table';
import { ErrorNotice } from '../../shared/error-notice';
import { Disclosure } from '../../shared/ui/disclosure';

/** Whose tokens the manager shows and what it may do with them. */
export type TokenOwnerScope =
  | { readonly kind: 'own' }
  | { readonly kind: 'user'; readonly name: string }
  | {
      readonly kind: 'service-account';
      readonly name: string;
      /** The key of the account's project (R-AUTHZ-026): its Owners issue and revoke the tokens. */
      readonly project?: string | null;
    };

interface RotateChoice {
  readonly overlapSeconds: number;
}

/** Asks how long the old value of a rotated token stays valid (R-AUTH-030). */
@Component({
  selector: 'trove-rotate-dialog',
  imports: [DialogTitle, DialogClose, SelectField],
  template: `
    <h2 troveDialogTitle i18n="@@rotate.title">Rotate this token?</h2>
    <div class="dialog-body">
      <p i18n="@@rotate.text">
        A new token with the same scope is created and shown once. Everything that uses the old token must switch to it.
      </p>
      <trove-select-field
        i18n-label="@@rotate.overlap"
        label="The old token stays valid for"
        [options]="options"
        [(value)]="overlap"
      />
    </div>
    <div class="dialog-actions">
      <button class="btn btn-tertiary" type="button" troveDialogClose i18n="@@rotate.cancel">Cancel</button>
      <button class="btn btn-confirm" type="button" (click)="confirm()" i18n="@@rotate.confirm">Rotate</button>
    </div>
  `,
})
export class RotateDialog {
  private readonly ref = inject<DialogRef<RotateChoice, RotateDialog>>(DialogRef);
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
 * (R-UI-029, R-UI-026), revoke one or all with confirmation, rotate. Revoking all is destructive, so it is a danger button last,
 * in a closed section **Advanced** (ui-style-guide.md, Settings pages): the manager's own at its end, or the page's own where the
 * page has one (`revokeAllHere` false, `offersRevokeAll`, `revokeAllTokens`). A created or rotated value is
 * shown once through `<trove-token-secret>` and lives in this component's signal only (R-UI-043);
 * it is dropped when the panel is dismissed or the component is destroyed. Writes use the
 * idempotency key of the open form (R-UI-042); a retried creation that answers
 * `token_already_issued` revokes the token whose value was lost and creates again with a new key.
 */
@Component({
  selector: 'trove-token-manager',
  imports: [Checkbox, Disclosure, ErrorNotice, TokenForm, TokenSecret, TokenTable],
  template: `
    <div class="toolbar">
      @if (createInToolbar() && offersCreate()) {
        <button class="btn btn-confirm" type="button" (click)="openForm()" i18n="@@tokens.create">Create token</button>
      }
      <trove-checkbox [checked]="showAll()" (checkedChange)="setShowAll($event)">
        <ng-container i18n="@@tokens.show-all">Show revoked and expired tokens</ng-container>
      </trove-checkbox>
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
          [allowNonExpiring]="allowNonExpiring()"
          [lifetimeCapped]="projectIssues() && !allowNonExpiring()"
          [tokenType]="scope().kind === 'service-account' ? 'sat' : 'pat'"
          [offerAdmin]="offerAdmin()"
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
        [emptyText]="emptyText()"
        [canRotate]="canRotate()"
        [canRevoke]="canRevoke()"
        [hasMore]="list.hasMore()"
        [loading]="list.loading()"
        (more)="list.more()"
        (revoke)="revoke($event)"
        (rotate)="rotate($event)"
      />
    } @else if (list.loading()) {
      <p role="status" i18n="@@tokens.loading">Loading tokens.</p>
    }

    @if (revokeAllHere() && offersRevokeAll()) {
      <trove-disclosure i18n-heading="@@tokens.advanced" heading="Advanced" i18n-hint="@@tokens.advanced-hint" hint="Revoke every token at once.">
        <p class="muted" i18n="@@tokens.revoke-all-note">Every token stops working at once, and the tools that use them are refused. This cannot be undone.</p>
        <p>
          <button class="btn btn-danger" type="button" (click)="revokeAllTokens()" i18n="@@tokens.revoke-all">Revoke all tokens</button>
        </p>
      </trove-disclosure>
    }
  `,
  styleUrl: '../../shared/access/access.scss',
})
export class TokenManager {
  readonly scope = input.required<TokenOwnerScope>();
  /** False when the page's header holds **Create token** (`offersCreate`, `openForm`). */
  readonly createInToolbar = input(true);
  /** For a service account: whether it holds the administrator role now (`ServiceAccount.isAdmin`, Q-AUTH-209). */
  readonly accountIsAdmin = input(false);
  /**
   * True: **Revoke all tokens** is in the manager's own closed section Advanced at its end (the profile). False: the page puts it
   * into its own section Advanced, last on the page, with `offersRevokeAll` and `revokeAllTokens` (a user, a service account).
   */
  readonly revokeAllHere = input(true);

  private readonly api = inject(TokensApi);
  protected readonly session = inject(SessionStore);
  private readonly confirmer = inject(Confirm);
  private readonly notifier = inject(Notifier);
  private readonly drafts = inject(DraftStore);
  private readonly dialog = inject(TroveDialog);

  protected readonly list = new PagedResource<Token>();
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

  /**
   * Administrator rights are offered to a caller who holds the role, for their own token and for the token of an instance-wide
   * service account that holds the role itself: for any other account the choice would take effect nowhere (Q-UI-220).
   */
  protected readonly offerAdmin = computed(() => {
    if (!this.session.hasRole('admin')) return false;
    const s = this.scope();
    return s.kind === 'own' || (s.kind === 'service-account' && !s.project && this.accountIsAdmin());
  });

  protected readonly owner = computed(() => {
    const s = this.scope();
    return s.kind === 'own' ? 'me' : s.kind === 'user' ? s.name : s.project ? `sa:${s.project}/${s.name}` : `sa:${s.name}`;
  });
  /**
   * The server lets the administrator role create and rotate the tokens of a service account (or a
   * token of the account itself, which is no screen); `identities:write` is not enough (R-AUTH-027,
   * R-AUTH-030), so a security administrator would get 403.
   */
  /** Whether **Create token** is offered now: the caller may create and the form is closed. */
  protected readonly offersCreate = computed(() => this.canCreate() && !this.formOpen());
  /** Whether the form is open; a page whose header holds **Create token** hides it meanwhile. */
  readonly isFormOpen = this.formOpen.asReadonly();
  /** The empty list says what is missing; nothing is filtered but the revoked and expired tokens (UI-A-20, UI-A-25). */
  protected readonly emptyText = computed(() =>
    this.showAll()
      ? $localize`:@@tokens.empty-all:No tokens yet.`
      : $localize`:@@tokens.empty-active:No active tokens. Revoked and expired ones are shown with the switch above.`,
  );
  protected readonly canCreate = computed(() => {
    const s = this.scope();
    return s.kind === 'own' || (s.kind === 'service-account' && (this.session.hasRole('admin') || this.projectIssues()));
  });
  /**
   * An Owner of the account's project issues its tokens (R-AUTHZ-026, Q-AUTHZ-177); the server caps the lifetime,
   * forbids "never expires" and any `system` scope for them.
   */
  protected readonly projectIssues = computed(() => {
    const s = this.scope();
    return s.kind === 'service-account' && this.session.isOwnerOf(s.project);
  });
  /** A non-expiring token is for the administrator role only; an Owner's tokens always expire. */
  protected readonly allowNonExpiring = computed(
    () => this.scope().kind === 'service-account' && this.session.hasRole('admin'),
  );
  protected readonly canRotate = computed(() => {
    const s = this.scope();
    return s.kind === 'own' || (s.kind === 'service-account' && this.session.hasRole('admin'));
  });
  /** `tokens:revoke`, or the Owner role in the account's project (Q-AUTHZ-177). */
  protected readonly canRevoke = computed(() => {
    const s = this.scope();
    return (
      s.kind === 'own' ||
      this.session.has('tokens:revoke') ||
      (s.kind === 'service-account' && this.session.isOwnerOf(s.project))
    );
  });
  protected readonly canRevokeAll = computed(() => this.canRevoke());
  private readonly held = signal(false);
  /**
   * Whether a page opens its section Tokens (Q-UI-333): the list has held a token since the owner's tokens were first read. It
   * does not turn false again for the same owner, so revoking the last token, or a reload for the revoked and expired ones,
   * leaves the section open with its empty line instead of folding it away under the reader. A page binds it one way, so a
   * reader who closes the section keeps it closed. Set by effects only: a page reads it before `scope` is bound.
   */
  readonly heldTokens = this.held.asReadonly();
  /**
   * Whether **Revoke all tokens** is offered: the owner has a token in the list, and the caller may. The page reads it in its own
   * template, which can run before `scope` is bound (the section Advanced of a user without other actions, such as the
   * break-glass account): the list is checked first, and it holds a token only after it was read for a bound `scope`.
   */
  readonly offersRevokeAll = computed(() => this.list.items().length > 0 && this.canRevokeAll());

  constructor() {
    inject(DestroyRef).onDestroy(() => this.secret.set(null));
    effect(() => {
      this.owner();
      this.showAll();
      untracked(() => void this.reload());
    });
    // Another owner on the same page starts closed again; its own tokens open the section.
    effect(() => {
      this.owner();
      this.held.set(false);
    });
    effect(() => {
      if (this.list.items().length > 0) this.held.set(true);
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

  openForm(): void {
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
      const error = asApiError(e);
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
        ? this.api.createForServiceAccount(s.name, s.project ?? null, request, key)
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
      lines: [$localize`:@@tokens.revoke-text:“${token.description}:description:” stops working at once on every replica. This cannot be undone.`],
      confirmLabel: $localize`:@@tokens.revoke-confirm:Revoke`,
    });
    if (answer === null) return;
    await this.act(async () => {
      await this.api.revoke(token.id);
      this.notifier.status($localize`:@@tokens.revoked:Token revoked.`);
    });
  }

  async revokeAllTokens(): Promise<void> {
    const answer = await this.confirmer.ask({
      title: $localize`:@@tokens.revoke-all-title:Revoke all tokens?`,
      lines: [$localize`:@@tokens.revoke-all-text:Every token of this owner stops working at once. This cannot be undone.`],
      confirmLabel: $localize`:@@tokens.revoke-all-confirm:Revoke all`,
    });
    if (answer === null) return;
    const s = this.scope();
    await this.act(async () => {
      if (s.kind === 'own') await this.api.revokeAllOwn();
      else if (s.kind === 'user') await this.api.revokeAllOfUser(s.name);
      else await this.api.revokeAllOfServiceAccount(s.name, s.project ?? null);
      this.notifier.status($localize`:@@tokens.revoked-all:All tokens revoked.`);
    });
  }

  protected async rotate(token: Token): Promise<void> {
    const ref = this.dialog.open<RotateDialog, undefined, RotateChoice>(RotateDialog, { maxWidth: '32rem' });
    const choice = await firstValueFrom(ref.closed);
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
      this.actionError.set(asApiError(e));
    }
  }
}
