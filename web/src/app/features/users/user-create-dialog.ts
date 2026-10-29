import { Component, inject, signal } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MatDialogModule, MatDialogRef } from '@angular/material/dialog';
import { newIdempotencyKey } from '../../api/client';
import { IdentityApi, NewUser, User } from '../../api/identity-api';
import { LoginApi } from '../../api/login-api';
import { ApiError } from '../../api/problem';
import { Notifier } from '../../core/notifier';
import { SelectField, SelectOption, TextField } from '../../shared/access/fields';
import { fieldErrorAt } from '../../shared/access/field-problem';
import { toApiError } from '../../shared/access/paged-signal';
import { ErrorNotice } from '../../shared/error-notice';

/**
 * "Add user" (R-UI-024, R-AUTH-016): pre-provisions a user and optionally links the identity of a
 * configured provider; the first login at that provider completes the link. No password exists.
 * The idempotency key is generated when the dialog opens and kept until it closes (R-UI-042).
 */
@Component({
  selector: 'trove-user-create-dialog',
  imports: [MatButtonModule, MatDialogModule, ErrorNotice, SelectField, TextField],
  template: `
    <h2 mat-dialog-title i18n="@@user-create.title">Add user</h2>
    <form (submit)="submit($event)" novalidate>
      <mat-dialog-content>
        @if (error(); as e) {
          <trove-error-notice [error]="e" />
        }
        <trove-text-field
          i18n-label="@@user-create.username"
          label="User name"
          name="username"
          [(value)]="username"
          [required]="true"
          [error]="fieldError('/username')"
          i18n-hint="@@user-create.username-hint"
          hint="Lower-case letters, digits and . _ @ + -"
        />
        <trove-text-field i18n-label="@@user-create.display-name" label="Display name" name="displayName" [(value)]="displayName" [error]="fieldError('/displayName')" />
        <trove-text-field i18n-label="@@user-create.email" label="Email" name="email" type="email" [(value)]="email" [error]="fieldError('/email')" />
        @if (providers().length > 1) {
          <trove-select-field
            i18n-label="@@user-create.provider"
            label="Link to the identity provider"
            name="provider"
            [options]="providers()"
            [(value)]="provider"
            [error]="fieldError('/identities')"
          />
        }
      </mat-dialog-content>
      <mat-dialog-actions align="end">
        <button mat-button type="button" mat-dialog-close i18n="@@user-create.cancel">Cancel</button>
        <button mat-flat-button type="submit" [disabled]="busy()" i18n="@@user-create.submit">Add user</button>
      </mat-dialog-actions>
    </form>
  `,
  styleUrl: '../../shared/access/access.scss',
})
export class UserCreateDialog {
  private readonly api = inject(IdentityApi);
  private readonly login = inject(LoginApi);
  private readonly ref = inject<MatDialogRef<UserCreateDialog, User>>(MatDialogRef);
  private readonly notifier = inject(Notifier);
  private readonly key = newIdempotencyKey();

  protected readonly username = signal('');
  protected readonly displayName = signal('');
  protected readonly email = signal('');
  protected readonly provider = signal('');
  protected readonly providers = signal<readonly SelectOption[]>([]);
  protected readonly busy = signal(false);
  protected readonly error = signal<ApiError | null>(null);

  constructor() {
    void this.loadProviders();
  }

  private async loadProviders(): Promise<void> {
    try {
      const options = await this.login.options();
      this.providers.set([
        { value: '', label: $localize`:@@user-create.no-provider:No link yet` },
        ...options.providers.map((p) => ({ value: p.id, label: p.displayName })),
      ]);
    } catch {
      // Without the list the user is created unlinked; the link can be added by editing later.
    }
  }

  protected fieldError(pointer: string): string | null {
    return fieldErrorAt(this.error(), pointer);
  }

  protected async submit(event: Event): Promise<void> {
    event.preventDefault();
    this.busy.set(true);
    this.error.set(null);
    const body: NewUser = {
      username: this.username().trim(),
      ...(this.email().trim() ? { email: this.email().trim() } : {}),
      ...(this.displayName().trim() ? { displayName: this.displayName().trim() } : {}),
      ...(this.provider() ? { identities: [{ provider: this.provider() }] } : {}),
    };
    try {
      const created = await this.api.createUser(body, this.key);
      this.notifier.status($localize`:@@user-create.created:User added.`);
      this.ref.close(created);
    } catch (e) {
      this.error.set(toApiError(e));
    } finally {
      this.busy.set(false);
    }
  }
}
