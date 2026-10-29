import { Component, effect, inject, input, signal, untracked } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { RouterLink } from '@angular/router';
import { PermissionPolicy, PolicyApi, PolicyFilters } from '../../api/authz-api';
import { SessionStore } from '../../core/session-store';
import { TextField } from '../../shared/access/fields';
import { Needs } from '../../shared/access/needs';
import { PagedResource } from '../../shared/paged-resource';
import { ErrorNotice } from '../../shared/error-notice';
import { expandActions } from './policy-model';

/** "Users: 2, groups: 3": the entries of a policy by kind of principal (R-UI-027). */
export function principalSummary(policy: Pick<PermissionPolicy, 'entries'>): string {
  const count = { user: 0, group: 0, sa: 0, special: 0 };
  for (const e of policy.entries) {
    if (e.principal.startsWith('user:')) count.user++;
    else if (e.principal.startsWith('group:')) count.group++;
    else if (e.principal.startsWith('sa:')) count.sa++;
    else count.special++;
  }
  const parts: string[] = [];
  if (count.user) parts.push($localize`:@@policies.sum.users:Users: ${count.user}:count:`);
  if (count.group) parts.push($localize`:@@policies.sum.groups:Groups: ${count.group}:count:`);
  if (count.sa) parts.push($localize`:@@policies.sum.sas:Service accounts: ${count.sa}:count:`);
  if (count.special) parts.push($localize`:@@policies.sum.special:Anonymous or all signed-in: ${count.special}:count:`);
  return parts.join(', ');
}

/** The union of the actions of all entries, in canonical order. */
export function actionSummary(policy: Pick<PermissionPolicy, 'entries'>): string {
  return expandActions(policy.entries.flatMap((e) => e.actions)).join(', ');
}

/**
 * Permission policies (R-UI-027): name, description, principal summary, resources summary and
 * actions; filters by principal and repository; cursor paging. The `principal` query parameter
 * opens the list filtered (the authentication settings link to `principal=anonymous`).
 */
@Component({
  selector: 'trove-policy-list',
  imports: [MatButtonModule, RouterLink, ErrorNotice, Needs, TextField],
  templateUrl: './policy-list.html',
  styleUrl: '../../shared/access/access.scss',
})
export class PolicyList {
  /** Query parameters, bound by the router's component input binding. */
  readonly principal = input<string | undefined>();
  readonly repository = input<string | undefined>();

  private readonly api = inject(PolicyApi);
  protected readonly session = inject(SessionStore);

  protected readonly list = new PagedResource<PermissionPolicy>();
  protected readonly principalFilter = signal('');
  protected readonly repositoryFilter = signal('');
  protected readonly principalSummary = principalSummary;
  protected readonly actionSummary = actionSummary;

  constructor() {
    effect(() => {
      this.principalFilter.set(this.principal() ?? '');
      this.repositoryFilter.set(this.repository() ?? '');
      untracked(() => void this.apply());
    });
  }

  protected async apply(): Promise<void> {
    const filters: PolicyFilters = {
      principal: this.principalFilter().trim() || undefined,
      repository: this.repositoryFilter().trim() || undefined,
      // Groups are expanded by the API unless told otherwise; the list shows policies that name
      // the principal or one of its groups, as the filter says.
    };
    await this.list.start(this.api.list(filters));
  }

  protected submit(event: Event): void {
    event.preventDefault();
    void this.apply();
  }

  protected repositorySummary(p: PermissionPolicy): string {
    const repos = p.resources.repositories ?? [];
    const shown = repos.slice(0, 3).join(', ');
    return repos.length > 3 ? $localize`:@@policies.sum.more:${shown}:list: and ${repos.length - 3}:count: more` : shown;
  }
}
