import { DatePipe } from '@angular/common';
import { Component, computed, effect, inject, input, signal, untracked } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MatCheckboxModule } from '@angular/material/checkbox';
import { Router, RouterLink } from '@angular/router';
import { PermissionPolicy, PolicyApi } from '../../api/authz-api';
import { Group, GroupMember, GroupPatch, IdentityApi } from '../../api/identity-api';
import { collectAll } from '../../api/pagination';
import { ApiError } from '../../api/problem';
import { Versioned } from '../../api/repositories-api';
import { Notifier } from '../../core/notifier';
import { SessionStore } from '../../core/session-store';
import { Confirm } from '../../shared/confirm-dialog';
import { ConflictResolver } from '../../shared/conflict-dialog';
import { SelectField, SelectOption, TextField } from '../../shared/access/fields';
import { fieldErrorAt } from '../../shared/field-errors';
import { Needs } from '../../shared/access/needs';
import { PagedResource, asApiError } from '../../shared/paged-resource';
import { ErrorNotice } from '../../shared/error-notice';
import { groupOriginLabel } from './group-list';

/**
 * One group (R-UI-025): name, description and auto-join; members with their sources, where only
 * manual members change; the system roles bound to the group (read only, linked to the roles
 * screen); the policies that name the group (read only links). A group managed by SCIM is
 * read-only and says why.
 */
@Component({
  selector: 'trove-group-detail',
  imports: [DatePipe, MatButtonModule, MatCheckboxModule, RouterLink, ErrorNotice, Needs, SelectField, TextField],
  templateUrl: './group-detail.html',
  styleUrl: '../../shared/access/access.scss',
})
export class GroupDetail {
  /** Route parameter `:name`. */
  readonly name = input.required<string>();

  private readonly api = inject(IdentityApi);
  private readonly policyApi = inject(PolicyApi);
  private readonly confirmer = inject(Confirm);
  private readonly conflicts = inject(ConflictResolver);
  private readonly notifier = inject(Notifier);
  private readonly router = inject(Router);
  protected readonly session = inject(SessionStore);

  protected readonly group = signal<Group | null>(null);
  /** The `ETag` the record was read with; every write sends it as `If-Match` (Q-API-42). */
  private etag = '';
  protected readonly loadError = signal<ApiError | null>(null);
  protected readonly editError = signal<ApiError | null>(null);
  protected readonly memberError = signal<ApiError | null>(null);
  protected readonly policies = signal<readonly PermissionPolicy[] | null>(null);
  protected readonly members = new PagedResource<GroupMember>();

  protected readonly newName = signal('');
  protected readonly description = signal('');
  protected readonly autoJoin = signal(false);
  protected readonly newMember = signal('');
  protected readonly sourceFilter = signal('');
  protected readonly busy = signal(false);
  protected readonly originLabel = groupOriginLabel;

  protected readonly canWrite = computed(() => this.session.has('identities:write'));
  protected readonly readOnly = computed(() => this.group()?.origin === 'scim');
  protected readonly sourceOptions: readonly SelectOption[] = [
    { value: '', label: $localize`:@@group.source.all:All sources` },
    { value: 'manual', label: $localize`:@@group.source.manual:Manual` },
    { value: 'auto', label: $localize`:@@group.source.auto:Auto-join` },
  ];

  constructor() {
    effect(() => {
      this.name();
      untracked(() => void this.load());
    });
    effect(() => {
      this.sourceFilter();
      untracked(() => void this.members.start(this.api.groupMembers(this.name(), this.sourceFilter() || undefined)));
    });
  }

  protected async load(): Promise<void> {
    this.loadError.set(null);
    try {
      this.adopt(await this.api.group(this.name()));
    } catch (e) {
      this.group.set(null);
      this.loadError.set(asApiError(e));
      return;
    }
    await this.loadPolicies();
  }

  private adopt(loaded: Versioned<Group>): void {
    this.etag = loaded.etag;
    this.group.set(loaded.value);
    this.newName.set(loaded.value.name);
    this.description.set(loaded.value.description);
    this.autoJoin.set(loaded.value.autoJoin);
  }

  private async loadPolicies(): Promise<void> {
    if (!this.session.has('policies:read')) {
      this.policies.set(null);
      return;
    }
    try {
      this.policies.set(
        await collectAll(this.policyApi.list({ principal: `group:${this.name()}`, expandGroups: false })),
      );
    } catch {
      this.policies.set(null);
    }
  }

  protected fieldError(pointer: string): string | null {
    return fieldErrorAt(this.editError(), pointer);
  }

  protected async save(event: Event): Promise<void> {
    event.preventDefault();
    const g = this.group();
    if (!g) return;
    const patch: GroupPatch = {};
    if (this.newName().trim() !== g.name) patch.name = this.newName().trim();
    if (this.description().trim() !== g.description) patch.description = this.description().trim();
    if (this.autoJoin() !== g.autoJoin) patch.autoJoin = this.autoJoin();
    if (Object.keys(patch).length === 0) return;
    this.busy.set(true);
    this.editError.set(null);
    try {
      const result = await this.conflicts.save<Group>({
        etag: this.etag,
        patch,
        send: (etag) => this.api.updateGroup(g.name, patch, etag),
        reload: () => this.api.group(g.name),
      });
      if (result.outcome === 'dismissed') return;
      if (result.outcome === 'reloaded') {
        this.adopt(result.current);
        return;
      }
      this.notifier.status($localize`:@@group.saved:Group saved.`);
      if (result.current.value.name !== g.name) {
        await this.router.navigate(['/admin/groups', result.current.value.name]);
      } else {
        await this.load();
      }
    } catch (e) {
      this.editError.set(asApiError(e));
    } finally {
      this.busy.set(false);
    }
  }

  protected async remove(): Promise<void> {
    const answer = await this.confirmer.ask({
      title: $localize`:@@group.delete-title:Delete this group?`,
      lines: [$localize`:@@group.delete-text:${this.name()}:group: and its memberships are removed. Policies and roles that name it stop applying.`],
      confirmLabel: $localize`:@@group.delete-confirm:Delete`,
    });
    if (answer === null) return;
    this.editError.set(null);
    try {
      await this.api.deleteGroup(this.name());
      this.notifier.status($localize`:@@group.deleted:Group deleted.`);
      await this.router.navigate(['/admin/groups']);
    } catch (e) {
      this.editError.set(asApiError(e));
    }
  }

  protected async addMember(event: Event): Promise<void> {
    event.preventDefault();
    const principal = this.newMember().trim();
    if (!principal) return;
    await this.changeMembers({ add: [principal] }, $localize`:@@group.member-added:Member added.`, () =>
      this.newMember.set(''),
    );
  }

  protected async removeMember(member: GroupMember): Promise<void> {
    await this.changeMembers({ remove: [member.principal] }, $localize`:@@group.member-removed:Member removed.`);
  }

  private async changeMembers(
    change: { add?: string[]; remove?: string[] },
    done: string,
    after?: () => void,
  ): Promise<void> {
    this.busy.set(true);
    this.memberError.set(null);
    try {
      await this.api.changeGroupMembers(this.name(), { ...change, source: 'manual' });
      after?.();
      this.notifier.status(done);
      await this.members.reload();
      await this.load();
    } catch (e) {
      this.memberError.set(asApiError(e));
    } finally {
      this.busy.set(false);
    }
  }

  /** A member can be removed here when it holds the manual source (R-UI-025). */
  protected removable(m: GroupMember): boolean {
    return this.canWrite() && !this.readOnly() && m.sources.includes('manual');
  }

  protected sourceLabel(source: string): string {
    if (source === 'manual') return $localize`:@@group.src.manual:Manual`;
    if (source === 'auto') return $localize`:@@group.src.auto:Auto-join`;
    if (source === 'scim') return 'SCIM';
    return $localize`:@@group.src.provider:Identity provider`;
  }

  protected memberLink(m: GroupMember): string[] | null {
    return m.kind === 'user' ? ['/admin/users', m.principal] : ['/admin/service-accounts', m.principal.replace(/^sa:/, '')];
  }
}
