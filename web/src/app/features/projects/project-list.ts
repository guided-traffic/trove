import { Component, computed, inject, signal } from '@angular/core';
import { TroveDialog } from '../../shared/ui/dialog';
import { Router, RouterLink } from '@angular/router';
import { firstValueFrom } from 'rxjs';
import { ApiError } from '../../api/problem';
import { Project, ProjectRole, ProjectsApi } from '../../api/projects-api';
import { projectRoleLabel } from './project-roles';
import { ProjectFilter } from '../../core/project-filter';
import { SessionStore } from '../../core/session-store';
import { TextField } from '../../shared/access/fields';
import { Needs } from '../../shared/access/needs';
import { ErrorNotice } from '../../shared/error-notice';
import { PagedResource, asApiError } from '../../shared/paged-resource';
import { DataTable, TableColumn, TableSort } from '../../shared/ui/data-table';
import { FilterCategory, FilterPanel, FilterToggle } from '../../shared/ui/filter-panel';
import { ProjectDialog } from './project-dialog';

/**
 * Projects (R-UI-041): key, display name, Owners, members, number of repositories and the caller's own role.
 * A holder of `projects:read` sees every project; a member without that capability sees the projects it is a
 * member of (Q-AUTHZ-207). Creating needs `projects:write`; editing through the row also the Owner of the project.
 */
@Component({
  selector: 'trove-project-list',
  imports: [RouterLink, DataTable, ErrorNotice, FilterCategory, FilterPanel, FilterToggle, Needs, TextField],
  template: `
    <header class="page-header">
      <h1 tabindex="-1" i18n="@@projects.heading">Projects</h1>
      <button class="btn btn-confirm"
        type="button"
        [disabled]="!canWrite()"
        aria-describedby="needs-projects"
        (click)="create()"
        i18n="@@projects.add"
      >
        New project
      </button>
    </header>
    <trove-needs id="needs-projects" capability="projects:write" />
    <p i18n="@@projects.hint">
      A project holds a team's repositories. Its members have a role in it; its Owners manage the repositories, permission policies and service accounts without a system role.
    </p>

    @if (actionError(); as e) {
      <trove-error-notice [error]="e" />
    }

    <div class="list-layout" [class.panel-open]="panelOpen()">
      <div class="list-main">
        <div class="list-toolbar">
          <trove-filter-toggle [(open)]="panelOpen" [activeCount]="activeCount()" [controls]="panel.panelId" />
        </div>

        <trove-data-table
          [caption]="caption"
          [columns]="columns"
          [sort]="sort()"
          (sortChange)="sortBy($event)"
          [loaded]="list.loaded()"
          [loading]="list.loading()"
          [error]="list.error()"
          [empty]="list.items().length === 0"
          i18n-emptyText="@@projects.empty"
          emptyText="There is no project yet."
          [hasMore]="list.hasMore()"
          (more)="list.more()"
          (retry)="list.reload()"
        >
          @for (p of list.items(); track p.key) {
            <tr>
              <th scope="row"><a [routerLink]="['/admin/projects', p.key]" translate="no">{{ p.key }}</a></th>
              <td>{{ p.displayName }}</td>
              <td class="wrap" translate="no">{{ ownerSummary(p) }}</td>
              <td>{{ p.members }}</td>
              <td>{{ p.repositories }}</td>
              <td>{{ p.role ? roleLabel(p.role) : '' }}</td>
              <td class="actions">
                @if (canEdit(p)) {
                  <button class="btn btn-tertiary" type="button" (click)="edit(p)">
                    <ng-container i18n="@@projects.edit">Edit</ng-container>
                    <span class="visually-hidden"> {{ p.key }}</span>
                  </button>
                }
              </td>
            </tr>
          }
        </trove-data-table>
      </div>

      <trove-filter-panel
        #panel
        class="list-side"
        [(open)]="panelOpen"
        [activeCount]="activeCount()"
        [applyLabel]="applyLabel"
        (apply)="apply()"
        (clearAll)="clear()"
      >
        <trove-filter-category i18n-label="@@projects.q" label="Key or name" [active]="!!q().trim()" [expanded]="!!q().trim()">
          <trove-text-field i18n-label="@@projects.q" label="Key or name" name="q" [(value)]="q" />
        </trove-filter-category>
      </trove-filter-panel>
    </div>
  `,
  styleUrl: '../../shared/access/access.scss',
})
export class ProjectList {
  private readonly api = inject(ProjectsApi);
  private readonly dialog = inject(TroveDialog);
  private readonly router = inject(Router);
  private readonly filter = inject(ProjectFilter);
  protected readonly session = inject(SessionStore);

  protected readonly list = new PagedResource<Project>();
  protected readonly q = signal('');
  protected readonly panelOpen = signal(false);
  protected readonly activeCount = signal(0);
  protected readonly sort = signal<TableSort>({ column: 'key', direction: 'asc' });
  protected readonly actionError = signal<ApiError | null>(null);
  protected readonly canWrite = computed(() => this.session.has('projects:write'));
  protected readonly caption = $localize`:@@projects.caption:Projects`;
  protected readonly applyLabel = $localize`:@@projects.apply:Apply filter`;
  protected readonly columns: readonly TableColumn[] = [
    { id: 'key', label: $localize`:@@projects.col.key:Key`, sortable: true },
    { id: 'name', label: $localize`:@@projects.col.name:Display name` },
    { id: 'owners', label: $localize`:@@projects.col.owners:Owners` },
    { id: 'members', label: $localize`:@@projects.col.members:Members` },
    { id: 'repositories', label: $localize`:@@projects.col.repositories:Repositories` },
    {
      id: 'role',
      label: $localize`:@@projects.col.role:Your role`,
      help: $localize`:@@projects.help.role:Your role in the project; empty when you see it through a system role and are no member.`,
    },
    { id: 'actions', label: $localize`:@@projects.col.actions:Actions`, hideLabel: true },
  ];

  constructor() {
    void this.apply();
  }

  /** "group:web-leads, user:alice": the references of the Owners, as the API orders them. */
  protected ownerSummary(p: Project): string {
    return p.owners.join(', ');
  }

  protected roleLabel(role: ProjectRole): string {
    return projectRoleLabel(role);
  }

  /** `projects:write`, or the Owner role of the project (Q-AUTHZ-208). */
  protected canEdit(p: Project): boolean {
    return this.canWrite() || this.session.isOwnerOf(p.key);
  }

  protected async apply(): Promise<void> {
    const q = this.q().trim() || undefined;
    this.activeCount.set(q ? 1 : 0);
    await this.list.start(this.api.list({ q, sort: this.sort().direction === 'desc' ? '-key' : 'key' }));
  }

  protected sortBy(sort: TableSort): void {
    this.sort.set(sort);
    void this.apply();
  }

  protected clear(): void {
    this.q.set('');
    void this.apply();
  }

  protected async create(): Promise<void> {
    const ref = this.dialog.open<ProjectDialog, { project?: undefined }, Project>(ProjectDialog, {
      data: {},
      maxWidth: '40rem',
    });
    const created = await firstValueFrom(ref.closed);
    if (created) await this.router.navigate(['/admin/projects', created.key]);
  }

  protected async edit(p: Project): Promise<void> {
    this.actionError.set(null);
    try {
      const loaded = await this.api.get(p.key);
      const ref = this.dialog.open<ProjectDialog, { project: typeof loaded }, Project>(ProjectDialog, {
        data: { project: loaded },
        maxWidth: '40rem',
      });
      const saved = await firstValueFrom(ref.closed);
      if (saved) {
        this.list.replace((x) => x.key === saved.key, saved);
        void this.filter.refresh();
      }
    } catch (e) {
      this.actionError.set(asApiError(e));
    }
  }
}
