import { Component, inject, signal } from '@angular/core';
import { DIALOG_DATA, DialogClose, DialogRef, DialogTitle } from '../../shared/ui/dialog';
import { newIdempotencyKey } from '../../api/client';
import { ApiError } from '../../api/problem';
import { Bastion, BASTION_NAME_PATTERN, BastionsApi, BastionTokenCreated, NewBastionToken } from '../../api/bastions-api';
import { Notifier } from '../../core/notifier';
import { DraftStore } from '../../shared/drafts';
import { TextField } from '../../shared/access/fields';
import { fromDays } from '../../shared/access/duration';
import { fieldErrorAt } from '../../shared/field-errors';
import { ErrorNotice } from '../../shared/error-notice';
import { asApiError } from '../../shared/paged-resource';

/**
 * "New Bastion" (R-BAS-007, R-BAS-004). The idempotency key lives as long as the dialog (R-UI-042).
 * The name rule is shown while typing; the server decides.
 */
@Component({
  selector: 'trove-bastion-create-dialog',
  imports: [DialogTitle, DialogClose, ErrorNotice, TextField],
  template: `
    <h2 troveDialogTitle i18n="@@bastion-create.title">New Bastion</h2>
    <form (submit)="submit($event)" novalidate>
      <div class="dialog-body">
        @if (error(); as e) {
          <trove-error-notice [error]="e" />
        }
        <trove-text-field
          i18n-label="@@bastion-create.name"
          label="Name"
          name="name"
          [(value)]="name"
          [required]="true"
          [error]="nameError()"
          [hint]="nameHint"
        />
        <trove-text-field
          i18n-label="@@bastion-create.description"
          label="Description"
          name="description"
          [(value)]="description"
          [error]="fieldError('/description')"
        />
      </div>
      <div class="dialog-actions">
        <button class="btn btn-tertiary" type="button" troveDialogClose i18n="@@bastion-create.cancel">Cancel</button>
        <button class="btn btn-confirm" type="submit" [disabled]="busy()" i18n="@@bastion-create.submit">Create Bastion</button>
      </div>
    </form>
  `,
  styleUrl: '../../shared/access/access.scss',
})
export class BastionCreateDialog {
  private readonly api = inject(BastionsApi);
  private readonly ref = inject<DialogRef<Bastion, BastionCreateDialog>>(DialogRef);
  private readonly notifier = inject(Notifier);
  private readonly key = newIdempotencyKey();

  protected readonly nameHint = $localize`:@@bastion-create.name-hint:Lower-case letters, digits and -, starting with a letter, one to 20 characters. It cannot be changed later.`;
  protected readonly name = signal('');
  protected readonly description = signal('');
  protected readonly busy = signal(false);
  protected readonly error = signal<ApiError | null>(null);

  protected nameError(): string | null {
    const typed = this.name().trim();
    if (typed !== '' && !BASTION_NAME_PATTERN.test(typed)) {
      return $localize`:@@bastion-create.name-invalid:The name must start with a lower-case letter and hold lower-case letters, digits and - only, at most 20 characters.`;
    }
    return fieldErrorAt(this.error(), '/name');
  }

  protected fieldError(pointer: string): string | null {
    return fieldErrorAt(this.error(), pointer);
  }

  protected async submit(event: Event): Promise<void> {
    event.preventDefault();
    if (this.busy()) return;
    this.busy.set(true);
    this.error.set(null);
    try {
      const created = await this.api.create({ name: this.name().trim(), description: this.description().trim() }, this.key);
      this.notifier.status($localize`:@@bastion-create.created:Bastion created. Issue a token for it next.`);
      this.ref.close(created);
    } catch (e) {
      this.error.set(asApiError(e));
    } finally {
      this.busy.set(false);
    }
  }
}

/**
 * The non-secret entries of the token dialog, kept across a re-authentication (R-API-007, R-UI-042): the
 * registration they are for, the description and the lifetime as typed. Never a token value (R-UI-043).
 */
export interface BastionTokenDraft {
  readonly bastion: string;
  readonly description: string;
  readonly days: string;
}

/** What the token dialog is opened with: the registration the token is for, and a draft to fill it with. */
export interface BastionTokenDialogData {
  readonly name: string;
  readonly draft?: BastionTokenDraft;
}

/**
 * "Issue token" for one registration (R-BAS-004, R-BAS-006). The lifetime is a number of days; empty
 * takes the installation's default (`auth.tokens.bst.default_ttl`). The secret comes back to the card,
 * which shows it once (R-UI-043). A repeated request whose first answer was lost answers
 * `token_already_issued`: the token whose value was lost is revoked and the request is made again with a
 * new key, like the token screens do (R-UI-042). A session whose sign-in is not recent answers
 * `reauthentication_required` (R-API-007): the entries are kept as a draft of the route, and the screen
 * opens the dialog with them again after the sign-in, as the token screens do.
 */
@Component({
  selector: 'trove-bastion-token-dialog',
  imports: [DialogTitle, DialogClose, ErrorNotice, TextField],
  template: `
    <h2 troveDialogTitle i18n="@@bastion-token.title">Issue a token for the Bastion</h2>
    <form (submit)="submit($event)" novalidate>
      <div class="dialog-body">
        <p class="subject" translate="no">{{ data.name }}</p>
        <p i18n="@@bastion-token.text">
          The token goes into the Secret of the Bastion's chart. It is shown once, when you create it.
        </p>
        @if (error(); as e) {
          <trove-error-notice [error]="e" />
        }
        <trove-text-field
          i18n-label="@@bastion-token.description"
          label="Description"
          name="description"
          [(value)]="description"
          [error]="fieldError('/description')"
          [hint]="descriptionHint"
        />
        <trove-text-field
          i18n-label="@@bastion-token.days"
          label="Lifetime in days"
          name="days"
          type="number"
          [(value)]="days"
          [error]="daysError()"
          [hint]="daysHint"
        />
      </div>
      <div class="dialog-actions">
        <button class="btn btn-tertiary" type="button" troveDialogClose i18n="@@bastion-token.cancel">Cancel</button>
        <button class="btn btn-confirm" type="submit" [disabled]="busy()" i18n="@@bastion-token.submit">Issue token</button>
      </div>
    </form>
  `,
  styleUrl: '../../shared/access/access.scss',
})
export class BastionTokenDialog {
  protected readonly data = inject<BastionTokenDialogData>(DIALOG_DATA);
  private readonly api = inject(BastionsApi);
  private readonly ref = inject<DialogRef<BastionTokenCreated, BastionTokenDialog>>(DialogRef);
  private readonly drafts = inject(DraftStore);
  private key = newIdempotencyKey();

  protected readonly descriptionHint = $localize`:@@bastion-token.description-hint:For the renewal: say which year or rollout the token is for.`;
  protected readonly daysHint = $localize`:@@bastion-token.days-hint:Leave empty for the installation's default. A token always expires.`;
  protected readonly description = signal(this.data.draft?.description ?? '');
  protected readonly days = signal(this.data.draft?.days ?? '');
  protected readonly busy = signal(false);
  protected readonly error = signal<ApiError | null>(null);
  private readonly localDaysError = signal<string | null>(null);

  protected daysError(): string | null {
    return this.localDaysError() ?? fieldErrorAt(this.error(), '/ttlSeconds');
  }

  protected fieldError(pointer: string): string | null {
    return fieldErrorAt(this.error(), pointer);
  }

  private request(): NewBastionToken | null {
    this.localDaysError.set(null);
    const body: { description: string; ttlSeconds?: number } = { description: this.description().trim() };
    if (this.days().trim() !== '') {
      const seconds = fromDays(this.days());
      if (!Number.isFinite(seconds) || seconds < 1) {
        this.localDaysError.set($localize`:@@bastion-token.days-invalid:Enter a number of days greater than zero.`);
        return null;
      }
      body.ttlSeconds = seconds;
    }
    return body;
  }

  protected async submit(event: Event): Promise<void> {
    event.preventDefault();
    const body = this.request();
    if (!body || this.busy()) return;
    this.busy.set(true);
    this.error.set(null);
    try {
      this.ref.close(await this.createOnce(body));
    } catch (e) {
      const error = asApiError(e);
      this.error.set(error);
      if (error.code === 'reauthentication_required') {
        this.drafts.keep({ bastion: this.data.name, description: this.description(), days: this.days() } satisfies BastionTokenDraft);
      }
    } finally {
      this.busy.set(false);
    }
  }

  private async createOnce(body: NewBastionToken): Promise<BastionTokenCreated> {
    try {
      return await this.api.createToken(this.data.name, body, this.key);
    } catch (e) {
      const lost = e instanceof ApiError && e.code === 'token_already_issued' ? e.problem?.['tokenId'] : null;
      if (typeof lost !== 'string') throw e;
      await this.api.revokeToken(this.data.name, lost);
      this.key = newIdempotencyKey();
      return this.api.createToken(this.data.name, body, this.key);
    }
  }
}
