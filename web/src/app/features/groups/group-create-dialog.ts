import { Component, inject, signal } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MatCheckboxModule } from '@angular/material/checkbox';
import { MatDialogModule, MatDialogRef } from '@angular/material/dialog';
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
  imports: [MatButtonModule, MatCheckboxModule, MatDialogModule, ErrorNotice, TextField],
  template: `
    <h2 mat-dialog-title i18n="@@group-create.title">New group</h2>
    <form (submit)="submit($event)" novalidate>
      <mat-dialog-content>
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
        <mat-checkbox [checked]="autoJoin()" (change)="autoJoin.set($event.checked)">
          <ng-container i18n="@@group-create.auto-join">Every user joins this group automatically</ng-container>
        </mat-checkbox>
      </mat-dialog-content>
      <mat-dialog-actions align="end">
        <button mat-button type="button" mat-dialog-close i18n="@@group-create.cancel">Cancel</button>
        <button mat-flat-button type="submit" [disabled]="busy()" i18n="@@group-create.submit">Create group</button>
      </mat-dialog-actions>
    </form>
  `,
  styleUrl: '../../shared/access/access.scss',
})
export class GroupCreateDialog {
  private readonly api = inject(IdentityApi);
  private readonly ref = inject<MatDialogRef<GroupCreateDialog, Group>>(MatDialogRef);
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
