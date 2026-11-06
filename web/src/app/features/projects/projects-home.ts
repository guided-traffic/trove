import { Component, computed, inject, signal } from '@angular/core';
import { Router, RouterLink } from '@angular/router';
import { firstValueFrom } from 'rxjs';
import { Project, ProjectRole } from '../../api/projects-api';
import { ProjectContext } from '../../core/project-context';
import { SessionStore } from '../../core/session-store';
import { TextField } from '../../shared/access/fields';
import { TroveDialog } from '../../shared/ui/dialog';
import { DataTable, TableColumn } from '../../shared/ui/data-table';
import { ProjectDialog } from './project-dialog';
import { projectRoleLabel } from './project-roles';

/**
 * The start page (Q-UI-129): the projects the caller may see, from `GET /api/v1/projects` (the memberships; every
 * project for a holder of `projects:read`). A caller with exactly one project never sees it: the start route sends them
 * into that project. Creating a project needs `projects:write` (R-UI-041).
 */
@Component({
  selector: 'trove-projects-home',
  imports: [RouterLink, DataTable, TextField],
  template: `
    <header class="page-header">
      <h1 tabindex="-1" i18n="@@projects-home.heading">Projects</h1>
      @if (canCreate()) {
        <button class="btn btn-confirm" type="button" (click)="create()" i18n="@@projects.add">New project</button>
      }
    </header>
    <p i18n="@@projects-home.hint">
      A project holds a team's repositories, packages and access. Open one to work in it; its sidebar and the project switcher keep you there.
    </p>

    @if (context.projects().length > 5) {
      <trove-text-field i18n-label="@@projects.q" label="Key or name" name="q" [(value)]="q" />
    }

    <trove-data-table
      [caption]="caption"
      [columns]="columns"
      [loaded]="context.loaded()"
      [loading]="!context.loaded()"
      [empty]="shown().length === 0"
      [emptyText]="emptyText()"
    >
      @for (p of shown(); track p.key) {
        <tr>
          <th scope="row"><a [routerLink]="['/projects', p.key]" translate="no">{{ p.key }}</a></th>
          <td>{{ p.displayName }}</td>
          <td>{{ p.description }}</td>
          <td>{{ p.role ? roleLabel(p.role) : '' }}</td>
          <td>{{ p.repositories }}</td>
          <td>{{ p.members }}</td>
        </tr>
      }
    </trove-data-table>
  `,
  styleUrl: '../../shared/access/access.scss',
})
export class ProjectsHome {
  protected readonly context = inject(ProjectContext);
  private readonly session = inject(SessionStore);
  private readonly dialog = inject(TroveDialog);
  private readonly router = inject(Router);

  protected readonly q = signal('');
  protected readonly canCreate = computed(() => this.session.has('projects:write'));
  protected readonly shown = computed<readonly Project[]>(() => {
    const q = this.q().trim().toLowerCase();
    return this.context
      .projects()
      .filter((p) => !q || p.key.includes(q) || p.displayName.toLowerCase().includes(q));
  });
  protected readonly emptyText = computed(() =>
    this.context.projects().length === 0
      ? $localize`:@@projects-home.empty:You are not a member of a project yet. An Owner of a project adds you with a role.`
      : $localize`:@@projects-home.empty-filtered:No project matches.`,
  );
  protected readonly caption = $localize`:@@projects-home.caption:Your projects`;
  protected readonly columns: readonly TableColumn[] = [
    { id: 'key', label: $localize`:@@projects.col.key:Key` },
    { id: 'name', label: $localize`:@@projects.col.name:Display name` },
    { id: 'description', label: $localize`:@@projects-home.col.description:Description` },
    {
      id: 'role',
      label: $localize`:@@projects.col.role:Your role`,
      help: $localize`:@@projects.help.role:Your role in the project; empty when you see it through a system role and are no member.`,
    },
    { id: 'repositories', label: $localize`:@@projects.col.repositories:Repositories` },
    { id: 'members', label: $localize`:@@projects.col.members:Members` },
  ];

  protected roleLabel(role: ProjectRole): string {
    return projectRoleLabel(role);
  }

  protected async create(): Promise<void> {
    const ref = this.dialog.open<ProjectDialog, { project?: undefined }, Project>(ProjectDialog, {
      data: {},
      maxWidth: '40rem',
    });
    const created = await firstValueFrom(ref.closed);
    if (created) {
      await this.context.refresh();
      await this.router.navigate(['/projects', created.key, 'settings']);
    }
  }
}
