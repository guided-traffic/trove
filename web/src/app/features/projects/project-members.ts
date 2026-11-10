import { CdkMenu, CdkMenuItem, CdkMenuItemRadio, CdkMenuTrigger } from '@angular/cdk/menu';
import { Component, computed, effect, inject, input, output, signal, untracked } from '@angular/core';
import { firstValueFrom } from 'rxjs';
import { ApiError } from '../../api/problem';
import { PROJECT_ROLES, ProjectMember, ProjectRole, ProjectsApi } from '../../api/projects-api';
import { SessionStore } from '../../core/session-store';
import { Notifier } from '../../core/notifier';
import { SelectOption } from '../../shared/access/fields';
import { TroveAvatar } from '../../shared/ui/avatar';
import { TroveDialog } from '../../shared/ui/dialog';
import { ErrorNotice } from '../../shared/error-notice';
import { asApiError } from '../../shared/paged-resource';
import { ProjectNeeds } from './project-access';
import { PickablePrincipal } from './principal-picker';
import { ProjectMemberChoice, ProjectMemberDialog, ProjectMemberDialogData } from './project-member-dialog';
import { projectRoleLabel } from './project-roles';

/**
 * The members of a project with their roles (R-UI-041, R-AUTHZ-026, Q-AUTHZ-207, Q-UI-214): the list, adding a user or group
 * with a role in a dialog, changing a role and removing a member. The Owner of the project and the holder of `projects:write`
 * change it; every other member reads. Removing or demoting the last Owner is refused by the server (409
 * `last_owner`, Q-AUTHZ-217) and the refusal is shown here; an administrator of trove may leave a project without an
 * Owner, so the UI does not hide the action. The API decides in any case (R-UI-005).
 */
@Component({
  selector: 'trove-project-members',
  imports: [CdkMenu, CdkMenuItem, CdkMenuItemRadio, CdkMenuTrigger, ErrorNotice, ProjectNeeds, TroveAvatar],
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

    <div class="tab-nav">
      <span class="tab-link" aria-current="page">
        <ng-container i18n="@@project-members.tab">Members</ng-container>
        <span class="badge count">{{ members().length }}</span>
      </span>
    </div>

    @if (members().length === 0 && loaded()) {
      <p class="muted" i18n="@@project-members.empty">The project has no member yet.</p>
    } @else if (members().length > 0) {
      <div class="toolbar">
        <div class="filter">
          <input
            class="text-input"
            type="search"
            name="member-filter"
            i18n-placeholder="@@project-members.filter"
            placeholder="Filter members"
            i18n-aria-label="@@project-members.filter"
            aria-label="Filter members"
            [value]="filter()"
            (input)="filter.set($any($event.target).value)"
          />
          <span class="material-icons" aria-hidden="true">search</span>
        </div>
        <button class="btn btn-default" type="button" [cdkMenuTriggerFor]="sortMenu">
          {{ sort() === 'role' ? roleLabel : accountLabel }}
          <span class="material-icons" aria-hidden="true">expand_more</span>
        </button>
        <ng-template #sortMenu>
          <div class="menu" cdkMenu>
            <button type="button" class="menu-item" cdkMenuItemRadio [cdkMenuItemChecked]="sort() === 'account'" (cdkMenuItemTriggered)="sort.set('account')">
              <span class="material-icons" aria-hidden="true">{{ sort() === 'account' ? 'check' : '' }}</span>
              {{ accountLabel }}
            </button>
            <button type="button" class="menu-item" cdkMenuItemRadio [cdkMenuItemChecked]="sort() === 'role'" (cdkMenuItemTriggered)="sort.set('role')">
              <span class="material-icons" aria-hidden="true">{{ sort() === 'role' ? 'check' : '' }}</span>
              {{ roleLabel }}
            </button>
          </div>
        </ng-template>
        <button
          class="btn btn-default btn-icon"
          type="button"
          i18n-aria-label="@@project-members.direction"
          aria-label="Reverse the order"
          (click)="descending.set(!descending())"
        >
          <span class="material-icons" aria-hidden="true">{{ descending() ? 'south' : 'north' }}</span>
        </button>
      </div>

      <table class="members" [attr.aria-label]="caption">
        <thead>
          <tr>
            <th scope="col">{{ accountLabel }}</th>
            <th scope="col" i18n="@@project-members.col.source">Source</th>
            <th scope="col">{{ roleLabel }}</th>
            <th scope="col"><span class="visually-hidden" i18n="@@project-members.col.actions">Actions</span></th>
          </tr>
        </thead>
        <tbody>
          @for (m of shown(); track m.principal) {
            <tr>
              <th scope="row" class="account">
                @if (m.kind === 'group') {
                  <span class="avatar group" aria-hidden="true"><span class="material-icons">groups</span></span>
                } @else {
                  <trove-avatar class="avatar" aria-hidden="true" [seed]="m.id ?? m.principal" [imageUrl]="m.avatarUrl" />
                }
                <span class="who">
                  <span class="line">
                    <span class="name" translate="no">{{ m.displayName || m.name }}</span>
                    @if (isMe(m)) {
                      <span class="badge info" i18n="@@project-members.me">It's you</span>
                    }
                  </span>
                  @if (m.kind === 'group') {
                    <span class="muted" i18n="@@project-members.kind-group">Group</span>
                  } @else {
                    <span class="muted" translate="no">&#64;{{ m.name }}</span>
                  }
                </span>
              </th>
              <td i18n="@@project-members.direct">Direct member</td>
              <td>
                @if (canWrite()) {
                  <select
                    class="select-input role"
                    [name]="'role-' + m.principal"
                    [attr.aria-label]="roleOf(m)"
                    [disabled]="busy()"
                    (change)="change(m, $any($event.target).value)"
                  >
                    @for (o of roleOptions; track o.value) {
                      <option [value]="o.value" [selected]="o.value === m.role">{{ o.label }}</option>
                    }
                  </select>
                } @else {
                  {{ roleText(m.role) }}
                }
              </td>
              <td class="actions">
                @if (canWrite()) {
                  <button
                    class="btn btn-tertiary btn-icon"
                    type="button"
                    [attr.aria-label]="actionsOf(m)"
                    [disabled]="busy()"
                    [cdkMenuTriggerFor]="rowMenu"
                  >
                    <span class="material-icons" aria-hidden="true">more_vert</span>
                  </button>
                  <ng-template #rowMenu>
                    <div class="menu" cdkMenu>
                      <button type="button" class="menu-item" cdkMenuItem (cdkMenuItemTriggered)="remove(m)" i18n="@@project-members.remove">
                        Remove member
                      </button>
                    </div>
                  </ng-template>
                }
              </td>
            </tr>
          } @empty {
            <tr>
              <td colspan="4" class="muted" i18n="@@project-members.no-match">No member matches.</td>
            </tr>
          }
        </tbody>
      </table>
    }

    @if (!canWrite()) {
      <trove-project-needs capability="projects:write" [state]="'denied'" />
    }
  `,
  styleUrl: '../../shared/access/access.scss',
  styles: `
    .count {
      margin-inline-start: var(--trove-space-1);
    }
    .toolbar {
      display: flex;
      gap: var(--trove-space-2);
      align-items: center;
      padding: var(--trove-space-3);
      background: var(--trove-surface-subtle, transparent);
      border-block-end: 1px solid var(--trove-border);
    }
    .filter {
      position: relative;
      flex: 1;
    }
    .filter input {
      inline-size: 100%;
      box-sizing: border-box;
      padding-inline-end: var(--trove-space-6);
    }
    .filter .material-icons {
      position: absolute;
      inset-inline-end: var(--trove-space-2);
      inset-block-start: 50%;
      transform: translateY(-50%);
      color: var(--trove-text-muted);
      font-size: 1.125rem;
      pointer-events: none;
    }
    .members {
      inline-size: 100%;
      border-collapse: collapse;
      margin-block-end: var(--trove-space-3);
    }
    .members th,
    .members td {
      padding: var(--trove-space-3);
      text-align: start;
      vertical-align: middle;
      border-block-end: 1px solid var(--trove-border);
    }
    .members thead th {
      font-weight: 600;
    }
    .account {
      display: flex;
      align-items: center;
      gap: var(--trove-space-3);
      font-weight: 400;
    }
    .avatar {
      flex: none;
      inline-size: 3rem;
      block-size: 3rem;
      border-radius: 50%;
    }
    .avatar.group {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      background: var(--trove-badge-neutral-bg);
      color: var(--trove-badge-neutral-fg);
    }
    .who,
    .line {
      display: flex;
      gap: var(--trove-space-1);
    }
    .who {
      flex-direction: column;
    }
    .line {
      align-items: center;
      gap: var(--trove-space-2);
    }
    .name {
      font-weight: 600;
    }
    .role {
      inline-size: auto;
    }
    .actions {
      text-align: end;
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
  private readonly dialog = inject(TroveDialog);

  protected readonly members = signal<readonly ProjectMember[]>([]);
  protected readonly loaded = signal(false);
  protected readonly busy = signal(false);
  protected readonly error = signal<ApiError | null>(null);
  protected readonly filter = signal('');
  protected readonly sort = signal<'account' | 'role'>('account');
  protected readonly descending = signal(false);
  protected readonly accountLabel = $localize`:@@project-members.col.account:Account`;
  protected readonly roleLabel = $localize`:@@project-members.col.role:Role`;
  protected readonly refs = computed(() => this.members().map((m) => m.principal));
  /** The members that match the filter, in the chosen order. */
  protected readonly shown = computed(() => {
    const text = this.filter().trim().toLowerCase();
    const found = this.members().filter((m) => !text || accountOf(m).toLowerCase().includes(text) || m.name.toLowerCase().includes(text));
    const by = this.sort() === 'role'
      ? (a: ProjectMember, b: ProjectMember) => PROJECT_ROLES.indexOf(b.role) - PROJECT_ROLES.indexOf(a.role) || accountOf(a).localeCompare(accountOf(b))
      : (a: ProjectMember, b: ProjectMember) => accountOf(a).localeCompare(accountOf(b));
    return [...found].sort((a, b) => (this.descending() ? by(b, a) : by(a, b)));
  });
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

  protected isMe(m: ProjectMember): boolean {
    return m.kind === 'user' && m.name === this.session.whoami()?.principal?.name;
  }

  protected actionsOf(m: ProjectMember): string {
    return $localize`:@@project-members.actions-of:Actions for ${m.principal}:member:`;
  }

  /** Opens the dialog that adds a member. */
  async openAdd(): Promise<void> {
    const data: ProjectMemberDialogData = { search: this.search, exclude: this.refs() };
    const ref = this.dialog.open<ProjectMemberDialog, ProjectMemberDialogData, ProjectMemberChoice>(ProjectMemberDialog, { data, width: '32rem' });
    const choice = await firstValueFrom(ref.closed);
    if (choice) await this.add(choice);
  }

  protected roleOf(m: ProjectMember): string {
    return $localize`:@@project-members.role-of:Role of ${m.principal}:member:`;
  }

  protected roleText(role: ProjectRole): string {
    return projectRoleLabel(role);
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

  private async add(choice: ProjectMemberChoice): Promise<void> {
    await this.write(async () => {
      await this.api.setMember(this.project(), choice.principal, choice.role);
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

function accountOf(m: ProjectMember): string {
  return m.displayName || m.name;
}
