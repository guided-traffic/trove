import { Component, effect, inject, input, signal, untracked } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { RouterLink } from '@angular/router';
import { PermissionPolicy, PolicyApi, PolicyFilters } from '../../api/authz-api';
import { SessionStore } from '../../core/session-store';
import { TextField } from '../../shared/access/fields';
import { Needs } from '../../shared/access/needs';
import { PagedResource } from '../../shared/paged-resource';
import { DataTable, TableColumn, TableSort } from '../../shared/ui/data-table';
import { FilterCategory, FilterPanel, FilterToggle } from '../../shared/ui/filter-panel';
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
 * actions; filters by principal and repository in the filter panel; the name column sorts
 * through the API's `sort` parameter; cursor paging. The `principal` query parameter
 * opens the list filtered (the authentication settings link to `principal=anonymous`).
 */
@Component({
  selector: 'trove-policy-list',
  imports: [MatButtonModule, RouterLink, DataTable, FilterCategory, FilterPanel, FilterToggle, Needs, TextField],
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
  protected readonly panelOpen = signal(false);
  protected readonly activeCount = signal(0);
  protected readonly sort = signal<TableSort>({ column: 'name', direction: 'asc' });
  protected readonly caption = $localize`:@@policies.caption:Permission policies`;
  protected readonly columns: readonly TableColumn[] = [
    { id: 'name', label: $localize`:@@policies.col.name:Name`, sortable: true },
    { id: 'description', label: $localize`:@@policies.col.description:Description` },
    {
      id: 'principals',
      label: $localize`:@@policies.col.principals:Principals`,
      help: $localize`:@@policies.help.principals:Who the policy grants to, counted by kind: users, groups, service accounts, and the special principals for anonymous and all signed-in callers.`,
    },
    {
      id: 'resources',
      label: $localize`:@@policies.col.resources:Repositories`,
      help: $localize`:@@policies.help.resources:The repository keys or patterns the policy covers. Open the policy to see which repositories they match now.`,
    },
    { id: 'actions', label: $localize`:@@policies.col.actions:Actions` },
  ];
  protected readonly principalSummary = principalSummary;
  protected readonly actionSummary = actionSummary;

  constructor() {
    effect(() => {
      this.principalFilter.set(this.principal() ?? '');
      this.repositoryFilter.set(this.repository() ?? '');
      // A link with a filter (the authentication settings point at `principal=anonymous`) shows it.
      if (this.principal() || this.repository()) this.panelOpen.set(true);
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
    this.activeCount.set(Object.values(filters).filter((v) => v !== undefined).length);
    const sort = this.sort();
    await this.list.start(this.api.list({ ...filters, sort: sort.direction === 'desc' ? '-name' : 'name' }));
  }

  protected sortBy(sort: TableSort): void {
    this.sort.set(sort);
    void this.apply();
  }

  protected clear(): void {
    this.principalFilter.set('');
    this.repositoryFilter.set('');
    void this.apply();
  }

  protected repositorySummary(p: PermissionPolicy): string {
    const repos = p.resources.repositories ?? [];
    const shown = repos.slice(0, 3).join(', ');
    return repos.length > 3 ? $localize`:@@policies.sum.more:${shown}:list: and ${repos.length - 3}:count: more` : shown;
  }
}
