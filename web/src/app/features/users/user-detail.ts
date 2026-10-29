import { DatePipe } from '@angular/common';
import { Component, computed, effect, inject, input, signal, untracked } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { Router, RouterLink } from '@angular/router';
import { IdentityApi, User, UserGroupMembership, UserPatch } from '../../api/identity-api';
import { ApiError } from '../../api/problem';
import { SessionsApi, UserSession } from '../../api/sessions-api';
import { Notifier } from '../../core/notifier';
import { SessionStore } from '../../core/session-store';
import { Confirmer } from '../../shared/access/confirm-dialog';
import { TextField } from '../../shared/access/fields';
import { fieldErrorAt } from '../../shared/access/field-problem';
import { toApiError } from '../../shared/access/paged-signal';
import { Needs } from '../../shared/access/needs';
import { PrincipalRoles } from '../../shared/access/principal-roles';
import { ErrorNotice } from '../../shared/error-notice';
import { TokenManager, TokenOwnerScope } from '../tokens/token-manager';
import { isLocked, originLabel } from './user-labels';

/**
 * One user (R-UI-024). Attributes from the identity provider are read-only and labelled as such;
 * an administrator's correction of email or display name lasts until the next login. Group
 * memberships from the identity provider or SCIM are read-only with a badge; manual ones are
 * editable. The self-lockout guard of R-UI-005 is shown before the API refuses it: nobody can
 * disable or delete themselves here.
 */
@Component({
  selector: 'trove-user-detail',
  imports: [DatePipe, MatButtonModule, RouterLink, ErrorNotice, Needs, PrincipalRoles, TextField, TokenManager],
  templateUrl: './user-detail.html',
  styleUrl: '../../shared/access/access.scss',
})
export class UserDetail {
  /** Route parameter `:username`. */
  readonly username = input.required<string>();

  private readonly api = inject(IdentityApi);
  private readonly sessionsApi = inject(SessionsApi);
  private readonly confirmer = inject(Confirmer);
  private readonly notifier = inject(Notifier);
  private readonly router = inject(Router);
  protected readonly session = inject(SessionStore);

  protected readonly user = signal<User | null>(null);
  protected readonly loadError = signal<ApiError | null>(null);
  protected readonly actionError = signal<ApiError | null>(null);
  protected readonly editError = signal<ApiError | null>(null);
  protected readonly sessions = signal<readonly UserSession[] | null>(null);
  protected readonly sessionError = signal<ApiError | null>(null);

  protected readonly email = signal('');
  protected readonly displayName = signal('');
  protected readonly newGroup = signal('');
  protected readonly busy = signal(false);

  protected readonly originLabel = originLabel;
  protected readonly scope = computed<TokenOwnerScope>(() => ({ kind: 'user', name: this.username() }));
  protected readonly isSelf = computed(() => this.session.whoami()?.principal.name === this.username());
  protected readonly canWrite = computed(() => this.session.has('identities:write'));
  protected readonly locked = computed(() => {
    const u = this.user();
    return u !== null && isLocked(u);
  });
  /** The break-glass account cannot be edited or deleted through the API. */
  protected readonly editable = computed(() => this.user()?.origin !== 'system');

  constructor() {
    effect(() => {
      this.username();
      untracked(() => void this.load());
    });
  }

  protected async load(): Promise<void> {
    this.loadError.set(null);
    try {
      const u = await this.api.user(this.username());
      this.user.set(u);
      this.email.set(u.email ?? '');
      this.displayName.set(u.displayName ?? '');
    } catch (e) {
      this.user.set(null);
      this.loadError.set(toApiError(e));
      return;
    }
    await this.loadSessions();
  }

  private async loadSessions(): Promise<void> {
    this.sessionError.set(null);
    try {
      this.sessions.set(await this.sessionsApi.ofUser(this.username()));
    } catch (e) {
      this.sessions.set(null);
      this.sessionError.set(toApiError(e));
    }
  }

  protected fieldError(pointer: string): string | null {
    return fieldErrorAt(this.editError(), pointer);
  }

  protected async saveAttributes(event: Event): Promise<void> {
    event.preventDefault();
    const u = this.user();
    if (!u) return;
    const patch: { email?: string | null; displayName?: string | null } = {};
    if ((u.email ?? '') !== this.email().trim()) patch.email = this.email().trim() || null;
    if ((u.displayName ?? '') !== this.displayName().trim()) patch.displayName = this.displayName().trim() || null;
    if (Object.keys(patch).length === 0) return;
    await this.run(
      this.editError,
      async () => {
        const next = await this.api.updateUser(u.username, patch as UserPatch);
        this.user.set(next);
      },
      $localize`:@@user.saved:User saved.`,
    );
  }

  protected async disable(): Promise<void> {
    const answer = await this.confirmer.ask({
      title: $localize`:@@user.disable-title:Disable this user?`,
      text: $localize`:@@user.disable-text:${this.username()}:user: can no longer sign in, and their sessions end.`,
      confirm: $localize`:@@user.disable-confirm:Disable`,
      option: { label: $localize`:@@user.disable-revoke:Also revoke all tokens of this user`, checked: true },
    });
    if (!answer.confirmed) return;
    await this.act(
      () => this.api.disableUser(this.username(), { revokeTokens: answer.option }),
      $localize`:@@user.disabled:User disabled.`,
    );
  }

  protected async enable(): Promise<void> {
    await this.act(() => this.api.enableUser(this.username()), $localize`:@@user.enabled:User enabled.`);
  }

  protected async unlock(): Promise<void> {
    await this.act(() => this.api.unlockUser(this.username()), $localize`:@@user.unlocked:Account unlocked.`);
  }

  protected async remove(): Promise<void> {
    const answer = await this.confirmer.ask({
      title: $localize`:@@user.delete-title:Delete this user?`,
      text: $localize`:@@user.delete-text:${this.username()}:user: and their tokens and sessions are removed. This cannot be undone.`,
      confirm: $localize`:@@user.delete-confirm:Delete`,
    });
    if (!answer.confirmed) return;
    this.actionError.set(null);
    try {
      await this.api.deleteUser(this.username());
      this.notifier.status($localize`:@@user.deleted:User deleted.`);
      await this.router.navigate(['/admin/users']);
    } catch (e) {
      this.actionError.set(toApiError(e));
    }
  }

  protected async addGroup(event: Event): Promise<void> {
    event.preventDefault();
    const name = this.newGroup().trim();
    if (!name) return;
    await this.run(
      this.actionError,
      async () => {
        this.setGroups(await this.api.changeUserGroups(this.username(), { add: [name] }));
        this.newGroup.set('');
      },
      $localize`:@@user.group-added:Group membership added.`,
    );
  }

  protected async removeGroup(group: UserGroupMembership): Promise<void> {
    await this.run(
      this.actionError,
      async () => this.setGroups(await this.api.changeUserGroups(this.username(), { remove: [group.name] })),
      $localize`:@@user.group-removed:Group membership removed.`,
    );
  }

  protected async endSessions(): Promise<void> {
    const answer = await this.confirmer.ask({
      title: $localize`:@@user.end-sessions-title:End all sessions of this user?`,
      text: $localize`:@@user.end-sessions-text:${this.username()}:user: is signed out of every browser at once.`,
      confirm: $localize`:@@user.end-sessions-confirm:End sessions`,
    });
    if (!answer.confirmed) return;
    await this.run(
      this.actionError,
      async () => {
        await this.sessionsApi.endAllOfUser(this.username());
        await this.loadSessions();
      },
      $localize`:@@user.sessions-ended:Sessions ended.`,
    );
  }

  /** Only manual memberships can be changed here; the rest follow the identity provider or SCIM. */
  protected editableGroup(g: UserGroupMembership): boolean {
    return this.canWrite() && g.source === 'manual';
  }

  protected sourceLabel(source: string): string {
    if (source === 'manual') return $localize`:@@user.source.manual:Manual`;
    if (source === 'auto') return $localize`:@@user.source.auto:Auto-join`;
    if (source === 'scim') return 'SCIM';
    return $localize`:@@user.source.provider:Identity provider`;
  }

  private setGroups(groups: readonly UserGroupMembership[]): void {
    this.user.update((u) => (u ? { ...u, groups: [...groups] } : u));
  }

  private async act(call: () => Promise<void>, done: string): Promise<void> {
    await this.run(
      this.actionError,
      async () => {
        await call();
        await this.load();
      },
      done,
    );
  }

  private async run(
    target: { set(value: ApiError | null): void },
    call: () => Promise<void>,
    done: string,
  ): Promise<void> {
    this.busy.set(true);
    target.set(null);
    try {
      await call();
      this.notifier.status(done);
    } catch (e) {
      target.set(toApiError(e));
    } finally {
      this.busy.set(false);
    }
  }
}
