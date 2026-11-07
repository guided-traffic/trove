import { Component, computed, effect, inject, input, output, signal, untracked } from '@angular/core';
import { ApiError } from '../../api/problem';
import { PROJECT_ROLES, ProjectMember, ProjectRole, ProjectsApi } from '../../api/projects-api';
import { SessionStore } from '../../core/session-store';
import { Notifier } from '../../core/notifier';
import { SelectField, SelectOption } from '../../shared/access/fields';
import { ErrorNotice } from '../../shared/error-notice';
import { asApiError } from '../../shared/paged-resource';
import { ProjectNeeds } from './project-access';
import { PickablePrincipal, PrincipalPicker } from './principal-picker';
import { projectRoleHint, projectRoleLabel } from './project-roles';

/**
 * The members of a project with their roles (R-UI-041, R-AUTHZ-026, Q-AUTHZ-207): the list, adding a user or group
 * with a role, changing a role and removing a member. The Owner of the project and the holder of `projects:write`
 * change it; every other member reads. Removing or demoting the last Owner is refused by the server (409
 * `last_owner`, Q-AUTHZ-217) and the refusal is shown here; an administrator of trove may leave a project without an
 * Owner, so the UI does not hide the action. The API decides in any case (R-UI-005).
 */
@Component({
  selector: 'trove-project-members',
  imports: [ErrorNotice, PrincipalPicker, ProjectNeeds, SelectField],
  template: `
    @if (error(); as e) {
      @if (e.code === 'last_owner') {
        <p class="notice" role="alert">
          <span class="material-icons" aria-hidden="true">error</span>
          <span i18n="@@project-members.last-owner">A project keeps at least one Owner. Make another member an Owner first.</span>
        </p>
      } @else {
        <trove-error-notice [error]="e" (retry)="load()" />
      }
    }

    @if (members().length === 0 && loaded()) {
      <p class="muted" i18n="@@project-members.empty">The project has no member yet.</p>
    } @else if (members().length > 0) {
      <table class="members" [attr.aria-label]="caption">
        <thead>
          <tr>
            <th scope="col" i18n="@@project-members.col.member">Member</th>
            <th scope="col" i18n="@@project-members.col.role">Role</th>
            <th scope="col"><span class="visually-hidden" i18n="@@project-members.col.actions">Actions</span></th>
          </tr>
        </thead>
        <tbody>
          @for (m of members(); track m.principal) {
            <tr>
              <th scope="row" translate="no">{{ m.principal }}</th>
              <td>
                @if (canWrite()) {
                  <trove-select-field
                    [label]="roleOf(m)"
                    [name]="'role-' + m.principal"
                    [options]="roleOptions"
                    [value]="m.role"
                    (valueChange)="change(m, $event)"
                  />
                } @else {
                  {{ roleText(m.role) }}
                }
              </td>
              <td class="actions">
                @if (canWrite()) {
                  <button class="btn btn-tertiary" type="button" [disabled]="busy()" (click)="remove(m)">
                    <ng-container i18n="@@project-members.remove">Remove</ng-container>
                    <span class="visually-hidden"> {{ m.principal }}</span>
                  </button>
                }
              </td>
            </tr>
          }
        </tbody>
      </table>
    }

    @if (canWrite()) {
      <fieldset class="add">
        <legend i18n="@@project-members.add">Add a member</legend>
        <trove-principal-picker
          i18n-label="@@project-members.find"
          label="Find a user or group"
          name="member-search"
          [search]="search"
          [exclude]="refs()"
          (picked)="chosen.set($event)"
        />
        @if (chosen(); as c) {
          <p class="chosen">
            <span translate="no">{{ c }}</span>
          </p>
          <trove-select-field
            i18n-label="@@project-members.role"
            label="Role"
            name="new-role"
            [options]="roleOptions"
            [value]="newRole()"
            (valueChange)="newRole.set($any($event))"
          />
          <p class="muted">{{ hint(newRole()) }}</p>
          <button class="btn btn-confirm" type="button" [disabled]="busy()" (click)="add(c)" i18n="@@project-members.add-confirm">
            Add member
          </button>
        }
      </fieldset>
    } @else {
      <trove-project-needs capability="projects:write" [state]="'denied'" />
    }
  `,
  styleUrl: '../../shared/access/access.scss',
  styles: `
    .members {
      inline-size: 100%;
      border-collapse: collapse;
      margin-block-end: var(--trove-space-3);
    }
    .members th,
    .members td {
      padding: var(--trove-space-2);
      text-align: start;
      border-block-end: 1px solid var(--trove-border);
    }
    .add {
      margin: 0;
      padding: var(--trove-space-2) var(--trove-space-3);
      border: 1px solid var(--trove-border);
      border-radius: var(--trove-radius-m);
    }
    .notice {
      display: flex;
      gap: var(--trove-space-3);
      padding: var(--trove-space-3) var(--trove-space-4);
      border: 1px solid var(--trove-error);
      border-radius: var(--trove-radius-m);
      background: var(--trove-error-container);
      color: var(--trove-on-error-container);
    }
  `,
})
export class ProjectMembers {
  readonly project = input.required<string>();
  /** Emitted after a write so that the counts of the project screen are read again. */
  readonly changed = output<void>();

  private readonly api = inject(ProjectsApi);
  private readonly session = inject(SessionStore);
  private readonly notifier = inject(Notifier);

  protected readonly members = signal<readonly ProjectMember[]>([]);
  protected readonly loaded = signal(false);
  protected readonly busy = signal(false);
  protected readonly error = signal<ApiError | null>(null);
  protected readonly chosen = signal<string | null>(null);
  protected readonly newRole = signal<ProjectRole>('developer');
  protected readonly refs = computed(() => this.members().map((m) => m.principal));
  protected readonly canWrite = computed(
    () => this.session.has('projects:write') || this.session.isOwnerOf(this.project()),
  );
  protected readonly caption = $localize`:@@project-members.caption:Members of the project`;
  protected readonly roleOptions: readonly SelectOption[] = PROJECT_ROLES.map((r) => ({
    value: r,
    label: projectRoleLabel(r),
  }));

  /** The users and groups whose name starts with the text (Q-AUTHZ-182). */
  protected readonly search = async (text: string): Promise<readonly PickablePrincipal[]> => {
    const found = await this.api.lookupPrincipals(this.project(), text);
    return found.map((p): PickablePrincipal => ({ ref: p.principal, kind: p.kind }));
  };

  constructor() {
    effect(() => {
      this.project();
      untracked(() => void this.load());
    });
  }

  protected roleOf(m: ProjectMember): string {
    return $localize`:@@project-members.role-of:Role of ${m.principal}:member:`;
  }

  protected roleText(role: ProjectRole): string {
    return projectRoleLabel(role);
  }

  protected hint(role: ProjectRole): string {
    return projectRoleHint(role);
  }

  protected async load(): Promise<void> {
    this.error.set(null);
    try {
      this.members.set(await this.api.members(this.project()));
    } catch (e) {
      this.error.set(asApiError(e));
    } finally {
      this.loaded.set(true);
    }
  }

  protected async add(principal: string): Promise<void> {
    await this.write(async () => {
      await this.api.setMember(this.project(), principal, this.newRole());
      this.chosen.set(null);
      this.notifier.status($localize`:@@project-members.added:Member added.`);
    });
  }

  protected async change(m: ProjectMember, role: string): Promise<void> {
    if (role === m.role) return;
    await this.write(async () => {
      await this.api.setMember(this.project(), m.principal, role as ProjectRole);
      this.notifier.status($localize`:@@project-members.changed:Role changed.`);
    });
  }

  protected async remove(m: ProjectMember): Promise<void> {
    await this.write(async () => {
      await this.api.removeMember(this.project(), m.principal);
      this.notifier.status($localize`:@@project-members.removed:Member removed.`);
    });
  }

  private async write(step: () => Promise<void>): Promise<void> {
    this.busy.set(true);
    this.error.set(null);
    let failure: ApiError | null = null;
    try {
      await step();
      this.changed.emit();
    } catch (e) {
      failure = asApiError(e);
    }
    await this.load();
    this.error.set(failure);
    this.busy.set(false);
  }
}
