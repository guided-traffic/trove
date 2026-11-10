import { Component, inject, signal } from '@angular/core';
import { DIALOG_DATA, DialogClose, DialogRef, DialogTitle } from '../../shared/ui/dialog';
import { PROJECT_ROLES, ProjectRole } from '../../api/projects-api';
import { SelectField, SelectOption } from '../../shared/access/fields';
import { PickablePrincipal, PrincipalPicker } from './principal-picker';
import { projectRoleHint, projectRoleLabel } from './project-roles';

export interface ProjectMemberDialogData {
  /** The users and groups whose name starts with the text. */
  readonly search: (text: string) => Promise<readonly PickablePrincipal[]>;
  /** The references that are members already. */
  readonly exclude: readonly string[];
}

export interface ProjectMemberChoice {
  readonly principal: string;
  readonly role: ProjectRole;
}

/** Adds a member to a project (R-UI-041, Q-UI-214): the user or group through the principal lookup and its role. */
@Component({
  selector: 'trove-project-member-dialog',
  imports: [DialogTitle, DialogClose, PrincipalPicker, SelectField],
  template: `
    <h2 troveDialogTitle i18n="@@project-members.add">Add member</h2>
    <form (submit)="submit($event)" novalidate>
      <div class="dialog-body">
        <trove-principal-picker
          i18n-label="@@project-members.find"
          label="Find a user or group"
          name="member-search"
          [search]="data.search"
          [exclude]="data.exclude"
          (picked)="chosen.set($event)"
        />
        @if (chosen(); as c) {
          <p class="chosen"><span translate="no">{{ c }}</span></p>
          <trove-select-field
            i18n-label="@@project-members.role"
            label="Role"
            name="new-role"
            [options]="roleOptions"
            [value]="role()"
            (valueChange)="role.set($any($event))"
          />
          <p class="muted">{{ hint() }}</p>
        }
      </div>
      <div class="dialog-actions">
        <button class="btn btn-tertiary" type="button" troveDialogClose i18n="@@project-members.cancel">Cancel</button>
        <button class="btn btn-confirm" type="submit" [disabled]="!chosen()" i18n="@@project-members.add-confirm">
          Add member
        </button>
      </div>
    </form>
  `,
  styleUrl: '../../shared/access/access.scss',
})
export class ProjectMemberDialog {
  protected readonly data = inject<ProjectMemberDialogData>(DIALOG_DATA);
  private readonly ref = inject<DialogRef<ProjectMemberChoice>>(DialogRef);

  protected readonly chosen = signal<string | null>(null);
  protected readonly role = signal<ProjectRole>('developer');
  protected readonly roleOptions: readonly SelectOption[] = PROJECT_ROLES.map((r) => ({ value: r, label: projectRoleLabel(r) }));

  protected hint(): string {
    return projectRoleHint(this.role());
  }

  protected submit(event: Event): void {
    event.preventDefault();
    const principal = this.chosen();
    if (principal) this.ref.close({ principal, role: this.role() });
  }
}
