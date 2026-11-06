import { Component, inject, signal } from '@angular/core';
import { Checkbox } from '../../shared/ui/checkbox';
import { DialogClose, DialogRef, DialogTitle } from '../../shared/ui/dialog';
import { newIdempotencyKey } from '../../api/client';
import { Group, IdentityApi } from '../../api/identity-api';
import { ApiError } from '../../api/problem';
import { Notifier } from '../../core/notifier';
import { TextField } from '../../shared/access/fields';
import { fieldErrorAt } from '../../shared/field-errors';
import { asApiError } from '../../shared/paged-resource';
import { ErrorNotice } from '../../shared/error-notice';

/** "New group" (R-UI-025). The idempotency key lives as long as the dialog (R-UI-042). */
@Component({
  selector: 'trove-group-create-dialog',
  imports: [Checkbox, DialogTitle, DialogClose, ErrorNotice, TextField],
  template: `
    <h2 troveDialogTitle i18n="@@group-create.title">New group</h2>
    <form (submit)="submit($event)" novalidate>
      <div class="dialog-body">
        @if (error(); as e) {
          <trove-error-notice [error]="e" />
        }
        <trove-text-field
          i18n-label="@@group-create.name"
          label="Name"
          name="name"
          [(value)]="name"
          [required]="true"
          [error]="fieldError('/name')"
          i18n-hint="@@group-create.name-hint"
          hint="Lower-case letters, digits and . _ -"
        />
        <trove-text-field i18n-label="@@group-create.description" label="Description" name="description" [(value)]="description" [error]="fieldError('/description')" />
        <trove-checkbox [checked]="autoJoin()" (checkedChange)="autoJoin.set($event)">
          <ng-container i18n="@@group-create.auto-join">Every user joins this group automatically</ng-container>
        </trove-checkbox>
      </div>
      <div class="dialog-actions">
        <button class="btn btn-tertiary" type="button" troveDialogClose i18n="@@group-create.cancel">Cancel</button>
        <button class="btn btn-confirm" type="submit" [disabled]="busy()" i18n="@@group-create.submit">Create group</button>
      </div>
    </form>
  `,
  styleUrl: '../../shared/access/access.scss',
})
export class GroupCreateDialog {
  private readonly api = inject(IdentityApi);
  private readonly ref = inject<DialogRef<Group, GroupCreateDialog>>(DialogRef);
  private readonly notifier = inject(Notifier);
  private readonly key = newIdempotencyKey();

  protected readonly name = signal('');
  protected readonly description = signal('');
  protected readonly autoJoin = signal(false);
  protected readonly busy = signal(false);
  protected readonly error = signal<ApiError | null>(null);

  protected fieldError(pointer: string): string | null {
    return fieldErrorAt(this.error(), pointer);
  }

  protected async submit(event: Event): Promise<void> {
    event.preventDefault();
    this.busy.set(true);
    this.error.set(null);
    try {
      const created = await this.api.createGroup(
        { name: this.name().trim(), description: this.description().trim(), autoJoin: this.autoJoin() },
        this.key,
      );
      this.notifier.status($localize`:@@group-create.created:Group created.`);
      this.ref.close(created);
    } catch (e) {
      this.error.set(asApiError(e));
    } finally {
      this.busy.set(false);
    }
  }
}
